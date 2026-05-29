<script lang="ts">
  // Network detail — editorial. Header + summary tiles + tabs (Overview ·
  // Containers · IPAM). Uses /networks/{id} inspect endpoint. Back-link
  // to /resources?tab=networks. Delete action gated on network.write.
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import {
    ChevronLeft, Network as NetworkIcon, Trash2, Copy, Box, RefreshCw
  } from 'lucide-svelte';

  const id = $derived($page.params.id);
  const hostId = $derived($page.url.searchParams.get('host') || 'local');

  type Tab = 'overview' | 'containers' | 'ipam';
  let tab = $state<Tab>(((new URLSearchParams($page.url.search).get('tab')) as Tab) || 'overview');

  const SYSTEM_NETWORKS = new Set(['bridge', 'host', 'none']);

  let inspect = $state<any>(null);
  let inspectLoading = $state(true);
  async function loadInspect() {
    inspectLoading = true;
    try {
      inspect = await api.networks.inspect(id, hostId);
    } catch (err) {
      toast.error('Failed to load network', err instanceof ApiError ? err.message : undefined);
    } finally {
      inspectLoading = false;
    }
  }

  $effect(() => { id; hostId; loadInspect(); });

  $effect(() => {
    pageContext.set(inspect?.Name ?? id);
    return () => pageContext.clear();
  });

  function copy(s: string) {
    void copyWithToast(s, 'Copied');
  }

  // Connected containers come back from inspect.Containers as a
  // map<containerId, { Name, EndpointID, MacAddress, IPv4Address, IPv6Address }>.
  // Flatten into a sortable array; if the network is empty Docker omits
  // the field entirely.
  interface AttachedRow {
    id: string;
    name: string;
    ipv4: string;
    ipv6: string;
    mac: string;
  }
  const attached = $derived.by<AttachedRow[]>(() => {
    if (!inspect?.Containers) return [];
    const out: AttachedRow[] = [];
    for (const [cid, c] of Object.entries<any>(inspect.Containers)) {
      out.push({
        id: cid,
        name: (c.Name ?? cid).replace(/^\//, ''),
        ipv4: c.IPv4Address ?? '',
        ipv6: c.IPv6Address ?? '',
        mac: c.MacAddress ?? ''
      });
    }
    return out.sort((a, b) => a.name.localeCompare(b.name));
  });

  const owner = $derived(inspect?.Labels?.['com.docker.compose.project'] ?? null);
  const labelEntries = $derived(inspect?.Labels ? Object.entries(inspect.Labels) : []);
  const optionsEntries = $derived(inspect?.Options ? Object.entries(inspect.Options) : []);
  const ipamConfig = $derived(inspect?.IPAM?.Config ?? []);
  const isSystem = $derived(inspect && SYSTEM_NETWORKS.has(inspect.Name));
  const canWrite = $derived(allowed('networks.delete'));

  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toISOString().slice(0, 10);
  }
  function driverPill(d: string): 'success' | 'warning' | 'neutral' {
    if (d === 'overlay') return 'success';
    if (d === 'macvlan' || d === 'ipvlan') return 'warning';
    return 'neutral';
  }

  async function deleteNetwork() {
    if (!inspect) return;
    if (isSystem) {
      toast.error('Cannot delete', 'system network');
      return;
    }
    if (!(await confirm.ask({
      title: 'Delete network',
      message: `Delete network "${inspect.Name}"?`,
      body: 'Docker refuses if any container is still attached.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.networks.remove(id);
      toast.success('Deleted', inspect.Name);
      goto('/resources?tab=networks');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<section class="net-detail">
  <header class="net-detail-header">
    <div class="net-detail-back-row">
      <a href="/resources?tab=networks" class="net-detail-back" aria-label="Back to resources">
        <ChevronLeft size={14} strokeWidth={1.5} /> resources / networks
      </a>
    </div>
    <div class="net-detail-title-row">
      <div class="net-detail-title-text">
        <h1 class="ed-title net-detail-title">{inspect?.Name ?? id}</h1>
        <p class="ed-subtitle net-detail-subtitle">
          {inspect?.Driver ?? '—'} · {inspect?.Scope ?? '—'} · host {hostId}{owner ? ` · stack ${owner}` : ''}{isSystem ? ' · system' : ''}
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={loadInspect}>
          <RefreshCw size={12} strokeWidth={1.5} class={inspectLoading ? 'ed-spin' : ''} /> Refresh
        </button>
        {#if canWrite && !isSystem}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm net-detail-danger" onclick={deleteNetwork}>
            <Trash2 size={12} strokeWidth={1.5} /> Delete network
          </button>
        {/if}
      </div>
    </div>
  </header>

  <!-- ─── Summary tiles ─── -->
  <div class="net-detail-summary">
    <div class="ed-metric">
      <span class="ed-metric-label">Driver</span>
      <div class="ed-metric-value net-detail-tile-text">{inspect?.Driver ?? '—'}</div>
      <span class="ed-metric-meta">{inspect?.Scope ?? ''}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">Containers</span>
      <div class="ed-metric-value">{attached.length || (inspectLoading ? '…' : '0')}</div>
      <span class="ed-metric-meta">attached</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">Subnet</span>
      <div class="ed-metric-value net-detail-tile-text">{ipamConfig[0]?.Subnet ?? '—'}</div>
      <span class="ed-metric-meta">{ipamConfig[0]?.Gateway ? `gw ${ipamConfig[0].Gateway}` : 'no gateway'}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">Flags</span>
      <div class="net-detail-flags-tile">
        {#if inspect?.Internal}<span class="dm-pill dm-pill-warning net-detail-pill">internal</span>{/if}
        {#if inspect?.Attachable}<span class="dm-pill dm-pill-neutral net-detail-pill">attachable</span>{/if}
        {#if inspect?.IPv6Enabled}<span class="dm-pill dm-pill-neutral net-detail-pill">ipv6</span>{/if}
        {#if !inspect?.Internal && !inspect?.Attachable && !inspect?.IPv6Enabled}<span class="font-mono muted">none</span>{/if}
      </div>
      <span class="ed-metric-meta">scope flags</span>
    </div>
  </div>

  <!-- ─── Tabs ─── -->
  <div class="ed-tabs net-detail-tabs">
    <button type="button" class="ed-tab" class:active={tab === 'overview'} onclick={() => (tab = 'overview')}>
      Overview
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'containers'} onclick={() => (tab = 'containers')}>
      Containers <span class="count">{attached.length}</span>
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'ipam'} onclick={() => (tab = 'ipam')}>
      IPAM <span class="count">{ipamConfig.length}</span>
    </button>
  </div>

  <!-- ============================================================== -->
  <!--  OVERVIEW                                                       -->
  <!-- ============================================================== -->
  {#if tab === 'overview'}
    {#if inspectLoading && !inspect}
      <div class="dm-card net-detail-card-pad"><Skeleton width="80%" height="6rem" /></div>
    {:else if inspect}
      <div class="dm-card net-detail-overview">
        <dl class="net-detail-dl">
          <dt>Name</dt>
          <dd class="font-mono">{inspect.Name}</dd>
          <dt>ID</dt>
          <dd class="net-detail-mountpoint">
            <span class="font-mono muted">{inspect.Id}</span>
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => copy(inspect.Id)} title="Copy">
              <Copy size={11} strokeWidth={1.5} />
            </button>
          </dd>
          <dt>Driver</dt>
          <dd>
            <span class="dm-pill dm-pill-{driverPill(inspect.Driver)} net-detail-pill">{inspect.Driver}</span>
          </dd>
          <dt>Scope</dt>
          <dd class="font-mono">{inspect.Scope}</dd>
          <dt>Created</dt>
          <dd class="font-mono">{fmtDate(inspect.Created)}</dd>
          <dt>Internal</dt>
          <dd class="font-mono">{inspect.Internal ? 'yes — no external egress' : 'no'}</dd>
          <dt>Attachable</dt>
          <dd class="font-mono">{inspect.Attachable ? 'yes' : 'no'}</dd>
          <dt>IPv6</dt>
          <dd class="font-mono">{inspect.IPv6Enabled ? 'enabled' : 'disabled'}</dd>
          {#if labelEntries.length > 0}
            <dt>Labels</dt>
            <dd>
              <div class="net-detail-labels">
                {#each labelEntries as [k, v] (k)}
                  <span class="net-detail-label-chip font-mono">
                    <span class="net-detail-label-k">{k}</span>=<span class="net-detail-label-v">{v}</span>
                  </span>
                {/each}
              </div>
            </dd>
          {/if}
          {#if optionsEntries.length > 0}
            <dt>Options</dt>
            <dd>
              <pre class="net-detail-opts font-mono">{JSON.stringify(inspect.Options, null, 2)}</pre>
            </dd>
          {/if}
        </dl>
      </div>
    {/if}
  {/if}

  <!-- ============================================================== -->
  <!--  CONTAINERS                                                     -->
  <!-- ============================================================== -->
  {#if tab === 'containers'}
    {#if inspectLoading && attached.length === 0}
      <div class="dm-card net-detail-card-pad"><Skeleton width="80%" height="5rem" /></div>
    {:else if attached.length === 0}
      <div class="dm-card net-detail-card-pad">
        <EmptyState icon={Box} title="No containers attached" description="This network is currently empty. Containers attached to it will appear here with their per-network IP and MAC." />
      </div>
    {:else}
      <div class="net-detail-container-table">
        <div class="net-detail-container-row net-detail-container-row--head">
          <span>name</span>
          <span>ipv4</span>
          <span>ipv6</span>
          <span>mac</span>
        </div>
        {#each attached as c (c.id)}
          <a href={`/containers/${c.id}`} class="net-detail-container-row net-detail-container-row--data">
            <span class="font-mono net-detail-container-name">{c.name}</span>
            <span class="font-mono">{c.ipv4 || '—'}</span>
            <span class="font-mono">{c.ipv6 || '—'}</span>
            <span class="font-mono">{c.mac || '—'}</span>
          </a>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- ============================================================== -->
  <!--  IPAM                                                           -->
  <!-- ============================================================== -->
  {#if tab === 'ipam'}
    <div class="dm-card net-detail-card-pad">
      <div class="net-detail-ipam-head">
        <Eyebrow>IPAM</Eyebrow>
        <span class="font-mono net-detail-ipam-driver">driver: {inspect?.IPAM?.Driver ?? '—'}</span>
      </div>
      {#if ipamConfig.length === 0}
        <p class="net-detail-foot font-mono">No IPAM configuration declared. Docker chose a default subnet automatically.</p>
      {:else}
        <div class="net-detail-ipam-table">
          <div class="net-detail-ipam-row net-detail-ipam-row--head">
            <span>subnet</span>
            <span>gateway</span>
            <span>ip range</span>
            <span>aux addresses</span>
          </div>
          {#each ipamConfig as cfg, i (i)}
            <div class="net-detail-ipam-row">
              <span class="font-mono">{cfg.Subnet ?? '—'}</span>
              <span class="font-mono">{cfg.Gateway ?? '—'}</span>
              <span class="font-mono">{cfg.IPRange ?? '—'}</span>
              <span class="font-mono">
                {#if cfg.AuxiliaryAddresses && Object.keys(cfg.AuxiliaryAddresses).length > 0}
                  {Object.keys(cfg.AuxiliaryAddresses).length} entries
                {:else}—{/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}
      {#if inspect?.IPAM?.Options && Object.keys(inspect.IPAM.Options).length > 0}
        <p class="net-detail-foot font-mono" style="margin-top: 14px;">IPAM options:</p>
        <pre class="net-detail-opts font-mono">{JSON.stringify(inspect.IPAM.Options, null, 2)}</pre>
      {/if}
    </div>
  {/if}
</section>

<style>
  .net-detail { display: block; }

  /* ─── Header ─── */
  .net-detail-header { margin-bottom: 22px; }
  .net-detail-back-row { margin-bottom: 12px; }
  .net-detail-back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-decoration: none;
    letter-spacing: 0.04em;
  }
  .net-detail-back:hover { color: var(--fg); }
  .net-detail-title-row {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .net-detail-title-text { min-width: 0; max-width: 70ch; }
  .net-detail-title { font-size: 26px; }
  .net-detail-subtitle {
    margin-top: 6px;
    font-size: 12px;
    color: var(--fg-subtle);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .net-detail-pill { font-size: 9.5px; padding: 1px 6px; }
  .net-detail-danger { color: var(--color-danger-400); }

  /* ─── Summary ─── */
  .net-detail-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
    margin: 18px 0 14px;
  }
  @media (max-width: 720px) {
    .net-detail-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  .net-detail-tile-text {
    font-size: 14px;
    font-family: var(--font-mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .net-detail-flags-tile {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
    margin-top: 4px;
  }

  .net-detail-tabs { margin-top: 14px; }
  .net-detail-card-pad { padding: 22px 24px; margin-top: 18px; }

  /* ─── Overview dl/dt/dd ─── */
  .net-detail-overview { padding: 22px 24px 24px; margin-top: 18px; }
  .net-detail-dl {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 10px 18px;
    font-size: 13px;
    margin: 0;
  }
  .net-detail-dl dt {
    font-family: var(--font-mono);
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
    padding-top: 4px;
  }
  .net-detail-dl dd { margin: 0; color: var(--fg); }
  .net-detail-mountpoint {
    display: flex;
    align-items: center;
    gap: 8px;
    word-break: break-all;
  }
  .net-detail-labels { display: flex; flex-wrap: wrap; gap: 4px; }
  .net-detail-label-chip {
    display: inline-flex;
    align-items: center;
    gap: 1px;
    padding: 2px 6px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-size: 10.5px;
    color: var(--fg-muted);
    background: var(--surface);
  }
  .net-detail-label-k { color: var(--fg-subtle); }
  .net-detail-label-v { color: var(--fg); }
  .net-detail-opts {
    margin: 0;
    padding: 10px 12px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 4px;
    font-size: 12px;
    overflow: auto;
    color: var(--fg-muted);
  }
  .net-detail-foot {
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.6;
  }

  /* ─── Containers tab ─── */
  .net-detail-container-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin-top: 18px;
    overflow: hidden;
  }
  .net-detail-container-row {
    display: grid;
    grid-template-columns: minmax(180px, 1.6fr) minmax(140px, 1fr) minmax(140px, 1fr) minmax(140px, 1fr);
    gap: 14px;
    align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    text-decoration: none;
    color: var(--fg);
  }
  .net-detail-container-row:last-child { border-bottom: 0; }
  .net-detail-container-row--head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .net-detail-container-row--data:hover { background: var(--surface-hover); }
  .net-detail-container-name {
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* ─── IPAM tab ─── */
  .net-detail-ipam-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 14px;
  }
  .net-detail-ipam-driver { font-size: 11px; color: var(--fg-subtle); }
  .net-detail-ipam-table {
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }
  .net-detail-ipam-row {
    display: grid;
    grid-template-columns: 1.4fr 1fr 1fr 1fr;
    gap: 14px;
    align-items: center;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
  }
  .net-detail-ipam-row:last-child { border-bottom: 0; }
  .net-detail-ipam-row--head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .muted { color: var(--fg-subtle); }
  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
