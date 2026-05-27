<script lang="ts">
  // Reverse Proxy — embedded Caddy managed by Dockmesh. Public hostname
  // mappings with automatic TLS via Let's Encrypt or internal CA.
  //
  // Slice 1 scope: status hero + sortable route table + create/edit
  // modal + bulk delete. Per-route metrics, cert-expiry, policy chips,
  // ACME stream all wait on backend additions (punch-list).
  import { goto } from '$app/navigation';
  import { api, ApiError, isFanOut } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import {
    Globe, Plus, Trash2, Power, PowerOff, RefreshCw, Lock,
    Search, ExternalLink, Edit2, ChevronUp, ChevronDown,
  } from 'lucide-svelte';

  interface ProxyRoute {
    id: number;
    host: string;
    upstream: string;
    tls_mode: 'auto' | 'internal' | 'none';
    created_at?: string;
    updated_at?: string;
  }

  type Status = {
    enabled: boolean;
    running: boolean;
    admin_ok: boolean;
    version?: string;
    container?: string;
  };

  type MetricsSnapshot = {
    scraped_at: string;
    total_requests: number;
    requests_per_second: number;
    status_buckets: Record<string, number>;
    p95_latency_ms: number;
    per_host: Array<{
      host: string;
      requests: number;
      requests_per_second: number;
      status_buckets: Record<string, number>;
      p95_latency_ms: number;
    }>;
  };

  type ACMEEvent = {
    ts: string;
    kind: 'obtain' | 'renew' | 'failure';
    host?: string;
    issuer?: string;
    message: string;
    successful: boolean;
  };

  let status = $state<Status | null>(null);
  let routes = $state<ProxyRoute[]>([]);
  let metrics = $state<MetricsSnapshot | null>(null);
  let acmeEvents = $state<ACMEEvent[]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let search = $state('');

  // Sort
  type SortKey = 'host' | 'upstream' | 'tls' | 'created';
  let sortKey = $state<SortKey>('host');
  let sortAsc = $state(true);

  // Bulk
  let selected = $state<Set<number>>(new Set());
  let bulkBusy = $state(false);

  // Modal
  let showModal = $state(false);
  let editing = $state<ProxyRoute | null>(null);
  let formHost = $state('');
  let formUpstream = $state('');
  let formTls = $state<'auto' | 'internal' | 'none'>('auto');
  let saving = $state(false);

  // Container-suggestions for the upstream datalist. Caddy connects to
  // containers via the Docker network, so we suggest <container-name>:
  // <internal-port> for every reachable container with exposed ports.
  // The input stays free-form: operators can still type external
  // upstreams like 192.168.x.x:8080.
  let upstreamSuggestions = $state<Array<{ value: string; label: string }>>([]);

  async function loadUpstreamSuggestions() {
    try {
      const res = await api.containers.list(false, hosts.id);
      const items: any[] = isFanOut(res) ? (res.items as any[]) : (res as any[]);
      const out: Array<{ value: string; label: string }> = [];
      const seen = new Set<string>();
      for (const c of items) {
        const name = (c.Names?.[0] ?? '').replace(/^\//, '');
        if (!name) continue;
        const internalPorts = [
          ...new Set(
            (c.Ports ?? [])
              .map((p: any) => Number(p?.PrivatePort))
              .filter((n: number) => Number.isFinite(n) && n > 0),
          ),
        ];
        const hostHint = c.host_name ? ` · ${c.host_name}` : '';
        if (internalPorts.length === 0) {
          const v = `${name}:80`;
          if (!seen.has(v)) { out.push({ value: v, label: `${name} — ${c.Image}${hostHint}` }); seen.add(v); }
        } else {
          for (const p of internalPorts) {
            const v = `${name}:${p}`;
            if (!seen.has(v)) { out.push({ value: v, label: `${name}:${p} — ${c.Image}${hostHint}` }); seen.add(v); }
          }
        }
      }
      // Stable sort by container name then port.
      out.sort((a, b) => a.value.localeCompare(b.value));
      upstreamSuggestions = out;
    } catch {
      upstreamSuggestions = [];
    }
  }

  // Refresh suggestions whenever the modal opens.
  let lastModalOpen = false;
  $effect(() => {
    if (showModal && !lastModalOpen) loadUpstreamSuggestions();
    lastModalOpen = showModal;
  });

  async function load() {
    loading = true;
    try {
      const [s, r, m, e] = await Promise.all([
        api.proxy.status().catch(() => null),
        api.proxy.listRoutes().catch(() => []),
        api.proxy.metrics().catch(() => null),
        api.proxy.acmeEvents().catch(() => [] as ACMEEvent[]),
      ]);
      status = s;
      routes = r;
      metrics = m;
      acmeEvents = e;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (!allowed('proxy.update')) { goto('/'); return; }
    load();
  });
  // Caddy container state + cert lifecycle drift over time — 10s poll.
  $effect(() => autoRefresh(load, 10_000));

  // ── Derived ──────────────────────────────────────────────────────────
  const visible = $derived(
    routes
      .filter((r) => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        return r.host.toLowerCase().includes(q) || r.upstream.toLowerCase().includes(q);
      })
      .sort((a, b) => {
        let cmp = 0;
        switch (sortKey) {
          case 'host':     cmp = a.host.localeCompare(b.host); break;
          case 'upstream': cmp = a.upstream.localeCompare(b.upstream); break;
          case 'tls':      cmp = a.tls_mode.localeCompare(b.tls_mode); break;
          case 'created':  cmp = (a.created_at ?? '').localeCompare(b.created_at ?? ''); break;
        }
        return sortAsc ? cmp : -cmp;
      }),
  );

  const counts = $derived({
    total: routes.length,
    auto: routes.filter((r) => r.tls_mode === 'auto').length,
    internal: routes.filter((r) => r.tls_mode === 'internal').length,
    none: routes.filter((r) => r.tls_mode === 'none').length,
  });

  const allSelected = $derived(visible.length > 0 && visible.every((r) => selected.has(r.id)));

  function toggleAll() {
    if (allSelected) selected = new Set();
    else selected = new Set(visible.map((r) => r.id));
  }
  function toggleOne(id: number) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  function toggleSort(key: SortKey) {
    if (sortKey === key) sortAsc = !sortAsc;
    else { sortKey = key; sortAsc = true; }
  }

  // ── Status actions ──────────────────────────────────────────────────
  async function enableProxy() {
    busy = true;
    toast.info('Starting Caddy', 'pulling image if needed…');
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
      title: 'Stop Caddy proxy',
      message: 'Stop and remove the Caddy container?',
      body: 'Route configurations stay in the database. Start the proxy again to reapply them.',
      confirmLabel: 'Stop', danger: true,
    }))) return;
    busy = true;
    try {
      await api.proxy.disable();
      toast.info('Proxy disabled');
      await load();
    } catch (err) {
      toast.error('Disable failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  // ── Modal ────────────────────────────────────────────────────────────
  function openCreate() {
    editing = null;
    formHost = '';
    formUpstream = '';
    formTls = 'auto';
    showModal = true;
  }

  function openEdit(r: ProxyRoute) {
    editing = r;
    formHost = r.host;
    formUpstream = r.upstream;
    formTls = r.tls_mode;
    showModal = true;
  }

  async function saveRoute(e: Event) {
    e.preventDefault();
    if (!formHost.trim() || !formUpstream.trim()) return;
    saving = true;
    try {
      if (editing) {
        await api.proxy.updateRoute(editing.id, formUpstream.trim(), formTls);
        toast.success('Route updated', formHost);
      } else {
        await api.proxy.createRoute(formHost.trim(), formUpstream.trim(), formTls);
        toast.success('Route created', formHost);
      }
      showModal = false;
      await load();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      saving = false;
    }
  }

  async function deleteRoute(id: number, host: string) {
    if (!(await confirm.ask({
      title: 'Remove proxy route',
      message: `Remove route "${host}"?`,
      body: 'Incoming requests to this hostname will start returning 404 on the next Caddy reload.',
      confirmLabel: 'Remove', danger: true,
    }))) return;
    try {
      await api.proxy.deleteRoute(id);
      toast.success('Removed', host);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function bulkDelete() {
    if (!(await confirm.ask({
      title: 'Delete proxy routes',
      message: `Delete ${selected.size} route${selected.size === 1 ? '' : 's'}?`,
      body: 'Cannot be undone. Incoming requests to these hostnames will 404 on next reload.',
      confirmLabel: 'Delete', danger: true,
    }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const r of routes.filter((r) => selected.has(r.id))) {
      try { await api.proxy.deleteRoute(r.id); ok++; }
      catch { fail++; }
    }
    toast[fail === 0 ? 'success' : 'info'](`Deleted ${ok}${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await load();
  }

  // ── Helpers ─────────────────────────────────────────────────────────
  function tlsLabel(m: 'auto' | 'internal' | 'none'): string {
    if (m === 'auto')     return "Let's Encrypt";
    if (m === 'internal') return 'Internal CA';
    return 'HTTP only';
  }
  function tlsPillClass(m: 'auto' | 'internal' | 'none'): string {
    if (m === 'auto')     return 'pxy-tls-pill pxy-tls-auto';
    if (m === 'internal') return 'pxy-tls-pill pxy-tls-internal';
    return 'pxy-tls-pill pxy-tls-none';
  }
  function fmtDate(ts?: string): string {
    if (!ts) return '—';
    return new Date(ts).toLocaleDateString(undefined, {
      year: 'numeric', month: 'short', day: '2-digit',
    });
  }
</script>

<EditorialPage>
  <section class="pxy">
    <!-- ───────────────────────── Header ───────────────────────── -->
    <header class="pxy-header">
      <div class="pxy-header-text">
        <h1 class="ed-title pxy-title">Proxy routes</h1>
        <p class="ed-subtitle pxy-subtitle">
          {#if routes.length === 0}
            No routes configured
          {:else}
            {counts.total} route{counts.total === 1 ? '' : 's'} · {counts.auto} auto-TLS{counts.internal > 0 ? ` · ${counts.internal} internal CA` : ''}{counts.none > 0 ? ` · ${counts.none} HTTP-only` : ''}
          {/if}
        </p>
      </div>
    </header>

    <!-- ───────────────────── Status Hero Card ───────────────────── -->
    <div
      class="pxy-status"
      data-state={status === null ? 'unknown' : !status.enabled ? 'off' : status.running ? 'running' : 'stopped'}
    >
      <span class="pxy-status-icon">
        {#if status?.running}
          <Globe size={18} strokeWidth={1.5} />
        {:else}
          <PowerOff size={18} strokeWidth={1.5} />
        {/if}
      </span>

      <div class="pxy-status-text">
        {#if loading && status === null}
          <Skeleton width="40%" height="1.2rem" />
        {:else if !status?.enabled}
          <div class="pxy-status-title">Proxy disabled</div>
          <p class="ed-subtitle pxy-status-blurb">
            Enabling pulls the Caddy image (if missing), starts a managed container, and applies
            every route below.
          </p>
        {:else if status.running}
          <div class="pxy-status-title">
            Caddy <em class="ed-accent">running</em>
          </div>
          <div class="pxy-status-meta">
            {#if status.version}<span class="pxy-meta-item">{status.version}</span>{/if}
            <span class="pxy-meta-item">:80 / :443</span>
            {#if status.admin_ok}<span class="pxy-meta-item pxy-meta-ok">admin API</span>{/if}
            {#if status.container}<span class="pxy-meta-item">{status.container.slice(0, 12)}</span>{/if}
          </div>
        {:else}
          <div class="pxy-status-title">
            Caddy <em class="pxy-status-stopped-em">stopped</em>
          </div>
          <p class="ed-subtitle pxy-status-blurb">
            Enabled but container is not running. Start it to apply routes.
          </p>
        {/if}
      </div>

      <div class="pxy-status-actions">
        {#if status?.running}
          <button
            type="button"
            class="dm-btn dm-btn-secondary dm-btn-sm"
            onclick={disableProxy}
            disabled={busy}
          >
            <PowerOff size={12} strokeWidth={1.5} /> Stop
          </button>
        {:else if status?.enabled}
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            onclick={enableProxy}
            disabled={busy}
          >
            <Power size={12} strokeWidth={1.5} /> Start
          </button>
        {:else}
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            onclick={enableProxy}
            disabled={busy}
          >
            <Power size={12} strokeWidth={1.5} /> Enable proxy
          </button>
        {/if}
      </div>
    </div>

    <!-- ───────────────────── Toolbar ───────────────────── -->
    <div class="pxy-toolbar">
      <div class="pxy-search">
        <Search size={12} strokeWidth={1.5} class="pxy-search-icon" />
        <input
          type="search"
          placeholder="search hostname · upstream…"
          bind:value={search}
          class="ed-underline-input pxy-search-input"
        />
      </div>

      <span class="pxy-spacer"></span>

      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-sm"
        onclick={load}
        disabled={loading}
        title="Refresh"
      >
        <RefreshCw size={12} strokeWidth={1.5} class={loading ? 'pxy-spin' : ''} />
      </button>

      {#if status?.enabled}
        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          onclick={openCreate}
        >
          <Plus size={12} strokeWidth={1.5} /> Add route
        </button>
      {/if}
    </div>

    <!-- ───────────────────── Bulk action bar ───────────────────── -->
    {#if selected.size > 0}
      <div class="pxy-bulkbar">
        <span class="pxy-bulkbar-count">{selected.size} selected</span>
        <div class="pxy-bulkbar-actions">
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs pxy-danger-btn"
            onclick={bulkDelete}
            disabled={bulkBusy}
          >
            <Trash2 size={11} strokeWidth={1.5} /> Delete
          </button>
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs"
            onclick={() => (selected = new Set())}
          >
            Clear
          </button>
        </div>
      </div>
    {/if}

    <!-- ───────────────────── Table ───────────────────── -->
    {#if loading && routes.length === 0}
      <div class="pxy-loading"><Skeleton width="100%" height="6rem" /></div>
    {:else if routes.length === 0}
      <div class="pxy-empty">
        <Globe size={20} strokeWidth={1.4} />
        <p class="pxy-empty-title">No routes yet</p>
        <p class="ed-subtitle pxy-empty-blurb">
          {status?.enabled
            ? 'Add a host → upstream mapping and Caddy gets configured automatically.'
            : 'Enable the proxy first to start configuring routes.'}
        </p>
        {#if status?.enabled}
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={openCreate}>
            <Plus size={12} strokeWidth={1.5} /> Add first route
          </button>
        {/if}
      </div>
    {:else if visible.length === 0}
      <div class="pxy-empty">
        <Eyebrow>no match</Eyebrow>
        <p class="pxy-empty-title">No routes match „{search}".</p>
      </div>
    {:else}
      <div class="pxy-table">
        <div class="pxy-row pxy-row-head">
          <span class="pxy-col-check">
            <input type="checkbox" checked={allSelected} onchange={toggleAll} />
          </span>
          <button type="button" class="pxy-h" onclick={() => toggleSort('host')}>
            hostname
            {#if sortKey === 'host'}{#if sortAsc}<ChevronUp size={10} />{:else}<ChevronDown size={10} />{/if}{/if}
          </button>
          <button type="button" class="pxy-h" onclick={() => toggleSort('upstream')}>
            upstream
            {#if sortKey === 'upstream'}{#if sortAsc}<ChevronUp size={10} />{:else}<ChevronDown size={10} />{/if}{/if}
          </button>
          <button type="button" class="pxy-h" onclick={() => toggleSort('tls')}>
            TLS
            {#if sortKey === 'tls'}{#if sortAsc}<ChevronUp size={10} />{:else}<ChevronDown size={10} />{/if}{/if}
          </button>
          <button type="button" class="pxy-h" onclick={() => toggleSort('created')}>
            created
            {#if sortKey === 'created'}{#if sortAsc}<ChevronUp size={10} />{:else}<ChevronDown size={10} />{/if}{/if}
          </button>
          <span class="pxy-h pxy-h-right">·</span>
        </div>

        {#each visible as r (r.id)}
          <div class="pxy-row" class:pxy-row-selected={selected.has(r.id)}>
            <span class="pxy-col-check">
              <input
                type="checkbox"
                checked={selected.has(r.id)}
                onchange={() => toggleOne(r.id)}
              />
            </span>

            <div class="pxy-cell-host">
              <span class="pxy-host">{r.host}</span>
              <a
                href="https://{r.host}"
                target="_blank"
                rel="noopener"
                class="pxy-host-open"
                title="Open in browser"
                aria-label="Open in browser"
              >
                <ExternalLink size={10} strokeWidth={1.5} />
              </a>
            </div>

            <span class="pxy-cell-upstream">{r.upstream}</span>

            <span class={tlsPillClass(r.tls_mode)}>
              {#if r.tls_mode !== 'none'}
                <Lock size={9} strokeWidth={1.8} />
              {/if}
              {tlsLabel(r.tls_mode)}
            </span>

            <span class="pxy-cell-created">{fmtDate(r.created_at)}</span>

            <div class="pxy-cell-actions">
              <button
                type="button"
                class="pxy-icon-btn"
                onclick={() => openEdit(r)}
                title="Edit"
                aria-label="Edit"
              >
                <Edit2 size={11} strokeWidth={1.5} />
              </button>
              <button
                type="button"
                class="pxy-icon-btn pxy-icon-danger"
                onclick={() => deleteRoute(r.id, r.host)}
                title="Delete"
                aria-label="Delete"
              >
                <Trash2 size={11} strokeWidth={1.5} />
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <!-- ─────────────────────── Metrics + ACME timeline ─────────────────────── -->
    {#if metrics || acmeEvents.length > 0}
      <div class="pxy-insight-grid">
        {#if metrics}
          <div class="pxy-card pxy-metrics-card">
            <Eyebrow>traffic · last 5 min</Eyebrow>
            <div class="pxy-metric-row">
              <div class="pxy-metric">
                <div class="pxy-metric-value">{metrics.requests_per_second.toFixed(1)}</div>
                <div class="pxy-metric-label">req/s</div>
              </div>
              <div class="pxy-metric">
                <div class="pxy-metric-value">{metrics.p95_latency_ms.toFixed(0)}<span class="pxy-metric-unit">ms</span></div>
                <div class="pxy-metric-label">p95 latency</div>
              </div>
              <div class="pxy-metric">
                <div class="pxy-metric-value">{metrics.total_requests.toLocaleString()}</div>
                <div class="pxy-metric-label">total requests</div>
              </div>
            </div>
            {#if Object.keys(metrics.status_buckets).length > 0}
              <div class="pxy-status-row">
                {#each ['2xx','3xx','4xx','5xx'] as bucket}
                  <span class="pxy-status-pill pxy-status-{bucket}">
                    {bucket} · {(metrics.status_buckets[bucket] ?? 0).toLocaleString()}
                  </span>
                {/each}
              </div>
            {/if}
            {#if metrics.per_host.length > 0}
              <table class="pxy-per-host">
                <thead>
                  <tr><th>host</th><th>req/s</th><th>p95</th><th>2xx</th><th>4xx</th><th>5xx</th></tr>
                </thead>
                <tbody>
                  {#each metrics.per_host as h (h.host)}
                    <tr>
                      <td class="pxy-per-host-name">{h.host}</td>
                      <td>{h.requests_per_second.toFixed(1)}</td>
                      <td>{h.p95_latency_ms.toFixed(0)}ms</td>
                      <td>{(h.status_buckets['2xx'] ?? 0).toLocaleString()}</td>
                      <td>{(h.status_buckets['4xx'] ?? 0).toLocaleString()}</td>
                      <td>{(h.status_buckets['5xx'] ?? 0).toLocaleString()}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {:else}
              <p class="pxy-metrics-empty">
                Caddy hasn't reported any requests since the last restart.
              </p>
            {/if}
          </div>
        {/if}

        {#if acmeEvents.length > 0}
          <div class="pxy-card pxy-acme-card">
            <Eyebrow>ACME activity</Eyebrow>
            <ul class="pxy-acme-list">
              {#each [...acmeEvents].reverse() as ev}
                <li
                  class="pxy-acme-item"
                  class:pxy-acme-success={ev.successful}
                  class:pxy-acme-failure={!ev.successful}
                >
                  <span class="pxy-acme-kind pxy-acme-kind-{ev.kind}">{ev.kind}</span>
                  <span class="pxy-acme-host">{ev.host ?? '—'}</span>
                  <span class="pxy-acme-msg" title={ev.message}>{ev.message}</span>
                  <span class="pxy-acme-ts">{new Date(ev.ts).toLocaleString()}</span>
                </li>
              {/each}
            </ul>
          </div>
        {/if}
      </div>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── Create / Edit Modal ───────────────────────── -->
<EditorialModal
  bind:open={showModal}
  eyebrow={editing ? 'Edit route' : 'New route'}
  width={560}
>
  {#snippet title()}
    {#if editing}
      Edit <em class="ed-accent">{editing.host}</em>
    {:else}
      Add a <em>public hostname</em>
    {/if}
  {/snippet}

  <form id="pxy-form" class="pxy-form" onsubmit={saveRoute}>
    <Field label="Hostname" hint="Public DNS name. Wildcards (*.example.com) are allowed for auto-TLS with DNS-01.">
      <input
        class="dm-input pxy-input"
        bind:value={formHost}
        placeholder="grafana.example.com"
        disabled={editing !== null || saving}
      />
    </Field>

    <Field
      label="Upstream"
      hint="host:port — Caddy connects via the Docker network. Pick a container from the dropdown or type a custom upstream."
    >
      <input
        class="dm-input pxy-input"
        bind:value={formUpstream}
        list="pxy-upstream-suggestions"
        placeholder="container-name:8080 or 127.0.0.1:3000"
        disabled={saving}
        autocomplete="off"
      />
      <datalist id="pxy-upstream-suggestions">
        {#each upstreamSuggestions as s (s.value)}
          <option value={s.value} label={s.label}></option>
        {/each}
      </datalist>
    </Field>

    <Field label="TLS mode">
      <div class="pxy-tls-picker" role="radiogroup">
        {#each [
          { id: 'auto'     as const, label: "Let's Encrypt",   hint: 'requires public DNS' },
          { id: 'internal' as const, label: 'Internal CA',     hint: 'self-signed, for .local' },
          { id: 'none'     as const, label: 'HTTP only',       hint: 'no TLS' },
        ] as opt}
          <button
            type="button"
            class="pxy-tls-option"
            class:pxy-tls-active={formTls === opt.id}
            onclick={() => (formTls = opt.id)}
            disabled={saving}
          >
            <span class="pxy-tls-option-label">{opt.label}</span>
            <span class="pxy-tls-option-hint">{opt.hint}</span>
          </button>
        {/each}
      </div>
    </Field>
  </form>

  {#snippet footer()}
    <span></span>
    <div class="pxy-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showModal = false)}>
        Cancel
      </button>
      <button
        type="submit"
        form="pxy-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={saving || !formHost.trim() || !formUpstream.trim()}
      >
        {saving ? 'Saving…' : editing ? 'Save' : 'Create'}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .pxy {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .pxy-header {
    display: flex;
    align-items: flex-end;
    gap: 24px;
    flex-wrap: wrap;
  }
  .pxy-header-text { min-width: 0; max-width: 70ch; }
  .pxy-title {
    font-size: 28px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .pxy-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }

  /* ── Status hero ────────────────────────────────────────────── */
  .pxy-status {
    padding: 16px 18px;
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: 6px;
    display: grid;
    grid-template-columns: 44px 1fr auto;
    gap: 14px;
    align-items: center;
  }
  .pxy-status[data-state="running"] {
    border-color: color-mix(in srgb, var(--color-success-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 4%, var(--surface));
  }
  .pxy-status[data-state="stopped"] {
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 5%, var(--surface));
  }
  .pxy-status[data-state="off"] {
    border-color: var(--border);
    background: var(--surface);
  }
  .pxy-status-icon {
    width: 44px;
    height: 44px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg);
    color: var(--fg-subtle);
  }
  .pxy-status[data-state="running"] .pxy-status-icon {
    border-color: var(--color-success-500);
    color: var(--color-success-400);
  }
  .pxy-status[data-state="stopped"] .pxy-status-icon {
    border-color: var(--color-warning-500);
    color: var(--color-warning-400);
  }
  .pxy-status-text { min-width: 0; }
  .pxy-status-title {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .pxy-status-stopped-em {
    color: var(--color-warning-400);
    font-style: normal;
  }
  .pxy-status-blurb {
    margin-top: 4px;
    max-width: 56ch;
    font-size: 12px;
  }
  .pxy-status-meta {
    margin-top: 4px;
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
  }
  .pxy-meta-item {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .pxy-meta-ok { color: var(--color-success-400); }

  .pxy-status-actions {
    display: flex;
    gap: 6px;
    flex-shrink: 0;
  }

  /* ── Toolbar ────────────────────────────────────────────────── */
  .pxy-toolbar {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .pxy-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .pxy-search :global(.pxy-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .pxy-search-input {
    padding-left: 18px;
    font-size: 12.5px;
    font-family: var(--font-mono);
  }
  .pxy-spacer { flex: 1; }
  .pxy-spin { animation: pxy-spin 0.9s linear infinite; }
  @keyframes pxy-spin {
    from { transform: rotate(0deg); }
    to   { transform: rotate(360deg); }
  }

  /* ── Bulk action bar ────────────────────────────────────────── */
  .pxy-bulkbar {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 14px;
    border: 1px solid color-mix(in srgb, var(--color-brand-500) 35%, var(--border));
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-brand-500) 5%, var(--surface));
  }
  .pxy-bulkbar-count {
    font-size: 12.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .pxy-bulkbar-actions {
    display: flex;
    gap: 6px;
    margin-left: auto;
  }
  .pxy-danger-btn { color: var(--color-danger-400); }

  /* ── Table ──────────────────────────────────────────────────── */
  .pxy-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .pxy-row {
    display: grid;
    grid-template-columns: 28px minmax(220px, 1.4fr) minmax(200px, 1.4fr) 130px 100px 80px;
    gap: 14px;
    align-items: center;
    padding: 11px 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .pxy-row:last-child { border-bottom: 0; }
  .pxy-row-head {
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
    padding: 10px 14px;
  }
  .pxy-row-selected {
    background: color-mix(in srgb, var(--color-brand-500) 4%, transparent);
  }

  .pxy-col-check input { accent-color: var(--color-brand-500); }
  .pxy-h {
    background: transparent;
    border: 0;
    padding: 0;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .pxy-h:hover { color: var(--fg); }
  .pxy-h-right { text-align: right; justify-content: flex-end; }

  .pxy-cell-host {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .pxy-host {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pxy-host-open {
    color: var(--fg-subtle);
    display: inline-flex;
    text-decoration: none;
    flex-shrink: 0;
  }
  .pxy-host-open:hover { color: var(--accent-fg); }

  .pxy-cell-upstream {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pxy-tls-pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 8px;
    border-radius: 999px;
    border: 1px solid var(--border-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    width: fit-content;
  }
  .pxy-tls-auto {
    color: var(--color-success-400);
    border-color: color-mix(in srgb, var(--color-success-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 6%, transparent);
  }
  .pxy-tls-internal {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
    background: color-mix(in srgb, var(--accent) 6%, transparent);
  }
  .pxy-tls-none {
    color: var(--fg-subtle);
  }

  .pxy-cell-created {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }

  .pxy-cell-actions {
    display: inline-flex;
    justify-content: flex-end;
    gap: 2px;
  }
  .pxy-icon-btn {
    width: 24px;
    height: 24px;
    background: transparent;
    border: 0;
    border-radius: 4px;
    color: var(--fg-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .pxy-icon-btn:hover {
    color: var(--fg);
    background: var(--surface-hover);
  }
  .pxy-icon-danger { color: var(--color-danger-400); }
  .pxy-icon-danger:hover {
    color: var(--color-danger-400);
    background: color-mix(in srgb, var(--color-danger-500) 12%, transparent);
  }

  /* ── Loading / empty ────────────────────────────────────────── */
  .pxy-loading { padding: 0; }
  .pxy-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
    color: var(--fg-subtle);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
  }
  .pxy-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .pxy-empty-blurb {
    margin: 4px 0 12px;
    max-width: 50ch;
  }

  /* ── Modal ──────────────────────────────────────────────────── */
  .pxy-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .pxy-form :global(.dm-input) {
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .pxy-input {
    font-family: var(--font-mono);
  }
  .pxy-tls-picker {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 6px;
  }
  .pxy-tls-option {
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    display: flex;
    flex-direction: column;
    gap: 2px;
    color: var(--fg-muted);
    font: inherit;
    transition: border-color 120ms, background 120ms;
  }
  .pxy-tls-option:hover:not(:disabled) { border-color: var(--border-strong); }
  .pxy-tls-option:disabled { opacity: 0.5; cursor: not-allowed; }
  .pxy-tls-active {
    background: var(--accent-bg);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--fg);
  }
  .pxy-tls-option-label {
    font-size: 12.5px;
    font-weight: 500;
  }
  .pxy-tls-option-hint {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
  }
  .pxy-modal-actions {
    display: flex;
    gap: 8px;
  }

  @media (max-width: 880px) {
    .pxy-status {
      grid-template-columns: 44px 1fr;
    }
    .pxy-status-actions { grid-column: 1 / -1; justify-content: flex-end; }
    .pxy-row,
    .pxy-row-head { display: none; }
    .pxy-tls-picker { grid-template-columns: 1fr; }
  }

  /* ───────────────────── Metrics + ACME timeline ───────────────────── */
  .pxy-insight-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 24px;
    margin-top: 32px;
  }
  @media (max-width: 1100px) {
    .pxy-insight-grid { grid-template-columns: 1fr; }
  }
  .pxy-card {
    background: var(--ed-surface, #fff);
    border: 1px solid var(--ed-border, rgba(0, 0, 0, 0.08));
    border-radius: 6px;
    padding: 20px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .pxy-metric-row {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 16px;
  }
  .pxy-metric { display: flex; flex-direction: column; gap: 2px; }
  .pxy-metric-value {
    font-size: 1.6rem;
    font-weight: 600;
    font-feature-settings: 'tnum';
  }
  .pxy-metric-unit { font-size: 0.8em; font-weight: 400; opacity: 0.6; margin-left: 2px; }
  .pxy-metric-label {
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    opacity: 0.6;
  }
  .pxy-status-row { display: flex; gap: 8px; flex-wrap: wrap; }
  .pxy-status-pill {
    font-size: 0.72rem;
    padding: 3px 8px;
    border-radius: 999px;
    background: rgba(0, 0, 0, 0.04);
    font-feature-settings: 'tnum';
  }
  .pxy-status-2xx { background: rgba(34, 197, 94, 0.12); color: rgb(22, 101, 52); }
  .pxy-status-3xx { background: rgba(59, 130, 246, 0.12); color: rgb(30, 64, 175); }
  .pxy-status-4xx { background: rgba(234, 179, 8, 0.14); color: rgb(133, 77, 14); }
  .pxy-status-5xx { background: rgba(239, 68, 68, 0.14); color: rgb(153, 27, 27); }
  .pxy-per-host {
    width: 100%;
    font-size: 0.78rem;
    border-collapse: collapse;
  }
  .pxy-per-host th {
    text-align: left;
    text-transform: uppercase;
    font-size: 0.65rem;
    letter-spacing: 0.05em;
    opacity: 0.6;
    padding: 6px 4px;
    border-bottom: 1px solid var(--ed-border, rgba(0, 0, 0, 0.08));
  }
  .pxy-per-host td {
    padding: 6px 4px;
    border-bottom: 1px solid var(--ed-border-faint, rgba(0, 0, 0, 0.04));
    font-feature-settings: 'tnum';
  }
  .pxy-per-host-name { font-family: var(--ed-font-mono, ui-monospace, monospace); }
  .pxy-metrics-empty {
    font-size: 0.8rem;
    opacity: 0.6;
    margin: 0;
  }
  .pxy-acme-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .pxy-acme-item {
    display: grid;
    grid-template-columns: 70px 1fr 2fr auto;
    gap: 12px;
    align-items: center;
    font-size: 0.78rem;
    padding: 6px 10px;
    border-left: 2px solid var(--ed-border, rgba(0, 0, 0, 0.08));
  }
  .pxy-acme-success { border-left-color: rgb(34, 197, 94); }
  .pxy-acme-failure { border-left-color: rgb(239, 68, 68); }
  .pxy-acme-kind {
    font-size: 0.65rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 2px 6px;
    border-radius: 3px;
    background: rgba(0, 0, 0, 0.05);
    text-align: center;
  }
  .pxy-acme-kind-obtain { background: rgba(34, 197, 94, 0.12); color: rgb(22, 101, 52); }
  .pxy-acme-kind-renew { background: rgba(59, 130, 246, 0.12); color: rgb(30, 64, 175); }
  .pxy-acme-kind-failure { background: rgba(239, 68, 68, 0.14); color: rgb(153, 27, 27); }
  .pxy-acme-host { font-family: var(--ed-font-mono, ui-monospace, monospace); }
  .pxy-acme-msg {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    opacity: 0.8;
  }
  .pxy-acme-ts {
    font-size: 0.7rem;
    opacity: 0.5;
    font-feature-settings: 'tnum';
  }
</style>
