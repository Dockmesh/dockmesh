<script lang="ts">
  import { untrack } from 'svelte';
  import { tagProblem } from '../_host';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo, fmtBytes, fmtMem } from '$lib/format';
  const fmtAgoNever = (iso?: string) => fmtAgo(iso, 'never');
  import { latest } from '$lib/latest';
  import { fmtVersion } from '$lib/version';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    api, ApiError,
    type Agent, type SystemMetrics, type Migration, type StackListEntry,
    type DrainPlan
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, EmptyState, StatBar, StatusChip, Tabs, TagPill, Modal, LiveIndicator } from '$lib/components/ui';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import MountTable from '$lib/components/MountTable.svelte';
  import { ArrowDownToLine, RefreshCw, Trash2, X, AlertTriangle } from 'lucide-svelte';

  const id = $derived(page.params.id ?? '');
  const isLocal = $derived(id === 'local');

  type TabId = 'overview' | 'containers' | 'stacks' | 'migrations' | 'tags' | 'maintenance';
  const TAB_IDS: TabId[] = ['overview', 'containers', 'stacks', 'migrations', 'tags', 'maintenance'];
  const openTab = urlTab(TAB_IDS, 'overview');
  const tab = $derived(openTab.current);

  let agent = $state<Agent | null>(null);
  let localName = $state('local');
  let metrics = $state<SystemMetrics | null>(null);
  let containers = $state<any[]>([]);
  let stacks = $state<StackListEntry[]>([]);
  let migrations = $state<Migration[]>([]);
  let tags = $state<string[]>([]);
  let allFleetTags = $state<string[]>([]);
  let upgradePolicyVersion = $state<string>('');
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let loadError = $state<string | null>(null);

  let tagsDraft = $state<string[]>([]);
  let tagInput = $state('');
  let tagsBusy = $state(false);

  let showDrain = $state(false);
  let drainPlan = $state<DrainPlan | null>(null);
  let drainLoading = $state(false);
  let drainBusy = $state(false);

  let upgradeBusy = $state(false);
  let revokeBusy = $state(false);

  const nextLoad = latest();
  async function loadAll() {
    const current = nextLoad();
    refreshing = true;
    loadError = null;
    try {
      if (isLocal) {
        const [m, ctrRes, stk, hostList, polRes] = await Promise.all([
          api.system.metrics('local').catch(() => null),
          api.containers.list(false, 'local').catch(() => []),
          api.stacks.list().catch(() => []),
          api.hosts.list().catch(() => []),
          api.agents.getUpgradePolicy().catch(() => null)
        ]);
        if (!current()) return;
        localName = hostList.find((h) => h.kind === 'local')?.name ?? 'local';
        agent = null;
        if (m && !('hosts' in m)) metrics = m as SystemMetrics;
        containers = Array.isArray(ctrRes) ? ctrRes : [];
        stacks = stk;
        upgradePolicyVersion = polRes?.server_version ?? '';
      } else {
        const [a, m, ctrRes, stk, polRes] = await Promise.all([
          api.agents.get(id).catch((e) => { if (current()) loadError = e instanceof ApiError ? e.message : 'Load failed'; return null; }),
          api.system.metrics(id).catch(() => null),
          api.containers.list(false, id).catch(() => []),
          api.stacks.list().catch(() => []),
          api.agents.getUpgradePolicy().catch(() => null)
        ]);
        if (!current()) return;
        agent = a;
        if (m && !('hosts' in m)) metrics = m as SystemMetrics;
        containers = Array.isArray(ctrRes) ? ctrRes : [];
        stacks = stk;
        upgradePolicyVersion = polRes?.server_version ?? '';
      }
      const [t, allT, migRes] = await Promise.all([
        api.hosts.listTags(id).catch(() => []),
        api.hosts.allTags().catch(() => []),
        api.migrations.list(50).catch(() => [])
      ]);
      if (!current()) return;
      // A tag edit in progress survives the 10 s refresh.
      const dirty = tagsDraft.join(',') !== tags.join(',');
      tags = t;
      if (!dirty) tagsDraft = [...t];
      allFleetTags = allT;
      migrations = migRes.filter((m) => m.source_host_id === id || m.target_host_id === id);
      lastLoaded = Date.now();
    } catch (err) {
      if (current()) loadError = err instanceof ApiError ? err.message : 'Load failed';
    } finally {
      if (current()) {
        loading = false;
        refreshing = false;
      }
    }
  }

  $effect(() => {
    id;
    untrack(() => {
      agent = null;
      metrics = null;
      containers = [];
      migrations = [];
      tags = [];
      tagsDraft = [];
      loading = true;
      loadAll();
    });
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') loadAll();
    }, 10_000);
    return () => clearInterval(timer);
  });

  // Stacks have no deployed-on field; remote hosts are matched through the
  // compose project label of their containers.
  const stacksOnHost = $derived.by<StackListEntry[]>(() => {
    // Stacks not deployed anywhere yet live in the server's stack folder.
    if (isLocal) return stacks.filter((s) => !s.deployment || s.deployment.host_id === 'local' || s.deployment.host_id === '');
    const projects = new Set<string>();
    for (const c of containers) {
      const proj = c?.Labels?.['com.docker.compose.project'];
      if (proj) projects.add(proj);
    }
    return stacks.filter((s) => projects.has(s.name));
  });

  function fmtDur(sec: number): string {
    if (sec < 3600) return `${Math.max(1, Math.round(sec / 60))} min`;
    if (sec < 86400) return `${Math.round(sec / 3600)} h`;
    return `${Math.floor(sec / 86400)} d ${Math.floor((sec % 86400) / 3600)} h`;
  }
  function fmtUptime(iso?: string): string {
    if (!iso) return '—';
    return fmtDur((Date.now() - new Date(iso).getTime()) / 1000);
  }
  function fmtDate(iso?: string): string {
    return iso ? iso.slice(0, 10) : '—';
  }
  function selectTab(t: string) {
    openTab.set(t);
  }

  const status = $derived(isLocal ? 'online' : (agent?.status ?? 'offline'));
  const statusTone = $derived<Tone>(status === 'online' ? 'ok' : status === 'pending' ? 'warn' : status === 'offline' ? 'bad' : 'off');
  const hostname = $derived(isLocal ? 'localhost' : (agent?.hostname ?? ''));
  const hostKind = $derived(isLocal ? 'local' : 'agent');
  const hostName = $derived(isLocal ? localName : (agent?.name ?? id));
  const hostVersion = $derived(isLocal ? upgradePolicyVersion : (agent?.version ?? ''));

  $effect(() => {
    pageContext.set(hostName);
    return () => pageContext.clear();
  });
  const needsUpgrade = $derived(
    !isLocal && !!agent?.version && !!upgradePolicyVersion && agent.version !== upgradePolicyVersion
  );

  const running = $derived(containers.filter((c) => c.State === 'running').length);
  const memPct = $derived(metrics && metrics.mem_total > 0 ? Math.round(metrics.mem_percent) : null);
  const cpuPct = $derived(metrics ? Math.round(metrics.cpu_percent) : null);
  const diskPct = $derived(metrics && metrics.disk_total > 0 ? Math.round(metrics.disk_percent) : null);
  function tone(p: number | null, warnAt = 70): '' | 'warn' | 'bad' {
    if (p == null) return '';
    return p >= 90 ? 'bad' : p >= warnAt ? 'warn' : '';
  }
  const snapshot = $derived([
    {
      label: metrics?.docker_limited ? 'CPU · Docker limit' : 'CPU',
      value: cpuPct != null ? `${cpuPct} %` : '—',
      note: metrics ? `${metrics.cpu_used_cores.toFixed(2)} of ${metrics.cpu_cores.toFixed(2)} cores` : '',
      tone: tone(cpuPct)
    },
    {
      label: metrics?.docker_limited ? 'Memory · Docker limit' : 'Memory',
      value: memPct != null ? `${memPct} %` : '—',
      note: metrics && metrics.mem_total > 0 ? `${fmtMem(metrics.mem_used)} of ${fmtMem(metrics.mem_total)}` : 'unavailable on this host',
      tone: tone(memPct, 80)
    },
    {
      label: 'Disk',
      value: diskPct != null ? `${diskPct} %` : '—',
      note: metrics && metrics.disk_total > 0 ? `${fmtBytes(metrics.disk_used)} of ${fmtBytes(metrics.disk_total)}` : 'unavailable on this host',
      tone: tone(diskPct, 80)
    },
    { label: 'Containers', value: `${running} / ${containers.length}`, note: 'running' },
    { label: 'Stacks', value: stacksOnHost.length }
  ]);

  const tabs = $derived([
    { key: 'overview', label: 'Overview' },
    { key: 'containers', label: 'Containers', count: containers.length },
    { key: 'stacks', label: 'Stacks', count: stacksOnHost.length },
    { key: 'migrations', label: 'Migrations', count: migrations.length },
    { key: 'tags', label: 'Tags', count: tags.length },
    { key: 'maintenance', label: 'Maintenance', dot: needsUpgrade }
  ]);

  function addTagDraft(t: string) {
    const v = t.trim().toLowerCase();
    if (!v || tagsDraft.includes(v)) { tagInput = ''; return; }
    const problem = tagProblem(v, tagsDraft);
    if (problem) { toast.error(problem.title, problem.detail); return; }
    tagsDraft = [...tagsDraft, v];
    tagInput = '';
  }
  async function saveTags() {
    tagsBusy = true;
    try {
      tags = await api.hosts.setTags(id, tagsDraft);
      tagsDraft = [...tags];
      toast.success('Tags saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally { tagsBusy = false; }
  }

  async function openDrain() {
    showDrain = true;
    drainPlan = null;
    drainLoading = true;
    try { drainPlan = await api.drains.plan(id); }
    catch (err) { toast.error('Plan failed', err instanceof ApiError ? err.message : undefined); }
    finally { drainLoading = false; }
  }
  async function executeDrain() {
    drainBusy = true;
    try {
      const d = await api.drains.execute(id);
      toast.success('Drain started', `${d.plan.length} stack(s) queued`);
      showDrain = false;
    } catch (err) {
      toast.error('Drain failed', err instanceof ApiError ? err.message : undefined);
    } finally { drainBusy = false; }
  }
  async function upgradeAgent() {
    if (isLocal) return;
    upgradeBusy = true;
    try {
      const r = await api.agents.upgrade(id);
      toast.success('Upgrade dispatched', `${hostName} → ${fmtVersion(r.version)}`);
      await loadAll();
    } catch (err) {
      toast.error('Upgrade failed', err instanceof ApiError ? err.message : undefined);
    } finally { upgradeBusy = false; }
  }

  let tokenModal = $state<{ open: boolean; token: string; install: string; expiresAt: string }>({
    open: false, token: '', install: '', expiresAt: ''
  });
  async function rotateToken() {
    if (isLocal) return;
    if (!(await confirm.ask({
      title: `Re-issue token for ${hostName}`,
      message: 'Generate a new enrollment token?',
      body: 'Running the new install command re-enrolls this host under the same ID. The old certificate stays valid until then. Use this if the agent’s certificate is suspect or the host needs a reinstall.',
      confirmLabel: 'Re-issue'
    }))) return;
    try {
      const data = await api.agents.rotateToken(id);
      tokenModal = { open: true, token: data.token, install: data.install_hint, expiresAt: data.token_expires_at };
    } catch (err) {
      toast.error('Re-issue failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function revokeHost() {
    if (isLocal) return;
    if (!(await confirm.ask({
      title: `Revoke ${hostName}`,
      message: `Revoke host "${hostName}"?`,
      body: 'The agent’s certificate is invalidated. Containers running there are forgotten by Dockmesh — they keep running on the host until you stop them manually.',
      confirmLabel: 'Revoke', danger: true, typeToConfirm: hostName
    }))) return;
    revokeBusy = true;
    try {
      await api.agents.delete(id);
      toast.success('Revoked', hostName);
      goto('/hosts');
    } catch (err) {
      toast.error('Revoke failed', err instanceof ApiError ? err.message : undefined);
    } finally { revokeBusy = false; }
  }

  const lensIsThis = $derived(hosts.id === id);
  const CT_COLS = '14px minmax(180px, 1.4fr) minmax(160px, 1.4fr) 110px 150px';
  const ST_COLS = 'minmax(180px, 1.6fr) 110px 110px 120px';
  const MG_COLS = 'minmax(160px, 1.4fr) minmax(140px, 1fr) 110px 110px 120px 110px';
</script>

<section class="dm-page">
  {#if loading && !agent && !isLocal}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="4rem" />
  {:else if loadError && !isLocal}
    <EmptyState tone="error" title="Could not load this host" description={loadError}>
      {#snippet action()}<a class="dm-btn" href="/hosts">Back to hosts</a>{/snippet}
    </EmptyState>
  {:else}
    <div class="dm-ph">
      <h1>{hostName}</h1>
      <StatusChip tone={statusTone} label={status} />
      <span class="dm-sub dm-trunc">
        {hostKind}{hostname ? ` · ${hostname}` : ''}{!isLocal && agent?.os ? ` · ${agent.os}/${agent.arch}` : ''}{!isLocal && agent?.docker_version ? ` · Docker ${agent.docker_version}` : ''}{agent?.online_since ? ` · up ${fmtUptime(agent.online_since)}` : ''}
      </span>
      <span class="htags">
        {#each tags as t (t)}<TagPill label={t} />{/each}
        {#if allowed('hosts.tag')}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => selectTab('tags')}>{tags.length ? 'Edit' : '+ Tag'}</button>
        {/if}
      </span>
      <span class="dm-sp"></span>
      <LiveIndicator updatedAt={lastLoaded} onrefresh={loadAll} busy={refreshing} />
      <button type="button" class="dm-btn" disabled={lensIsThis || status !== 'online'} onclick={() => hosts.set(id)} title="Filter every page by this host">
        {lensIsThis ? 'Current lens' : 'Set as lens'}
      </button>
      {#if allowed('hosts.update')}
        <button type="button" class="dm-btn" disabled={isLocal || status !== 'online'} onclick={openDrain} title={isLocal ? 'The local server runs Dockmesh itself and cannot be drained' : ''}>
          <ArrowDownToLine size={13} strokeWidth={1.8} /> Drain…
        </button>
        {#if needsUpgrade}
          <button type="button" class="dm-btn dm-btn-primary" disabled={upgradeBusy} onclick={upgradeAgent}>
            <RefreshCw size={13} strokeWidth={1.8} /> {upgradeBusy ? 'Upgrading…' : `Upgrade to ${fmtVersion(upgradePolicyVersion)}`}
          </button>
        {/if}
      {/if}
    </div>

    <Tabs {tabs} active={tab} onchange={selectTab} label="Host sections" />

    {#if tab === 'overview'}
      <StatBar items={snapshot} />
      <div class="grid2">
        <div class="col">
          {#if metrics && (metrics.mounts?.length || metrics.mounts_unsupported)}
            <div>
              <h2 class="dm-h2">Disks</h2>
              <MountTable mounts={metrics.mounts} unsupported={metrics.mounts_unsupported} />
            </div>
          {/if}
          <div>
            <h2 class="dm-h2">Stacks on this host <span class="dm-dim dm-num">{stacksOnHost.length}</span></h2>
            {#if stacksOnHost.length === 0}
              <div class="dm-tbl"><div class="dm-empty">{status === 'pending' ? 'The host has not connected yet.' : `No stacks run on ${hostName}.`}</div></div>
            {:else}
              <div class="dm-tbl">
                {#each stacksOnHost.slice(0, 8) as s (s.name)}
                  <a class="dm-tr dm-click lnk" style="grid-template-columns: minmax(0, 1fr) 90px" href="/stacks/{encodeURIComponent(s.name)}">
                    <span class="dm-nm"><span class="dm-t">{s.name}</span></span>
                    <span class="dm-num dm-dim" style="text-align: right">{s.containers ?? '—'} containers</span>
                  </a>
                {/each}
              </div>
              {#if stacksOnHost.length > 8}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm more" onclick={() => selectTab('stacks')}>All {stacksOnHost.length} stacks →</button>
              {/if}
            {/if}
          </div>
        </div>
        <div class="col">
          <div>
            <h2 class="dm-h2">Details</h2>
            <dl class="dm-kv">
              <dt>ID</dt><dd class="dm-mono">{id}</dd>
              <dt>Hostname</dt><dd class="dm-mono">{hostname || '—'}</dd>
              {#if !isLocal && agent}
                <dt>OS</dt><dd>{agent.os ? `${agent.os}/${agent.arch}` : '—'}{metrics ? ` · ${metrics.cpu_cores} cores · ${fmtMem(metrics.mem_total)}` : ''}</dd>
                <dt>Docker</dt><dd class="dm-mono">{agent.docker_version || '—'}</dd>
                <dt>Agent</dt>
                <dd class="dm-mono">
                  {agent.version ? fmtVersion(agent.version) : '—'}
                  {#if needsUpgrade}<span class="warn"> · server is on {fmtVersion(upgradePolicyVersion)}</span>
                  {:else if agent.update_recommended}<span class="warn"> · agent update recommended</span>{/if}
                </dd>
                <dt>Last seen</dt><dd>{fmtAgoNever(agent.last_seen_at)}</dd>
                <dt>Online since</dt><dd>{agent.online_since ? `${fmtAgoNever(agent.online_since)} (${fmtUptime(agent.online_since)})` : '—'}</dd>
                <dt>Certificate</dt><dd class="dm-mono" title={agent.cert_fingerprint}>expires {fmtDate(agent.cert_expires_at)}{agent.cert_fingerprint ? ` · ${agent.cert_fingerprint.slice(0, 23)}…` : ''}</dd>
                <dt>Enrolled</dt><dd>{fmtDate(agent.created_at)}</dd>
              {:else}
                <dt>Server</dt><dd class="dm-mono">{hostVersion ? fmtVersion(hostVersion) : '—'}</dd>
                {#if metrics}<dt>Capacity</dt><dd>{metrics.cpu_cores} cores · {fmtMem(metrics.mem_total)}</dd>{/if}
                <dt>Uptime</dt><dd>{metrics?.uptime_seconds ? fmtDur(metrics.uptime_seconds) : '—'}</dd>
              {/if}
              <dt>Containers</dt><dd>{running} of {containers.length} running</dd>
            </dl>
          </div>
          <div>
            <h2 class="dm-h2">Recent migrations</h2>
            {#if migrations.length === 0}
              <p class="dm-sub">No migrations involving {hostName} yet.</p>
            {:else}
              <div class="dm-tbl">
                {#each migrations.slice(0, 5) as m (m.id)}
                  {@const out = m.source_host_id === id}
                  <div class="dm-tr" style="grid-template-columns: 40px minmax(0, 1fr) 100px 90px">
                    <span class="dm-num dm-dim">{out ? '↗ out' : '↙ in'}</span>
                    <a class="dm-trunc" href="/stacks/{encodeURIComponent(m.stack_name)}">{m.stack_name}</a>
                    <StatusChip tone={m.status === 'completed' ? 'ok' : m.status === 'failed' ? 'bad' : 'warn'} label={m.status} />
                    <span class="dm-num dm-dim" style="text-align: right">{fmtAgoNever(m.completed_at ?? m.started_at)}</span>
                  </div>
                {/each}
              </div>
              {#if migrations.length > 5}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm more" onclick={() => selectTab('migrations')}>All {migrations.length} migrations →</button>
              {/if}
            {/if}
          </div>
        </div>
      </div>

    {:else if tab === 'containers'}
      {#if containers.length === 0}
        <EmptyState title="No containers" description={status === 'pending' ? 'The host has not connected yet.' : `No containers on ${hostName}.`} />
      {:else}
        <div class="dm-tbl" role="table" aria-label="Containers on {hostName}">
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {CT_COLS}">
            <span role="columnheader"></span><span role="columnheader">Container</span><span role="columnheader">Image</span><span role="columnheader">State</span><span role="columnheader">Status</span>
          </div>
          {#each containers as c (c.Id)}
            {@const name = c.Names?.[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)}
            {@const href = `/containers/${c.Id}${isLocal ? '' : `?host=${id}`}`}
            {@const st = c.State === 'running' ? (String(c.Status).includes('unhealthy') ? 'warn' : 'ok') : c.State === 'exited' || c.State === 'dead' ? 'bad' : 'off'}
            <a class="dm-tr dm-click lnk" role="row" style="grid-template-columns: {CT_COLS}" {href}>
              <span role="cell"><span class="dm-dot {st === 'off' ? '' : st}"></span></span>
              <span role="cell" class="dm-nm"><span class="dm-t">{name}</span><small>{c.Labels?.['com.docker.compose.project'] ?? 'standalone'}</small></span>
              <span role="cell" class="dm-mono dm-dim" title={c.Image}>{c.Image}</span>
              <span role="cell"><StatusChip tone={st as Tone} label={c.State} /></span>
              <span role="cell" class="dm-dim" title={c.Status}>{c.Status ?? '—'}</span>
            </a>
          {/each}
        </div>
      {/if}

    {:else if tab === 'stacks'}
      {#if stacksOnHost.length === 0}
        <EmptyState title="No stacks here" description={`No stacks deployed on ${hostName}.`} />
      {:else}
        <div class="dm-tbl" role="table" aria-label="Stacks on {hostName}">
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {ST_COLS}">
            <span role="columnheader">Stack</span><span role="columnheader">Containers</span><span role="columnheader">Volumes</span><span role="columnheader"></span>
          </div>
          {#each stacksOnHost as s (s.name)}
            <div class="dm-tr" role="row" style="grid-template-columns: {ST_COLS}">
              <span role="cell" class="dm-nm"><a href="/stacks/{encodeURIComponent(s.name)}">{s.name}</a></span>
              <span role="cell" class="dm-num">{s.containers ?? '—'}</span>
              <span role="cell" class="dm-dim">{s.has_volumes ? 'stateful' : '—'}</span>
              <span role="cell" style="text-align: right">
                {#if allowed('stacks.migrate')}<a href="/stacks/{encodeURIComponent(s.name)}?action=migrate" class="dm-btn dm-btn-ghost dm-btn-sm">Migrate…</a>{/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}

    {:else if tab === 'migrations'}
      {#if migrations.length === 0}
        <EmptyState title="No migrations" description={`No stack migrations involving ${hostName} yet.`} />
      {:else}
        <div class="dm-tbl" role="table" aria-label="Migrations">
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {MG_COLS}">
            <span role="columnheader">Stack</span><span role="columnheader">Direction</span><span role="columnheader">Status</span><span role="columnheader">Started</span><span role="columnheader">By</span><span role="columnheader">Completed</span>
          </div>
          {#each migrations as m (m.id)}
            <div class="dm-tr" role="row" style="grid-template-columns: {MG_COLS}">
              <span role="cell" class="dm-nm"><a href="/stacks/{encodeURIComponent(m.stack_name)}">{m.stack_name}</a></span>
              <span role="cell" class="dm-num dm-dim">{m.source_host_id === id ? `→ ${m.target_host_id}` : `← ${m.source_host_id}`}</span>
              <span role="cell"><StatusChip tone={m.status === 'completed' ? 'ok' : m.status === 'failed' ? 'bad' : 'warn'} label={m.status} /></span>
              <span role="cell" class="dm-num dm-dim">{fmtAgoNever(m.started_at)}</span>
              <span role="cell" class="dm-dim">{m.initiated_by}</span>
              <span role="cell" class="dm-num dm-dim">{m.completed_at ? fmtAgoNever(m.completed_at) : '—'}</span>
            </div>
          {/each}
        </div>
      {/if}

    {:else if tab === 'tags'}
      <div class="dm-form-sec">
        <div>
          <h4>Tags on this host</h4>
          <p>Tags group hosts for filtering and limit which hosts a user or role can see and manage (host scope).</p>
        </div>
        <div>
          <div class="tagedit">
            {#each tagsDraft as t (t)}
              <span class="tagx"><TagPill label={t} />{#if allowed('hosts.tag')}<button type="button" onclick={() => (tagsDraft = tagsDraft.filter((x) => x !== t))} aria-label="Remove {t}"><X size={11} /></button>{/if}</span>
            {/each}
            {#if allowed('hosts.tag')}
              <input
                type="text"
                class="dm-filter"
                placeholder={tagsDraft.length === 0 ? 'add a tag…' : 'add tag'}
                aria-label="Add tag"
                bind:value={tagInput}
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTagDraft(tagInput); } }}
              />
            {/if}
          </div>
          {#if allowed('hosts.tag') && allFleetTags.filter((t) => !tagsDraft.includes(t)).length > 0}
            <div class="suggest">
              <span class="dm-dim">In use:</span>
              {#each allFleetTags.filter((t) => !tagsDraft.includes(t)) as s (s)}
                <TagPill label={s} onclick={addTagDraft} title="Add {s}" />
              {/each}
            </div>
          {/if}
          {#if allowed('hosts.tag')}
            <div class="row">
              <button type="button" class="dm-btn dm-btn-primary" disabled={tagsBusy || tagsDraft.join(',') === tags.join(',')} onclick={saveTags}>{tagsBusy ? 'Saving…' : 'Save tags'}</button>
              <button type="button" class="dm-btn dm-btn-ghost" disabled={tagsDraft.join(',') === tags.join(',')} onclick={() => (tagsDraft = [...tags])}>Revert</button>
            </div>
          {/if}
        </div>
      </div>

    {:else if tab === 'maintenance'}
      <div>
        <div class="dm-form-sec">
          <div>
            <h4>Drain host</h4>
            <p>Moves every stack off {hostName} to other matching hosts. The host stays available for new deploys. Use before reboots or kernel upgrades.</p>
          </div>
          <div>
            <button type="button" class="dm-btn" disabled={isLocal || !allowed('hosts.update') || status !== 'online'} onclick={openDrain}>
              <ArrowDownToLine size={13} strokeWidth={1.8} /> Plan drain…
            </button>
            {#if isLocal}<span class="dm-sub">The local server runs Dockmesh itself and cannot be drained.</span>{/if}
          </div>
        </div>
        <div class="dm-form-sec">
          <div>
            <h4>Upgrade agent</h4>
            <p>
              {#if isLocal}The server updates itself under Settings → Updates.
              {:else if !needsUpgrade}{hostVersion ? `Already on ${fmtVersion(hostVersion)}.` : 'Version unknown.'}
              {:else}On {fmtVersion(hostVersion)}, the server is on {fmtVersion(upgradePolicyVersion)}. The agent reconnects within about 10 seconds; containers keep running.{/if}
            </p>
          </div>
          <div>
            <button type="button" class="dm-btn dm-btn-primary" disabled={isLocal || !needsUpgrade || upgradeBusy || !allowed('hosts.update')} onclick={upgradeAgent}>
              <RefreshCw size={13} strokeWidth={1.8} /> {upgradeBusy ? 'Upgrading…' : 'Upgrade now'}
            </button>
          </div>
        </div>
        {#if allowed('hosts.create')}
          <div class="dm-form-sec">
            <div>
              <h4>Re-issue enrollment token</h4>
              <p>For a suspect certificate or a reinstalled host. The old certificate is revoked once the agent presents the new one.</p>
            </div>
            <div>
              <button type="button" class="dm-btn" disabled={isLocal} onclick={rotateToken}><RefreshCw size={13} strokeWidth={1.8} /> Re-issue…</button>
            </div>
          </div>
        {/if}
        {#if allowed('hosts.delete')}
          <div class="dm-form-sec">
            <div>
              <h4 class="danger">Revoke host</h4>
              <p>Removes {hostName} from the fleet and invalidates its certificate. Containers keep running on the machine but are no longer managed.</p>
            </div>
            <div>
              <button type="button" class="dm-btn dm-btn-danger" disabled={isLocal || revokeBusy} onclick={revokeHost}>
                <Trash2 size={13} strokeWidth={1.8} /> {revokeBusy ? 'Revoking…' : 'Revoke…'}
              </button>
            </div>
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</section>

<Modal bind:open={showDrain} title="Drain {hostName}" maxWidth="max-w-xl">
  {#if drainLoading}
    <p class="dm-sub">Working out where each stack can go…</p>
  {:else if drainPlan}
    {#if drainPlan.entries.length === 0}
      <p class="dm-sub">No stacks run here, so there is nothing to move.</p>
    {:else}
      {@const stuck = drainPlan.entries.filter((e) => !e.feasible)}
      {#if stuck.length > 0}
        <div class="warnbox" role="alert">
          <AlertTriangle size={14} strokeWidth={1.8} />
          <span><b>{stuck.length} stack{stuck.length === 1 ? '' : 's'} cannot be moved.</b> No other host carries a matching tag. Pin the stack here, drop the affinity, or add the tag to another host.</span>
        </div>
      {/if}
      <div class="dm-tbl">
        {#each drainPlan.entries as e (e.stack_name)}
          <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 20px minmax(0, 1fr)">
            <span class="dm-mono">{e.stack_name}</span>
            <span class="dm-dim">→</span>
            <span class="dm-mono" class:bad={!e.feasible}>{e.feasible ? e.target_name : 'no candidate'}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (showDrain = false)}>Cancel</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={drainBusy || !drainPlan?.feasible} onclick={executeDrain}>
      <ArrowDownToLine size={13} strokeWidth={1.8} /> {drainBusy ? 'Starting…' : 'Begin drain'}
    </button>
  {/snippet}
</Modal>

<Modal bind:open={tokenModal.open} title="New enrollment token for {hostName}" maxWidth="max-w-xl">
  <div class="tok">
    <p class="dm-sub">Run this command on the agent host. The old certificate stays valid until the agent presents the new one.</p>
    <pre class="cmd dm-mono">{tokenModal.install}</pre>
    <p class="dm-sub">The token is shown only once and expires {tokenModal.expiresAt ? new Date(tokenModal.expiresAt).toLocaleString() : '—'}.</p>
    <dl class="dm-kv"><dt>Token</dt><dd class="dm-mono">{tokenModal.token}</dd></dl>
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => copyWithToast(tokenModal.install, 'Install command copied')}>Copy install command</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={() => (tokenModal.open = false)}>Done</button>
  {/snippet}
</Modal>

<style>
  .htags { display: inline-flex; gap: 4px; align-items: center; flex-wrap: wrap; }
  .grid2 { display: grid; grid-template-columns: minmax(0, 3fr) minmax(300px, 2fr); gap: 28px; align-items: start; }
  .col { display: flex; flex-direction: column; gap: 22px; min-width: 0; }
  @media (max-width: 1100px) { .grid2 { grid-template-columns: minmax(0, 1fr); } }
  .lnk { color: var(--fg); text-decoration: none; }
  .more { margin-top: 6px; }
  .warn { color: var(--warn); }
  .bad { color: var(--bad); }
  .danger { color: var(--bad); }
  .tagedit { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .tagedit input { min-width: 160px; }
  .tagx { display: inline-flex; align-items: center; gap: 2px; }
  .tagx button { all: unset; cursor: pointer; color: var(--subtle); display: inline-flex; }
  .tagx button:hover { color: var(--bad); }
  .suggest { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; font-size: var(--fs-sm); }
  .row { display: flex; gap: 8px; }
  .warnbox {
    display: flex; gap: 8px; align-items: flex-start; margin-bottom: 12px;
    border: 1px solid var(--warn-bd); background: var(--warn-bg); color: var(--warn);
    border-radius: var(--r); padding: 8px 12px; font-size: var(--fs-sm);
  }
  .warnbox span { color: var(--fg); }
  .tok { display: flex; flex-direction: column; gap: 12px; }
  .tok p { margin: 0; }
  .cmd { margin: 0; padding: 10px 12px; background: var(--field); border-radius: var(--r-sm); font-size: 12px; white-space: pre-wrap; word-break: break-all; }
</style>
