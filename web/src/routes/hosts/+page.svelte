<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { hostStatusLabel } from '$lib/labels';
  import { onMount, onDestroy } from 'svelte';
  import { tagProblem } from './_host';
  import { fmtAgo, fmtMem, pctTone } from '$lib/format';
  const fmtAgoNever = (iso?: string) => fmtAgo(iso, m.common_never());
  import { goto } from '$app/navigation';
  import {
    api, ApiError, isFanOut,
    type Agent, type AgentUpgradePolicy, type HostInfo, type Cluster, type ClusterCandidate, type ClusterAccess, type ClusterMembership
  } from '$lib/api';
  import { clusterTone, clusterStateLabel, clusterSummary, clusterLine } from '$lib/cluster';
  import { allowed } from '$lib/rbac.svelte';
  import { fmtVersion } from '$lib/version';
  import { Skeleton, EmptyState, StatBar, Segmented, ChipList, TagPill, Menu, Modal, LiveIndicator, InfoTip, StatusChip } from '$lib/components/ui';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Server, Plus, RefreshCw, X, Network, ChevronRight, ChevronDown } from 'lucide-svelte';

  let remoteAgents = $state<Agent[]>([]);
  let localHostInfo = $state<HostInfo | null>(null);
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let containerCounts = $state<Record<string, number>>({});
  let metricsByHost = $state<Record<string, { cpu: number; cores: number; memUsed: number; memTotal: number; disk: number } | null>>({});
  let upgradePolicy = $state<AgentUpgradePolicy | null>(null);
  let clusterList = $state<Cluster[]>([]);
  let clusterCandidates = $state<ClusterCandidate[]>([]);
  let creatingCluster = $state<string | null>(null);

  let memberships = $state<ClusterMembership[]>([]);
  const COLLAPSED_KEY = 'dm.hosts.collapsedClusters';
  let collapsed = $state<Set<string>>(new Set(readCollapsed()));

  function readCollapsed(): string[] {
    try {
      const v = JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? '[]');
      return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : [];
    } catch {
      return [];
    }
  }

  function toggleCluster(id: string) {
    const next = new Set(collapsed);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    collapsed = next;
    try {
      localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...next]));
    } catch {
      /* private mode: the state just isn't kept */
    }
  }

  // host id → its membership, from the hosts the caller can see.
  const membership = $derived(Object.fromEntries(memberships.map((x) => [x.host_id, x])) as Record<string, ClusterMembership>);

  // One group per cluster the caller can open or has a node host of; a
  // host-only user's group carries no Cluster and so no state.
  type Group = { id: string; name: string; cluster: Cluster | null; hostIds: string[] };
  const groups = $derived.by<Group[]>(() => {
    const out = new Map<string, Group>();
    for (const c of clusterList) out.set(c.id, { id: c.id, name: c.name, cluster: c, hostIds: [] });
    for (const x of memberships) {
      let g = out.get(x.cluster_id);
      if (!g) {
        g = { id: x.cluster_id, name: x.cluster_name, cluster: null, hostIds: [] };
        out.set(x.cluster_id, g);
      }
      g.hostIds.push(x.host_id);
    }
    return [...out.values()].sort((a, b) => a.name.localeCompare(b.name));
  });

  let filter = $state<'all' | 'online' | 'offline' | 'pending' | 'agents'>('all');
  let search = $state('');
  let tagFilter = $state<string | null>(null);

  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);

  let showAdd = $state(false);

  let tagsHostId = $state<string | null>(null);
  let tagsOpen = $state(false);
  let tagsDraft = $state<string[]>([]);
  let tagInput = $state('');
  let tagSuggestions = $state<string[]>([]);
  let tagsBusy = $state(false);
  let hostTags = $state<Record<string, string[]>>({});

  // The local host is pinned at the top as an Agent-shaped row; drain,
  // revoke and upgrade do not apply to it.
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
    for (const a of remoteAgents) out.push({ ...a, kind: 'agent' });
    return out;
  });

  const filteredRows = $derived(
    rows.filter((r) => {
      if (filter === 'online' && r.status !== 'online') return false;
      if (filter === 'offline' && r.status !== 'offline') return false;
      if (filter === 'pending' && r.status !== 'pending') return false;
      if (filter === 'agents' && r.kind !== 'agent') return false;
      const tags = hostTags[r.id] ?? [];
      if (tagFilter && !tags.includes(tagFilter)) return false;
      if (search) {
        const q = search.toLowerCase();
        if (
          !r.name.toLowerCase().includes(q) &&
          !(r.hostname ?? '').toLowerCase().includes(q) &&
          !tags.some((t) => t.toLowerCase().includes(q))
        ) return false;
      }
      return true;
    })
  );

  const ungrouped = $derived(filteredRows.filter((r) => !membership[r.id]));

  // A group shows when one of its hosts passes the filters, or with no
  // status/tag filter when the search is empty or names the cluster.
  const shownGroups = $derived.by(() => {
    const q = search.toLowerCase();
    const plain = filter === 'all' && !tagFilter;
    return groups.flatMap((g) => {
      const nameHit = !!q && g.name.toLowerCase().includes(q);
      const kids = (nameHit && plain ? rows : filteredRows).filter((r) => membership[r.id]?.cluster_id === g.id);
      if (kids.length === 0 && !(plain && (!q || nameHit))) return [];
      return [{ ...g, rows: kids }];
    });
  });

  function clusterMenu(c: Cluster): MenuItem[] {
    return [{ label: m.hosts_cluster_delete_ellipsis(), danger: true, onclick: () => deleteCluster(c) }];
  }

  const counts = $derived({
    all: rows.length,
    online: rows.filter((r) => r.status === 'online').length,
    offline: rows.filter((r) => r.status === 'offline').length,
    pending: rows.filter((r) => r.status === 'pending').length,
    agents: rows.filter((r) => r.kind === 'agent').length
  });

  function needsUpgrade(r: Row): boolean {
    return r.kind === 'agent' && !!r.version && !!upgradePolicy && r.version !== upgradePolicy.server_version;
  }

  const stats = $derived.by(() => {
    const offline = rows.filter((r) => r.status === 'offline');
    let cores = 0;
    let mem = 0;
    for (const mt of Object.values(metricsByHost)) {
      if (!mt) continue;
      cores += mt.cores;
      mem += mt.memTotal;
    }
    const containers = Object.values(containerCounts).reduce((a, n) => a + n, 0);
    return [
      {
        label: m.common_online(),
        value: m.hosts_n_of_m({ n: counts.online, total: counts.all }),
        note: offline.length ? m.hosts_names_offline({ names: offline.map((r) => r.name).join(', ') }) : counts.pending ? m.hosts_n_pending({ n: counts.pending }) : '',
        tone: offline.length ? ('bad' as const) : ('' as const)
      },
      { label: m.common_containers(), value: containers },
      { label: m.hosts_total_capacity(), value: cores ? `${m.hosts_cores({ n: Math.round(cores) })} · ${fmtMem(mem)}` : '—' }
    ];
  });

  async function loadAll() {
    if (!allowed('hosts.update') && !allowed('hosts.view')) return;
    refreshing = true;
    try {
      const [agents, hostList, policy, clist, cands, mships] = await Promise.all([
        api.agents.list(),
        api.hosts.list().catch(() => []),
        api.agents.getUpgradePolicy().catch(() => null),
        api.clusters.list().catch(() => []),
        allowed('clusters.create') ? api.clusters.candidates().catch(() => []) : Promise.resolve([]),
        api.clusters.memberships().catch(() => [])
      ]);
      remoteAgents = agents;
      clusterList = clist;
      memberships = mships;
      clusterCandidates = cands;
      localHostInfo = hostList.find((h) => h.kind === 'local') ?? null;
      upgradePolicy = policy;
      const ids = [...(localHostInfo ? ['local'] : []), ...agents.map((a) => a.id)];
      await Promise.all(
        ids.flatMap((id) => [
          api.hosts.listTags(id).then((t) => (hostTags[id] = t)).catch(() => (hostTags[id] = [])),
          api.system
            .metrics(id)
            .then((mt) => {
              if (!isFanOut(mt)) {
                metricsByHost[id] = { cpu: mt.cpu_percent, cores: mt.cpu_cores, memUsed: mt.mem_used, memTotal: mt.mem_total, disk: mt.disk_percent ?? 0 };
              }
            })
            .catch(() => (metricsByHost[id] = null)),
          api.containers.summary(id).then((s) => (containerCounts[id] = s.total)).catch(() => (containerCounts[id] = 0))
        ])
      );
      lastLoaded = Date.now();
    } catch (err) {
      toast.error(m.hosts_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
      refreshing = false;
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

  function toggleSelect(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  const selectable = $derived(filteredRows.filter((r) => r.kind !== 'local'));
  function toggleAll() {
    selected = selected.size === selectable.length && selectable.length > 0 ? new Set() : new Set(selectable.map((r) => r.id));
  }

  let upgradingId = $state<string | null>(null);
  async function upgradeHost(id: string, name: string) {
    upgradingId = id;
    try {
      const res = await api.agents.upgrade(id);
      toast.success(m.hosts_upgrade_dispatched(), `${name} → ${fmtVersion(res.version)}`);
      await loadAll();
    } catch (err) {
      toast.error(m.hosts_upgrade_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      upgradingId = null;
    }
  }

  async function revokeHost(r: Row) {
    if (r.kind === 'local') return;
    if (!(await confirm.ask({
      title: m.hosts_revoke_title({ name: r.name }),
      message: m.hosts_revoke_message({ name: r.name }),
      body: m.hosts_revoke_body(),
      confirmLabel: m.hosts_revoke(), danger: true, typeToConfirm: r.name
    }))) return;
    try {
      await api.agents.delete(r.id);
      toast.success(m.hosts_revoked(), r.name);
      await loadAll();
    } catch (err) {
      toast.error(m.hosts_revoke_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function runPolicy() {
    try {
      upgradePolicy = await api.agents.runUpgradePolicy();
      toast.success(m.hosts_evaluation_triggered());
      await loadAll();
    } catch (e) {
      toast.error(m.common_failed(), e instanceof ApiError ? e.message : undefined);
    }
  }

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
    toast[fail === 0 ? 'success' : 'info'](m.hosts_n_upgraded({ n: ok }), fail > 0 ? m.hosts_n_failed({ n: fail }) : undefined);
    await loadAll();
  }
  async function bulkRevoke() {
    if (!(await confirm.ask({
      title: m.hosts_revoke_selected_title(), message: m.hosts_revoke_selected_message({ count: selected.size }),
      body: m.hosts_revoke_selected_body(), confirmLabel: m.hosts_revoke(), danger: true
    }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const id of selected) {
      try { await api.agents.delete(id); ok++; }
      catch { fail++; }
    }
    bulkBusy = false;
    selected = new Set();
    toast[fail === 0 ? 'success' : 'info'](m.hosts_n_revoked({ n: ok }), fail > 0 ? m.hosts_n_failed({ n: fail }) : undefined);
    await loadAll();
  }

  async function openTagsModal(id: string) {
    tagsHostId = id;
    tagsDraft = [...(hostTags[id] ?? [])];
    tagInput = '';
    tagsOpen = true;
    try { tagSuggestions = await api.hosts.allTags(); } catch { tagSuggestions = []; }
  }
  function addTagDraft(t: string) {
    const v = t.trim().toLowerCase();
    if (!v || tagsDraft.includes(v)) { tagInput = ''; return; }
    const problem = tagProblem(v, tagsDraft);
    if (problem) { toast.error(problem.title, problem.detail); return; }
    tagsDraft = [...tagsDraft, v];
    tagInput = '';
  }
  async function saveTags() {
    if (!tagsHostId) return;
    tagsBusy = true;
    try {
      hostTags[tagsHostId] = await api.hosts.setTags(tagsHostId, tagsDraft);
      toast.success(m.hosts_tags_saved());
      tagsOpen = false;
    } catch (err) {
      toast.error(m.hosts_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { tagsBusy = false; }
  }

  function fmtUptime(iso?: string): string {
    if (!iso) return '';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 3600) return m.hosts_up_min({ n: Math.max(1, Math.round(d / 60)) });
    if (d < 86400) return m.hosts_up_h({ n: Math.round(d / 3600) });
    return m.hosts_up_d({ n: Math.round(d / 86400) });
  }
  // A connected host whose stacks directory was never confirmed.
  function waitingSetup(r: Row): boolean {
    return r.kind === 'agent' && r.status === 'online' && !!r.needs_setup;
  }

  function dotTone(s: string): string {
    if (s === 'online') return 'ok';
    if (s === 'offline') return 'bad';
    if (s === 'pending') return 'warn';
    return '';
  }

  function rowMenu(r: Row): MenuItem[] {
    const items: MenuItem[] = [
      { label: m.common_open(), kbd: '↵', href: `/hosts/${r.id}` },
      { separator: true },
      { label: m.hosts_edit_tags(), onclick: () => openTagsModal(r.id) }
    ];
    if (r.kind === 'agent') {
      if (needsUpgrade(r)) {
        items.push({ label: upgradingId === r.id ? m.hosts_upgrading() : m.hosts_upgrade_agent(), disabled: upgradingId === r.id, onclick: () => upgradeHost(r.id, r.name) });
      }
      items.push({ separator: true }, { label: m.hosts_revoke_ellipsis(), danger: true, onclick: () => revokeHost(r) });
    }
    return items;
  }

  async function createCluster(c: ClusterCandidate, hostId: string) {
    creatingCluster = c.cluster_id;
    try {
      const created = await api.clusters.create(hostId);
      toast.success(m.hosts_cluster_created({ name: created.name }), m.hosts_cluster_linked({ count: created.members.length }));
      await loadAll();
    } catch (err) {
      toast.error(m.hosts_cluster_create_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      creatingCluster = null;
    }
  }

  function accessLines(a: ClusterAccess): string[] {
    return [
      ...a.assignments.map((x) => m.hosts_cluster_delete_assignment({ user: x.username, role: x.role, via: x.via ? m.hosts_cluster_delete_via({ group: x.via }) : '' })),
      ...a.mappings.map((x) => m.hosts_cluster_delete_mapping({ kind: x.kind.toUpperCase(), provider: x.provider, group: x.group, role: x.role })),
      ...a.invites.map((x) => (x.expired ? m.hosts_cluster_delete_invite_expired : m.hosts_cluster_delete_invite)({ email: x.email_hint || `#${x.id}`, role: x.role })),
      ...a.tokens.map((x) => m.hosts_cluster_delete_token({ name: x.name, owner: x.owner ? m.hosts_cluster_delete_owner({ owner: x.owner }) : '' }))
    ];
  }

  async function deleteCluster(c: Cluster) {
    let access: ClusterAccess;
    try {
      access = await api.clusters.access(c.id);
    } catch (err) {
      toast.error(m.hosts_cluster_delete_failed(), err instanceof ApiError ? err.message : undefined);
      return;
    }
    const n = access.counts;
    let body: string = n.assignments + n.mappings + n.invites + n.tokens > 0
      ? m.hosts_cluster_delete_access({ assignments: n.assignments, mappings: n.mappings, invites: n.invites, tokens: n.tokens })
      : m.hosts_cluster_delete_no_access();
    if (!access.complete) body += ` ${m.hosts_cluster_delete_names_hidden()}`;
    if (!(await confirm.ask({
      title: m.hosts_cluster_delete_title({ name: c.name }),
      message: m.hosts_cluster_delete_message({ count: c.members.length }),
      body, items: accessLines(access),
      confirmLabel: m.hosts_cluster_delete(), danger: true, typeToConfirm: c.name
    }))) return;
    try {
      await api.clusters.delete(c.id);
      toast.success(m.hosts_cluster_deleted({ name: c.name }));
      await loadAll();
    } catch (err) {
      toast.error(m.hosts_cluster_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  const COLS = '22px 14px minmax(180px, 1.6fr) 64px 100px 100px 100px minmax(110px, 200px) minmax(90px, 150px) 40px';
</script>

<section class="dm-page">
  {#if !allowed('hosts.update') && !allowed('hosts.view')}
    <EmptyState icon={Server} title={m.hosts_no_access()} description={m.hosts_no_access_hint()} />
  {:else}
    <div class="dm-ph">
      <h1>{m.common_hosts()}</h1>
      <span class="dm-sub dm-num">{counts.all}</span>
      {#if tagFilter}
        <span class="tagf">{m.hosts_tag_filter({ tag: tagFilter })}<button type="button" onclick={() => (tagFilter = null)} aria-label={m.hosts_clear_tag_filter()}><X size={11} /></button></span>
      {/if}
      <span class="dm-sp"></span>
      <LiveIndicator updatedAt={lastLoaded} onrefresh={loadAll} busy={refreshing} />
      {#if allowed('hosts.create')}
        <button type="button" class="dm-btn dm-btn-primary" data-testid="add-host" onclick={() => (showAdd = true)}>
          <Plus size={14} strokeWidth={2} /> {m.hosts_add()}
        </button>
      {/if}
    </div>

    {#if !loading}
      <StatBar items={stats} />
    {/if}

    {#if upgradePolicy && upgradePolicy.connected_pending > 0}
      <div class="policy" role="status">
        <RefreshCw size={14} strokeWidth={1.8} />
        <span class="dm-trunc">
          <b>{m.hosts_policy_behind({ count: upgradePolicy.connected_pending, version: fmtVersion(upgradePolicy.server_version) })}</b>
          <span class="dm-dim">
            {m.hosts_policy_up_to_date({ n: upgradePolicy.connected_up_to_date, total: upgradePolicy.connected_total })} · {m.hosts_policy_mode({ mode: upgradePolicy.mode })}{upgradePolicy.mode === 'staged' && upgradePolicy.stage_percent ? ` ${upgradePolicy.stage_percent} %` : ''}{upgradePolicy.last_run_at ? ` · ${m.hosts_policy_last_run({ ago: fmtAgoNever(upgradePolicy.last_run_at) })}` : ''}
          </span>
        </span>
        <span class="dm-sp"></span>
        {#if upgradePolicy.mode !== 'manual'}
          <button type="button" class="dm-btn dm-btn-sm" onclick={runPolicy}>{m.hosts_run_policy()}</button>
        {/if}
      </div>
    {/if}

    {#each clusterCandidates as c (c.cluster_id)}
      <div class="swarm-hint" role="status" data-testid="cluster-hint">
        <Network size={14} strokeWidth={1.8} />
        <span class="dm-trunc">
          <b>{m.hosts_cluster_hint({ hosts: c.managers.map((x) => x.host_name).join(', ') })}</b>
          <span class="dm-dim">{m.hosts_cluster_hint_detail()}</span>
        </span>
        <span class="dm-sp"></span>
        {#each c.managers as mg (mg.host_id)}
          <button type="button" class="dm-btn dm-btn-sm dm-btn-primary" data-testid="create-cluster" disabled={creatingCluster !== null}
            title={m.hosts_cluster_create_from_title({ host: mg.host_name })} onclick={() => createCluster(c, mg.host_id)}>
            {creatingCluster === c.cluster_id ? m.hosts_cluster_creating() : c.managers.length > 1 ? m.hosts_cluster_create_from({ host: mg.host_name }) : m.hosts_cluster_create()}
          </button>
        {/each}
      </div>
    {/each}

    {#if !loading && rows.length > 0}
      <div class="dm-toolbar">
        <Segmented
          label={m.hosts_filter()}
          bind:value={filter}
          options={[
            { key: 'all', label: m.common_all(), count: counts.all },
            { key: 'online', label: m.common_online(), count: counts.online },
            { key: 'offline', label: m.common_offline(), count: counts.offline },
            { key: 'pending', label: m.hosts_pending_label(), count: counts.pending },
            { key: 'agents', label: m.common_agents(), count: counts.agents }
          ]}
        />
        {#if selected.size > 0}
          <span class="bulk">
            <span class="dm-num">{m.hosts_selected({ n: selected.size })}</span>
            <button type="button" class="dm-btn dm-btn-sm" disabled={bulkBusy} onclick={bulkUpgrade}>{m.hosts_upgrade()}</button>
            <button type="button" class="dm-btn dm-btn-sm dm-btn-danger" disabled={bulkBusy} onclick={bulkRevoke}>{m.hosts_revoke_ellipsis()}</button>
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (selected = new Set())}>{m.hosts_clear()}</button>
          </span>
        {/if}
        <span class="dm-sp"></span>
        <input type="search" class="dm-filter" placeholder={m.hosts_filter_ph()} aria-label={m.hosts_filter()} bind:value={search} style="width: 260px" />
      </div>
    {/if}

    {#if loading && rows.length === 0}
      <div class="dm-tbl">
        {#each Array(4) as _, i (i)}
          <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>
        {/each}
      </div>
    {:else if ungrouped.length === 0 && shownGroups.length === 0}
      <EmptyState icon={Server} title={m.hosts_no_match()} description={search || filter !== 'all' || tagFilter ? m.hosts_no_match_hint() : m.hosts_empty_hint()}>
        {#snippet action()}
          {#if allowed('hosts.create')}
            <button type="button" class="dm-btn dm-btn-primary" onclick={() => (showAdd = true)}><Plus size={14} strokeWidth={2} /> {m.hosts_add()}</button>
          {/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.common_hosts()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
          <span role="columnheader"><input type="checkbox" aria-label={m.hosts_select_all()} checked={selected.size > 0 && selected.size === selectable.length} onchange={toggleAll} /></span>
          <span role="columnheader"></span>
          <span role="columnheader">{m.common_host()}</span>
          <span role="columnheader">{m.hosts_col_kind()}</span>
          <span role="columnheader">CPU</span>
          <span role="columnheader">{m.hosts_col_memory()}</span>
          <span role="columnheader">{m.hosts_col_disk()}</span>
          <span role="columnheader">{m.common_agent()}</span>
          <span role="columnheader">{m.hosts_col_tags()}</span>
          <span role="columnheader"></span>
        </div>
        {#each ungrouped as r (r.id)}
          {@render hostRow(r, false)}
        {/each}
        {#each shownGroups as g (g.id)}
          {@const st = g.cluster?.status}
          {@const open = !collapsed.has(g.id)}
          {@const summary = g.cluster ? clusterSummary(st) : m.hosts_cluster_name_only()}
          {@const line = g.cluster ? clusterLine(st) : ''}
          <div
            class="dm-tr grp"
            role="row"
            tabindex="0"
            data-testid="cluster-row"
            aria-expanded={open}
            style="grid-template-columns: {COLS}"
            onclick={() => toggleCluster(g.id)}
            onkeydown={(e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target === e.currentTarget) { e.preventDefault(); toggleCluster(g.id); } }}
          >
            <span role="cell"></span>
            <span role="cell" class="caret" title={open ? m.hosts_cluster_collapse({ name: g.name }) : m.hosts_cluster_expand({ name: g.name })}>
              {#if open}<ChevronDown size={13} strokeWidth={2} />{:else}<ChevronRight size={13} strokeWidth={2} />{/if}
            </span>
            <span role="cell" class="dm-nm gnm">
              <span class="nmline">
                <span class="gname">{g.name}</span>
                {#if g.cluster}
                  <StatusChip tone={clusterTone(st?.state)} label={clusterStateLabel(st?.state)} />
                {/if}
              </span>
              <small title={summary}>{summary}</small>
            </span>
            <span role="cell" class="gline dm-dim dm-trunc" title={line}>{line}</span>
            <span role="cell"></span>
            <span role="cell"></span>
            <span role="cell" class="dm-tr-menu">
              {#if g.cluster && allowed('clusters.create')}
                <span role="presentation" onclick={(e) => e.stopPropagation()}>
                  <Menu label={m.hosts_cluster_actions_for({ name: g.name })} items={clusterMenu(g.cluster)} />
                </span>
              {/if}
            </span>
          </div>
          {#if open}
            {#each g.rows as r (r.id)}
              {@render hostRow(r, true)}
            {/each}
          {/if}
        {/each}
      </div>
    {/if}
  {/if}
</section>

{#snippet hostRow(r: Row, child: boolean)}
  {@const hm = metricsByHost[r.id]}
  {@const memPct = hm && hm.memTotal > 0 ? Math.round((hm.memUsed / hm.memTotal) * 100) : null}
  {@const cpuPct = hm ? Math.round(hm.cpu) : null}
  {@const diskPct = hm ? Math.round(hm.disk) : null}
  {@const tags = hostTags[r.id] ?? []}
  {@const online = r.status === 'online'}
  <div
    class="dm-tr dm-click"
    class:child
    class:dm-sel={selected.has(r.id)}
    role="row"
    tabindex="0"
    style="grid-template-columns: {COLS}"
    onclick={() => goto(`/hosts/${r.id}`)}
    onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(`/hosts/${r.id}`); }}
  >
    <span role="cell">
      <input type="checkbox" aria-label={m.hosts_select_one({ name: r.name })} disabled={r.kind === 'local'} checked={selected.has(r.id)}
        onclick={(e) => e.stopPropagation()} onchange={() => toggleSelect(r.id)} />
    </span>
    <span role="cell"><span class="dm-dot {waitingSetup(r) ? 'ring' : dotTone(r.status)}" title={waitingSetup(r) ? m.label_host_waiting_setup() : hostStatusLabel(r.status)}></span></span>
    <span role="cell" class="dm-nm">
      <span class="nmline">
        <a href="/hosts/{r.id}" onclick={(e) => e.stopPropagation()}>{r.name}</a>
        {#if membership[r.id]}
          {@const ms = membership[r.id]}
          <span class="dm-pill" title={m.hosts_cluster_member_title({ cluster: ms.cluster_name })}>{ms.role === 'manager' ? m.hosts_member_role_manager() : m.hosts_member_role_worker()}</span>
          {#if ms.node?.leader}<span class="lead">{m.hosts_cluster_leader()}</span>{/if}
        {/if}
        {#if waitingSetup(r)}
          {#if allowed('hosts.update')}
            <a class="dm-pill dm-pill-info setup" data-testid="row-setup" href="/hosts/{r.id}?tab=setup" title={m.hosts_setup_row_title()} onclick={(e) => e.stopPropagation()}>{m.label_host_waiting_setup()} →</a>
          {:else}
            <span class="dm-pill dm-pill-info setup">{m.label_host_waiting_setup()}</span>
          {/if}
        {/if}
      </span>
      <small>
        {#if r.status === 'offline'}
          <span class="off">{m.hosts_offline_last_seen({ ago: fmtAgoNever(r.last_seen_at) })}</span>
        {:else if r.status === 'pending'}
          {m.hosts_waiting_for_agent()}
        {:else if r.status === 'removed'}
          {m.label_host_removed()}
        {:else if waitingSetup(r)}
          <span title={r.hostname}>{r.hostname || '—'}{r.os ? ` · ${r.os}/${r.arch}` : ''}</span>
        {:else}
          {r.hostname || (r.kind === 'local' ? 'localhost' : '—')}{r.os ? ` · ${r.os}/${r.arch}` : ''} · {m.hosts_n_containers({ n: containerCounts[r.id] ?? '—' })}{r.online_since ? ` · ${fmtUptime(r.online_since)}` : ''}
        {/if}
      </small>
    </span>
    <span role="cell" class="dm-dim">{r.kind === 'local' ? m.hosts_kind_local() : m.hosts_kind_agent()}</span>
    {#if online && hm}
      <span role="cell" class="mbar"><i><b class={pctTone(cpuPct ?? 0)} style="width:{Math.min(100, cpuPct ?? 0)}%"></b></i><span class="dm-num">{cpuPct}%</span></span>
      <span role="cell" class="mbar" title={memPct != null ? m.hosts_mem_of({ used: fmtMem(hm.memUsed), total: fmtMem(hm.memTotal) }) : m.hosts_unavailable()}>
        {#if memPct != null}<i><b class={pctTone(memPct, 80)} style="width:{Math.min(100, memPct)}%"></b></i><span class="dm-num">{memPct}%</span>{:else}<span class="dm-num dm-dim">—</span>{/if}
      </span>
      <span role="cell" class="mbar"><i><b class={pctTone(diskPct ?? 0, 80)} style="width:{Math.min(100, diskPct ?? 0)}%"></b></i><span class="dm-num">{diskPct}%</span></span>
    {:else}
      <span role="cell" class="dm-num dm-dim">—</span>
      <span role="cell" class="dm-num dm-dim">—</span>
      <span role="cell" class="dm-num dm-dim">—</span>
    {/if}
    <span role="cell" class="dm-mono" class:warn={needsUpgrade(r) || r.update_recommended} title={[r.version ? fmtVersion(r.version) : '', needsUpgrade(r) ? m.hosts_server_is_on({ version: fmtVersion(upgradePolicy?.server_version) }) : r.update_recommended ? m.hosts_older_protocol() : ''].filter(Boolean).join(' · ')}>
      {r.version ? fmtVersion(r.version) : '—'}{needsUpgrade(r) || r.update_recommended ? ' ↑' : ''}
      {#if r.layout === 'legacy'}<span class="dm-pill dm-pill-info legacy" title={m.hosts_legacy_badge_title()}>{m.hosts_legacy_badge()}</span>{/if}
    </span>
    <span role="cell" class="tagcell">
      {#if tags.length}
        <span role="presentation" onclick={(e) => e.stopPropagation()}>
          <ChipList items={tags} max={2} active={tagFilter} onpick={(t) => (tagFilter = tagFilter === t ? null : t)} />
        </span>
      {:else}
        <span class="dm-dim">—</span>
      {/if}
    </span>
    <span role="cell" class="dm-tr-menu">
      <span role="presentation" onclick={(e) => e.stopPropagation()}>
        <Menu label={m.hosts_actions_for({ name: r.name })} items={rowMenu(r)} />
      </span>
    </span>
  </div>
{/snippet}

{#if showAdd}
  {#await import('./_AddHostModal.svelte') then mod}
    <mod.default
      onclose={() => (showAdd = false)}
      onconnected={() => loadAll()}
      knownTags={[...new Set([...tagSuggestions, ...Object.values(hostTags).flat()])]}
    />
  {/await}
{/if}

<Modal bind:open={tagsOpen} title={m.hosts_tags_title({ name: rows.find((r) => r.id === tagsHostId)?.name ?? '' })} maxWidth="max-w-lg">
  <div class="tagsdlg">
    <p class="dm-sub">{m.hosts_tags_intro()} <InfoTip text={m.hosts_tags_tip()} /></p>
    <div class="tagedit">
      {#each tagsDraft as t (t)}
        <span class="tagx"><TagPill label={t} /><button type="button" onclick={() => (tagsDraft = tagsDraft.filter((x) => x !== t))} aria-label={m.hosts_remove_tag({ tag: t })}><X size={11} /></button></span>
      {/each}
      <input
        type="text"
        class="dm-filter"
        placeholder={tagsDraft.length === 0 ? m.hosts_tags_ph() : m.hosts_add_tag_ph()}
        aria-label={m.hosts_add_tag()}
        bind:value={tagInput}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTagDraft(tagInput); } }}
      />
    </div>
    {#if tagSuggestions.filter((s) => !tagsDraft.includes(s)).length > 0}
      <div class="suggest">
        <span class="dm-dim">{m.hosts_in_use()}</span>
        {#each tagSuggestions.filter((s) => !tagsDraft.includes(s)) as s (s)}
          <TagPill label={s} onclick={addTagDraft} title={m.hosts_add_tag_named({ tag: s })} />
        {/each}
      </div>
    {/if}
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (tagsOpen = false)}>{m.common_cancel()}</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={tagsBusy} onclick={saveTags}>{tagsBusy ? m.common_saving() : m.hosts_save_tags()}</button>
  {/snippet}
</Modal>

<style>
  .nmline { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .nmline > a:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .setup { flex: none; font-family: var(--font-sans); font-size: var(--fs-xs); line-height: 18px; text-decoration: none; white-space: nowrap; }
  a.setup:hover { text-decoration: underline; }
  .legacy { margin-left: 6px; font-family: var(--font-sans); font-size: var(--fs-xs); line-height: 18px; }
  .tagf {
    display: inline-flex; align-items: center; gap: 4px;
    font-size: 12px; line-height: 20px; padding: 0 4px 0 8px; border-radius: 999px;
    background: var(--accent-soft); color: var(--fg);
  }
  .tagf button { all: unset; cursor: pointer; display: inline-flex; padding: 2px; color: var(--muted); }
  .tagf button:hover { color: var(--fg); }
  .swarm-hint {
    display: flex; align-items: center; gap: 10px;
    border: 1px solid var(--accent); background: var(--accent-soft); color: inherit;
    border-radius: var(--r); padding: 8px 14px; font-size: var(--fs-sm); min-width: 0; margin-bottom: 10px;
  }
  .grp { cursor: pointer; }
  .grp .caret { display: inline-flex; align-items: center; color: var(--muted); }
  .grp .gname { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .grp .gnm { grid-column: span 2; }
  .grp .gline { grid-column: span 3; font-size: var(--fs-xs); }
  .child .dm-nm { padding-left: 18px; }
  .lead { flex: none; font-size: var(--fs-xs); font-weight: 600; color: var(--accent); }
  .policy {
    display: flex; align-items: center; gap: 10px;
    border: 1px solid var(--warn-bd); background: var(--warn-bg); color: var(--warn);
    border-radius: var(--r); padding: 8px 14px; font-size: var(--fs-sm); min-width: 0;
  }
  .policy b { color: var(--fg); font-weight: 600; }
  .bulk { display: inline-flex; align-items: center; gap: 8px; margin-left: 8px; }
  .mbar { display: flex; align-items: center; gap: 8px; }
  .mbar i { flex: 1; height: 5px; border-radius: 3px; background: var(--field); overflow: hidden; min-width: 30px; }
  .mbar i b { display: block; height: 100%; background: var(--accent); }
  .mbar i b.warn { background: var(--warn); }
  .mbar i b.bad { background: var(--bad); }
  .mbar .dm-num { width: 34px; text-align: right; }
  .off { color: var(--bad); }
  .tagcell { display: flex; gap: 4px; align-items: center; }
  .tagsdlg { display: flex; flex-direction: column; gap: 12px; }
  .tagsdlg p { margin: 0; }
  .tagedit { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .tagedit input { min-width: 140px; flex: 1; }
  .tagx { display: inline-flex; align-items: center; gap: 2px; }
  .tagx button { all: unset; cursor: pointer; color: var(--subtle); display: inline-flex; }
  .tagx button:hover { color: var(--bad); }
  .suggest { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; font-size: var(--fs-sm); }
</style>
