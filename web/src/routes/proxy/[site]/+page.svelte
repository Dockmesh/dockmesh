<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError, type Operation } from '$lib/api';
  import { fmtAgo } from '$lib/format';
  import { Skeleton, StatBar, StatusChip, Switch, Menu, EmptyState, Tabs } from '$lib/components/ui';
  import type { Stat } from '$lib/components/ui/StatBar.svelte';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { operations, isActive, opRef } from '$lib/stores/operations.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { ExternalLink } from 'lucide-svelte';
  import SiteModal from '../_SiteModal.svelte';
  import OptionsEditor from '../_OptionsEditor.svelte';
  import { TLS_LABEL, certNeedsAttention, optionSummary, type Site, type ACMEEvent } from '../_site';
  import { toForm, toPayload, changedGroups, parseFieldError, type OptionsForm } from '../_options';

  const host = $derived(page.params.site ?? '');

  type Metrics = { requests: number; requests_per_second: number; status_buckets: Record<string, number>; p95_latency_ms: number };
  let site = $state<Site | null>(null);
  let loading = $state(true);
  let metrics = $state<Metrics | null>(null);
  let acme = $state<ACMEEvent[]>([]);
  let ops = $state<Operation[]>([]);
  let preview = $state<string | null>(null);
  let previewErr = $state('');
  let showEdit = $state(false);

  type TabKey = 'overview' | 'options' | 'config';
  const TAB_KEYS: TabKey[] = ['overview', 'options', 'config'];
  const initialTab = page.url.searchParams.get('tab') as TabKey | null;
  let tab = $state<TabKey>(initialTab && TAB_KEYS.includes(initialTab) ? initialTab : 'overview');

  // base is the form of the stored options; form holds the edits. A
  // refresh only replaces the form while nothing is edited.
  let base = $state<OptionsForm>(toForm(null));
  let form = $state<OptionsForm>(toForm(null));
  let fieldErrors = $state<Record<string, string>>({});
  let saveErr = $state('');
  let saving = $state(false);
  const changed = $derived(changedGroups(form, base));

  const canUpdate = $derived(allowed('proxy.update'));
  const canDelete = $derived(allowed('proxy.delete'));

  $effect(() => {
    pageContext.set(host, [{ label: 'Reverse proxy', href: '/proxy' }]);
    return () => pageContext.clear();
  });

  async function load() {
    try {
      const [routes, events, list] = await Promise.all([
        api.proxy.listRoutes() as Promise<Site[]>,
        api.proxy.acmeEvents().catch(() => [] as ACMEEvent[]),
        api.operations.list({ kind: 'proxy.site', limit: 30 }).catch(() => [] as Operation[])
      ]);
      const s = routes.find((r) => r.host === host) ?? null;
      const clean = changedGroups(form, base).length === 0;
      site = s;
      if (s) {
        base = toForm(s.options);
        if (clean) form = toForm(s.options);
      }
      acme = events.filter((e) => e.host === host);
      ops = list.filter((o) => o.ref === opRef.proxySite(host));
      operations.track(ops);
      if (s) metrics = await api.proxy.routeMetrics(s.id).catch(() => null);
    } catch (e) {
      toast.error('Could not load the site', (e as Error).message);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void host;
    preview = null;
    previewErr = '';
    form = toForm(null);
    base = toForm(null);
    untrack(load);
  });
  $effect(() => {
    if (tab === 'config' && site && preview === null && !previewErr) untrack(loadPreview);
  });
  $effect(() => autoRefresh(load, 15_000));

  async function loadPreview() {
    if (!site) return;
    previewErr = '';
    try {
      const p = await api.proxy.routeConfig(site.id);
      preview = JSON.stringify(p.config, null, 2);
      if (p.blocked) previewErr = 'The stored options fail validation, so Caddy does not serve this site.';
    } catch (e) {
      previewErr = (e as Error).message;
    }
  }

  async function saveOptions() {
    if (!site) return;
    saving = true;
    fieldErrors = {};
    saveErr = '';
    try {
      const updated = (await api.proxy.updateRoute(site.id, site.upstream, site.tls_mode, undefined, toPayload(form))) as Site;
      site = { ...site, ...updated };
      base = toForm(updated.options);
      form = toForm(updated.options);
      preview = null;
      previewErr = '';
      toast.success('Options saved', site.host);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Saving failed';
      const fe = parseFieldError(msg);
      if (fe) fieldErrors = { [fe.field]: fe.message };
      else saveErr = msg;
      toast.error('Options not saved', fe ? fe.message : msg);
    } finally {
      saving = false;
    }
  }

  function resetOptions() {
    form = toForm(site?.options);
    fieldErrors = {};
    saveErr = '';
  }

  async function toggle() {
    if (!site) return;
    const next = !site.enabled;
    try {
      await api.proxy.setRouteEnabled(site.id, next);
      toast.success(next ? 'Site enabled' : 'Site disabled', site.host);
      await load();
    } catch (err) {
      toast.error('Change failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function remove() {
    if (!site) return;
    if (!(await confirm.ask({
      title: 'Delete site',
      message: `Delete ${site.host}?`,
      body: 'Requests to this name get no answer from Caddy after the next reload. The container it points to is not touched.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.proxy.deleteRoute(site.id);
      toast.success('Site deleted', site.host);
      goto('/proxy');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const url = $derived(site ? `${site.tls_mode === 'none' ? 'http' : 'https'}://${site.host}` : '');
  const opts = $derived(optionSummary(site?.options));
  const certTone = $derived(!site || site.tls_mode === 'none' ? 'off' : certNeedsAttention(site) ? 'warn' : 'ok');
  const errors5xx = $derived(metrics?.status_buckets['5xx'] ?? 0);

  const stats = $derived.by<Stat[]>(() => {
    if (!site) return [];
    return [
      { label: 'Requests', value: metrics ? metrics.requests.toLocaleString() : '—', note: metrics ? `${metrics.requests_per_second.toFixed(1)} req/s` : 'since Caddy started' },
      { label: 'Latency p95', value: metrics && metrics.requests ? `${metrics.p95_latency_ms.toFixed(0)} ms` : '—' },
      { label: 'Errors (5xx)', value: metrics ? errors5xx.toLocaleString() : '—', tone: errors5xx > 0 ? 'bad' : '' },
      {
        label: 'Certificate',
        value: site.tls_mode === 'none' ? 'None' : site.cert_days_left != null ? `${site.cert_days_left} d` : 'Not served',
        note: TLS_LABEL[site.tls_mode],
        tone: certTone === 'warn' ? 'warn' : ''
      }
    ];
  });

  type Row = { ts: string; what: string; text: string; tone: 'ok' | 'warn' | 'bad' | 'off'; op?: Operation };
  const activity = $derived.by<Row[]>(() => {
    const out: Row[] = acme.map((e) => ({
      ts: e.ts,
      what: e.kind === 'renew' ? 'Certificate renewed' : e.kind === 'obtain' ? 'Certificate' : 'Certificate failed',
      text: `${e.message}${e.issuer ? ` (${e.issuer})` : ''}`,
      tone: e.successful ? 'ok' : 'bad'
    }));
    for (const o of ops) {
      out.push({
        ts: o.finished_at ?? o.created_at,
        what: 'Created',
        text: `${o.actor_name ? `by ${o.actor_name}` : ''}${o.error ? ` · ${o.error}` : ''}`,
        tone: isActive(o) ? 'off' : o.status === 'failed' ? 'bad' : 'ok',
        op: o
      });
    }
    return out.sort((a, b) => b.ts.localeCompare(a.ts)).slice(0, 20);
  });

  const ACT_COLS = '90px 150px minmax(0, 1fr)';

  const menu = $derived.by<MenuItem[]>(() => {
    const items: MenuItem[] = [{ label: `Visit ${site?.host ?? ''}`, href: url }];
    if (canDelete) items.push({ separator: true }, { label: 'Delete…', danger: true, onclick: remove });
    return items;
  });
</script>

{#snippet actRow(a: Row)}
  <span class="dm-num dm-dim" title={new Date(a.ts).toLocaleString()}>{fmtAgo(a.ts)}</span>
  <span class="ev"><span class="dm-dot {a.tone === 'off' ? '' : a.tone}"></span>{a.what}</span>
  <span class="dm-trunc dm-dim" title={a.text}>{a.text || '—'}</span>
{/snippet}

<section class="dm-page">
  {#if loading}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="3.2rem" />
  {:else if !site}
    <EmptyState title="Site not found" description="It was deleted, or the name is spelled differently.">
      {#snippet action()}<a class="dm-btn dm-btn-primary" href="/proxy">Back to the reverse proxy</a>{/snippet}
    </EmptyState>
  {:else}
    <div class="dm-ph">
      <h1 class="dm-trunc">{site.host}</h1>
      {#if !canUpdate}<StatusChip tone={site.enabled ? 'ok' : 'off'} label={site.enabled ? 'Live' : 'Off'} />{/if}
      <span class="dm-sub dm-trunc">→ <span class="dm-mono">{site.upstream}</span> · {TLS_LABEL[site.tls_mode]}</span>
      <span class="dm-sp"></span>
      <a class="dm-btn" href={url} target="_blank" rel="noopener">Open site <ExternalLink size={13} /></a>
      {#if canUpdate}
        <Switch checked={site.enabled} label={site.enabled ? 'Live' : 'Off'} onchange={toggle} />
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => (showEdit = true)}>Edit</button>
      {/if}
      <Menu label="Actions for {site.host}" items={menu} />
    </div>

    <StatBar items={stats} />

    <Tabs
      label="Site sections"
      tabs={[
        { key: 'overview', label: 'Overview' },
        { key: 'options', label: 'Options', count: opts.length || null, dot: changed.length > 0 },
        { key: 'config', label: 'Config' }
      ]}
      active={tab}
      onchange={(k) => (tab = k as TabKey)}
    />

    {#if tab === 'overview'}
    <div class="cols">
      <section>
        <h2 class="dm-h2">Details</h2>
        <dl class="dm-kv">
          <dt>Goes to</dt><dd class="dm-mono">{site.upstream}</dd>
          <dt>HTTPS</dt><dd>{TLS_LABEL[site.tls_mode]}</dd>
          {#if site.tls_mode !== 'none'}
            <dt>Certificate</dt>
            <dd>
              {#if site.cert_valid_to}
                {site.cert_days_left} days left · {site.cert_issuer || 'unknown issuer'}
                <span class="dm-dim" title={new Date(site.cert_valid_to).toLocaleString()}>· until {new Date(site.cert_valid_to).toLocaleDateString()}</span>
              {:else}
                <span class="dm-dim">Not served yet. The name may not point here, or Caddy is still getting it.</span>
              {/if}
            </dd>
          {/if}
          <dt>Options</dt>
          <dd>{opts.length ? opts.join(' · ') : 'Caddy defaults'} <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (tab = 'options')}>{canUpdate ? 'Edit' : 'Show'}</button></dd>
          <dt>Created</dt><dd title={site.created_at ? new Date(site.created_at).toLocaleString() : ''}>{site.created_at ? fmtAgo(site.created_at) : '—'}</dd>
          {#if site.updated_at && site.updated_at !== site.created_at}
            <dt>Changed</dt><dd title={new Date(site.updated_at).toLocaleString()}>{fmtAgo(site.updated_at)}</dd>
          {/if}
        </dl>
        {#if metrics && metrics.requests > 0}
          <h2 class="dm-h2 sp">Responses</h2>
          <dl class="dm-kv">
            {#each ['2xx', '3xx', '4xx', '5xx'] as b (b)}
              <dt>{b}</dt><dd class="dm-num" class:bad={b === '5xx' && (metrics.status_buckets[b] ?? 0) > 0}>{(metrics.status_buckets[b] ?? 0).toLocaleString()}</dd>
            {/each}
          </dl>
        {/if}
      </section>

      <section>
        <h2 class="dm-h2">Activity</h2>
        <div class="dm-tbl">
          {#each activity as a, i (i)}
            {#if a.op}
              {@const op = a.op}
              <button type="button" class="dm-tr dm-click act" style="grid-template-columns: {ACT_COLS}" onclick={() => operations.open(op.id)}>
                {@render actRow(a)}
              </button>
            {:else}
              <div class="dm-tr" style="grid-template-columns: {ACT_COLS}">{@render actRow(a)}</div>
            {/if}
          {:else}
            <div class="dm-empty">No certificate events for this site yet.</div>
          {/each}
        </div>
      </section>
    </div>

    {:else if tab === 'options'}
      <section>
        <div class="sec-h">
          <span class="dm-sub">
            {canUpdate
              ? "Every option starts at Caddy's default. Dockmesh checks each value before Caddy sees it."
              : 'Read-only: changing options needs proxy edit rights. Header values are hidden.'}
          </span>
          <span class="dm-sp"></span>
          {#if canUpdate}
            {#if changed.length}<span class="dm-sub">{changed.length} group{changed.length === 1 ? '' : 's'} changed</span>{/if}
            <button type="button" class="dm-btn dm-btn-sm" disabled={!changed.length || saving} onclick={resetOptions}>Reset</button>
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={!changed.length || saving} onclick={saveOptions}>Save options</button>
          {/if}
        </div>
        {#if saveErr}<p class="err" role="alert">{saveErr}</p>{/if}
        <OptionsEditor bind:form tls={site.tls_mode} host={site.host} errors={fieldErrors} readonly={!canUpdate} />
      </section>
    {:else}
      <section>
        <div class="sec-h">
          <span class="dm-sub">What Dockmesh sends to Caddy for this site, as saved (read-only). Secrets and password hashes are hidden.</span>
          <span class="dm-sp"></span>
          <button type="button" class="dm-btn dm-btn-sm" onclick={() => { preview = null; previewErr = ''; loadPreview(); }}>Reload</button>
        </div>
        {#if previewErr}<p class="err">{previewErr}</p>{/if}
        {#if preview !== null}<pre class="pv dm-mono">{preview}</pre>{:else if !previewErr}<Skeleton width="100%" height="8rem" />{/if}
      </section>
    {/if}
  {/if}
</section>

<SiteModal bind:open={showEdit} editing={site} onsaved={() => { preview = null; previewErr = ''; load(); }} />

<style>
  .cols { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 32px; align-items: start; }
  @media (max-width: 1100px) { .cols { grid-template-columns: minmax(0, 1fr); } }
  .sp { margin-top: 20px; }
  .act { width: 100%; background: none; border: 0; border-bottom: 1px solid var(--border); font: inherit; color: inherit; text-align: left; }
  .act:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
  .ev { display: inline-flex; align-items: center; gap: 8px; }
  .sec-h { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }
  .pv { max-height: 640px; overflow: auto; background: var(--field); padding: 10px 12px; border-radius: var(--r); font-size: 12px; margin: 0; }
  .err { color: var(--bad); font-size: var(--fs-sm); margin: 0 0 8px; }
  dd.bad { color: var(--bad); }
</style>
