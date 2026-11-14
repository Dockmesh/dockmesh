<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo } from '$lib/format';
  import { fmtDateTime, fmtNumber } from '$lib/i18n';
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
  import { TLS_LABEL, certExpiresSoon, certNeedsAttention, optionSummary, skipsVerify, type Site, type ProxyStatus, type ACMEEvent } from './_site';

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
    if (r.tls_mode === 'none') return m.proxy_not_encrypted();
    if (r.cert_days_left == null) return m.proxy_no_cert_yet();
    return m.proxy_expires_in_d({ n: r.cert_days_left });
  }
  const siteHref = (r: Site) => `/proxy/${encodeURIComponent(r.host)}`;
  const opOf = (host: string) => operations.latest(opRef.proxySite(host), 'proxy.site');

  async function enableProxy() {
    busy = true;
    toast.info(m.proxy_starting(), m.proxy_starting_hint());
    try {
      await api.proxy.enable();
      toast.success(m.proxy_enabled_toast());
      await load();
    } catch (err) {
      toast.error(m.proxy_enable_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
  async function disableProxy() {
    if (!(await confirm.ask({
      title: m.proxy_stop_title(),
      message: m.proxy_stop_message(),
      body: m.proxy_stop_body(),
      confirmLabel: m.common_stop(), danger: true
    }))) return;
    busy = true;
    try {
      await api.proxy.disable();
      toast.info(m.proxy_stopped_toast());
      await load();
    } catch (err) {
      toast.error(m.proxy_stop_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function toggleEnabled(r: Site) {
    const next = !r.enabled;
    try {
      await api.proxy.setRouteEnabled(r.id, next);
      toast.success(next ? m.proxy_site_enabled() : m.proxy_site_disabled(), r.host);
      await load();
    } catch (err) {
      toast.error(m.proxy_change_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function deleteSite(r: Site) {
    if (!(await confirm.ask({
      title: m.proxy_delete_site_title(),
      message: m.proxy_delete_site_message({ host: r.host }),
      body: m.proxy_delete_site_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    try {
      await api.proxy.deleteRoute(r.id);
      toast.success(m.proxy_site_deleted(), r.host);
      await load();
    } catch (err) {
      toast.error(m.proxy_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkDelete() {
    const list = routes.filter((r) => selected.has(r.id));
    if (!(await confirm.ask({
      title: m.proxy_delete_sites_title(),
      message: m.proxy_delete_sites_message({ count: list.length }),
      body: list.map((r) => r.host).join(', '),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    let ok = 0, fail = 0;
    for (const r of list) {
      try { await api.proxy.deleteRoute(r.id); ok++; } catch { fail++; }
    }
    toast[fail ? 'warning' : 'success'](fail ? m.proxy_bulk_deleted_failed({ ok, fail }) : m.proxy_bulk_deleted({ ok }));
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
      { label: m.common_open(), kbd: '↵', href: siteHref(r) },
      { label: m.proxy_open_in_browser(), href: `${r.tls_mode === 'none' ? 'http' : 'https'}://${r.host}` }
    ];
    if (canUpdate) items.push({ label: m.common_edit(), onclick: () => openEdit(r) });
    const op = opOf(r.host);
    if (op) items.push({ label: m.proxy_show_creation_steps(), onclick: () => operations.open(op.id) });
    if (canDelete) items.push({ separator: true }, { label: m.common_delete_ellipsis(), danger: true, onclick: () => deleteSite(r) });
    return items;
  }

  // Activity merges Caddy's certificate events with the new-site
  // operations Dockmesh ran.
  type Activity = { ts: string; what: string; text: string; tone: 'ok' | 'warn' | 'bad' | 'off'; op?: Operation };
  const activity = $derived.by<Activity[]>(() => {
    const out: Activity[] = acme.map((e) => ({
      ts: e.ts,
      what: e.kind === 'renew' ? m.proxy_act_cert_renewed() : e.kind === 'obtain' ? m.proxy_sm_certificate() : m.proxy_act_cert_failed(),
      text: `${e.host ?? '—'}: ${e.message}${e.issuer ? ` (${e.issuer})` : ''}`,
      tone: e.successful ? 'ok' : 'bad'
    }));
    for (const o of siteOps) {
      const warn = o.steps.find((s) => s.status === 'warning');
      out.push({
        ts: o.finished_at ?? o.created_at,
        what: m.proxy_new_site(),
        text: `${o.target}${o.actor_name ? ` ${m.proxy_act_by({ name: o.actor_name })}` : ''}${o.error ? `: ${o.error}` : warn ? `: ${warn.detail}` : ''}`,
        tone: isActive(o) ? 'off' : o.status === 'failed' ? 'bad' : warn ? 'warn' : 'ok',
        op: o
      });
    }
    return out.sort((a, b) => b.ts.localeCompare(a.ts));
  });

  const SITE_COLS = '22px 14px minmax(180px, 1.4fr) minmax(160px, 1.2fr) 150px minmax(140px, 1fr) 80px 56px 36px';
  const CERT_COLS = 'minmax(200px, 1.4fr) minmax(160px, 1fr) 120px 120px';
  const ACT_COLS = '110px 150px minmax(0, 1fr) 110px';
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>{m.proxy_title()}</h1>
    <span class="dm-sub">
      {#if status === null}{m.proxy_status_unknown()}
      {:else if !status.enabled}{m.proxy_caddy_off()}
      {:else}Caddy{status.version ? ` ${status.version}` : ''} · <span class={status.running ? 'ok' : 'bad'}>{status.running ? m.proxy_running() : m.proxy_stopped()}</span>{/if}
      · {m.proxy_sites_live({ count: live })}
      {#if latestCaddy}· <a href="/proxy?tab=caddy" onclick={(e) => { e.preventDefault(); setTab('caddy'); }}>{m.proxy_version_recommended({ version: latestCaddy })}</a>{/if}
    </span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if canCreate && status?.enabled}
      <button type="button" class="dm-btn" onclick={() => (showImport = true)} disabled={!status.running} title={status.running ? '' : m.proxy_import_needs_running()}>{m.proxy_import_title()}</button>
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} strokeWidth={2} /> {m.proxy_new_site()}</button>
    {/if}
    {#if status?.running && canCreate}
      <Menu label={m.proxy_actions()} items={[{ label: m.proxy_stop_menu(), danger: true, icon: PowerOff, onclick: disableProxy }]} />
    {/if}
  </div>

  {#if status && (!status.enabled || !status.running)}
    <div class="px-notice" role="status">
      <div>
        <b>{!status.enabled ? m.proxy_notice_off() : m.proxy_notice_not_running()}</b>
        <p>{!status.enabled ? m.proxy_notice_off_hint() : m.proxy_notice_not_running_hint()}</p>
      </div>
      {#if canCreate}
        <button type="button" class="dm-btn dm-btn-primary" disabled={busy} onclick={enableProxy}><Power size={14} /> {status.enabled ? m.common_start() : m.proxy_enable()}</button>
      {/if}
    </div>
  {/if}

  {#if status?.enabled && status.foreign_ports?.length}
    <div class="px-notice" role="status">
      <div>
        <b>{m.proxy_notice_foreign_ports({ ports: status.foreign_ports.join(', ') })}</b>
        <p>{m.proxy_notice_foreign_ports_hint({ port: status.foreign_ports[0] })}</p>
      </div>
    </div>
  {/if}

  <Tabs
    tabs={[
      { key: 'sites', label: m.proxy_tab_sites(), count: routes.length },
      { key: 'certificates', label: m.proxy_tab_certificates(), count: certSites.length || null, dot: certWarn > 0 },
      { key: 'activity', label: m.proxy_tab_activity() },
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
      <EmptyState title={m.proxy_empty_title()} description={status?.enabled ? m.proxy_empty_hint() : m.proxy_empty_hint_off()}>
        {#snippet action()}
          {#if canCreate && status?.enabled}
            <button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} /> {m.proxy_new_site()}</button>
          {/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-toolbar">
        <input class="dm-filter" type="search" placeholder={m.proxy_filter_ph()} bind:value={search} aria-label={m.proxy_filter()} />
        <span class="dm-sp"></span>
        {#if selected.size && canDelete}
          <span class="dm-sub">{m.proxy_selected({ n: selected.size })}</span>
          <button type="button" class="dm-btn dm-btn-sm dm-btn-danger" onclick={bulkDelete}>{m.common_delete_ellipsis()}</button>
          <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => (selected = new Set())}>{m.proxy_clear()}</button>
        {/if}
      </div>
      <div class="dm-tbl" role="table" aria-label={m.proxy_tab_sites()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {SITE_COLS}">
          <span role="columnheader">{#if canDelete}<input type="checkbox" aria-label={m.proxy_select_all()} checked={allSelected} onchange={() => (selected = allSelected ? new Set() : new Set(visible.map((r) => r.id)))} />{/if}</span>
          <span role="columnheader"></span>
          <span role="columnheader">{m.proxy_col_site()}</span>
          <span role="columnheader">{m.proxy_sm_goes_to()}</span>
          <span role="columnheader">HTTPS</span>
          <span role="columnheader">{m.proxy_col_options()}</span>
          <span role="columnheader" class="num-h">Req/s</span>
          <span role="columnheader">{m.common_on()}</span>
          <span role="columnheader"></span>
        </div>
        {#each visible as r (r.id)}
          {@const op = opOf(r.host)}
          {@const opts = optionSummary(r.options)}
          {@const hm = perHost.get(r.host)}
          <div class="dm-tr dm-click" role="row" class:dm-sel={selected.has(r.id)} style="grid-template-columns: {SITE_COLS}"
            tabindex="0" onclick={() => goto(siteHref(r))} onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(siteHref(r)); }}>
            <span role="cell">
              {#if canDelete}<input type="checkbox" aria-label={m.proxy_select_one({ host: r.host })} checked={selected.has(r.id)} onclick={(e) => e.stopPropagation()} onchange={() => toggleOne(r.id)} />{/if}
            </span>
            <span role="cell"><span class="dm-dot {siteTone(r)}"></span></span>
            <span role="cell" class="dm-nm">
              <a href={siteHref(r)} onclick={(e) => e.stopPropagation()}>{r.host}</a>
              <small>{r.enabled ? (r.options?.routing?.www_redirect ? m.proxy_www_redirects_here({ host: r.host }) : ' ') : m.proxy_disabled()}</small>
            </span>
            <span role="cell" class="dm-mono px-up" title={r.upstream}>{r.upstream}</span>
            <span role="cell" class="dm-nm">
              {#if op && isActive(op)}
                <OpProgress {op} compact />
              {:else}
                <span class="px-tls">{TLS_LABEL[r.tls_mode]}{#if skipsVerify(r)} <span class="dm-badge dm-badge-warning" title={m.proxy_tls_unverified_title()}>{m.proxy_tls_unverified()}</span>{/if}</span>
                <small class:warn={r.enabled && certNeedsAttention(r)}>{httpsNote(r)}</small>
              {/if}
            </span>
            <span role="cell" class="dm-dim" title={opts.join(', ')}>
              {#if opts.length}{opts.slice(0, 2).join(', ')}{#if opts.length > 2} <span class="dm-num">+{opts.length - 2}</span>{/if}{:else}<span class="dm-dim">{m.proxy_defaults()}</span>{/if}
            </span>
            <span role="cell" class="dm-num num-c">{r.enabled && hm ? fmtNumber(hm.requests_per_second, { minimumFractionDigits: 1, maximumFractionDigits: 1 }) : '—'}</span>
            <span role="cell"><span role="presentation" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
              <Switch checked={r.enabled} label={m.proxy_serve({ host: r.host })} hideLabel disabled={!canUpdate} onchange={() => toggleEnabled(r)} />
            </span></span>
            <span role="cell" class="dm-tr-menu"><span role="presentation" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
              <Menu label={m.proxy_actions_for({ host: r.host })} items={menuFor(r)} />
            </span></span>
          </div>
        {/each}
        {#if visible.length === 0}<div class="dm-empty">{m.proxy_no_match({ q: search })}</div>{/if}
      </div>
      {#if metrics}
        <p class="dm-sub px-foot">
          {m.proxy_foot_metrics({ rps: fmtNumber(metrics.requests_per_second, { minimumFractionDigits: 1, maximumFractionDigits: 1 }), p95: fmtNumber(Math.round(metrics.p95_latency_ms)), errors: fmtNumber(metrics.status_buckets['5xx'] ?? 0) })}
        </p>
      {/if}
    {/if}
  {:else if tab === 'certificates'}
    {#if certSites.length === 0}
      <EmptyState title={m.proxy_no_https_title()} description={m.proxy_no_https_hint()} />
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.proxy_tab_certificates()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {CERT_COLS}">
          <span role="columnheader">{m.common_name()}</span><span role="columnheader">{m.proxy_col_issuer()}</span><span role="columnheader">{m.proxy_col_expires_in()}</span><span role="columnheader">{m.common_status()}</span>
        </div>
        {#each certSites as r (r.id)}
          {@const soon = certExpiresSoon(r)}
          <div class="dm-tr dm-click" role="row" style="grid-template-columns: {CERT_COLS}" tabindex="0"
            onclick={() => goto(siteHref(r))} onkeydown={(e) => { if (e.key === 'Enter') goto(siteHref(r)); }}>
            <span role="cell" class="dm-mono">{r.host}</span>
            <span role="cell" class="dm-dim">{r.cert_issuer || TLS_LABEL[r.tls_mode]}</span>
            <span role="cell" class="dm-num" class:warn={soon}>{r.cert_days_left != null ? m.proxy_days({ count: r.cert_days_left }) : '—'}</span>
            <span role="cell">
              {#if !r.enabled}<StatusChip tone="off" label={m.proxy_chip_site_off()} />
              {:else if r.cert_days_left == null}<StatusChip tone="warn" label={m.proxy_chip_not_served()} title={m.proxy_chip_not_served_hint()} />
              {:else}<StatusChip tone={soon ? 'warn' : 'ok'} label={soon ? m.proxy_chip_renew_soon() : m.proxy_chip_valid()} />{/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="dm-sub px-foot">{m.proxy_renew_note()} <InfoTip text={m.proxy_renew_tip()} /></p>
    {/if}
  {:else if tab === 'activity'}
    {#if activity.length === 0}
      <EmptyState title={m.proxy_no_activity_title()} description={m.proxy_no_activity_hint()} />
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.proxy_activity_table()}>
        {#each activity as a, i (i)}
          <div class="dm-tr" class:dm-click={!!a.op} role="row" style="grid-template-columns: {ACT_COLS}" tabindex={a.op ? 0 : -1}
            onclick={() => a.op && operations.open(a.op.id)} onkeydown={(e) => { if (e.key === 'Enter' && a.op) operations.open(a.op.id); }}>
            <span role="cell" class="dm-num dm-dim" title={fmtDateTime(a.ts)}>{fmtAgo(a.ts)}</span>
            <span role="cell">{a.what}</span>
            <span role="cell" class="dm-dim" title={a.text}>{a.text}</span>
            <span role="cell">
              {#if a.op && isActive(a.op)}<OpProgress op={a.op} compact />
              {:else}<StatusChip tone={a.tone} label={a.tone === 'ok' ? m.proxy_chip_ok() : a.tone === 'warn' ? m.proxy_chip_warning() : a.tone === 'bad' ? m.proxy_chip_failed() : m.proxy_running()} />{/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="dm-sub px-foot">{m.proxy_activity_note()}</p>
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
