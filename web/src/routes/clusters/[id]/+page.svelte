<script lang="ts">
  // A Swarm cluster: state, resilience, how the work spreads over the nodes,
  // and read-only lists of nodes, services, tasks, stacks and networks, all
  // read through the cluster's managing node.
  import { m } from '$lib/paraglide/messages.js';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import {
    api, ApiError,
    type Cluster, type ClusterDistribution, type ClusterDistNode, type ClusterDistService,
    type ClusterMembership, type ClusterProof, type HostInfo, type SwarmService, type SwarmTask, type SwarmNetwork
  } from '$lib/api';
  import {
    clusterTone, clusterStateLabel, heaviest, taskProblems, servicesByStack,
    nodeStateLabel, availabilityLabel, nodeLabel, pinningConstraint, FAILED_STATES, RESTART_WINDOW_MS, type TaskProblem
  } from '$lib/cluster';
  import { fmtAgo, fmtMem } from '$lib/format';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { fmtNumber } from '$lib/i18n';
  import { latest } from '$lib/latest';
  import { urlTab } from '$lib/urlTab.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { Button, Skeleton, EmptyState, StatBar, StatusChip, Tabs, LiveIndicator, Segmented } from '$lib/components/ui';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import AdoptStackDialog from './_AdoptStackDialog.svelte';
  import ScaleServiceDialog from './_ScaleServiceDialog.svelte';
  import ImageUpdateDialog from './_ImageUpdateDialog.svelte';
  import { AlertTriangle, Info, CircleSlash, CircleDashed, Network, Waypoints, ShieldCheck, ShieldAlert } from 'lucide-svelte';

  const id = $derived(page.params.id ?? '');

  type TabId = 'overview' | 'nodes' | 'services' | 'tasks' | 'stacks' | 'networks';
  const TAB_IDS: TabId[] = ['overview', 'nodes', 'services', 'tasks', 'stacks', 'networks'];
  const openTab = urlTab(TAB_IDS, 'overview');
  const tab = $derived(openTab.current);

  // Above this share of CPU or memory a node's bar turns yellow.
  const BUSY_PCT = 65;
  const MAX_TASKS = 500;

  let cluster = $state<Cluster | null>(null);
  let memberOnly = $state<ClusterMembership | null>(null);
  let hostNames = $state<Record<string, string>>({});
  let dist = $state<ClusterDistribution | null>(null);
  let services = $state<SwarmService[] | null>(null);
  let tasks = $state<SwarmTask[] | null>(null);
  let networks = $state<SwarmNetwork[] | null>(null);
  // Set when a list is refused for want of a permission on the cluster.
  let svcDenied = $state(false);
  let netDenied = $state(false);
  let readError = $state<string | null>(null);
  // When the lists shown were read; they stay after a failed read.
  let readAt = $state<number | null>(null);
  let loading = $state(true);
  let notFound = $state(false);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);

  let distView = $state<'node' | 'service'>('node');
  let taskFilter = $state<'current' | 'problems' | 'all'>('current');
  let taskSearch = $state('');

  const denied = (e: unknown) => e instanceof ApiError && e.status === 403;
  const errText = (e: unknown) => (e instanceof ApiError ? e.message : m.cl_read_failed());

  const nextLoad = latest();
  async function loadAll() {
    const current = nextLoad();
    refreshing = true;
    try {
      const [list, mships, hostList] = await Promise.all([
        api.clusters.list().catch(() => [] as Cluster[]),
        api.clusters.memberships().catch(() => [] as ClusterMembership[]),
        api.hosts.list().catch(() => [] as HostInfo[])
      ]);
      if (!current()) return;
      hostNames = Object.fromEntries(hostList.map((h) => [h.id, h.name]));
      cluster = list.find((c) => c.id === id) ?? null;
      if (!cluster) {
        memberOnly = mships.find((x) => x.cluster_id === id) ?? null;
        notFound = !memberOnly;
        return;
      }
      memberOnly = null;
      notFound = false;
      const [d, sv, tk, nw] = await Promise.allSettled([
        api.clusters.distribution(id),
        api.clusters.services(id),
        api.clusters.tasks(id),
        api.clusters.networks(id)
      ]);
      if (!current()) return;
      readError = null;
      if (d.status === 'fulfilled') dist = d.value;
      else readError = errText(d.reason);
      if (sv.status === 'fulfilled') services = sv.value.services;
      svcDenied = sv.status === 'rejected' && denied(sv.reason);
      if (tk.status === 'fulfilled') tasks = tk.value.tasks;
      if (nw.status === 'fulfilled') networks = nw.value.networks;
      netDenied = nw.status === 'rejected' && denied(nw.reason);
      if (d.status === 'fulfilled' && !d.value.stale) readAt = Date.now();
      lastLoaded = Date.now();
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
      cluster = null;
      dist = null;
      services = null;
      tasks = null;
      networks = null;
      readAt = null;
      loading = true;
      loadAll();
    });
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') loadAll();
    }, 15_000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    pageContext.set(cluster?.name ?? memberOnly?.cluster_name ?? '');
    return () => pageContext.clear();
  });

  const st = $derived(cluster?.status);
  const cstate = $derived(st?.state ?? 'unknown');
  // Without a fresh read the page shows what was last read, read-only.
  const readOnly = $derived(cstate === 'no_quorum' || cstate === 'unreachable' || !!dist?.stale);
  // Managers first, then by name, as docker node ls reads.
  const nodes = $derived(
    [...(dist?.nodes ?? [])].sort((a, b) => (a.role === b.role ? nodeLabel(a).localeCompare(nodeLabel(b)) : a.role === 'manager' ? -1 : 1))
  );
  const nodeName = (nodeId: string | undefined) => {
    const n = nodes.find((x) => x.id === nodeId);
    return n ? nodeLabel(n) : nodeId ? nodeId.slice(0, 12) : '—';
  };
  // Hosts the caller can't view are named by their node's hostname.
  const nameOf = (hostId: string) => {
    const n = nodes.find((x) => x.host_id === hostId);
    return hostNames[hostId] ?? (n ? nodeLabel(n) : hostId);
  };
  const originName = $derived(cluster ? nameOf(cluster.origin_host) : '');
  const readId = $derived(st?.read_host ?? cluster?.origin_host ?? '');
  const readNode = $derived(nodes.find((n) => n.host_id === readId));
  const readName = $derived(readId ? nameOf(readId) : '');
  const failover = $derived(!!st?.failover);
  const managers = $derived(st?.managers ?? nodes.filter((n) => n.role === 'manager').length);
  const reachable = $derived(st?.managers_reachable ?? 0);
  const quorum = $derived(Math.floor(managers / 2) + 1);
  const tolerance = $derived(Math.max(0, reachable - quorum));
  const downNodes = $derived(nodes.filter((n) => n.state !== 'ready' || (n.role === 'manager' && n.reachability && n.reachability !== 'reachable')));

  const problems = $derived(tasks ? taskProblems(tasks) : new Map<string, TaskProblem>());
  const svcById = $derived(Object.fromEntries((services ?? []).map((s) => [s.id, s])) as Record<string, SwarmService>);
  const distSvcById = $derived(Object.fromEntries((dist?.services ?? []).map((s) => [s.id, s])) as Record<string, ClusterDistService>);
  const stacks = $derived(servicesByStack(services ?? []));
  const namedStacks = $derived(stacks.filter((g) => g.stack !== ''));
  // A stack whose every service carries the spec hash is Dockmesh's.
  const isOwn = (g: { services: SwarmService[] }) => g.services.length > 0 && g.services.every((s) => !!s.spec_hash);
  const externalStacks = $derived(namedStacks.filter((g) => !isOwn(g)));
  let acting = $state('');
  const updateLabel = (st: string) =>
    ({
      updating: m.cl_upd_updating(),
      paused: m.cl_upd_paused(),
      rollback_started: m.cl_upd_rolling_back(),
      rollback_paused: m.cl_upd_rollback_paused(),
      rollback_completed: m.cl_upd_rolled_back()
    })[st] ?? st;
  const updateTone = (st: string) => (st === 'paused' || st === 'rollback_paused' ? 'dm-pill-danger' : st === 'rollback_completed' ? 'dm-pill-warning' : 'dm-pill-info');
  async function serviceAction(stack: string, service: string, s: SwarmService, action: 'rollback' | 'resume') {
    const ok = await confirm.ask({
      title: action === 'rollback' ? m.cl_svc_rollback_title({ name: s.name }) : m.cl_svc_resume_title({ name: s.name }),
      message: action === 'rollback' ? m.cl_svc_rollback_msg() : m.cl_svc_resume_msg(),
      body: s.update_message || undefined,
      confirmLabel: action === 'rollback' ? m.cl_svc_rollback() : m.cl_svc_resume(),
      danger: action === 'rollback'
    });
    if (!ok) return;
    acting = s.id;
    try {
      await api.stacks.swarmServiceAction(stack, service, action);
      toast.success(action === 'rollback' ? m.cl_svc_rollback_done({ name: s.name }) : m.cl_svc_resume_done({ name: s.name }));
      loadAll();
    } catch (e) {
      toast.error(action === 'rollback' ? m.cl_svc_rollback_failed() : m.cl_svc_resume_failed(), e instanceof ApiError ? e.message : undefined);
    } finally {
      acting = '';
    }
  }
  let imageStack = $state('');
  let imageOpen = $state(false);
  let scaleOf = $state<{ stack: string; service: SwarmService } | null>(null);
  let scaleOpen = $state(false);
  let adoptStack = $state('');
  let adoptOpen = $state(false);
  function openAdopt(stack: string) {
    adoptStack = stack;
    adoptOpen = true;
  }
  const desired = $derived((services ?? []).reduce((a, s) => a + s.desired_tasks, 0));
  const running = $derived(services ? services.reduce((a, s) => a + s.running_tasks, 0) : (dist?.tasks ?? 0));
  const waitText = $derived(readError ?? (readOnly ? m.cl_stale_none() : m.cl_loading()));
  const top = $derived(heaviest(dist));
  const maxTasks = $derived(Math.max(1, ...nodes.map((n) => n.tasks)));

  const tabs = $derived([
    { key: 'overview', label: m.cl_tab_overview() },
    { key: 'nodes', label: m.cl_tab_nodes(), count: nodes.length || null },
    ...(svcDenied ? [] : [
      { key: 'services', label: m.cl_tab_services(), count: services?.length ?? null },
      { key: 'tasks', label: m.cl_tab_tasks(), count: problems.size || null, dot: problems.size > 0 },
      { key: 'stacks', label: m.cl_tab_stacks(), count: namedStacks.length || null }
    ]),
    ...(netDenied ? [] : [{ key: 'networks', label: m.cl_tab_networks(), count: networks?.length ?? null }])
  ]);

  const figures = $derived([
    { label: m.cl_fig_nodes(), value: nodes.length || '—', note: st?.known_at ? `${m.cluster_managers({ n: managers })} · ${st.workers === 1 ? m.cluster_workers_one() : m.cluster_workers({ n: st.workers })}` : '' },
    {
      label: m.cl_fig_managers(),
      value: cstate === 'healthy' || cstate === 'degraded' ? m.cl_of({ n: reachable, total: managers }) : '—',
      note: managers ? m.cl_quorum_needs({ n: quorum }) : '',
      tone: (cstate === 'degraded' ? 'warn' : cstate === 'no_quorum' ? 'bad' : '') as '' | 'warn' | 'bad'
    },
    {
      label: m.cl_fig_leader(),
      value: cstate === 'healthy' || cstate === 'degraded' ? (st?.leader ?? '—') : '—',
      note: cstate === 'healthy' || cstate === 'degraded' ? (nodes.find((n) => n.leader)?.host_id ? m.cl_has_agent() : m.cl_no_agent_short()) : ''
    },
    ...(svcDenied ? [] : [
      { label: m.cl_fig_services(), value: services?.length ?? '—', note: services ? (externalStacks.length === 1 ? m.cl_stacks_external_one() : m.cl_stacks_external({ n: externalStacks.length })) : '' },
      {
        label: m.cl_fig_tasks(),
        value: readOnly ? '—' : running,
        note: readOnly || !services ? '' : running >= desired ? m.cl_tasks_as_desired() : m.cl_tasks_desired({ n: desired }),
        tone: (!readOnly && services && running < desired ? 'warn' : '') as '' | 'warn' | 'bad'
      }
    ])
  ]);

  function nodeTone(n: ClusterDistNode): Tone {
    if (n.state !== 'ready') return 'bad';
    if (n.role === 'manager' && n.reachability && n.reachability !== 'reachable') return 'bad';
    return n.availability === 'active' ? 'ok' : 'warn';
  }
  function managerStatus(n: ClusterDistNode): string {
    if (n.role !== 'manager') return '—';
    if (n.leader) return m.cl_mgr_leader();
    return n.reachability === 'reachable' ? m.cl_mgr_reachable() : n.reachability === 'unreachable' ? m.cl_mgr_unreachable() : m.cluster_node_unknown();
  }
  function noMetricsText(n: ClusterDistNode): string {
    switch (n.no_metrics) {
      case 'no_agent':
        return m.cl_no_metrics_agent();
      case 'unreachable':
        return m.cl_no_metrics_unreachable();
      case 'agent_offline':
        return m.cl_no_metrics_offline();
      case 'no_access':
        return m.cl_no_metrics_access();
      default:
        return '—';
    }
  }
  function svcTone(s: SwarmService): Tone {
    if (s.desired_tasks === 0) return 'off';
    return s.running_tasks >= s.desired_tasks ? 'ok' : s.running_tasks === 0 ? 'bad' : 'warn';
  }
  function nodesOf(s: SwarmService): string {
    const per = distSvcById[s.id]?.per_node;
    if (!per) return '—';
    const parts = Object.entries(per).sort(([a], [b]) => nodeName(a).localeCompare(nodeName(b))).map(([n, c]) => (c > 1 ? `${nodeName(n)} ×${c}` : nodeName(n)));
    return parts.length ? parts.join(', ') : '—';
  }
  function cpuMem(s: ClusterDistService | undefined): { cpu: string; mem: string; partial: boolean } {
    if (!s || s.cpu_cores == null) return { cpu: '—', mem: '—', partial: !!s?.partial };
    return { cpu: fmtNumber(s.cpu_cores, { minimumFractionDigits: 2, maximumFractionDigits: 2 }), mem: fmtMem(s.mem_bytes ?? 0), partial: !!s.partial };
  }
  function taskName(t: SwarmTask): string {
    const svc = svcById[t.service_id]?.name ?? t.service_id.slice(0, 12);
    return t.slot ? `${svc}.${t.slot}` : `${svc}.${nodeName(t.node_id)}`;
  }
  function taskTone(t: SwarmTask): Tone {
    if (FAILED_STATES.has(t.state)) return 'bad';
    if (t.state === 'running') return 'ok';
    if (t.desired_state === 'running') return 'warn';
    return 'off';
  }

  const shownTasks = $derived.by(() => {
    const q = taskSearch.trim().toLowerCase();
    const list = (tasks ?? []).filter((t) => {
      if (taskFilter === 'current' && t.desired_state !== 'running' && !problems.has(t.id)) return false;
      if (taskFilter === 'problems' && !problems.has(t.id)) return false;
      if (!q) return true;
      return taskName(t).toLowerCase().includes(q) || nodeName(t.node_id).toLowerCase().includes(q);
    });
    return list.slice(0, MAX_TASKS);
  });

  function showProblems() {
    taskFilter = 'problems';
    openTab.set('tasks');
  }

  const memberOf = (hostId: string | undefined) => (hostId ? cluster?.members.find((x) => x.host_id === hostId) : undefined);
  function proofText(p: ClusterProof): string {
    switch (p) {
      case 'verified':
        return m.cl_proof_verified();
      case 'local':
        return m.cl_proof_local();
      case 'agent_too_old':
        return m.cl_proof_agent_old();
      case 'no_trust_root':
        return m.cl_proof_no_root();
      case 'key_locked':
        return m.cl_proof_key_locked();
      case 'invalid':
        return m.cl_proof_invalid();
      case 'failed':
        return m.cl_proof_failed();
      default:
        return m.cl_proof_pending();
    }
  }
  function addrHint(w: string): string {
    const [agent, node] = w.split('|');
    return m.cl_addr_differs({ agent, node });
  }

  let confirming = $state(false);
  async function confirmRoot() {
    if (!cluster || confirming) return;
    confirming = true;
    try {
      cluster = await api.clusters.confirmTrustRoot(cluster.id, cluster.trust.pending_fingerprint ?? '');
      toast.success(m.cl_trust_confirmed());
    } catch (e) {
      toast.error(m.cl_trust_confirm_failed(), e instanceof ApiError ? e.message : undefined);
    } finally {
      confirming = false;
    }
  }

  const NODE_COLS = '20px minmax(160px, 1.4fr) 80px 90px 100px 110px 60px 90px minmax(110px, 1fr) 120px';
  const DIST_COLS = 'minmax(170px, 1.3fr) minmax(150px, 1.2fr) minmax(130px, 1fr) minmax(130px, 1fr) 110px';
  const SVC_COLS = '20px minmax(170px, 1.4fr) 92px 74px minmax(120px, 1fr) 64px 84px minmax(140px, 1.2fr) minmax(120px, 1fr) 80px';
  const TASK_COLS = '20px minmax(170px, 1.3fr) minmax(110px, 0.8fr) 90px 80px 92px minmax(180px, 2fr) 90px';
  const STACK_COLS = '20px minmax(160px, 1.4fr) minmax(200px, 1.6fr) 80px 90px';
  const NET_COLS = 'minmax(160px, 1.3fr) 80px minmax(120px, 1fr) minmax(110px, 1fr) 100px minmax(160px, 1.6fr)';
