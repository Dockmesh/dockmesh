<script lang="ts">
  // Hosts list — editorial rebuild based on `Dockmesh Wizard (6)/hosts.jsx`.
  // Sticky upgrade-policy strip when connected_pending > 0; filter pills
  // (All/Online/Offline/Pending/Agents) + name/hostname/tag search; table
  // with status dot, kind pill, tags, live cpu/mem/disk bars, container
  // count, version + upgrade-hint, last-seen, ellipsis menu. Bulk action
  // bar when rows are selected. AddHostModal is a 3-step inline flow.
  // See project_hosts_open_punch_list.md memory for deferred features.
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import {
    api, ApiError, isFanOut,
    type Agent, type AgentCreateResult, type AgentUpgradePolicy,
    type HostInfo
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Server, Plus, RefreshCw, Search, MoreVertical,
    ArrowDownToLine, ArrowUpCircle, Trash2, Check, Copy, X,
    AlertTriangle, ChevronRight
  } from 'lucide-svelte';

  let remoteAgents = $state<Agent[]>([]);
  let localHostInfo = $state<HostInfo | null>(null);
  let loading = $state(true);
  let containerCounts = $state<Record<string, number>>({});
  let metricsByHost = $state<Record<string, { cpu: number; memUsed: number; memTotal: number; disk: number } | null>>({});
  let upgradePolicy = $state<AgentUpgradePolicy | null>(null);

  // Filter + search
  let filter = $state<'all' | 'online' | 'offline' | 'pending' | 'agents'>('all');
  let search = $state('');

  // Selection + open menu
  let selected = $state<Set<string>>(new Set());
  let openMenu = $state<string | null>(null);
  let bulkBusy = $state(false);

  // Add-host modal
  let showAdd = $state(false);

  // Edit-tags modal (re-used from old legacy implementation)
  let tagsHostId = $state<string | null>(null);
  let tagsDraft = $state<string[]>([]);
  let tagInput = $state('');
  let tagSuggestions = $state<string[]>([]);
  let tagsBusy = $state(false);
  let hostTags = $state<Record<string, string[]>>({});

  // Treat the local host as a synthetic Agent-shape row pinned at top
  // of the list. Many actions are N/A on local (no drain, no revoke,
  // no upgrade). We use kind === 'local' to gate them in the row UI.
  type Row = Agent & { kind: 'local' | 'agent' };
  const rows = $derived.by<Row[]>(() => {
    const out: Row[] = [];
    if (localHostInfo) {
      out.push({
        id: 'local',
        name: localHostInfo.name,
        status: 'online',
        version: upgradePolicy?.server_version,
        kind: 'local',
        created_at: '',
        updated_at: ''
      } as Row);
    }
    for (const a of remoteAgents) {
      out.push({ ...a, kind: 'agent' });
    }
    return out;
  });

  const filteredRows = $derived(
    rows.filter((r) => {
      if (filter === 'online' && r.status !== 'online') return false;
      if (filter === 'offline' && r.status !== 'offline') return false;
      if (filter === 'pending' && r.status !== 'pending') return false;
      if (filter === 'agents' && r.kind !== 'agent') return false;
      if (search) {
        const q = search.toLowerCase();
        const tags = hostTags[r.id] ?? [];
        if (
          !r.name.toLowerCase().includes(q) &&
          !(r.hostname ?? '').toLowerCase().includes(q) &&
          !tags.some((t) => t.toLowerCase().includes(q))
        ) return false;
      }
      return true;
    })
  );

  const counts = $derived({
    all: rows.length,
    online: rows.filter((r) => r.status === 'online').length,
    offline: rows.filter((r) => r.status === 'offline').length,
    pending: rows.filter((r) => r.status === 'pending').length,
    agents: rows.filter((r) => r.kind === 'agent').length
  });

  // ─── Loaders ──────────────────────────────────────────────────────────
  async function loadAll() {
    if (!allowed('hosts.update') && !allowed('hosts.view')) return;
    loading = true;
    try {
      const [agents, hostList, policy] = await Promise.all([
        api.agents.list(),
        api.hosts.list().catch(() => []),
        api.agents.getUpgradePolicy().catch(() => null)
      ]);
      remoteAgents = agents;
      localHostInfo = hostList.find((h) => h.kind === 'local') ?? null;
      upgradePolicy = policy;
      // Tags + per-host metrics in parallel for every row.
      const tasks: Promise<void>[] = [];
      const allRows: { id: string }[] = [
        ...(localHostInfo ? [{ id: 'local' }] : []),
        ...agents.map((a) => ({ id: a.id }))
      ];
      for (const r of allRows) {
        tasks.push((async () => {
          try { hostTags[r.id] = await api.hosts.listTags(r.id); }
          catch { hostTags[r.id] = []; }
        })());
        tasks.push((async () => {
          try {
            const m = await api.system.metrics(r.id);
            if (!isFanOut(m)) {
              metricsByHost[r.id] = {
                cpu: m.cpu_percent, memUsed: m.mem_used, memTotal: m.mem_total,
                disk: m.disk_percent ?? 0
              };
            }
          } catch { metricsByHost[r.id] = null; }
        })());
        tasks.push((async () => {
          try {
            const s = await api.containers.summary(r.id);
            containerCounts[r.id] = s.total;
          } catch { containerCounts[r.id] = 0; }
        })());
      }
      await Promise.all(tasks);
    } catch (err) {
      toast.error('Failed to load hosts', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  let refreshTimer: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    loadAll();
    refreshTimer = setInterval(() => {
      if (document.visibilityState === 'visible') loadAll();
    }, 10_000);
  });
  onDestroy(() => { if (refreshTimer) clearInterval(refreshTimer); });

  // ─── Row actions ──────────────────────────────────────────────────────
  function toggleSelect(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  function toggleAll() {
    if (selected.size === filteredRows.length && filteredRows.length > 0) {
      selected = new Set();
    } else {
      selected = new Set(filteredRows.filter((r) => r.kind !== 'local').map((r) => r.id));
    }
  }

  let upgradingId = $state<string | null>(null);
  async function upgradeHost(id: string, name: string) {
    upgradingId = id;
    try {
      const res = await api.agents.upgrade(id);
      toast.success('Upgrade dispatched', `${name} → v${res.version}`);
      await loadAll();
    } catch (err) {
      toast.error('Upgrade failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      upgradingId = null;
    }
    openMenu = null;
  }

  async function revokeHost(r: Row) {
    if (r.kind === 'local') return;
    if (!(await confirm.ask({
      title: `Revoke ${r.name}`,
      message: `Revoke host "${r.name}"?`,
      body: 'The host’s agent certificate is invalidated. Containers running there are forgotten by Dockmesh — they keep running on the host until you stop them manually. You can re-enroll later by running the install script again.',
      confirmLabel: 'Revoke', danger: true
    }))) return;
    try {
      await api.agents.delete(r.id);
      toast.success('Revoked', r.name);
      await loadAll();
    } catch (err) {
      toast.error('Revoke failed', err instanceof ApiError ? err.message : undefined);
    }
    openMenu = null;
  }

  async function copyEnrollURL(_r: Row) {
    // The original install URL isn't stored after creation; provide a
    // best-effort URL pointing at the agent endpoint. For a real reissue,
    // use the rotate-token action.
    const url = `${window.location.origin}/api/v1/agents/enroll`;
    await copyWithToast(url, 'Enrollment URL copied');
    openMenu = null;
  }

  // Bulk
  async function bulkUpgrade() {
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const id of selected) {
      const r = rows.find((x) => x.id === id);
      if (!r || r.kind === 'local') continue;
      try { await api.agents.upgrade(id); ok++; }
      catch { fail++; }
    }
    bulkBusy = false;
    selected = new Set();
    toast[fail === 0 ? 'success' : 'info'](`${ok} upgraded`, fail > 0 ? `${fail} failed` : undefined);
    await loadAll();
  }
  async function bulkRevoke() {
    if (!(await confirm.ask({
      title: 'Revoke selected hosts', message: `Revoke ${selected.size} host${selected.size === 1 ? '' : 's'}?`,
      body: 'Each host’s agent certificate is invalidated.', confirmLabel: 'Revoke', danger: true
    }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const id of selected) {
      try { await api.agents.delete(id); ok++; }
      catch { fail++; }
    }
    bulkBusy = false;
    selected = new Set();
    toast[fail === 0 ? 'success' : 'info'](`${ok} revoked`, fail > 0 ? `${fail} failed` : undefined);
    await loadAll();
  }

  // Tags modal helpers (reused, simplified)
  async function openTagsModal(id: string) {
    tagsHostId = id;
    tagsDraft = [...(hostTags[id] ?? [])];
    tagInput = '';
    try { tagSuggestions = await api.hosts.allTags(); } catch { tagSuggestions = []; }
    openMenu = null;
  }
  function addTagDraft(t: string) {
    const v = t.trim().toLowerCase();
    if (!v || tagsDraft.includes(v)) { tagInput = ''; return; }
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(v)) { toast.error('Invalid tag', 'lowercase letters, digits, hyphens, 1-32 chars'); return; }
    if (tagsDraft.length >= 20) { toast.error('Max 20 tags'); return; }
    tagsDraft = [...tagsDraft, v];
    tagInput = '';
  }
  async function saveTags() {
    if (!tagsHostId) return;
    tagsBusy = true;
    try {
      const saved = await api.hosts.setTags(tagsHostId, tagsDraft);
      hostTags[tagsHostId] = saved;
      toast.success('Tags saved');
      tagsHostId = null;
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally { tagsBusy = false; }
  }

  // Helpers
  function fmtAgo(iso?: string): string {
    if (!iso) return 'never';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return `${Math.round(d)}s ago`;
    if (d < 3600) return `${Math.round(d / 60)}m ago`;
    if (d < 86400) return `${Math.round(d / 3600)}h ago`;
    return `${Math.round(d / 86400)}d ago`;
  }
  function fmtUptime(iso?: string): string {
    if (!iso) return '';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return `up ${Math.round(d)}s`;
    if (d < 3600) return `up ${Math.round(d / 60)}m`;
    if (d < 86400) return `up ${Math.round(d / 3600)}h`;
    return `up ${Math.round(d / 86400)}d`;
  }

  function statusDot(s: string): string {
    if (s === 'online') return 'ok-dot';
    if (s === 'offline') return 'fail-dot';
    if (s === 'pending') return 'warn-dot';
    return 'neutral-dot';
  }
</script>

<section class="ed-hosts">
  {#if !allowed('hosts.update') && !allowed('hosts.view')}
    <div class="dm-card" style="padding: 32px;">
      <EmptyState icon={Server} title="Admin-only" description="Host management requires hosts.view permission." />
    </div>
  {:else}
    <!-- Header -->
    <header class="ed-hosts-header">
      <div class="ed-hosts-header-text">
        <h1 class="ed-title ed-hosts-title">Hosts</h1>
        <p class="ed-hosts-subtitle">
          {counts.all} host{counts.all === 1 ? '' : 's'} ·
          {counts.online} online{counts.offline > 0 ? ` · ${counts.offline} offline` : ''}{counts.pending > 0 ? ` · ${counts.pending} pending` : ''}
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={loadAll} title="Refresh">
          <RefreshCw size={12} strokeWidth={1.5} class={loading ? 'ed-spin' : ''} /> Refresh
        </button>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => (showAdd = true)}>
          <Plus size={12} strokeWidth={1.5} /> Add host
        </button>
      </div>
    </header>

    <!-- Upgrade-policy strip (only when relevant) -->
    {#if upgradePolicy && (upgradePolicy.connected_pending > 0 || upgradePolicy.connected_total > 0)}
      <div class="ed-hosts-policy" class:alert={upgradePolicy.connected_pending > 0}>
        <div class="ed-hosts-policy-mark">
          <RefreshCw size={14} strokeWidth={1.5} />
        </div>
        <div class="ed-hosts-policy-text">
          <div class="ed-hosts-policy-head">
            <Eyebrow>Agent upgrade policy</Eyebrow>
            <span class="font-mono ed-hosts-policy-meta">
              mode <span class="ed-hosts-policy-mode">{upgradePolicy.mode}</span>
              {#if upgradePolicy.mode === 'staged' && upgradePolicy.stage_percent}
                · stage {upgradePolicy.stage_percent}%
              {/if}
            </span>
          </div>
          <div class="ed-hosts-policy-body">
            {#if upgradePolicy.connected_pending > 0}
              <span class="ed-accent-mono">{upgradePolicy.connected_pending}</span>
              {upgradePolicy.connected_pending === 1 ? 'host is' : 'hosts are'}
              behind server v{upgradePolicy.server_version}.
              <span class="ed-hosts-policy-detail">
                {upgradePolicy.connected_up_to_date} of {upgradePolicy.connected_total} up-to-date.
              </span>
            {:else}
              All <span class="ed-accent-mono">{upgradePolicy.connected_total}</span> connected agents on v{upgradePolicy.server_version}.
            {/if}
          </div>
          {#if upgradePolicy.last_run_at}
            <div class="ed-hosts-policy-footnote font-mono">
              last run {fmtAgo(upgradePolicy.last_run_at)} · server v{upgradePolicy.server_version}
            </div>
          {/if}
        </div>
        <div class="ed-actions">
          {#if upgradePolicy.connected_pending > 0 && upgradePolicy.mode !== 'manual'}
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={async () => { try { upgradePolicy = await api.agents.runUpgradePolicy(); toast.success('Evaluation triggered'); await loadAll(); } catch (e) { toast.error('Failed', e instanceof ApiError ? e.message : undefined); } }}>
              <RefreshCw size={11} strokeWidth={1.5} /> Run policy now
            </button>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Filter + search bar -->
    <div class="ed-hosts-filterbar">
      <div class="ed-hosts-pills">
        {#each [
          ['all', 'All', counts.all],
          ['online', 'Online', counts.online],
          ['offline', 'Offline', counts.offline],
          ['pending', 'Pending', counts.pending],
          ['agents', 'Agents', counts.agents]
        ] as [id, label, count] (id)}
          <button
            type="button"
            class="ed-pill"
            class:active={filter === id}
            onclick={() => (filter = id as typeof filter)}
          >{label}<span class="ed-count">{count}</span></button>
        {/each}
      </div>
      <div class="ed-hosts-search">
        <Search size={12} strokeWidth={1.5} class="ed-hosts-search-icon" />
        <input
          type="text"
          class="ed-underline-input"
          placeholder="name, hostname, tag…"
          bind:value={search}
        />
      </div>
    </div>

    <!-- Bulk-action bar -->
    {#if selected.size > 0}
      <div class="ed-hosts-bulkbar">
        <span class="font-mono ed-hosts-bulk-count">
          <span class="ed-accent-mono">{selected.size}</span> selected
        </span>
        <span style="flex: 1"></span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" disabled={bulkBusy} onclick={bulkUpgrade}>
          <ArrowUpCircle size={11} strokeWidth={1.5} /> Bulk upgrade
        </button>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs ed-hosts-bulk-danger" disabled={bulkBusy} onclick={bulkRevoke}>
          <Trash2 size={11} strokeWidth={1.5} /> Revoke
        </button>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (selected = new Set())}>Clear</button>
      </div>
    {/if}

    <!-- Hosts table -->
    {#if loading && rows.length === 0}
      <div class="dm-card" style="padding: 22px;">
        <Skeleton width="80%" height="6rem" />
      </div>
    {:else if filteredRows.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={Server} title="No hosts match" description={search || filter !== 'all' ? 'Adjust filters or search.' : 'Add your first remote host to spread workload.'}>
          {#snippet action()}
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => (showAdd = true)}>
              <Plus size={12} strokeWidth={1.5} /> Add host
            </button>
          {/snippet}
        </EmptyState>
      </div>
    {:else}
      <div class="ed-hosts-table">
        <div class="ed-hosts-row ed-hosts-row-head">
          <input type="checkbox" checked={selected.size > 0 && selected.size === filteredRows.filter((r) => r.kind !== 'local').length} onchange={toggleAll} />
          <span>host</span>
          <span>tags</span>
          <span class="right">cpu</span>
          <span class="right">mem</span>
          <span class="right">disk</span>
          <span class="right">cont.</span>
          <span>version</span>
          <span>last seen</span>
          <span></span>
        </div>
        {#each filteredRows as r (r.id)}
          {@const m = metricsByHost[r.id]}
          {@const memPct = m && m.memTotal > 0 ? Math.round((m.memUsed / m.memTotal) * 100) : null}
          {@const cpuPct = m ? Math.round(m.cpu) : null}
          {@const diskPct = m ? Math.round(m.disk) : null}
          {@const ctrCount = containerCounts[r.id]}
          {@const tags = hostTags[r.id] ?? []}
          {@const needsUpgrade = r.version && upgradePolicy && r.version !== upgradePolicy.server_version && r.kind === 'agent'}
          <div
            class="ed-hosts-row"
            class:pending={r.status === 'pending'}
            class:offline={r.status === 'offline'}
          >
            <input
              type="checkbox"
              disabled={r.kind === 'local'}
              checked={selected.has(r.id)}
              onchange={() => toggleSelect(r.id)}
            />
            <div class="ed-hosts-row-host">
              <div class="ed-hosts-row-name-line">
                <span class={statusDot(r.status)}></span>
                <a href="/hosts/{r.id}" class="ed-hosts-row-name">{r.name}</a>
                {#if r.kind === 'local'}
                  <span class="dm-pill ed-hosts-row-pill ed-hosts-row-pill-local">local</span>
                {:else}
                  <span class="dm-pill dm-pill-neutral ed-hosts-row-pill">agent</span>
                {/if}
                {#if r.status === 'pending'}<span class="dm-pill dm-pill-warning ed-hosts-row-pill">pending</span>{/if}
                {#if r.status === 'offline'}<span class="dm-pill dm-pill-danger ed-hosts-row-pill">offline</span>{/if}
              </div>
              <div class="ed-hosts-row-sub font-mono">
                {#if r.hostname}{r.hostname}{:else if r.kind === 'local'}localhost{:else}<em class="ed-hosts-row-sub-empty">no hostname yet</em>{/if}
                {#if r.os}· {r.os}/{r.arch}{/if}
                {#if r.online_since}· {fmtUptime(r.online_since)}{/if}
              </div>
            </div>
            <div class="ed-hosts-row-tags">
              {#if tags.length === 0}<span class="ed-hosts-row-tags-empty font-mono">—</span>{/if}
              {#each tags.slice(0, 3) as t (t)}
                <span class="ed-hosts-tag-chip">{t}</span>
              {/each}
              {#if tags.length > 3}<span class="ed-hosts-tag-extra font-mono">+{tags.length - 3}</span>{/if}
            </div>
            <div class="ed-hosts-bar-cell" class:warn={cpuPct != null && cpuPct > 80}>
              {#if cpuPct == null}<span class="font-mono ed-hosts-bar-empty">—</span>
              {:else}
                <span class="font-mono">{cpuPct}%</span>
                <div class="ed-hosts-bar"><span class="ed-hosts-bar-fill" style="width: {Math.min(100, cpuPct)}%"></span></div>
              {/if}
              <!-- Reserved hint slot: keeps CPU vertically aligned with
                   MEM (which has a GB hint). Empty placeholder so all
                   bar cells have identical height. -->
              <div class="ed-hosts-bar-hint font-mono">&nbsp;</div>
            </div>
            <div class="ed-hosts-bar-cell">
              {#if memPct == null}<span class="font-mono ed-hosts-bar-empty">—</span>
              {:else}
                <span class="font-mono">{memPct}%</span>
                <div class="ed-hosts-bar"><span class="ed-hosts-bar-fill" style="width: {Math.min(100, memPct)}%"></span></div>
              {/if}
              <div class="ed-hosts-bar-hint font-mono">
                {#if m && m.memTotal > 0}{(m.memUsed / 1024 / 1024 / 1024).toFixed(1)}/{(m.memTotal / 1024 / 1024 / 1024).toFixed(0)} GB{:else}&nbsp;{/if}
              </div>
            </div>
            <div class="ed-hosts-bar-cell" class:warn={diskPct != null && diskPct > 80}>
              {#if diskPct == null}<span class="font-mono ed-hosts-bar-empty">—</span>
              {:else}
                <span class="font-mono">{diskPct}%</span>
                <div class="ed-hosts-bar"><span class="ed-hosts-bar-fill" style="width: {Math.min(100, diskPct)}%"></span></div>
              {/if}
              <div class="ed-hosts-bar-hint font-mono">&nbsp;</div>
            </div>
            <span class="ed-hosts-row-num font-mono">{ctrCount ?? '—'}</span>
            <div class="ed-hosts-row-version">
              <span class="font-mono">{r.version ? `v${r.version}` : '—'}</span>
              {#if needsUpgrade}
                <span class="ed-hosts-row-upgrade font-mono">↑ v{upgradePolicy?.server_version}</span>
              {/if}
            </div>
            <span class="font-mono ed-hosts-row-seen">
              {#if r.kind === 'local'}—{:else}{fmtAgo(r.last_seen_at)}{/if}
            </span>
            <div class="ed-hosts-row-actions">
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={(e) => { e.stopPropagation(); openMenu = openMenu === r.id ? null : r.id; }} aria-label="Actions">
                <MoreVertical size={13} strokeWidth={1.5} />
              </button>
              {#if openMenu === r.id}
                <div class="ed-hosts-row-menu">
                  <a href="/hosts/{r.id}" class="ed-hosts-menu-item">Open detail <ChevronRight size={11} strokeWidth={1.5} /></a>
                  <div class="ed-hosts-menu-sep"></div>
                  <button type="button" class="ed-hosts-menu-item" onclick={() => openTagsModal(r.id)}>Edit tags</button>
                  {#if r.kind === 'agent'}
                    {#if needsUpgrade}
                      <button type="button" class="ed-hosts-menu-item" disabled={upgradingId === r.id} onclick={() => upgradeHost(r.id, r.name)}>
                        {upgradingId === r.id ? 'Upgrading…' : 'Upgrade agent'}
                      </button>
                    {/if}
                    <button type="button" class="ed-hosts-menu-item" onclick={() => copyEnrollURL(r)}>Copy enrollment URL</button>
                    <div class="ed-hosts-menu-sep"></div>
                    <button type="button" class="ed-hosts-menu-item ed-hosts-menu-danger" onclick={() => revokeHost(r)}>Revoke…</button>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
      <p class="font-mono ed-hosts-foot">auto-refresh every 10s</p>
    {/if}
  {/if}
</section>

<!-- ─── Add Host Modal (3-step inline flow) ─── -->
{#if showAdd}
  {#await import('./_AddHostModal.svelte') then mod}
    <mod.default
      onclose={() => (showAdd = false)}
      onconnected={async () => { showAdd = false; await loadAll(); }}
      knownTags={[...new Set([...tagSuggestions, ...Object.values(hostTags).flat()])]}
    />
  {/await}
{/if}

<!-- ─── Edit Tags Modal ─── -->
{#if tagsHostId}
  <div class="ed-modal-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget) tagsHostId = null; }}>
    <div class="ed-modal" role="dialog" aria-modal="true" style="width: min(480px, 92vw);">
      <header class="ed-modal-head">
        <div>
          <Eyebrow>Tags · {rows.find((r) => r.id === tagsHostId)?.name}</Eyebrow>
          <h2 class="ed-modal-title">Manage tags</h2>
        </div>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (tagsHostId = null)} aria-label="Close">
          <X size={13} strokeWidth={1.5} />
        </button>
      </header>
      <div class="ed-modal-body">
        <p class="ed-modal-hint">
          Tags drive host-affinity rules. Lowercase letters, digits, hyphens. Max 20.
        </p>
        <div class="ed-host-tags-edit">
          {#each tagsDraft as t (t)}
            <span class="ed-hosts-tag-chip">
              {t}
              <button type="button" class="ed-host-tags-remove" onclick={() => (tagsDraft = tagsDraft.filter((x) => x !== t))} aria-label="Remove">
                <X size={10} strokeWidth={1.5} />
              </button>
            </span>
          {/each}
          <input
            type="text"
            class="ed-host-tags-input font-mono"
            placeholder={tagsDraft.length === 0 ? 'edge, production…' : '+ add'}
            bind:value={tagInput}
            onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTagDraft(tagInput); } }}
          />
        </div>
        {#if tagSuggestions.filter((s) => !tagsDraft.includes(s)).length > 0}
          <div class="ed-host-tags-suggest">
            <span class="font-mono ed-host-tags-suggest-label">fleet</span>
            {#each tagSuggestions.filter((s) => !tagsDraft.includes(s)) as s (s)}
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => addTagDraft(s)}>+ {s}</button>
            {/each}
          </div>
        {/if}
      </div>
      <footer class="ed-modal-foot">
        <span class="ed-modal-foot-status font-mono">{tagsDraft.length} tag{tagsDraft.length === 1 ? '' : 's'}</span>
        <div class="ed-actions">
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (tagsHostId = null)}>Cancel</button>
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={tagsBusy} onclick={saveTags}>
            {tagsBusy ? 'Saving…' : 'Save tags'}
          </button>
        </div>
      </footer>
    </div>
  </div>
{/if}

<style>
  .ed-hosts { display: flex; flex-direction: column; gap: 22px; }
  .ed-hosts-header { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; flex-wrap: wrap; }
  .ed-hosts-header-text { min-width: 0; max-width: 70ch; flex: 1; }
  .ed-hosts-title { font-size: 26px; }
  .ed-hosts-subtitle { font-size: 13px; color: var(--fg-muted); margin: 8px 0 0; line-height: 1.55; max-width: 70ch; }

  /* ─── Status dots (mockup standard) ─── */
  :global(.ok-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--color-success-400); }
  :global(.warn-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--color-warning-400); }
  :global(.fail-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--color-danger-400); }
  :global(.neutral-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--fg-subtle); }
  :global(.ed-spin) { animation: ed-spin 1s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
  .ed-accent-mono { color: var(--accent-fg); font-family: var(--font-mono); font-style: normal; font-weight: 500; }

  /* ─── Upgrade-policy strip ─── */
  .ed-hosts-policy {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    padding: 14px 18px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-elevated);
  }
  .ed-hosts-policy.alert {
    border-color: color-mix(in srgb, var(--color-warning-500) 50%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 5%, var(--bg-elevated));
  }
  .ed-hosts-policy-mark {
    width: 28px; height: 28px;
    border-radius: 5px;
    border: 1px solid var(--border);
    display: inline-flex; align-items: center; justify-content: center;
    color: var(--fg-muted);
    flex-shrink: 0;
  }
  .ed-hosts-policy.alert .ed-hosts-policy-mark { color: var(--color-warning-400); border-color: color-mix(in srgb, var(--color-warning-500) 30%, var(--border)); }
  .ed-hosts-policy-text { flex: 1; min-width: 0; }
  .ed-hosts-policy-head { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
  .ed-hosts-policy-meta { font-size: 10.5px; color: var(--fg-subtle); text-transform: uppercase; letter-spacing: 0.06em; }
  .ed-hosts-policy-mode { color: var(--accent-fg); font-style: normal; }
  .ed-hosts-policy-body { margin-top: 6px; font-size: 13.5px; color: var(--fg); }
  .ed-hosts-policy-detail { color: var(--fg-muted); }
  .ed-hosts-policy-footnote { margin-top: 4px; font-size: 11px; color: var(--fg-subtle); }

  /* ─── Filter bar ─── */
  .ed-hosts-filterbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .ed-hosts-pills { display: inline-flex; flex-wrap: wrap; gap: 4px; }
  .ed-pill {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 5px 10px;
    border: 1px solid var(--border);
    background: transparent;
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    cursor: pointer;
  }
  .ed-pill:hover { background: var(--surface-hover); color: var(--fg); }
  .ed-pill.active {
    background: var(--bg-elevated);
    border-color: var(--border-strong);
    color: var(--fg);
  }
  /* ed-pill-count moved to global .ed-count in app.css */

  .ed-hosts-search { position: relative; flex: 0 1 280px; min-width: 200px; margin-left: auto; }
  :global(.ed-hosts-search-icon) { position: absolute; left: 0; top: 50%; transform: translateY(-50%); color: var(--fg-subtle); }
  .ed-hosts-search .ed-underline-input { padding-left: 22px; font-size: 12px; }

  .ed-underline-input {
    width: 100%;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    padding: 6px 2px;
    font-size: 13px;
    color: var(--fg);
    font-family: var(--font-mono);
    outline: none;
    transition: border-color 0.1s;
  }
  .ed-underline-input::placeholder { color: var(--fg-subtle); }
  .ed-underline-input:focus { border-bottom-color: var(--accent); }

  /* ─── Bulk-action bar ─── */
  .ed-hosts-bulkbar {
    display: flex; align-items: center; gap: 8px;
    padding: 10px 14px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 5px;
  }
  .ed-hosts-bulk-count { font-size: 11.5px; color: var(--fg); }
  .ed-hosts-bulk-danger { color: var(--color-danger-400); }

  /* ─── Hosts table ─── */
  .ed-hosts-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: visible;
  }
  /* All data columns get fixed widths; host column is capped so it
     doesn't grab leftover space. Tags column is the ONLY flex column
     (1fr) — extra width on wide viewports goes there. Version column
     gets ellipsis truncation since dev-build version strings can be
     long ("dev-design-h1c-square-counts"). */
  .ed-hosts-row {
    display: grid;
    grid-template-columns:
      22px                  /* checkbox        */
      minmax(240px, 320px)  /* host name + sub */
      minmax(120px, 1fr)    /* tags — flex     */
      90px                  /* cpu             */
      130px                 /* mem (+GB hint)  */
      90px                  /* disk            */
      60px                  /* container count */
      120px                 /* version         */
      90px                  /* last seen       */
      32px;                 /* more menu       */
    align-items: center;
    gap: 14px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
  }
  .ed-hosts-row:last-child { border-bottom: 0; }
  .ed-hosts-row-head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .ed-hosts-row.pending { background: color-mix(in srgb, var(--color-warning-500) 4%, transparent); }
  .ed-hosts-row.offline { opacity: 0.65; }
  .ed-hosts-row .right { text-align: right; }
  .ed-hosts-row-host { min-width: 0; }
  .ed-hosts-row-name-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .ed-hosts-row-name {
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg);
    text-decoration: none;
    font-weight: 500;
  }
  .ed-hosts-row-name:hover { color: var(--accent-fg); }
  .ed-hosts-row-pill { font-size: 9.5px; padding: 1px 6px; }
  :global(.ed-hosts-row-pill-local) {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--color-brand-500) 40%, var(--border));
  }
  .ed-hosts-row-sub {
    font-size: 10.5px;
    color: var(--fg-subtle);
    padding-left: 16px;
    margin-top: 3px;
  }
  .ed-hosts-row-sub-empty { font-style: normal; color: var(--fg-subtle); }

  .ed-hosts-row-tags { display: flex; flex-wrap: wrap; gap: 4px; align-content: center; }
  .ed-hosts-row-tags-empty { font-size: 11px; color: var(--fg-subtle); }
  .ed-hosts-tag-chip {
    display: inline-flex; align-items: center; gap: 4px;
    padding: 2px 6px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-muted);
    background: var(--surface);
  }
  .ed-hosts-tag-extra { font-size: 10px; color: var(--fg-subtle); align-self: center; }

  .ed-hosts-bar-cell {
    text-align: right;
    min-width: 0;
    font-size: 11.5px;
    color: var(--fg);
  }
  .ed-hosts-bar-cell.warn { color: var(--color-warning-400); }
  .ed-hosts-bar { height: 2px; background: var(--border); border-radius: 1px; margin-top: 3px; position: relative; overflow: hidden; }
  .ed-hosts-bar-fill { position: absolute; inset: 0; right: auto; background: var(--accent); border-radius: 1px; }
  .ed-hosts-bar-cell.warn .ed-hosts-bar-fill { background: var(--color-warning-400); }
  .ed-hosts-bar-empty { color: var(--fg-subtle); }
  .ed-hosts-bar-hint { font-size: 9.5px; color: var(--fg-subtle); margin-top: 2px; }

  .ed-hosts-row-num {
    text-align: right;
    font-size: 11.5px;
    color: var(--fg);
  }
  .ed-hosts-row-version {
    display: flex; flex-direction: column; gap: 2px;
    min-width: 0; font-size: 11.5px; color: var(--fg);
    overflow: hidden;
  }
  .ed-hosts-row-version > span:first-child {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ed-hosts-row-upgrade { font-size: 9.5px; color: var(--color-warning-400); letter-spacing: 0.04em; }
  .ed-hosts-row-seen { font-size: 11px; color: var(--fg-subtle); }

  .ed-hosts-row-actions { position: relative; }
  .ed-hosts-row-menu {
    position: absolute;
    right: 0;
    top: 28px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 5px;
    padding: 4px;
    z-index: 5;
    min-width: 200px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .ed-hosts-menu-item {
    text-align: left;
    background: transparent;
    border: 0;
    padding: 7px 10px;
    font-size: 12px;
    color: var(--fg);
    cursor: pointer;
    border-radius: 4px;
    text-decoration: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 6px;
  }
  .ed-hosts-menu-item:hover:not(:disabled) { background: var(--surface-hover); }
  .ed-hosts-menu-item:disabled { color: var(--fg-subtle); cursor: not-allowed; }
  .ed-hosts-menu-sep { height: 1px; background: var(--border); margin: 2px 0; }
  .ed-hosts-menu-danger { color: var(--color-danger-400); }

  .ed-hosts-foot { font-size: 10.5px; color: var(--fg-subtle); margin: 14px 0 0; letter-spacing: 0.02em; }

  /* ─── Modal chrome (shared with users page; duplicated here so both
         pages can be edited independently). ─── */
  .ed-modal-backdrop {
    position: fixed;
    inset: 0;
    background: color-mix(in srgb, var(--bg) 70%, transparent);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .ed-modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    width: min(620px, 92vw);
    max-height: 92vh;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .ed-modal-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    padding: 18px 22px;
    border-bottom: 1px solid var(--border);
  }
  .ed-modal-title {
    margin: 4px 0 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--fg);
    font-family: var(--font-sans);
  }
  .ed-modal-body {
    padding: 18px 22px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .ed-modal-hint {
    margin: 0 0 4px;
    font-size: 13px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .ed-modal-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 22px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
  }
  .ed-modal-foot-status { font-size: 11px; color: var(--fg-subtle); }

  /* ─── Tags modal-specific ─── */
  .ed-host-tags-edit {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
  }
  .ed-host-tags-input {
    flex: 1 1 140px;
    border: 0;
    background: transparent;
    color: var(--fg);
    font-size: 11.5px;
    outline: none;
    min-width: 100px;
  }
  .ed-host-tags-input::placeholder { color: var(--fg-subtle); }
  .ed-host-tags-remove {
    background: transparent; border: 0; cursor: pointer;
    color: var(--fg-subtle); padding: 0;
    display: inline-flex;
  }
  .ed-host-tags-remove:hover { color: var(--color-danger-400); }
  .ed-host-tags-suggest {
    display: flex; gap: 6px; flex-wrap: wrap; margin-top: 8px; align-items: center;
  }
  .ed-host-tags-suggest-label {
    font-size: 9.5px; color: var(--fg-subtle); letter-spacing: 0.1em;
    text-transform: uppercase; margin-right: 4px;
  }
</style>
