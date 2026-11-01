<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { tagProblem } from './_host';
  import { fmtAgo, fmtMem, pctTone } from '$lib/format';
  const fmtAgoNever = (iso?: string) => fmtAgo(iso, 'never');
  import { goto } from '$app/navigation';
  import {
    api, ApiError, isFanOut,
    type Agent, type AgentUpgradePolicy, type HostInfo
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { fmtVersion } from '$lib/version';
  import { Skeleton, EmptyState, StatBar, Segmented, ChipList, TagPill, Menu, Modal, LiveIndicator, InfoTip } from '$lib/components/ui';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Server, Plus, RefreshCw, X } from 'lucide-svelte';

  let remoteAgents = $state<Agent[]>([]);
  let localHostInfo = $state<HostInfo | null>(null);
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let containerCounts = $state<Record<string, number>>({});
  let metricsByHost = $state<Record<string, { cpu: number; cores: number; memUsed: number; memTotal: number; disk: number } | null>>({});
  let upgradePolicy = $state<AgentUpgradePolicy | null>(null);

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
    for (const m of Object.values(metricsByHost)) {
      if (!m) continue;
      cores += m.cores;
      mem += m.memTotal;
    }
    const containers = Object.values(containerCounts).reduce((a, n) => a + n, 0);
    return [
      {
        label: 'Online',
        value: `${counts.online} of ${counts.all}`,
        note: offline.length ? offline.map((r) => r.name).join(', ') + ' offline' : counts.pending ? `${counts.pending} pending` : '',
        tone: offline.length ? ('bad' as const) : ('' as const)
      },
      { label: 'Containers', value: containers },
      { label: 'Total capacity', value: cores ? `${Math.round(cores)} cores · ${fmtMem(mem)}` : '—' }
    ];
  });

  async function loadAll() {
    if (!allowed('hosts.update') && !allowed('hosts.view')) return;
    refreshing = true;
    try {
      const [agents, hostList, policy] = await Promise.all([
        api.agents.list(),
        api.hosts.list().catch(() => []),
        api.agents.getUpgradePolicy().catch(() => null)
      ]);
      remoteAgents = agents;
      localHostInfo = hostList.find((h) => h.kind === 'local') ?? null;
      upgradePolicy = policy;
      const ids = [...(localHostInfo ? ['local'] : []), ...agents.map((a) => a.id)];
      await Promise.all(
        ids.flatMap((id) => [
          api.hosts.listTags(id).then((t) => (hostTags[id] = t)).catch(() => (hostTags[id] = [])),
          api.system
            .metrics(id)
            .then((m) => {
              if (!isFanOut(m)) {
                metricsByHost[id] = { cpu: m.cpu_percent, cores: m.cpu_cores, memUsed: m.mem_used, memTotal: m.mem_total, disk: m.disk_percent ?? 0 };
              }
            })
            .catch(() => (metricsByHost[id] = null)),
          api.containers.summary(id).then((s) => (containerCounts[id] = s.total)).catch(() => (containerCounts[id] = 0))
        ])
      );
      lastLoaded = Date.now();
    } catch (err) {
      toast.error('Failed to load hosts', err instanceof ApiError ? err.message : undefined);
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
      toast.success('Upgrade dispatched', `${name} → ${fmtVersion(res.version)}`);
      await loadAll();
    } catch (err) {
      toast.error('Upgrade failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      upgradingId = null;
    }
  }

  async function revokeHost(r: Row) {
    if (r.kind === 'local') return;
    if (!(await confirm.ask({
      title: `Revoke ${r.name}`,
      message: `Revoke host "${r.name}"?`,
      body: 'The host’s agent certificate is invalidated. Containers running there are forgotten by Dockmesh — they keep running on the host until you stop them manually. You can re-enroll later by running the install script again.',
      confirmLabel: 'Revoke', danger: true, typeToConfirm: r.name
    }))) return;
    try {
      await api.agents.delete(r.id);
      toast.success('Revoked', r.name);
      await loadAll();
    } catch (err) {
      toast.error('Revoke failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function copyEnrollURL() {
    // The install URL is not stored after creation; a fresh token comes
    // from the host's Maintenance tab.
    await copyWithToast(`${window.location.origin}/api/v1/agents/enroll`, 'Enrollment URL copied');
  }

  async function runPolicy() {
    try {
      upgradePolicy = await api.agents.runUpgradePolicy();
      toast.success('Evaluation triggered');
      await loadAll();
    } catch (e) {
      toast.error('Failed', e instanceof ApiError ? e.message : undefined);
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
    toast[fail === 0 ? 'success' : 'info'](`${ok} upgraded`, fail > 0 ? `${fail} failed` : undefined);
    await loadAll();
  }
  async function bulkRevoke() {
    if (!(await confirm.ask({
      title: 'Revoke selected hosts', message: `Revoke ${selected.size} host${selected.size === 1 ? '' : 's'}?`,
      body: 'Each host’s agent certificate is invalidated. Their containers keep running but are no longer managed.', confirmLabel: 'Revoke', danger: true
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
      toast.success('Tags saved');
      tagsOpen = false;
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally { tagsBusy = false; }
  }

  function fmtUptime(iso?: string): string {
    if (!iso) return '';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 3600) return `up ${Math.max(1, Math.round(d / 60))} min`;
    if (d < 86400) return `up ${Math.round(d / 3600)} h`;
    return `up ${Math.round(d / 86400)} d`;
  }
  function dotTone(s: string): string {
    if (s === 'online') return 'ok';
    if (s === 'offline') return 'bad';
    if (s === 'pending') return 'warn';
    return '';
  }

  function rowMenu(r: Row): MenuItem[] {
    const items: MenuItem[] = [
      { label: 'Open', kbd: '↵', href: `/hosts/${r.id}` },
      { separator: true },
      { label: 'Edit tags', onclick: () => openTagsModal(r.id) }
    ];
    if (r.kind === 'agent') {
      if (needsUpgrade(r)) {
        items.push({ label: upgradingId === r.id ? 'Upgrading…' : 'Upgrade agent', disabled: upgradingId === r.id, onclick: () => upgradeHost(r.id, r.name) });
      }
      items.push({ label: 'Copy enrollment URL', onclick: copyEnrollURL });
      items.push({ separator: true }, { label: 'Revoke…', danger: true, onclick: () => revokeHost(r) });
    }
    return items;
  }

  const COLS = '22px 14px minmax(200px, 1.6fr) 64px 130px 130px 130px 110px 150px 40px';
</script>

<section class="dm-page">
  {#if !allowed('hosts.update') && !allowed('hosts.view')}
    <EmptyState icon={Server} title="No access" description="Host management needs the hosts.view permission." />
  {:else}
    <div class="dm-ph">
      <h1>Hosts</h1>
      <span class="dm-sub dm-num">{counts.all}</span>
      {#if tagFilter}
        <span class="tagf">tag: {tagFilter}<button type="button" onclick={() => (tagFilter = null)} aria-label="Clear tag filter"><X size={11} /></button></span>
      {/if}
      <span class="dm-sp"></span>
      <LiveIndicator updatedAt={lastLoaded} onrefresh={loadAll} busy={refreshing} />
      {#if allowed('hosts.create')}
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => (showAdd = true)}>
          <Plus size={14} strokeWidth={2} /> Add host
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
          <b>{upgradePolicy.connected_pending} {upgradePolicy.connected_pending === 1 ? 'agent is' : 'agents are'} behind the server ({fmtVersion(upgradePolicy.server_version)}).</b>
          <span class="dm-dim">
            {upgradePolicy.connected_up_to_date} of {upgradePolicy.connected_total} up to date · policy {upgradePolicy.mode}{upgradePolicy.mode === 'staged' && upgradePolicy.stage_percent ? ` ${upgradePolicy.stage_percent} %` : ''}{upgradePolicy.last_run_at ? ` · last run ${fmtAgoNever(upgradePolicy.last_run_at)}` : ''}
          </span>
        </span>
        <span class="dm-sp"></span>
        {#if upgradePolicy.mode !== 'manual'}
          <button type="button" class="dm-btn dm-btn-sm" onclick={runPolicy}>Run policy now</button>
        {/if}
      </div>
    {/if}

    {#if !loading && rows.length > 0}
      <div class="dm-toolbar">
        <Segmented
          label="Filter hosts"
          bind:value={filter}
          options={[
            { key: 'all', label: 'All', count: counts.all },
            { key: 'online', label: 'Online', count: counts.online },
            { key: 'offline', label: 'Offline', count: counts.offline },
            { key: 'pending', label: 'Pending', count: counts.pending },
            { key: 'agents', label: 'Agents', count: counts.agents }
          ]}
        />
        {#if selected.size > 0}
          <span class="bulk">
            <span class="dm-num">{selected.size} selected</span>
            <button type="button" class="dm-btn dm-btn-sm" disabled={bulkBusy} onclick={bulkUpgrade}>Upgrade</button>
            <button type="button" class="dm-btn dm-btn-sm dm-btn-danger" disabled={bulkBusy} onclick={bulkRevoke}>Revoke…</button>
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (selected = new Set())}>Clear</button>
          </span>
        {/if}
        <span class="dm-sp"></span>
        <input type="search" class="dm-filter" placeholder="Filter by name, hostname or tag…" aria-label="Filter hosts" bind:value={search} style="width: 260px" />
      </div>
    {/if}

    {#if loading && rows.length === 0}
      <div class="dm-tbl">
        {#each Array(4) as _, i (i)}
          <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>
        {/each}
      </div>
    {:else if filteredRows.length === 0}
      <EmptyState icon={Server} title="No hosts match" description={search || filter !== 'all' || tagFilter ? 'Change the filter or search.' : 'Add a remote host to spread workload.'}>
        {#snippet action()}
          {#if allowed('hosts.create')}
            <button type="button" class="dm-btn dm-btn-primary" onclick={() => (showAdd = true)}><Plus size={14} strokeWidth={2} /> Add host</button>
          {/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label="Hosts">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
          <span role="columnheader"><input type="checkbox" aria-label="Select all" checked={selected.size > 0 && selected.size === selectable.length} onchange={toggleAll} /></span>
          <span role="columnheader"></span>
          <span role="columnheader">Host</span>
          <span role="columnheader">Kind</span>
          <span role="columnheader">CPU</span>
          <span role="columnheader">Memory</span>
          <span role="columnheader">Disk</span>
          <span role="columnheader">Agent</span>
          <span role="columnheader">Tags</span>
          <span role="columnheader"></span>
        </div>
        {#each filteredRows as r (r.id)}
          {@const m = metricsByHost[r.id]}
          {@const memPct = m && m.memTotal > 0 ? Math.round((m.memUsed / m.memTotal) * 100) : null}
          {@const cpuPct = m ? Math.round(m.cpu) : null}
          {@const diskPct = m ? Math.round(m.disk) : null}
          {@const tags = hostTags[r.id] ?? []}
          {@const online = r.status === 'online'}
          <div
            class="dm-tr dm-click"
            class:dm-sel={selected.has(r.id)}
            role="row"
            tabindex="0"
            style="grid-template-columns: {COLS}"
            onclick={() => goto(`/hosts/${r.id}`)}
            onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(`/hosts/${r.id}`); }}
          >
            <span role="cell">
              <input type="checkbox" aria-label="Select {r.name}" disabled={r.kind === 'local'} checked={selected.has(r.id)}
                onclick={(e) => e.stopPropagation()} onchange={() => toggleSelect(r.id)} />
            </span>
            <span role="cell"><span class="dm-dot {dotTone(r.status)}" title={r.status}></span></span>
            <span role="cell" class="dm-nm">
              <a href="/hosts/{r.id}" onclick={(e) => e.stopPropagation()}>{r.name}</a>
              <small>
                {#if r.status === 'offline'}
                  <span class="off">offline · last seen {fmtAgoNever(r.last_seen_at)}</span>
                {:else if r.status === 'pending'}
                  waiting for the agent to connect
                {:else}
                  {r.hostname || (r.kind === 'local' ? 'localhost' : '—')}{r.os ? ` · ${r.os}/${r.arch}` : ''} · {containerCounts[r.id] ?? '—'} containers{r.online_since ? ` · ${fmtUptime(r.online_since)}` : ''}
                {/if}
              </small>
            </span>
            <span role="cell" class="dm-dim">{r.kind}</span>
            {#if online && m}
              <span role="cell" class="mbar"><i><b class={pctTone(cpuPct ?? 0)} style="width:{Math.min(100, cpuPct ?? 0)}%"></b></i><span class="dm-num">{cpuPct}%</span></span>
              <span role="cell" class="mbar" title={memPct != null ? `${fmtMem(m.memUsed)} of ${fmtMem(m.memTotal)}` : 'unavailable'}>
                {#if memPct != null}<i><b class={pctTone(memPct, 80)} style="width:{Math.min(100, memPct)}%"></b></i><span class="dm-num">{memPct}%</span>{:else}<span class="dm-num dm-dim">—</span>{/if}
              </span>
              <span role="cell" class="mbar"><i><b class={pctTone(diskPct ?? 0, 80)} style="width:{Math.min(100, diskPct ?? 0)}%"></b></i><span class="dm-num">{diskPct}%</span></span>
            {:else}
              <span role="cell" class="dm-num dm-dim">—</span>
              <span role="cell" class="dm-num dm-dim">—</span>
              <span role="cell" class="dm-num dm-dim">—</span>
            {/if}
            <span role="cell" class="dm-mono" class:warn={needsUpgrade(r) || r.update_recommended} title={[r.version ? fmtVersion(r.version) : '', needsUpgrade(r) ? `Server is on ${fmtVersion(upgradePolicy?.server_version)}` : r.update_recommended ? 'Older protocol: some features need an upgrade' : ''].filter(Boolean).join(' · ')}>
              {r.version ? fmtVersion(r.version) : '—'}{needsUpgrade(r) || r.update_recommended ? ' ↑' : ''}
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
                <Menu label="Actions for {r.name}" items={rowMenu(r)} />
              </span>
            </span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

{#if showAdd}
  {#await import('./_AddHostModal.svelte') then mod}
    <mod.default
      onclose={() => (showAdd = false)}
      onconnected={async () => { showAdd = false; await loadAll(); }}
      knownTags={[...new Set([...tagSuggestions, ...Object.values(hostTags).flat()])]}
    />
  {/await}
{/if}

<Modal bind:open={tagsOpen} title="Tags · {rows.find((r) => r.id === tagsHostId)?.name ?? ''}" maxWidth="max-w-lg">
  <div class="tagsdlg">
    <p class="dm-sub">Tags group hosts and can limit which hosts a user sees. <InfoTip text="Used for filtering and for host scopes of users and roles. Lowercase letters, digits and hyphens, at most 20 tags." /></p>
    <div class="tagedit">
      {#each tagsDraft as t (t)}
        <span class="tagx"><TagPill label={t} /><button type="button" onclick={() => (tagsDraft = tagsDraft.filter((x) => x !== t))} aria-label="Remove {t}"><X size={11} /></button></span>
      {/each}
      <input
        type="text"
        class="dm-filter"
        placeholder={tagsDraft.length === 0 ? 'edge, production…' : 'add tag'}
        aria-label="Add tag"
        bind:value={tagInput}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTagDraft(tagInput); } }}
      />
    </div>
    {#if tagSuggestions.filter((s) => !tagsDraft.includes(s)).length > 0}
      <div class="suggest">
        <span class="dm-dim">In use:</span>
        {#each tagSuggestions.filter((s) => !tagsDraft.includes(s)) as s (s)}
          <TagPill label={s} onclick={addTagDraft} title="Add {s}" />
        {/each}
      </div>
    {/if}
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (tagsOpen = false)}>Cancel</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={tagsBusy} onclick={saveTags}>{tagsBusy ? 'Saving…' : 'Save tags'}</button>
  {/snippet}
</Modal>

<style>
  .tagf {
    display: inline-flex; align-items: center; gap: 4px;
    font-size: 12px; line-height: 20px; padding: 0 4px 0 8px; border-radius: 999px;
    background: var(--accent-soft); color: var(--fg);
  }
  .tagf button { all: unset; cursor: pointer; display: inline-flex; padding: 2px; color: var(--muted); }
  .tagf button:hover { color: var(--fg); }
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