</script>

<section class="dm-page">
  {#if loading && !cluster && !memberOnly}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="4rem" />
  {:else if notFound}
    <EmptyState tone="error" title={m.cl_not_found()} description={m.cl_not_found_hint()}>
      {#snippet action()}<a class="dm-btn" href="/hosts">{m.hosts_back()}</a>{/snippet}
    </EmptyState>
  {:else if memberOnly}
    <div class="dm-ph"><h1>{memberOnly.cluster_name}</h1></div>
    <div class="note info" role="note" data-testid="cluster-no-access">
      <Info size={14} strokeWidth={1.8} />
      <div><b>{m.cl_no_access_title()}</b> <span>{m.cl_no_access_text()}</span></div>
    </div>
  {:else if cluster}
    <div class="dm-ph">
      <h1>{cluster.name}</h1>
      <StatusChip tone={clusterTone(cstate)} label={clusterStateLabel(cstate)} />
      <span class="dm-sp"></span>
      <LiveIndicator updatedAt={lastLoaded} onrefresh={loadAll} busy={refreshing} />
      {#if !netDenied && !svcDenied}
        <a class="dm-btn" href="/topology?cluster={encodeURIComponent(cluster.id)}"><Waypoints size={13} strokeWidth={1.8} /> {m.cl_topology()}</a>
      {/if}
    </div>
    <div class="dm-sub sub" data-testid="cluster-sub">
      {m.cl_sub_swarm()} · {m.cl_sub_id()} <span class="dm-mono" title={cluster.id}>{cluster.id.slice(0, 12)}</span>
      · {m.cl_sub_managed({ host: readName })}{readNode?.leader ? ` (${m.cl_mgr_leader().toLowerCase()})` : ''}{failover ? ` · ${m.cl_sub_failover({ host: readName })}` : ''}
      · {m.cl_sub_added({ ago: fmtAgo(cluster.created_at) })}
    </div>

    {#if cstate === 'degraded'}
      <div class="note warn" role="status" data-testid="cluster-notice">
        <AlertTriangle size={14} strokeWidth={1.8} />
        <div>
          <b>{downNodes.length === 1 ? m.cl_degraded_one({ node: nodeLabel(downNodes[0]) }) : m.cl_degraded_many({ nodes: downNodes.map(nodeLabel).join(', ') })}</b>
          <span>
            {m.cl_degraded_quorum({ n: reachable, total: managers })}
            {tolerance === 0 ? m.cl_tolerance_none() : tolerance === 1 ? m.cl_tolerance_one() : m.cl_tolerance({ n: tolerance })}
            {m.cl_degraded_moves()}
          </span>
        </div>
      </div>
    {:else if cstate === 'no_quorum'}
      <div class="note bad" role="status" data-testid="cluster-notice">
        <CircleSlash size={14} strokeWidth={1.8} />
        <div>
          <b>{m.cl_noquorum_title()}</b>
          <span>{m.cl_noquorum_text({ n: quorum, total: managers })}{st?.known_at ? ` ${m.cl_last_known({ ago: fmtAgo(st.known_at) })}` : ''}</span>
        </div>
      </div>
    {:else if cstate === 'unreachable'}
      <div class="note grey" role="status" data-testid="cluster-notice">
        <CircleDashed size={14} strokeWidth={1.8} />
        <div>
          <b>{failover ? m.cl_unreach_title_any() : m.cl_unreach_title({ host: originName })}</b>
          <span>{st?.known_at ? m.cl_unreach_known({ ago: fmtAgo(st.known_at) }) : m.cl_unreach_unknown()} {failover ? m.cl_unreach_check_any() : m.cl_unreach_check({ host: originName })}</span>
        </div>
      </div>
    {:else if cstate === 'unknown'}
      <div class="note grey" role="status" data-testid="cluster-notice">
        <CircleDashed size={14} strokeWidth={1.8} />
        <div><span>{m.cluster_line_not_checked()}</span></div>
      </div>
    {/if}

    {#if cstate === 'healthy' || cstate === 'degraded'}
      <div class="note info" role="note" data-testid="cluster-resilience">
        <Info size={14} strokeWidth={1.8} />
        <div>
          <span>
            {failover ? m.cl_res_failover({ host: readName, n: st?.verified_managers ?? 0 }) : `${m.cl_res_read({ host: readName })} ${m.cl_res_need_two()}`}
            {managers < 3 ? m.cl_res_small() : managers === 3 ? m.cl_res_one() : m.cl_res_many({ n: managers - quorum })}
          </span>
        </div>
      </div>
    {/if}

    {#if cluster.trust.pending}
      <div class="note warn" role="status" data-testid="cluster-trust-pending">
        <ShieldAlert size={14} strokeWidth={1.8} />
        <div>
          <b>{m.cl_trust_pending_title()}</b>
          <span>{cluster.trust.confirmable ? m.cl_trust_pending_single() : m.cl_trust_pending_many()}</span>
          <small class="dm-mono fp" data-testid="cluster-trust-fp">
            {m.cl_trust_fp_pinned()} {cluster.trust.pinned_fingerprint ?? '—'}<br />
            {m.cl_trust_fp_new()} {cluster.trust.pending_fingerprint ?? '—'}
          </small>
          {#if cluster.trust.confirmable}<small class="dm-sub">{m.cl_trust_fp_hint()}</small>{/if}
          {#if cluster.trust.confirmable && allowed('clusters.create')}
            <div class="noteact"><button type="button" class="dm-btn" disabled={confirming} onclick={confirmRoot}>{m.cl_trust_confirm()}</button></div>
          {/if}
        </div>
      </div>
    {:else if !cluster.trust.origin_verified}
      <div class="note grey" role="note" data-testid="cluster-origin-unverified">
        <ShieldAlert size={14} strokeWidth={1.8} />
        <div>
          <b>{m.cl_origin_unverified_title({ host: originName })}</b>
          <span>{proofText(memberOf(cluster.origin_host)?.proof ?? 'pending')} {m.cl_origin_unverified_text()}</span>
        </div>
      </div>
    {/if}

    {#if readError && !dist}
      <div class="note grey" role="status"><CircleDashed size={14} strokeWidth={1.8} /><div><span>{readError}</span></div></div>
    {/if}

    <div class:dim={readOnly}><StatBar items={figures} /></div>

    <Tabs {tabs} active={tab} onchange={(t) => openTab.set(t)} label={m.cl_sections()} />

    <div class:dim={readOnly}>
      {#if readOnly && (dist || services)}
        <p class="dm-sub stale" data-testid="cluster-stale">{dist?.stale && dist.known_at ? m.cl_stale_nodes({ ago: fmtAgo(dist.known_at) }) : readAt ? m.cl_stale_read({ ago: fmtAgo(readAt) }) : m.cl_stale_none()}</p>
      {/if}

      {#if tab === 'overview'}
        {#if problems.size > 0 && !readOnly}
          <p class="probline" data-testid="tasks-failing">
            <AlertTriangle size={13} strokeWidth={1.8} />
            <button type="button" class="linkbtn" onclick={showProblems}>{problems.size === 1 ? m.cl_failing_one() : m.cl_failing({ n: problems.size })}</button>
          </p>
        {/if}
        <div class="sec">
          <div class="sechead">
            <div>
              <h2 class="dm-h2">{m.cl_dist_title()}</h2>
              <p class="dm-sub">{distView === 'service' ? m.cl_dist_sub_service() : m.cl_dist_sub_node()}</p>
            </div>
            <span class="dm-sp"></span>
            {#if dist?.services}
              <Segmented size="sm" label={m.cl_dist_view()} bind:value={distView}
                options={[{ key: 'node', label: m.cl_dist_by_node() }, { key: 'service', label: m.cl_dist_by_service() }]} />
            {/if}
          </div>
          {#if !dist}
            <p class="dm-sub">{waitText}</p>
          {:else if distView === 'service' && dist.services}
            {@render matrix(dist)}
          {:else}
            {@render byNode(dist)}
          {/if}
          {#if dist && !dist.stale}
            {#if top}
              <div class="note warn hint" role="note" data-testid="heavy-hint">
                <AlertTriangle size={14} strokeWidth={1.8} />
                <div>
                  <b>{m.cl_heavy_title()}</b>
                  <span>
                    {m.cl_heavy_text({ node: nodeLabel(top), n: top.tasks, total: dist.tasks, fair: Math.round(dist.fair_share) })}{top.cpu_percent != null ? ` ${m.cl_heavy_cpu({ cpu: Math.round(top.cpu_percent) })}` : ''}
                    {m.cl_heavy_never()}
                    {#if top.pinned?.length}
                      {m.cl_heavy_pinned({ n: top.pinned.reduce((a, p) => a + p.tasks, 0), services: top.pinned.map((p) => p.service).join(', ') })}
                    {/if}
                    {m.cl_heavy_force()}
                  </span>
                  <small class="dm-sub">{m.cl_heavy_rule()}</small>
                </div>
              </div>
            {:else if dist.tasks > 0}
              <p class="dm-sub foot">{m.cl_spread_even_note()}</p>
            {/if}
          {/if}
        </div>
        <div class="sec">
          <h2 class="dm-h2">{m.cl_tab_nodes()}</h2>
          {@render nodeTable()}
        </div>
      {:else if tab === 'nodes'}
        {@render nodeTable()}
        <p class="dm-sub foot">{m.cl_nodes_foot()}</p>
        {#if cluster?.trust.pinned_fingerprint}
          <p class="dm-sub foot fpline" data-testid="cluster-root-fp">{m.cl_trust_fp_pinned()} <span class="dm-mono">{cluster.trust.pinned_fingerprint}</span></p>
        {/if}
      {:else if tab === 'services'}
        {@render serviceTable()}
      {:else if tab === 'tasks'}
        {@render taskTable()}
      {:else if tab === 'stacks'}
        {@render stackTable()}
      {:else if tab === 'networks'}
        {@render networkTable()}
      {/if}
    </div>
  {/if}
</section>

{#if imageOpen}
  <ImageUpdateDialog bind:open={imageOpen} cluster={id} stack={imageStack} onupdated={loadAll} />
{/if}

{#if scaleOpen && scaleOf}
  <ScaleServiceDialog bind:open={scaleOpen} stack={scaleOf.stack} service={scaleOf.service} onscaled={loadAll} />
{/if}

{#if adoptOpen}
  <AdoptStackDialog bind:open={adoptOpen} cluster={id} stack={adoptStack} onadopted={loadAll} />
{/if}

{#snippet bar(v: number | undefined)}
  {#if v == null}
    <span class="dm-dim">—</span>
  {:else}
    <span class="mbar"><i><b class:busy={v >= BUSY_PCT} style="width:{Math.min(100, Math.max(0, v))}%"></b></i><span class="dm-num">{Math.round(v)}%</span></span>
  {/if}
{/snippet}

{#snippet byNode(d: ClusterDistribution)}
  <div class="dm-tbl" role="table" aria-label={m.cl_dist_title()} data-testid="dist-by-node">
    <div class="dm-tr dm-th" role="row" style="grid-template-columns: {DIST_COLS}">
      <span role="columnheader">{m.cl_col_node()}</span>
      <span role="columnheader">{m.cl_col_tasks()}</span>
      <span role="columnheader">CPU</span>
      <span role="columnheader">{m.hosts_col_memory()}</span>
      <span role="columnheader">{m.cl_col_availability()}</span>
    </div>
    {#each nodes as n (n.id)}
      {@const up = n.state === 'ready'}
      <div class="dm-tr" role="row" style="grid-template-columns: {DIST_COLS}">
        <span role="cell" class="dm-nm">
          <span class="nmline">
            {#if n.host_id && hostNames[n.host_id]}<a href="/hosts/{n.host_id}">{nodeLabel(n)}</a>{:else}<span class="nm">{nodeLabel(n)}</span>{/if}
            {#if n.leader}<span class="lead">{m.hosts_cluster_leader()}</span>{/if}
          </span>
          <small>{n.role === 'manager' ? m.hosts_member_role_manager() : m.hosts_member_role_worker()}{n.host_id ? '' : ` · ${m.cl_no_agent_short()}`}</small>
        </span>
        <span role="cell" class="tbar" title={m.cl_fair_title({ fair: fmtNumber(d.fair_share, { maximumFractionDigits: 1 }) })}>
          {#if up && !d.stale}
            <i><b class:busy={n.heavy} style="width:{(n.tasks / maxTasks) * 100}%"></b>{#if d.fair_share > 0}<em style="left:{Math.min(100, (d.fair_share / maxTasks) * 100)}%"></em>{/if}</i>
            <span class="dm-num">{n.tasks}</span>
          {:else}
            <span class="dm-dim">—</span>
          {/if}
        </span>
        {#if n.cpu_percent != null && up}
          <span role="cell">{@render bar(n.cpu_percent)}</span>
          <span role="cell">{@render bar(n.mem_percent)}</span>
        {:else}
          <span role="cell" class="na" title={noMetricsText(n)}>{noMetricsText(n)}</span>
        {/if}
        <span role="cell">
          {#if !up}<StatusChip tone="bad" label={nodeStateLabel(n.state)} />{:else}<span class:dm-dim={n.availability === 'active'} class:warnc={n.availability !== 'active'}>{availabilityLabel(n.availability)}</span>{/if}
        </span>
      </div>
    {/each}
  </div>
{/snippet}

{#snippet matrix(d: ClusterDistribution)}
  {@const cols = `minmax(170px, 1.4fr) 92px repeat(${nodes.length}, minmax(76px, 1fr)) 80px`}
  <div class="dm-tbl" role="table" aria-label={m.cl_dist_by_service()} data-testid="dist-by-service">
    <div class="dm-tr dm-th" role="row" style="grid-template-columns: {cols}">
      <span role="columnheader">{m.cl_col_service()}</span>
      <span role="columnheader">{m.cl_col_mode()}</span>
      {#each nodes as n (n.id)}
        <span role="columnheader" class="c" title={n.hostname}>{nodeLabel(n)}{n.leader ? ' ★' : ''}{n.host_id ? '' : ` (${m.cl_no_agent_short()})`}</span>
      {/each}
      <span role="columnheader" class="r">{m.cl_col_running()}</span>
    </div>
    {#each d.services ?? [] as s (s.id)}
      {@const sv = svcById[s.id]}
      <div class="dm-tr" role="row" style="grid-template-columns: {cols}">
        <span role="cell" class="dm-nm"><span class="nmline"><span class="nm">{s.name}</span>{#if sv && pinningConstraint(sv.constraints)}<span class="dm-pill" title={pinningConstraint(sv.constraints)}>{m.cl_pinned()}</span>{/if}</span></span>
        <span role="cell" class="dm-dim">{sv?.mode ?? '—'}</span>
        {#each nodes as n (n.id)}
          {@const c = s.per_node[n.id] ?? 0}
          <span role="cell" class="c dm-num" class:dm-dim={!c}>{n.state !== 'ready' ? '—' : c || '·'}</span>
        {/each}
        <span role="cell" class="r dm-num">{sv ? `${sv.running_tasks}/${sv.desired_tasks}` : s.running}</span>
      </div>
    {/each}
    <div class="dm-tr sum" role="row" style="grid-template-columns: {cols}">
      <span role="cell">{m.cl_all_tasks()}</span>
      <span role="cell"></span>
      {#each nodes as n (n.id)}
        <span role="cell" class="c dm-num" class:warnc={n.heavy}>{n.state !== 'ready' ? '—' : n.tasks}</span>
      {/each}
      <span role="cell" class="r dm-num">{d.tasks}</span>
    </div>
    <div class="dm-tr sum" role="row" style="grid-template-columns: {cols}">
      <span role="cell">{m.cl_cpu_mem()}</span>
      <span role="cell"></span>
      {#each nodes as n (n.id)}
        <span role="cell" class="c dm-num" title={n.cpu_percent == null ? noMetricsText(n) : ''}>{n.cpu_percent == null ? '—' : `${Math.round(n.cpu_percent)}% · ${Math.round(n.mem_percent ?? 0)}%`}</span>
      {/each}
      <span role="cell"></span>
    </div>
  </div>
  <p class="dm-sub foot">{m.cl_matrix_foot()}</p>
{/snippet}

{#snippet nodeTable()}
  {#if nodes.length === 0}
    <p class="dm-sub">{waitText}</p>
  {:else}
    <div class="dm-tbl" role="table" aria-label={m.cl_tab_nodes()} data-testid="cluster-nodes">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {NODE_COLS}">
        <span role="columnheader"></span>
        <span role="columnheader">{m.cl_col_node()}</span>
        <span role="columnheader">{m.cl_col_role()}</span>
        <span role="columnheader">{m.cl_col_state()}</span>
        <span role="columnheader">{m.cl_col_availability()}</span>
        <span role="columnheader">{m.cl_col_manager()}</span>
        <span role="columnheader" class="r">{m.cl_col_tasks()}</span>
        <span role="columnheader">{m.cl_col_engine()}</span>
        <span role="columnheader">{m.common_agent()}</span>
        <span role="columnheader">{m.cl_col_address()}</span>
      </div>
      {#each nodes as n (n.id)}
        {@const mb = memberOf(n.host_id)}
        <div class="dm-tr" role="row" style="grid-template-columns: {NODE_COLS}">
          <span role="cell"><span class="dm-dot {nodeTone(n)}"></span></span>
          <span role="cell" class="dm-nm">
            <span class="nmline">
              {#if n.host_id && hostNames[n.host_id]}<a href="/hosts/{n.host_id}">{nodeLabel(n)}</a>{:else}<span class="nm">{nodeLabel(n)}</span>{/if}
            </span>
          </span>
          <span role="cell" class="dm-dim">{n.role === 'manager' ? m.hosts_member_role_manager() : m.hosts_member_role_worker()}</span>
          <span role="cell" class:badc={n.state !== 'ready'}>{nodeStateLabel(n.state)}</span>
          <span role="cell" class:warnc={n.availability !== 'active'}>{availabilityLabel(n.availability)}</span>
          <span role="cell" class:lead-t={n.leader} class:badc={n.role === 'manager' && n.reachability === 'unreachable'} class:dm-dim={n.role !== 'manager'}>{managerStatus(n)}</span>
          <span role="cell" class="r dm-num">{n.state === 'ready' && !dist?.stale ? n.tasks : '—'}</span>
          <span role="cell" class="dm-mono">{n.engine_version || '—'}</span>
          <span role="cell" class="agentc">
            {#if n.host_id && hostNames[n.host_id]}<a class="lnk" href="/hosts/{n.host_id}">{hostNames[n.host_id]}</a>{:else if n.host_id}{m.cl_agent_yes()}{:else}<span class="dm-dim">{m.cl_no_agent()}</span>{/if}
            {#if mb?.verified}
              <span class="proof ok" title={proofText(mb.proof)} data-testid="node-verified"><ShieldCheck size={12} strokeWidth={1.8} /></span>
            {:else if mb}
              <span class="proof warn" title={proofText(mb.proof)} data-testid="node-unverified"><ShieldAlert size={12} strokeWidth={1.8} /> {m.cl_proof_short()}</span>
            {/if}
          </span>
          <span role="cell" class="dm-mono dm-dim">
            {n.addr || '—'}
            {#if mb?.addr_warning}
              <span class="proof warn" title={addrHint(mb.addr_warning)} data-testid="node-addr-warning"><AlertTriangle size={12} strokeWidth={1.8} /></span>
            {/if}
          </span>
        </div>
      {/each}
    </div>
  {/if}
{/snippet}

{#snippet serviceTable()}
  {#if !services}
    <p class="dm-sub">{waitText}</p>
  {:else if services.length === 0}
    <EmptyState icon={Network} title={m.cl_no_services()} description={m.cl_no_services_hint()} />
  {:else}
    <div class="dm-tbl" role="table" aria-label={m.cl_tab_services()} data-testid="cluster-services">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {SVC_COLS}">
        <span role="columnheader"></span>
        <span role="columnheader">{m.cl_col_service()}</span>
        <span role="columnheader">{m.cl_col_mode()}</span>
        <span role="columnheader" class="r">{m.cl_col_replicas()}</span>
        <span role="columnheader">{m.cl_col_on_nodes()}</span>
        <span role="columnheader" class="r">CPU</span>
        <span role="columnheader" class="r">{m.hosts_col_memory()}</span>
        <span role="columnheader">{m.cl_col_image()}</span>
        <span role="columnheader">{m.cl_col_placement()}</span>
        <span role="columnheader">{m.cl_col_updated()}</span>
      </div>
      {#each stacks as g (g.stack)}
        <div class="dm-tr dm-grp stackrow" role="row" style="grid-template-columns: 1fr">
          <span role="cell">
            <b>{g.stack || m.cl_no_stack()}</b>
            {#if g.stack && isOwn(g)}<span class="dm-pill ext" title={m.cl_dockmesh_title()}>{m.cl_dockmesh()}</span>{:else if g.stack}<span class="dm-pill ext" title={m.cl_external_title()}>{m.cl_external()}</span>{/if}
            <span class="dm-dim"> · {g.services.length === 1 ? m.cl_services_one() : m.cl_services({ n: g.services.length })} · {m.cl_of({ n: g.services.reduce((a, s) => a + s.running_tasks, 0), total: g.services.reduce((a, s) => a + s.desired_tasks, 0) })} {m.cl_tasks_word()}</span>
          </span>
        </div>
        {#each g.services as s (s.id)}
          {@const u = cpuMem(distSvcById[s.id])}
          {@const pin = pinningConstraint(s.constraints)}
          <div class="dm-tr" role="row" style="grid-template-columns: {SVC_COLS}">
            <span role="cell"><span class="dm-dot {svcTone(s)}"></span></span>
            <span role="cell" class="dm-nm svcname">
              <span class="nmline">
                <span class="nm" title={s.name}>{s.name}</span>
                {#if s.update_state && s.update_state !== 'completed'}
                  <span class="dm-pill {updateTone(s.update_state)}" title={s.update_message ?? ''} data-testid="update-state">{updateLabel(s.update_state)}</span>
                {/if}
              </span>
              {#if s.spec_hash && g.stack && !readOnly}
                {@const short = s.name.slice(g.stack.length + 1)}
                {#if (s.update_state === 'paused' || s.update_state === 'rollback_paused') && allowed('stacks.deploy')}
                  <Button size="xs" class="svcact" disabled={acting === s.id} onclick={() => serviceAction(g.stack, short, s, 'resume')}>{m.cl_svc_resume()}</Button>
                {/if}
                {#if s.mode === 'replicated' && allowed('stacks.deploy')}
                  <Button size="xs" class="svcact" onclick={() => { scaleOf = { stack: g.stack, service: s }; scaleOpen = true; }}>{m.cl_svc_scale()}</Button>
                {/if}
                {#if s.rollback && s.update_state !== 'rollback_started' && allowed('stacks.operate')}
                  <Button size="xs" class="svcact" disabled={acting === s.id} onclick={() => serviceAction(g.stack, short, s, 'rollback')}>{m.cl_svc_rollback()}</Button>
                {/if}
              {/if}
            </span>
            <span role="cell" class="dm-dim">{s.mode || '—'}</span>
            <span role="cell" class="r dm-num" class:warnc={s.running_tasks < s.desired_tasks}>{s.running_tasks}/{s.desired_tasks}</span>
            <span role="cell" class="dm-trunc" title={nodesOf(s)}>{nodesOf(s)}</span>
            <span role="cell" class="r dm-num" title={u.partial ? m.cl_partial_title() : ''}>{u.cpu}{u.partial && u.cpu !== '—' ? '*' : ''}</span>
            <span role="cell" class="r dm-num" title={u.partial ? m.cl_partial_title() : ''}>{u.mem}{u.partial && u.mem !== '—' ? '*' : ''}</span>
            <span role="cell" class="dm-mono dm-dim" title={s.image}>{s.image ? s.image.split('@')[0] : '—'}</span>
            <span role="cell" class="dm-dim small" title={(s.constraints ?? []).join(', ')}>{#if pin}<span class="dm-mono">{pin}</span>{:else if s.constraints?.length}<span class="dm-mono">{s.constraints.join(', ')}</span>{:else}{m.cl_any_node()}{/if}</span>
            <span role="cell" class="dm-dim">{fmtAgo(s.updated_at)}</span>
          </div>
        {/each}
      {/each}
    </div>
    <p class="dm-sub foot">{m.cl_services_foot()}{(dist?.services ?? []).some((s) => s.partial) ? ` ${m.cl_partial_note()}` : ''}</p>
  {/if}
{/snippet}

{#snippet taskTable()}
  <div class="dm-toolbar tasksbar">
    <Segmented size="sm" label={m.cl_task_filter()} bind:value={taskFilter}
      options={[
        { key: 'current', label: m.cl_tasks_current() },
        { key: 'problems', label: m.cl_tasks_problems(), count: problems.size || null },
        { key: 'all', label: m.cl_tasks_all() }
      ]} />
    <input type="search" class="dm-filter" style="width: 240px" placeholder={m.cl_task_search()} aria-label={m.cl_task_search()} bind:value={taskSearch} />
  </div>
  {#if !tasks}
    <p class="dm-sub">{waitText}</p>
  {:else if shownTasks.length === 0}
    <p class="dm-sub empty">{taskFilter === 'problems' ? m.cl_tasks_no_problems() : m.cl_tasks_none()}</p>
  {:else}
    <div class="dm-tbl" role="table" aria-label={m.cl_tab_tasks()} data-testid="cluster-tasks">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {TASK_COLS}">
        <span role="columnheader"></span>
        <span role="columnheader">{m.cl_col_task()}</span>
        <span role="columnheader">{m.cl_col_node()}</span>
        <span role="columnheader">{m.cl_col_state()}</span>
        <span role="columnheader">{m.cl_col_desired()}</span>
        <span role="columnheader" class="r" title={m.cl_restarts_title({ n: RESTART_WINDOW_MS / 60_000 })}>{m.cl_col_restarts()}</span>
        <span role="columnheader">{m.cl_col_error()}</span>
        <span role="columnheader">{m.cl_col_updated()}</span>
      </div>
      {#each shownTasks as t (t.id)}
        {@const p = problems.get(t.id)}
        {@const err = t.error || p?.lastError}
        <div class="dm-tr" class:problem={!!p} role="row" style="grid-template-columns: {TASK_COLS}" data-testid={p ? 'task-problem' : undefined}>
          <span role="cell"><span class="dm-dot {taskTone(t)}"></span></span>
          <span role="cell" class="dm-nm"><span class="nmline"><span class="nm" title={t.id}>{taskName(t)}</span>{#if p}<span class="dm-pill bad">{p.kind === 'failed' ? m.cl_task_failed() : m.cl_task_restarting()}</span>{/if}</span></span>
          <span role="cell">{nodeName(t.node_id)}</span>
          <span role="cell" class:badc={taskTone(t) === 'bad'}>{t.state}</span>
          <span role="cell" class="dm-dim">{t.desired_state}</span>
          <span role="cell" class="r dm-num" class:badc={!!p && p.failures >= 2}>{p ? p.failures : '—'}</span>
          <span role="cell" class="dm-trunc" class:badc={!!err} title={err || t.message}>{err || t.message || '—'}</span>
          <span role="cell" class="dm-dim">{fmtAgo(t.updated_at)}</span>
        </div>
      {/each}
    </div>
    {#if shownTasks.length === MAX_TASKS}<p class="dm-sub foot">{m.cl_tasks_capped()}</p>{/if}
  {/if}
{/snippet}

{#snippet stackTable()}
  {#if namedStacks.length === 0}
    <p class="dm-sub empty">{m.cl_no_stacks()}</p>
  {:else}
    <div class="dm-tbl" role="table" aria-label={m.cl_tab_stacks()} data-testid="cluster-stacks">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {STACK_COLS}">
        <span role="columnheader"></span>
        <span role="columnheader">{m.cl_col_stack()}</span>
        <span role="columnheader">{m.cl_col_source()}</span>
        <span role="columnheader" class="r">{m.cl_tab_services()}</span>
        <span role="columnheader" class="r">{m.cl_col_tasks()}</span>
      </div>
      {#each namedStacks as g (g.stack)}
        {@const run = g.services.reduce((a, s) => a + s.running_tasks, 0)}
        {@const want = g.services.reduce((a, s) => a + s.desired_tasks, 0)}
        <div class="dm-tr" role="row" style="grid-template-columns: {STACK_COLS}">
          <span role="cell"><span class="dm-dot {run >= want ? 'ok' : run === 0 ? 'bad' : 'warn'}"></span></span>
          <span role="cell" class="dm-nm">{#if isOwn(g)}<a class="nm lnk" href="/stacks/{encodeURIComponent(g.stack)}">{g.stack}</a>{:else}<span class="nm">{g.stack}</span>{/if}</span>
          <span role="cell">
            {#if isOwn(g)}
              <span class="dm-pill ext">{m.cl_dockmesh()}</span> <span class="dm-dim small">{m.cl_dockmesh_title()}</span>
              {#if allowed('stacks.deploy') && !readOnly}
                <Button size="xs" class="rowbtn" onclick={() => { imageStack = g.stack; imageOpen = true; }} data-testid="image-check">{m.cl_img_check()}</Button>
              {/if}
            {:else}
              <span class="dm-pill ext">{m.cl_external()}</span> <span class="dm-dim small">{m.cl_external_title()}</span>
              {#if allowed('stacks.adopt') && !readOnly}
                <Button size="xs" class="rowbtn" onclick={() => openAdopt(g.stack)} data-testid="adopt-stack">{m.cl_adopt()}</Button>
              {/if}
            {/if}
          </span>
          <span role="cell" class="r dm-num">{g.services.length}</span>
          <span role="cell" class="r dm-num" class:warnc={run < want}>{run}/{want}</span>
        </div>
      {/each}
    </div>
    <p class="dm-sub foot">{m.cl_stacks_foot()}</p>
  {/if}
{/snippet}

{#snippet networkTable()}
  {#if !networks}
    <p class="dm-sub">{waitText}</p>
  {:else if networks.length === 0}
    <p class="dm-sub empty">{m.cl_no_networks()}</p>
  {:else}
    <div class="dm-tbl" role="table" aria-label={m.cl_tab_networks()} data-testid="cluster-networks">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {NET_COLS}">
        <span role="columnheader">{m.cl_col_network()}</span>
        <span role="columnheader">{m.cl_col_driver()}</span>
        <span role="columnheader">{m.cl_col_options()}</span>
        <span role="columnheader">{m.cl_col_subnet()}</span>
        <span role="columnheader">{m.cl_col_stack()}</span>
        <span role="columnheader">{m.cl_tab_services()}</span>
      </div>
      {#each networks as n (n.id)}
        {@const opts = [n.ingress ? m.cl_net_ingress() : '', n.internal ? m.cl_net_internal() : '', n.encrypted ? m.cl_net_encrypted() : '', n.attachable ? m.cl_net_attachable() : ''].filter(Boolean).join(', ')}
        <div class="dm-tr" role="row" style="grid-template-columns: {NET_COLS}">
          <span role="cell" class="dm-nm"><span class="nm" title={n.id}>{n.name}</span></span>
          <span role="cell" class="dm-dim">{n.driver}</span>
          <span role="cell" class="dm-dim">{opts || '—'}</span>
          <span role="cell" class="dm-mono dm-dim">{(n.subnets ?? []).join(', ') || '—'}</span>
          <span role="cell">{n.stack || '—'}</span>
          <span role="cell" class="dm-trunc" title={(n.services ?? []).map((s) => s.name).join(', ')}>{(n.services ?? []).map((s) => s.name).join(', ') || '—'}</span>
        </div>
      {/each}
    </div>
  {/if}
{/snippet}

<style>
  .sub { margin-top: -14px; }
  .note {
    display: flex; gap: 8px; align-items: flex-start;
    border: 1px solid var(--border); border-radius: var(--r); padding: 8px 12px; font-size: var(--fs-sm);
  }
  .note > div { display: flex; flex-direction: column; gap: 3px; min-width: 0; color: var(--fg); }
  .note span { color: var(--muted); }
  .note.info { border-color: color-mix(in srgb, var(--accent) 35%, transparent); background: var(--accent-soft); color: var(--accent); }
  .note.warn { border-color: var(--warn-bd); background: var(--warn-bg); color: var(--warn); }
  .note.bad { border-color: var(--bad-bd); background: var(--bad-bg); color: var(--bad); }
  .note.grey { background: var(--field); color: var(--subtle); }
  .note small { font-size: var(--fs-xs); }
  .hint { margin-top: 12px; }
  .noteact { margin-top: 4px; }
  .fp { color: var(--fg); word-break: break-all; }
  .fpline { word-break: break-all; }
  .agentc { display: inline-flex; align-items: center; gap: 6px; }
  .proof { display: inline-flex; align-items: center; gap: 3px; font-size: var(--fs-xs); vertical-align: middle; }
  .proof.ok { color: var(--ok); }
  .proof.warn { color: var(--warn); }
  .dim { opacity: 0.62; }
  .stale { margin: 0 0 10px; }
  .sec { margin-top: 18px; }
  .sec:first-child { margin-top: 0; }
  .sechead { display: flex; align-items: flex-start; gap: 12px; margin-bottom: 8px; }
  .sechead p { margin: 0; }
  .foot { margin: 8px 0 0; font-size: var(--fs-xs); }
  .empty { padding: 18px 0; }
  .probline { display: flex; align-items: center; gap: 6px; margin: 0 0 4px; color: var(--bad); font-size: var(--fs-sm); }
  .linkbtn { all: unset; cursor: pointer; color: var(--bad); text-decoration: underline; }
  .nm { font-weight: 600; }
  .nmline { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .lead { color: var(--accent); font-size: var(--fs-xs); font-weight: 600; }
  .lead-t { color: var(--accent); font-weight: 600; }
  .warnc { color: var(--warn); }
  .badc { color: var(--bad); }
  .lnk { color: var(--fg); text-decoration: none; }
  .lnk:hover { text-decoration: underline; }
  :global(.rowbtn) { margin-left: 8px; }
  :global(.svcact) { margin: 2px 4px 0 0; }
  .small { font-size: var(--fs-xs); }
  .na { color: var(--subtle); font-size: var(--fs-xs); grid-column: span 2; }
  .r { text-align: right; }
  .c { text-align: center; }
  .sum { font-size: var(--fs-sm); color: var(--muted); font-weight: 500; }
  .stackrow { background: var(--band); }
  .stackrow b { font-weight: 600; }
  .svcname { padding-left: 4px; }
  .ext { margin-left: 6px; }
  .dm-pill.bad { color: var(--bad); }
  .problem { background: color-mix(in srgb, var(--bad) 6%, transparent); }
  .tasksbar { margin: 0 0 10px; }
  .mbar, .tbar { display: flex; align-items: center; gap: 8px; }
  .mbar i, .tbar i { flex: 1; height: 5px; border-radius: 3px; background: var(--field); overflow: visible; min-width: 30px; position: relative; }
  .mbar i b, .tbar i b { display: block; height: 100%; border-radius: 3px; background: var(--accent); }
  .mbar i b.busy, .tbar i b.busy { background: var(--warn); }
  .tbar i em { position: absolute; top: -4px; bottom: -4px; width: 0; border-left: 1.5px dashed var(--muted); }
  .mbar .dm-num, .tbar .dm-num { width: 34px; text-align: right; }
</style>
