<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo } from '$lib/format';
  import { api, ApiError, type Operation } from '$lib/api';
  import { Skeleton, StatusChip, Tabs, Switch, Menu, EmptyState, LiveIndicator, InfoTip } from '$lib/components/ui';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import OpProgress from '$lib/components/operations/OpProgress.svelte';
  import { operations, isActive, opRef } from '$lib/stores/operations.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { Plus, Power, PowerOff } from 'lucide-svelte';
  import SiteModal from './_SiteModal.svelte';
  import ImportModal from './_ImportModal.svelte';
  import { goto } from '$app/navigation';
  import CaddyTab from './_CaddyTab.svelte';
  import { TLS_LABEL, certExpiresSoon, certNeedsAttention, optionSummary, type Site, type ProxyStatus, type ACMEEvent } from './_site';

  type MetricsSnapshot = {
    total_requests: number;
    requests_per_second: number;
    status_buckets: Record<string, number>;
    p95_latency_ms: number;
    per_host: Array<{ host: string; requests: number; requests_per_second: number; status_buckets: Record<string, number>; p95_latency_ms: number }>;
  };

  const canCreate = $derived(allowed('proxy.create'));
  const canUpdate = $derived(allowed('proxy.update'));
  const canDelete = $derived(allowed('proxy.delete'));

  type TabKey = 'sites' | 'certificates' | 'activity' | 'caddy';
  const TABS: TabKey[] = ['sites', 'certificates', 'activity', 'caddy'];
  const tabs = urlTab(TABS, 'sites');
  const tab = $derived(tabs.current);
  const setTab = (k: string) => tabs.set(k);

  let status = $state<ProxyStatus | null>(null);
  let routes = $state<Site[]>([]);
  let metrics = $state<MetricsSnapshot | null>(null);
  let acme = $state<ACMEEvent[]>([]);
  let siteOps = $state<Operation[]>([]);
  let latestCaddy = $state<string>('');
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let busy = $state(false);
  let search = $state('');
  let selected = $state<Set<number>>(new Set());

  let showSite = $state(false);
  let editing = $state<Site | null>(null);
  let showImport = $state(false);

  async function load() {
    refreshing = true;
    try {
      const [s, r, m, e, ops, v] = await Promise.all([
        api.proxy.status().catch(() => null),
        api.proxy.listRoutes().catch(() => []) as Promise<Site[]>,
        api.proxy.metrics().catch(() => null),
        api.proxy.acmeEvents().catch(() => [] as ACMEEvent[]),
        api.operations.list({ kind: 'proxy.site', limit: 30 }).catch(() => [] as Operation[]),
        api.proxy.version().catch(() => null)
      ]);
      status = s;
      routes = r;
      metrics = m;
      acme = e;
      siteOps = ops;
      operations.track(ops);
      latestCaddy = v?.managed && v.default && v.current && v.default !== v.current ? v.default : '';
      const ids = new Set(r.map((x) => x.id));
      selected = new Set([...selected].filter((id) => ids.has(id)));
      lastLoaded = Date.now();
    } finally {
      loading = false;
      refreshing = false;
    }
  }
  $effect(() => {
    untrack(load);
  });
  $effect(() => autoRefresh(load, 15_000));

  // A new site's operation ends with its certificate; reload then so the
  // list shows it.
  let wasActive = 0;
  $effect(() => {
    const n = operations.active.filter((o) => o.kind === 'proxy.site').length;
    if (n < wasActive) untrack(load);
    wasActive = n;
  });

  const perHost = $derived(new Map((metrics?.per_host ?? []).map((h) => [h.host, h])));
  const visible = $derived(
    routes
      .filter((r) => {
        const q = search.trim().toLowerCase();
        return !q || r.host.toLowerCase().includes(q) || r.upstream.toLowerCase().includes(q);
      })
      .sort((a, b) => a.host.localeCompare(b.host))
  );
  const live = $derived(routes.filter((r) => r.enabled).length);
  const certSites = $derived(routes.filter((r) => r.tls_mode !== 'none'));
  const certWarn = $derived(certSites.filter((r) => r.enabled && certNeedsAttention(r)).length);
  const allSelected = $derived(visible.length > 0 && visible.every((r) => selected.has(r.id)));

  function siteTone(r: Site): '' | 'ok' | 'warn' {
    if (!r.enabled) return '';
    if (r.tls_mode !== 'none' && certExpiresSoon(r)) return 'warn';
    return 'ok';
  }
  function httpsNote(r: Site): string {
    if (r.tls_mode === 'none') return 'not encrypted';
    if (r.cert_days_left == null) return 'no certificate served yet';
    return `expires in ${r.cert_days_left} d`;
  }
  const siteHref = (r: Site) => `/proxy/${encodeURIComponent(r.host)}`;
  const opOf = (host: string) => operations.latest(opRef.proxySite(host), 'proxy.site');

  async function enableProxy() {
    busy = true;
    toast.info('Starting Caddy', 'Pulling the image if needed…');
    try {
      await api.proxy.enable();
      toast.success('Proxy enabled');
      await load();
    } catch (err) {
      toast.error('Enable failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
  async function disableProxy() {
    if (!(await confirm.ask({
      title: 'Stop the reverse proxy',
      message: 'Stop and remove the Caddy container?',
      body: 'Every site stops answering. Sites stay saved; starting the proxy again applies them.',
      confirmLabel: 'Stop', danger: true
    }))) return;
    busy = true;
    try {
      await api.proxy.disable();
      toast.info('Proxy stopped');
      await load();
    } catch (err) {
      toast.error('Stop failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function toggleEnabled(r: Site) {
    const next = !r.enabled;
    try {
      await api.proxy.setRouteEnabled(r.id, next);
      toast.success(next ? 'Site enabled' : 'Site disabled', r.host);
      await load();
    } catch (err) {
      toast.error('Change failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function deleteSite(r: Site) {
    if (!(await confirm.ask({
      title: 'Delete site',
      message: `Delete ${r.host}?`,
      body: 'Requests to this name get no answer from Caddy after the next reload. The container it points to is not touched.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.proxy.deleteRoute(r.id);
      toast.success('Site deleted', r.host);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkDelete() {
    const list = routes.filter((r) => selected.has(r.id));
    if (!(await confirm.ask({
      title: 'Delete sites',
      message: `Delete ${list.length} site${list.length === 1 ? '' : 's'}?`,
      body: list.map((r) => r.host).join(', '),
      confirmLabel: 'Delete', danger: true
    }))) return;
    let ok = 0, fail = 0;
    for (const r of list) {
      try { await api.proxy.deleteRoute(r.id); ok++; } catch { fail++; }
    }
    toast[fail ? 'warning' : 'success'](`Deleted ${ok}${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    await load();
  }
  function toggleOne(id: number) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }

  function openNew() {
    editing = null;
    showSite = true;
  }
  function openEdit(r: Site) {
    editing = r;
    showSite = true;
  }
  function menuFor(r: Site): MenuItem[] {
    const items: MenuItem[] = [
      { label: 'Open', kbd: '↵', href: siteHref(r) },
      { label: 'Open in browser', href: `${r.tls_mode === 'none' ? 'http' : 'https'}://${r.host}` }
    ];
    if (canUpdate) items.push({ label: 'Edit', onclick: () => openEdit(r) });
    const op = opOf(r.host);
    if (op) items.push({ label: 'Show last creation steps', onclick: () => operations.open(op.id) });
    if (canDelete) items.push({ separator: true }, { label: 'Delete…', danger: true, onclick: () => deleteSite(r) });
    return items;
  }

  // Activity merges Caddy's certificate events with the new-site
  // operations Dockmesh ran.
  type Activity = { ts: string; what: string; text: string; tone: 'ok' | 'warn' | 'bad' | 'off'; op?: Operation };
  const activity = $derived.by<Activity[]>(() => {
    const out: Activity[] = acme.map((e) => ({
      ts: e.ts,
      what: e.kind === 'renew' ? 'Certificate renewed' : e.kind === 'obtain' ? 'Certificate' : 'Certificate failed',
      text: `${e.host ?? '—'}: ${e.message}${e.issuer ? ` (${e.issuer})` : ''}`,
      tone: e.successful ? 'ok' : 'bad'
    }));
    for (const o of siteOps) {
      const warn = o.steps.find((s) => s.status === 'warning');
      out.push({
        ts: o.finished_at ?? o.created_at,
        what: 'New site',
        text: `${o.target}${o.actor_name ? ` by ${o.actor_name}` : ''}${o.error ? `: ${o.error}` : warn ? `: ${warn.detail}` : ''}`,
        tone: isActive(o) ? 'off' : o.status === 'failed' ? 'bad' : warn ? 'warn' : 'ok',
        op: o
      });
    }
    return out.sort((a, b) => b.ts.localeCompare(a.ts));
  });

  const SITE_COLS = '22px 14px minmax(200px, 1.4fr) minmax(170px, 1.2fr) 160px minmax(150px, 1fr) 80px 56px 36px';
  const CERT_COLS = 'minmax(200px, 1.4fr) minmax(160px, 1fr) 120px 120px';
  const ACT_COLS = '110px 150px minmax(0, 1fr) 110px';
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Reverse proxy</h1>
    <span class="dm-sub">
      {#if status === null}Caddy status unknown
      {:else if !status.enabled}Caddy is off
      {:else}Caddy{status.version ? ` ${status.version}` : ''} · <span class={status.running ? 'ok' : 'bad'}>{status.running ? 'running' : 'stopped'}</span>{/if}
      · {live} site{live === 1 ? '' : 's'} live
      {#if latestCaddy}· <a href="/proxy?tab=caddy" onclick={(e) => { e.preventDefault(); setTab('caddy'); }}>{latestCaddy} recommended</a>{/if}
    </span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if canCreate && status?.enabled}
      <button type="button" class="dm-btn" onclick={() => (showImport = true)} disabled={!status.running} title={status.running ? '' : 'Caddy must run to read a Caddyfile'}>Import Caddyfile</button>
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} strokeWidth={2} /> New site</button>
    {/if}
    {#if status?.running && canCreate}
      <Menu label="Proxy actions" items={[{ label: 'Stop the proxy…', danger: true, icon: PowerOff, onclick: disableProxy }]} />
    {/if}
  </div>

  {#if status && (!status.enabled || !status.running)}
    <div class="px-notice" role="status">
      <div>
        <b>{!status.enabled ? 'The reverse proxy is off' : 'Caddy is enabled but not running'}</b>
        <p>{!status.enabled ? 'Enabling pulls the Caddy image if needed, starts a managed container on ports 80 and 443 and applies every saved site.' : 'Start it to serve the saved sites again.'}</p>
      </div>
      {#if canCreate}
        <button type="button" class="dm-btn dm-btn-primary" disabled={busy} onclick={enableProxy}><Power size={14} /> {status.enabled ? 'Start' : 'Enable proxy'}</button>
      {/if}
    </div>
  {/if}

  <Tabs
    tabs={[
      { key: 'sites', label: 'Sites', count: routes.length },
      { key: 'certificates', label: 'Certificates', count: certSites.length || null, dot: certWarn > 0 },
      { key: 'activity', label: 'Activity' },
      { key: 'caddy', label: 'Caddy' }
    ]}
    active={tab}
    onchange={setTab}
  />

  {#if tab === 'sites'}
    {#if loading}
      <div class="dm-tbl">
        {#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="55%" height="0.9rem" /></div>{/each}
      </div>
    {:else if routes.length === 0}
      <EmptyState title="No sites yet" description={status?.enabled ? 'A site maps a public name to a container or address; Caddy gets its certificate.' : 'Enable the proxy first, then add sites.'}>
        {#snippet action()}
          {#if canCreate && status?.enabled}
            <button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} /> New site</button>
          {/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-toolbar">
        <input class="dm-filter" type="search" placeholder="Filter sites…" bind:value={search} aria-label="Filter sites" />
        <span class="dm-sp"></span>
        {#if selected.size && canDelete}
          <span class="dm-sub">{selected.size} selected</span>
          <button type="button" class="dm-btn dm-btn-sm dm-btn-danger" onclick={bulkDelete}>Delete…</button>
          <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => (selected = new Set())}>Clear</button>
        {/if}
      </div>
      <div class="dm-tbl" role="table" aria-label="Sites">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {SITE_COLS}">
          <span role="columnheader">{#if canDelete}<input type="checkbox" aria-label="Select all" checked={allSelected} onchange={() => (selected = allSelected ? new Set() : new Set(visible.map((r) => r.id)))} />{/if}</span>
          <span role="columnheader"></span>
          <span role="columnheader">Site</span>
          <span role="columnheader">Goes to</span>
          <span role="columnheader">HTTPS</span>
          <span role="columnheader">Options</span>
          <span role="columnheader" class="num-h">Req/s</span>
          <span role="columnheader">On</span>
          <span role="columnheader"></span>
        </div>
        {#each visible as r (r.id)}
          {@const op = opOf(r.host)}
          {@const opts = optionSummary(r.options)}
          {@const m = perHost.get(r.host)}
          <div class="dm-tr dm-click" role="row" class:dm-sel={selected.has(r.id)} style="grid-template-columns: {SITE_COLS}"
            tabindex="0" onclick={() => goto(siteHref(r))} onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(siteHref(r)); }}>
            <span role="cell">
              {#if canDelete}<input type="checkbox" aria-label="Select {r.host}" checked={selected.has(r.id)} onclick={(e) => e.stopPropagation()} onchange={() => toggleOne(r.id)} />{/if}
            </span>
            <span role="cell"><span class="dm-dot {siteTone(r)}"></span></span>
            <span role="cell" class="dm-nm">
              <a href={siteHref(r)} onclick={(e) => e.stopPropagation()}>{r.host}</a>
              <small>{r.enabled ? (r.options?.routing?.www_redirect ? `www.${r.host} redirects here` : ' ') : 'disabled'}</small>
            </span>
            <span role="cell" class="dm-mono px-up" title={r.upstream}>{r.upstream}</span>
            <span role="cell" class="dm-nm">
              {#if op && isActive(op)}
                <OpProgress {op} compact />
              {:else}
                <span class="px-tls">{TLS_LABEL[r.tls_mode]}</span>
                <small class:warn={r.enabled && certNeedsAttention(r)}>{httpsNote(r)}</small>
              {/if}
            </span>
            <span role="cell" class="dm-dim" title={opts.join(', ')}>
              {#if opts.length}{opts.slice(0, 2).join(', ')}{#if opts.length > 2} <span class="dm-num">+{opts.length - 2}</span>{/if}{:else}<span class="dm-dim">defaults</span>{/if}
            </span>
            <span role="cell" class="dm-num num-c">{r.enabled && m ? m.requests_per_second.toFixed(1) : '—'}</span>
            <span role="cell"><span role="presentation" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
              <Switch checked={r.enabled} label="Serve {r.host}" hideLabel disabled={!canUpdate} onchange={() => toggleEnabled(r)} />
            </span></span>
            <span role="cell" class="dm-tr-menu"><span role="presentation" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
              <Menu label="Actions for {r.host}" items={menuFor(r)} />
            </span></span>
          </div>
        {/each}
        {#if visible.length === 0}<div class="dm-empty">No sites match “{search}”.</div>{/if}
      </div>
      {#if metrics}
        <p class="dm-sub px-foot">
          All sites: {metrics.requests_per_second.toFixed(1)} req/s · p95 {metrics.p95_latency_ms.toFixed(0)} ms ·
          {(metrics.status_buckets['5xx'] ?? 0).toLocaleString()} server errors since Caddy started
        </p>
      {/if}
    {/if}
  {:else if tab === 'certificates'}
    {#if certSites.length === 0}
      <EmptyState title="No HTTPS sites" description="Sites with Let's Encrypt or the internal CA show their certificate here." />
    {:else}
      <div class="dm-tbl" role="table" aria-label="Certificates">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {CERT_COLS}">
          <span role="columnheader">Name</span><span role="columnheader">Issuer</span><span role="columnheader">Expires in</span><span role="columnheader">Status</span>
        </div>
        {#each certSites as r (r.id)}
          {@const soon = certExpiresSoon(r)}
          <div class="dm-tr dm-click" role="row" style="grid-template-columns: {CERT_COLS}" tabindex="0"
            onclick={() => goto(siteHref(r))} onkeydown={(e) => { if (e.key === 'Enter') goto(siteHref(r)); }}>
            <span role="cell" class="dm-mono">{r.host}</span>
            <span role="cell" class="dm-dim">{r.cert_issuer || TLS_LABEL[r.tls_mode]}</span>
            <span role="cell" class="dm-num" class:warn={soon}>{r.cert_days_left != null ? `${r.cert_days_left} days` : '—'}</span>
            <span role="cell">
              {#if !r.enabled}<StatusChip tone="off" label="site off" />
              {:else if r.cert_days_left == null}<StatusChip tone="warn" label="not served" title="The name may not point to this server yet, or Caddy is still getting the certificate" />
              {:else}<StatusChip tone={soon ? 'warn' : 'ok'} label={soon ? 'renew soon' : 'valid'} />{/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="dm-sub px-foot">Caddy renews certificates about 30 days before they expire. <InfoTip text="“Renew soon” means a renewal has not worked yet. The Activity tab shows why." /></p>
    {/if}
  {:else if tab === 'activity'}
    {#if activity.length === 0}
      <EmptyState title="No activity yet" description="Certificate events since Caddy started and the sites created in the last 7 days show up here." />
    {:else}
      <div class="dm-tbl" role="table" aria-label="Proxy activity">
        {#each activity as a, i (i)}
          <div class="dm-tr" class:dm-click={!!a.op} role="row" style="grid-template-columns: {ACT_COLS}" tabindex={a.op ? 0 : -1}
            onclick={() => a.op && operations.open(a.op.id)} onkeydown={(e) => { if (e.key === 'Enter' && a.op) operations.open(a.op.id); }}>
            <span role="cell" class="dm-num dm-dim" title={new Date(a.ts).toLocaleString()}>{fmtAgo(a.ts)}</span>
            <span role="cell">{a.what}</span>
            <span role="cell" class="dm-dim" title={a.text}>{a.text}</span>
            <span role="cell">
              {#if a.op && isActive(a.op)}<OpProgress op={a.op} compact />
              {:else}<StatusChip tone={a.tone} label={a.tone === 'ok' ? 'ok' : a.tone === 'warn' ? 'warning' : a.tone === 'bad' ? 'failed' : 'running'} />{/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="dm-sub px-foot">Certificate events come from Caddy's log since it last started (the last 200).</p>
    {/if}
  {:else}
    <CaddyTab running={!!status?.running} onchanged={load} />
  {/if}
</section>

<SiteModal bind:open={showSite} {editing} onsaved={load} />
<ImportModal bind:open={showImport} onimported={load} />

<style>
  .ok { color: var(--ok); }
  .bad { color: var(--bad); }
  .px-notice {
    display: flex; align-items: center; gap: 16px; padding: 12px 16px;
    border: 1px solid color-mix(in srgb, var(--warn) 45%, transparent);
    background: color-mix(in srgb, var(--warn) 8%, transparent);
    border-radius: var(--r);
  }
  .px-notice > div { flex: 1; min-width: 0; }
  .px-notice p { margin: 2px 0 0; color: var(--muted); font-size: var(--fs-sm); }
  .px-up { font-size: 12.5px; }
  .px-tls { font-weight: 500; }
  .dm-nm small.warn, .dm-num.warn { color: var(--warn); }
  .num-h, .num-c { text-align: right; }
  .px-foot { margin: 8px 0 0; }
</style>
