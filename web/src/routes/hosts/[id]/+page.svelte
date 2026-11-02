<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { containerStateLabel, hostStatusLabel } from '$lib/labels';
  import { untrack } from 'svelte';
  import { tagProblem } from '../_host';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo, fmtBytes, fmtMem } from '$lib/format';
  import { fmtDate as fmtLocaleDate, fmtDateTime, fmtNumber } from '$lib/i18n';
  const fmtAgoNever = (iso?: string) => fmtAgo(iso, m.common_never());
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
  import HostMetrics from '$lib/components/HostMetrics.svelte';
  import TopConsumers from '$lib/components/TopConsumers.svelte';
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
        const [met, ctrRes, stk, hostList, polRes] = await Promise.all([
          api.system.metrics('local').catch(() => null),
          api.containers.list(false, 'local').catch(() => []),
          api.stacks.list().catch(() => []),
          api.hosts.list().catch(() => []),
          api.agents.getUpgradePolicy().catch(() => null)
        ]);
        if (!current()) return;
        localName = hostList.find((h) => h.kind === 'local')?.name ?? 'local';
        agent = null;
        if (met && !('hosts' in met)) metrics = met as SystemMetrics;
        containers = Array.isArray(ctrRes) ? ctrRes : [];
        stacks = stk;
        upgradePolicyVersion = polRes?.server_version ?? '';
      } else {
        const [a, met, ctrRes, stk, polRes] = await Promise.all([
          api.agents.get(id).catch((e) => { if (current()) loadError = e instanceof ApiError ? e.message : m.hosts_load_failed_short(); return null; }),
          api.system.metrics(id).catch(() => null),
          api.containers.list(false, id).catch(() => []),
          api.stacks.list().catch(() => []),
          api.agents.getUpgradePolicy().catch(() => null)
        ]);
        if (!current()) return;
        agent = a;
        if (met && !('hosts' in met)) metrics = met as SystemMetrics;
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
      migrations = migRes.filter((mg) => mg.source_host_id === id || mg.target_host_id === id);
      lastLoaded = Date.now();
    } catch (err) {
      if (current()) loadError = err instanceof ApiError ? err.message : m.hosts_load_failed_short();
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
    return m.hosts_dur_dh({ d: Math.floor(sec / 86400), h: Math.floor((sec % 86400) / 3600) });
  }
  function fmtUptime(iso?: string): string {
    if (!iso) return '—';
    return fmtDur((Date.now() - new Date(iso).getTime()) / 1000);
  }
  function fmtDate(iso?: string): string {
    return iso ? fmtLocaleDate(iso) : '—';
  }
  function migStatusLabel(s: string): string {
    if (s === 'completed') return m.hosts_mig_completed();
    if (s === 'failed') return m.hosts_mig_failed();
    if (s === 'rolled_back') return m.hosts_mig_rolled_back();
    if (s === 'pending') return m.hosts_pending();
    return s;
  }
  function selectTab(t: string) {
    openTab.set(t);
  }

  const status = $derived(isLocal ? 'online' : (agent?.status ?? 'offline'));
  const statusTone = $derived<Tone>(status === 'online' ? 'ok' : status === 'pending' ? 'warn' : status === 'offline' ? 'bad' : 'off');
  const hostname = $derived(isLocal ? 'localhost' : (agent?.hostname ?? ''));
  const hostKind = $derived(isLocal ? m.hosts_kind_local() : m.hosts_kind_agent());
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
      label: metrics?.docker_limited ? m.hosts_cpu_docker_limit() : 'CPU',
      value: cpuPct != null ? `${cpuPct} %` : '—',
      note: metrics ? m.hosts_cores_of({ used: fmtNumber(metrics.cpu_used_cores, { minimumFractionDigits: 2, maximumFractionDigits: 2 }), total: fmtNumber(metrics.cpu_cores, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) }) : '',
      tone: tone(cpuPct)
    },
    {
      label: metrics?.docker_limited ? m.hosts_mem_docker_limit() : m.hosts_col_memory(),
      value: memPct != null ? `${memPct} %` : '—',
      note: metrics && metrics.mem_total > 0 ? m.hosts_mem_of({ used: fmtMem(metrics.mem_used), total: fmtMem(metrics.mem_total) }) : m.hosts_unavailable_here(),
      tone: tone(memPct, 80)
    },
    {
      label: m.hosts_col_disk(),
      value: diskPct != null ? `${diskPct} %` : '—',
      note: metrics && metrics.disk_total > 0 ? m.hosts_mem_of({ used: fmtBytes(metrics.disk_used), total: fmtBytes(metrics.disk_total) }) : m.hosts_unavailable_here(),
      tone: tone(diskPct, 80)
    },
    { label: m.common_containers(), value: `${running} / ${containers.length}`, note: m.containers_status_running() },
    { label: m.common_stacks(), value: stacksOnHost.length }
  ]);

  const tabs = $derived([
    { key: 'overview', label: m.common_overview() },
    { key: 'containers', label: m.common_containers(), count: containers.length },
    { key: 'stacks', label: m.common_stacks(), count: stacksOnHost.length },
    { key: 'migrations', label: m.hosts_tab_migrations(), count: migrations.length },
    { key: 'tags', label: m.hosts_col_tags(), count: tags.length },
    { key: 'maintenance', label: m.hosts_tab_maintenance(), dot: needsUpgrade }
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
      toast.success(m.hosts_tags_saved());
    } catch (err) {
      toast.error(m.hosts_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { tagsBusy = false; }
  }

  async function openDrain() {
    showDrain = true;
    drainPlan = null;
    drainLoading = true;
    try { drainPlan = await api.drains.plan(id); }
    catch (err) { toast.error(m.hosts_plan_failed(), err instanceof ApiError ? err.message : undefined); }
    finally { drainLoading = false; }
  }
  async function executeDrain() {
    drainBusy = true;
    try {
      const d = await api.drains.execute(id);
      toast.success(m.hosts_drain_started(), m.hosts_drain_queued({ n: d.plan.length }));
      showDrain = false;
    } catch (err) {
      toast.error(m.hosts_drain_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { drainBusy = false; }
  }
  async function upgradeAgent() {
    if (isLocal) return;
    upgradeBusy = true;
    try {
      const r = await api.agents.upgrade(id);
      toast.success(m.hosts_upgrade_dispatched(), `${hostName} → ${fmtVersion(r.version)}`);
      await loadAll();
    } catch (err) {
      toast.error(m.hosts_upgrade_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { upgradeBusy = false; }
  }

  let tokenModal = $state<{ open: boolean; token: string; install: string; expiresAt: string }>({
    open: false, token: '', install: '', expiresAt: ''
  });
  async function rotateToken() {
    if (isLocal) return;
    if (!(await confirm.ask({
      title: m.hosts_reissue_title({ name: hostName }),
      message: m.hosts_reissue_message(),
      body: m.hosts_reissue_body(),
      confirmLabel: m.hosts_reissue()
    }))) return;
    try {
      const data = await api.agents.rotateToken(id);
      tokenModal = { open: true, token: data.token, install: data.install_hint, expiresAt: data.token_expires_at };
    } catch (err) {
      toast.error(m.hosts_reissue_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function revokeHost() {
    if (isLocal) return;
    if (!(await confirm.ask({
      title: m.hosts_revoke_title({ name: hostName }),
      message: m.hosts_revoke_message({ name: hostName }),
      body: m.hosts_revoke_body_detail(),
      confirmLabel: m.hosts_revoke(), danger: true, typeToConfirm: hostName
    }))) return;
    revokeBusy = true;
    try {
      await api.agents.delete(id);
      toast.success(m.hosts_revoked(), hostName);
      goto('/hosts');
    } catch (err) {
      toast.error(m.hosts_revoke_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { revokeBusy = false; }
  }

  const lensIsThis = $derived(hosts.id === id);
  const CT_COLS = 'minmax(180px, 1.4fr) minmax(160px, 1.4fr) 120px 150px';
  const ST_COLS = 'minmax(180px, 1.6fr) 110px 110px 120px';
  const MG_COLS = 'minmax(160px, 1.4fr) minmax(140px, 1fr) 110px 110px 120px 110px';
</script>

<section class="dm-page">
  {#if loading && !agent && !isLocal}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="4rem" />
  {:else if loadError && !isLocal}
    <EmptyState tone="error" title={m.hosts_load_one_failed()} description={loadError}>
      {#snippet action()}<a class="dm-btn" href="/hosts">{m.hosts_back()}</a>{/snippet}
    </EmptyState>
  {:else}
    <div class="dm-ph">
      <h1>{hostName}</h1>
      <StatusChip tone={statusTone} label={hostStatusLabel(status)} />
      <span class="dm-sub dm-trunc">
        {hostKind}{hostname ? ` · ${hostname}` : ''}{!isLocal && agent?.os ? ` · ${agent.os}/${agent.arch}` : ''}{!isLocal && agent?.docker_version ? ` · Docker ${agent.docker_version}` : ''}{agent?.online_since ? ` · ${m.hosts_up({ dur: fmtUptime(agent.online_since) })}` : ''}
      </span>
      <span class="htags">
        {#each tags as t (t)}<TagPill label={t} />{/each}
        {#if allowed('hosts.tag')}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => selectTab('tags')}>{tags.length ? m.common_edit() : m.hosts_add_tag_btn()}</button>
        {/if}
      </span>
      <span class="dm-sp"></span>
      <LiveIndicator updatedAt={lastLoaded} onrefresh={loadAll} busy={refreshing} />
      <button type="button" class="dm-btn" disabled={lensIsThis || status !== 'online'} onclick={() => hosts.set(id)} title={m.hosts_lens_hint()}>
        {lensIsThis ? m.hosts_lens_current() : m.hosts_lens_set()}
      </button>
      {#if allowed('hosts.update')}
        <button type="button" class="dm-btn" disabled={isLocal || status !== 'online'} onclick={openDrain} title={isLocal ? m.hosts_local_no_drain() : ''}>
          <ArrowDownToLine size={13} strokeWidth={1.8} /> {m.hosts_drain_ellipsis()}
        </button>
        {#if needsUpgrade}
          <button type="button" class="dm-btn dm-btn-primary" disabled={upgradeBusy} onclick={upgradeAgent}>
            <RefreshCw size={13} strokeWidth={1.8} /> {upgradeBusy ? m.hosts_upgrading() : m.hosts_upgrade_to({ version: fmtVersion(upgradePolicyVersion) })}
          </button>
        {/if}
      {/if}
    </div>

    <Tabs {tabs} active={tab} onchange={selectTab} label={m.hosts_sections()} />

    {#if tab === 'overview'}
      <StatBar items={snapshot} />
      <HostMetrics host={id} {hostName} latest={metrics} />
      <div class="grid2">
        <div class="col">
          <TopConsumers host={id} {hostName} memTotal={metrics?.mem_total ?? 0} refreshAt={lastLoaded} />
          {#if metrics && (metrics.mounts?.length || metrics.mounts_unsupported)}
            <div>
              <h2 class="dm-h2">{m.hosts_disks()}</h2>
              <MountTable mounts={metrics.mounts} unsupported={metrics.mounts_unsupported} />
            </div>
          {/if}
          <div>
            <h2 class="dm-h2">{m.hosts_stacks_here()} <span class="dm-dim dm-num">{stacksOnHost.length}</span></h2>
            {#if stacksOnHost.length === 0}
              <div class="dm-tbl"><div class="dm-empty">{status === 'pending' ? m.hosts_not_connected() : m.hosts_no_stacks_run({ name: hostName })}</div></div>
            {:else}
              <div class="dm-tbl">
                {#each stacksOnHost.slice(0, 8) as s (s.name)}
                  <a class="dm-tr dm-click lnk" style="grid-template-columns: minmax(0, 1fr) 90px" href="/stacks/{encodeURIComponent(s.name)}">
                    <span class="dm-nm"><span class="dm-t">{s.name}</span></span>
                    <span class="dm-num dm-dim" style="text-align: right">{m.hosts_n_containers({ n: s.containers ?? '—' })}</span>
                  </a>
                {/each}
              </div>
              {#if stacksOnHost.length > 8}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm more" onclick={() => selectTab('stacks')}>{m.hosts_all_stacks({ n: stacksOnHost.length })}</button>
              {/if}
            {/if}
          </div>
        </div>
        <div class="col">
          <div>
            <h2 class="dm-h2">{m.common_details()}</h2>
            <dl class="dm-kv">
              <dt>ID</dt><dd class="dm-mono">{id}</dd>
              <dt>{m.hosts_hostname()}</dt><dd class="dm-mono">{hostname || '—'}</dd>
              {#if !isLocal && agent}
                <dt>OS</dt><dd>{agent.os ? `${agent.os}/${agent.arch}` : '—'}{metrics ? ` · ${m.hosts_cores({ n: metrics.cpu_cores })} · ${fmtMem(metrics.mem_total)}` : ''}</dd>
                <dt>Docker</dt><dd class="dm-mono">{agent.docker_version || '—'}</dd>
                <dt>{m.common_agent()}</dt>
                <dd class="dm-mono">
                  {agent.version ? fmtVersion(agent.version) : '—'}
                  {#if needsUpgrade}<span class="warn"> · {m.hosts_server_is_on_lc({ version: fmtVersion(upgradePolicyVersion) })}</span>
                  {:else if agent.update_recommended}<span class="warn"> · {m.hosts_update_recommended()}</span>{/if}
                </dd>
                <dt>{m.hosts_last_seen()}</dt><dd>{fmtAgoNever(agent.last_seen_at)}</dd>
                <dt>{m.hosts_online_since()}</dt><dd>{agent.online_since ? `${fmtAgoNever(agent.online_since)} (${fmtUptime(agent.online_since)})` : '—'}</dd>
                <dt>{m.hosts_certificate()}</dt><dd class="dm-mono" title={agent.cert_fingerprint}>{m.hosts_expires({ date: fmtDate(agent.cert_expires_at) })}{agent.cert_fingerprint ? ` · ${agent.cert_fingerprint.slice(0, 23)}…` : ''}</dd>
                <dt>{m.hosts_enrolled()}</dt><dd>{fmtDate(agent.created_at)}</dd>
              {:else}
                <dt>{m.hosts_server()}</dt><dd class="dm-mono">{hostVersion ? fmtVersion(hostVersion) : '—'}</dd>
                {#if metrics}<dt>{m.hosts_capacity()}</dt><dd>{m.hosts_cores({ n: metrics.cpu_cores })} · {fmtMem(metrics.mem_total)}</dd>{/if}
                <dt>{m.hosts_uptime()}</dt><dd>{metrics?.uptime_seconds ? fmtDur(metrics.uptime_seconds) : '—'}</dd>
              {/if}
              <dt>{m.common_containers()}</dt><dd>{m.hosts_running_of({ n: running, total: containers.length })}</dd>
            </dl>
          </div>
          <div>
            <h2 class="dm-h2">{m.hosts_recent_migrations()}</h2>
            {#if migrations.length === 0}
              <p class="dm-sub">{m.hosts_no_migrations_yet({ name: hostName })}</p>
            {:else}
              <div class="dm-tbl">
                {#each migrations.slice(0, 5) as mg (mg.id)}
                  {@const out = mg.source_host_id === id}
                  <div class="dm-tr" style="grid-template-columns: 40px minmax(0, 1fr) 100px 90px">
                    <span class="dm-num dm-dim">{out ? `↗ ${m.hosts_dir_out()}` : `↙ ${m.hosts_dir_in()}`}</span>
                    <a class="dm-trunc" href="/stacks/{encodeURIComponent(mg.stack_name)}">{mg.stack_name}</a>
                    <StatusChip tone={mg.status === 'completed' ? 'ok' : mg.status === 'failed' ? 'bad' : 'warn'} label={migStatusLabel(mg.status)} />
                    <span class="dm-num dm-dim" style="text-align: right">{fmtAgoNever(mg.completed_at ?? mg.started_at)}</span>
                  </div>
                {/each}
              </div>
              {#if migrations.length > 5}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm more" onclick={() => selectTab('migrations')}>{m.hosts_all_migrations({ n: migrations.length })}</button>
              {/if}
            {/if}
          </div>
        </div>
      </div>

    {:else if tab === 'containers'}
      {#if containers.length === 0}
        <EmptyState title={m.hosts_no_containers()} description={status === 'pending' ? m.hosts_not_connected() : m.hosts_no_containers_on({ name: hostName })} />
      {:else}
        <div class="dm-tbl" role="table" aria-label={m.hosts_containers_on({ name: hostName })}>
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {CT_COLS}">
            <span role="columnheader">{m.common_container()}</span><span role="columnheader">{m.common_image()}</span><span role="columnheader">{m.common_state()}</span><span role="columnheader">{m.common_status()}</span>
          </div>
          {#each containers as c (c.Id)}
            {@const name = c.Names?.[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)}
            {@const href = `/containers/${c.Id}${isLocal ? '' : `?host=${id}`}`}
            {@const st = c.State === 'running' ? (String(c.Status).includes('unhealthy') ? 'warn' : 'ok') : c.State === 'exited' || c.State === 'dead' ? 'bad' : 'off'}
            <a class="dm-tr dm-click lnk" role="row" style="grid-template-columns: {CT_COLS}" {href}>
              <span role="cell" class="dm-nm"><span class="dm-t">{name}</span><small>{c.Labels?.['com.docker.compose.project'] ?? m.hosts_standalone()}</small></span>
              <span role="cell" class="dm-mono dm-dim" title={c.Image}>{c.Image}</span>
              <span role="cell"><StatusChip tone={st as Tone} label={st === 'warn' ? m.containers_status_unhealthy() : containerStateLabel(c.State)} /></span>
              <span role="cell" class="dm-dim" title={c.Status}>{c.Status ?? '—'}</span>
            </a>
          {/each}
        </div>
      {/if}

    {:else if tab === 'stacks'}
      {#if stacksOnHost.length === 0}
        <EmptyState title={m.hosts_no_stacks_here()} description={m.hosts_no_stacks_deployed({ name: hostName })} />
      {:else}
        <div class="dm-tbl" role="table" aria-label={m.hosts_stacks_on({ name: hostName })}>
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {ST_COLS}">
            <span role="columnheader">{m.common_stack()}</span><span role="columnheader">{m.common_containers()}</span><span role="columnheader">{m.common_volumes()}</span><span role="columnheader"></span>
          </div>
          {#each stacksOnHost as s (s.name)}
            <div class="dm-tr" role="row" style="grid-template-columns: {ST_COLS}">
              <span role="cell" class="dm-nm"><a href="/stacks/{encodeURIComponent(s.name)}">{s.name}</a></span>
              <span role="cell" class="dm-num">{s.containers ?? '—'}</span>
              <span role="cell" class="dm-dim">{s.has_volumes ? m.hosts_stateful() : '—'}</span>
              <span role="cell" style="text-align: right">
                {#if allowed('stacks.migrate')}<a href="/stacks/{encodeURIComponent(s.name)}?action=migrate" class="dm-btn dm-btn-ghost dm-btn-sm">{m.hosts_migrate()}</a>{/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}

    {:else if tab === 'migrations'}
      {#if migrations.length === 0}
        <EmptyState title={m.hosts_no_migrations()} description={m.hosts_no_stack_migrations({ name: hostName })} />
      {:else}
        <div class="dm-tbl" role="table" aria-label={m.hosts_tab_migrations()}>
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {MG_COLS}">
            <span role="columnheader">{m.common_stack()}</span><span role="columnheader">{m.hosts_col_direction()}</span><span role="columnheader">{m.common_status()}</span><span role="columnheader">{m.hosts_col_started()}</span><span role="columnheader">{m.hosts_col_by()}</span><span role="columnheader">{m.hosts_col_completed()}</span>
          </div>
          {#each migrations as mg (mg.id)}
            <div class="dm-tr" role="row" style="grid-template-columns: {MG_COLS}">
              <span role="cell" class="dm-nm"><a href="/stacks/{encodeURIComponent(mg.stack_name)}">{mg.stack_name}</a></span>
              <span role="cell" class="dm-num dm-dim">{mg.source_host_id === id ? `→ ${mg.target_host_id}` : `← ${mg.source_host_id}`}</span>
              <span role="cell"><StatusChip tone={mg.status === 'completed' ? 'ok' : mg.status === 'failed' ? 'bad' : 'warn'} label={migStatusLabel(mg.status)} /></span>
              <span role="cell" class="dm-num dm-dim">{fmtAgoNever(mg.started_at)}</span>
              <span role="cell" class="dm-dim">{mg.initiated_by}</span>
              <span role="cell" class="dm-num dm-dim">{mg.completed_at ? fmtAgoNever(mg.completed_at) : '—'}</span>
            </div>
          {/each}
        </div>
      {/if}

    {:else if tab === 'tags'}
      <div class="dm-form-sec">
        <div>
          <h4>{m.hosts_tags_on_host()}</h4>
          <p>{m.hosts_tags_on_host_hint()}</p>
        </div>
        <div>
          <div class="tagedit">
            {#each tagsDraft as t (t)}
              <span class="tagx"><TagPill label={t} />{#if allowed('hosts.tag')}<button type="button" onclick={() => (tagsDraft = tagsDraft.filter((x) => x !== t))} aria-label={m.hosts_remove_tag({ tag: t })}><X size={11} /></button>{/if}</span>
            {/each}
            {#if allowed('hosts.tag')}
              <input
                type="text"
                class="dm-filter"
                placeholder={tagsDraft.length === 0 ? m.hosts_add_a_tag_ph() : m.hosts_add_tag_ph()}
                aria-label={m.hosts_add_tag()}
                bind:value={tagInput}
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTagDraft(tagInput); } }}
              />
            {/if}
          </div>
          {#if allowed('hosts.tag') && allFleetTags.filter((t) => !tagsDraft.includes(t)).length > 0}
            <div class="suggest">
              <span class="dm-dim">{m.hosts_in_use()}</span>
              {#each allFleetTags.filter((t) => !tagsDraft.includes(t)) as s (s)}
                <TagPill label={s} onclick={addTagDraft} title={m.hosts_add_tag_named({ tag: s })} />
              {/each}
            </div>
          {/if}
          {#if allowed('hosts.tag')}
            <div class="row">
              <button type="button" class="dm-btn dm-btn-primary" disabled={tagsBusy || tagsDraft.join(',') === tags.join(',')} onclick={saveTags}>{tagsBusy ? m.common_saving() : m.hosts_save_tags()}</button>
              <button type="button" class="dm-btn dm-btn-ghost" disabled={tagsDraft.join(',') === tags.join(',')} onclick={() => (tagsDraft = [...tags])}>{m.hosts_revert()}</button>
            </div>
          {/if}
        </div>
      </div>

    {:else if tab === 'maintenance'}
      <div>
        <div class="dm-form-sec">
          <div>
            <h4>{m.hosts_drain_host()}</h4>
            <p>{m.hosts_drain_host_hint({ name: hostName })}</p>
          </div>
          <div>
            <button type="button" class="dm-btn" disabled={isLocal || !allowed('hosts.update') || status !== 'online'} onclick={openDrain}>
              <ArrowDownToLine size={13} strokeWidth={1.8} /> {m.hosts_plan_drain()}
            </button>
            {#if isLocal}<span class="dm-sub">{m.hosts_local_no_drain_dot()}</span>{/if}
          </div>
        </div>
        <div class="dm-form-sec">
          <div>
            <h4>{m.hosts_upgrade_agent()}</h4>
            <p>
              {#if isLocal}{m.hosts_server_self_update()}
              {:else if !needsUpgrade}{hostVersion ? m.hosts_already_on({ version: fmtVersion(hostVersion) }) : m.hosts_version_unknown()}
              {:else}{m.hosts_upgrade_explain({ current: fmtVersion(hostVersion), server: fmtVersion(upgradePolicyVersion) })}{/if}
            </p>
          </div>
          <div>
            <button type="button" class="dm-btn dm-btn-primary" disabled={isLocal || !needsUpgrade || upgradeBusy || !allowed('hosts.update')} onclick={upgradeAgent}>
              <RefreshCw size={13} strokeWidth={1.8} /> {upgradeBusy ? m.hosts_upgrading() : m.hosts_upgrade_now()}
            </button>
          </div>
        </div>
        {#if allowed('hosts.create')}
          <div class="dm-form-sec">
            <div>
              <h4>{m.hosts_reissue_heading()}</h4>
              <p>{m.hosts_reissue_hint()}</p>
            </div>
            <div>
              <button type="button" class="dm-btn" disabled={isLocal} onclick={rotateToken}><RefreshCw size={13} strokeWidth={1.8} /> {m.hosts_reissue_ellipsis()}</button>
            </div>
          </div>
        {/if}
        {#if allowed('hosts.delete')}
          <div class="dm-form-sec">
            <div>
              <h4 class="danger">{m.hosts_revoke_host()}</h4>
              <p>{m.hosts_revoke_host_hint({ name: hostName })}</p>
            </div>
            <div>
              <button type="button" class="dm-btn dm-btn-danger" disabled={isLocal || revokeBusy} onclick={revokeHost}>
                <Trash2 size={13} strokeWidth={1.8} /> {revokeBusy ? m.hosts_revoking() : m.hosts_revoke_ellipsis()}
              </button>
            </div>
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</section>

<Modal bind:open={showDrain} title={m.hosts_drain_title({ name: hostName })} maxWidth="max-w-xl">
  {#if drainLoading}
    <p class="dm-sub">{m.hosts_drain_planning()}</p>
  {:else if drainPlan}
    {#if drainPlan.entries.length === 0}
      <p class="dm-sub">{m.hosts_drain_nothing()}</p>
    {:else}
      {@const stuck = drainPlan.entries.filter((e) => !e.feasible)}
      {#if stuck.length > 0}
        <div class="warnbox" role="alert">
          <AlertTriangle size={14} strokeWidth={1.8} />
          <span><b>{m.hosts_drain_stuck({ count: stuck.length })}</b> {m.hosts_drain_stuck_hint()}</span>
        </div>
      {/if}
      <div class="dm-tbl">
        {#each drainPlan.entries as e (e.stack_name)}
          <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 20px minmax(0, 1fr)">
            <span class="dm-mono">{e.stack_name}</span>
            <span class="dm-dim">→</span>
            <span class="dm-mono" class:bad={!e.feasible}>{e.feasible ? e.target_name : m.hosts_no_candidate()}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (showDrain = false)}>{m.common_cancel()}</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={drainBusy || !drainPlan?.feasible} onclick={executeDrain}>
      <ArrowDownToLine size={13} strokeWidth={1.8} /> {drainBusy ? m.hosts_starting() : m.hosts_begin_drain()}
    </button>
  {/snippet}
</Modal>

<Modal bind:open={tokenModal.open} title={m.hosts_token_title({ name: hostName })} maxWidth="max-w-xl">
  <div class="tok">
    <p class="dm-sub">{m.hosts_token_run()}</p>
    <pre class="cmd dm-mono">{tokenModal.install}</pre>
    <p class="dm-sub">{m.hosts_token_once({ date: tokenModal.expiresAt ? fmtDateTime(tokenModal.expiresAt) : '—' })}</p>
    <dl class="dm-kv"><dt>Token</dt><dd class="dm-mono">{tokenModal.token}</dd></dl>
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => copyWithToast(tokenModal.install, m.hosts_install_copied())}>{m.hosts_copy_install()}</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={() => (tokenModal.open = false)}>{m.common_done()}</button>
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
