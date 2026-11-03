<script lang="ts">
  import { untrack } from 'svelte';
  import { sevTone } from './alerts/ruleKinds';
  import { fmtAgo, fmtMem, pctTone } from '$lib/format';
  import { goto } from '$app/navigation';
  import { api, isFanOut, type AttentionItem, type SystemMetrics, type ContainerSummary, type HostInfo, type BackupRun } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { latest } from '$lib/latest';
  import { fmtVersion } from '$lib/version';
  import { Skeleton, StatBar, StatusChip, Segmented, LiveIndicator, EmptyState } from '$lib/components/ui';
  import { notices } from '$lib/stores/notices.svelte';
  import { withoutBanner } from '$lib/notices';
  import { attentionText, sortAttention } from '$lib/attention';
  import { inAppLink } from '$lib/links';
  import { stackInLens } from '$lib/lens';
  import { auditSentence, hostNames as hostNameMap, loadUserNames, targetLabel } from '$lib/auditText';
  import { m } from '$lib/paraglide/messages.js';
  import { alertSeverityLabel } from '$lib/labels';
  import { fmtDateTime, fmtNumber } from '$lib/i18n';
  import type { Stat } from '$lib/components/ui/StatBar.svelte';
  import StackHealth from '$lib/components/ui/StackHealth.svelte';
  import MountTable from '$lib/components/MountTable.svelte';
  import { AlertTriangle } from 'lucide-svelte';

  type PerHostMetrics = SystemMetrics & { host_id: string; host_name: string };

  type StackState = 'running' | 'stopped' | 'unhealthy' | 'partial' | 'unknown';
  interface StackRow {
    name: string;
    state: StackState;
    total: number;
    running: number;
    unhealthy: number;
    hosts: string[];
    deployed?: boolean;
  }

  let health = $state<{ status: string; version: string; docker: boolean } | null>(null);
  let sysMetrics = $state<SystemMetrics | null>(null);
  let perHostMetrics = $state<PerHostMetrics[]>([]);
  let summary = $state<ContainerSummary | null>(null);
  let stackRows = $state<StackRow[]>([]);
  let changes = $state<Awaited<ReturnType<typeof api.audit.list>>>([]);
  let userNames = $state(new Map<string, string>());
  $effect(() => {
    loadUserNames(allowed('audit.view') && allowed('users.view'), api.users.list).then((u) => (userNames = u));
  });
  const hostNames = $derived(hostNameMap(hosts.available));
  let attention = $state<AttentionItem[] | null>(null);
  let runs = $state<BackupRun[] | null>(null);
  let hostList = $state<HostInfo[]>([]);
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let error = $state('');
  let stackFilter = $state<'all' | 'running' | 'issues' | 'stopped'>('all');

  const lensKnown = () => hosts.usable(allowed('hosts.view'));

  const nextLoad = latest();
  async function load() {
    const current = nextLoad();
    refreshing = true;
    error = '';
    try {
      const [h, sysRaw, sum, stacksList, audit, hostListRaw, att, runList] = await Promise.all([
        api.health(),
        allowed('hosts.view') && lensKnown() ? api.system.metrics(hosts.id).catch(() => null) : Promise.resolve(null),
        allowed('containers.view') && lensKnown() ? api.containers.summary(hosts.id).catch(() => null) : Promise.resolve(null),
        allowed('stacks.view') ? api.stacks.list().catch(() => []) : Promise.resolve([]),
        allowed('audit.view')
          ? api.audit.list(8, '', '', 'changes').catch(() => [])
          : Promise.resolve([]),
        api.hosts.list().catch(() => []),
        api.attention.list().catch(() => null),
        allowed('backups.view') ? api.backups.listRuns(200).catch(() => null) : Promise.resolve(null)
      ]);
      if (!current()) return;
      health = h;
      if (sysRaw && isFanOut(sysRaw)) {
        perHostMetrics = sysRaw.items as PerHostMetrics[];
        sysMetrics = null;
      } else {
        perHostMetrics = [];
        sysMetrics = sysRaw as SystemMetrics | null;
      }
      summary = sum;
      stackRows = stacksList.filter((s: any) => stackInLens(s, hosts.id, !!sum?.by_stack[s.name])).map((s: any): StackRow => {
        if (!sum) return { name: s.name, state: 'unknown', total: 0, running: 0, unhealthy: 0, hosts: [] };
        const r = sum.by_stack[s.name];
        if (!r) return { name: s.name, state: 'stopped', total: 0, running: 0, unhealthy: 0, hosts: [], deployed: !!s.deployment };
        let state: StackState;
        if (r.unhealthy > 0) state = 'unhealthy';
        else if (r.running === r.total) state = 'running';
        else if (r.running === 0) state = 'stopped';
        else state = 'partial';
        return { name: s.name, state, total: r.total, running: r.running, unhealthy: r.unhealthy, hosts: r.hosts };
      });
      changes = audit;
      hostList = hostListRaw;
      attention = att;
      runs = runList;
      lastLoaded = Date.now();
    } catch (err: any) {
      if (current()) error = err.message ?? m.overview_load_failed();
    } finally {
      if (current()) {
        loading = false;
        refreshing = false;
      }
    }
  }

  $effect(() => {
    hosts.id;
    hosts.available.length;
    untrack(load);
  });
  $effect(() => autoRefresh(load, 10_000));

  const KIND_LABEL: Record<string, () => string> = { local: m.overview_kind_local, agent: m.overview_kind_agent };
  const kindLabel = (kind: string) => KIND_LABEL[kind]?.() ?? kind;

  const ORDER: Record<StackState, number> = { unhealthy: 0, partial: 1, running: 2, stopped: 3, unknown: 4 };
  const hostName = (id: string) => hostList.find((h) => h.id === id)?.name ?? id;

  // Hosts in the lens: every host in all-mode, else the selected one. A
  // host without metrics is offline or unreachable.
  interface Tile { id: string; name: string; kind: string; status: string; cpu: number | null; mem: number | null; disk: number | null }
  const tiles = $derived.by<Tile[]>(() => {
    const byId = new Map<string, SystemMetrics>();
    for (const pm of perHostMetrics) byId.set(pm.host_id, pm);
    const list = hosts.isAll ? hostList : hostList.filter((h) => h.id === hosts.id);
    if (!hosts.isAll && sysMetrics && list[0]) byId.set(list[0].id, sysMetrics);
    return list.map((h) => {
      const hm = byId.get(h.id);
      return {
        id: h.id,
        name: h.name,
        kind: h.kind,
        status: h.status,
        cpu: hm ? Math.round(hm.cpu_percent) : null,
        mem: hm && hm.mem_total > 0 ? Math.round(hm.mem_percent) : null,
        disk: hm && hm.disk_total > 0 ? Math.round(hm.disk_percent) : null
      };
    });
  });
  const stacksPerHost = $derived.by(() => {
    const c: Record<string, number> = {};
    for (const s of stackRows) for (const h of s.hosts) c[h] = (c[h] ?? 0) + 1;
    return c;
  });

  const lensHosts = $derived(hosts.isAll ? hostList : hostList.filter((h) => h.id === hosts.id));
  const online = $derived(lensHosts.filter((h) => h.status === 'online').length);
  const offline = $derived(lensHosts.filter((h) => h.status === 'offline'));
  const issues = $derived(stackRows.filter((s) => s.state === 'unhealthy' || s.state === 'partial'));
  const runs24 = $derived(runs ? runs.filter((r) => Date.now() - Date.parse(r.started_at) < 86_400_000) : null);
  const failed24 = $derived(runs24 ? runs24.filter((r) => r.status === 'failed') : []);
  const ATTN_LIMIT = 5;
  const TILE_LIMIT = 10;
  const attnSorted = $derived(attention ? sortAttention(withoutBanner(attention, notices.items)) : null);
  const fleetCpu = $derived.by(() => {
    if (sysMetrics) return Math.round(sysMetrics.cpu_percent);
    if (!perHostMetrics.length) return null;
    return Math.round(perHostMetrics.reduce((a, pm) => a + pm.cpu_percent, 0) / perHostMetrics.length);
  });

  const stats = $derived.by<Stat[]>(() => {
    const out: Stat[] = [
      {
        label: m.common_hosts(),
        value: `${online} / ${lensHosts.length}`,
        note: offline.length ? m.overview_hosts_offline({ count: offline.length }) : m.overview_all_online(),
        tone: offline.length ? 'bad' : '',
        href: '/hosts'
      },
      {
        label: m.common_stacks(),
        value: stackRows.length,
        note: issues.length ? m.overview_stacks_attention({ count: issues.length }) : m.overview_all_healthy(),
        tone: issues.length ? 'warn' : '',
        href: '/stacks'
      },
      {
        label: m.common_containers(),
        value: summary ? `${summary.running} / ${summary.total}` : '—',
        note: summary?.unhealthy ? m.overview_n_unhealthy({ count: summary.unhealthy }) : m.overview_containers_running(),
        tone: summary?.unhealthy ? 'warn' : ''
      },
      {
        label: hosts.isAll ? m.overview_fleet_cpu() : sysMetrics?.docker_limited ? m.overview_cpu_docker_limit() : 'CPU',
        value: fleetCpu != null ? `${fleetCpu} %` : '—',
        note: hosts.isAll ? m.overview_cpu_average({ count: perHostMetrics.length }) : sysMetrics ? m.overview_cpu_cores({ used: fmtNumber(sysMetrics.cpu_used_cores, { minimumFractionDigits: 1, maximumFractionDigits: 1 }), total: fmtNumber(sysMetrics.cpu_cores, { maximumFractionDigits: 0 }) }) : ''
      }
    ];
    if (sysMetrics && sysMetrics.mem_total > 0) {
      out.push({
        label: m.overview_memory(),
        value: `${Math.round(sysMetrics.mem_percent)} %`,
        note: m.overview_mem_of({ used: fmtMem(sysMetrics.mem_used), total: fmtMem(sysMetrics.mem_total) })
      });
    }
    if (runs24) {
      out.push({
        label: m.overview_backups_24h(),
        value: runs24.length,
        note: failed24.length ? m.overview_n_failed({ count: failed24.length }) : runs24.length ? m.overview_all_succeeded() : m.overview_none_ran(),
        tone: failed24.length ? 'bad' : '',
        href: '/backups'
      });
    }
    return out;
  });

  const stackCounts = $derived({
    all: stackRows.length,
    running: stackRows.filter((s) => s.state === 'running').length,
    issues: issues.length,
    stopped: stackRows.filter((s) => s.state === 'stopped').length
  });
  const STACK_LIMIT = 10;
  const filteredStacks = $derived(
    stackRows
      .filter((s) =>
        stackFilter === 'all' ||
        (stackFilter === 'running' && s.state === 'running') ||
        (stackFilter === 'issues' && (s.state === 'unhealthy' || s.state === 'partial')) ||
        (stackFilter === 'stopped' && s.state === 'stopped')
      )
      .sort((a, b) => ORDER[a.state] - ORDER[b.state] || a.name.localeCompare(b.name))
  );

  const subtitle = $derived.by(() => {
    const parts: string[] = [m.overview_sub_hosts({ online, count: lensHosts.length })];
    if (summary) parts.push(m.overview_sub_containers({ running: summary.running, count: summary.total }));
    if (health?.version) parts.push(`dockmesh ${fmtVersion(health.version)}`);
    return parts.join(' · ');
  });
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>{m.common_overview()}</h1>
    <span class="dm-sub">{subtitle}</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
  </div>

  {#if error}
    <p class="err" role="alert">{error}</p>
  {/if}

  {#if health && !health.docker}
    <div class="notice bad" role="alert">
      <AlertTriangle size={14} strokeWidth={1.8} />
      <span><b>{m.overview_docker_down()}</b> {m.overview_docker_down_hint()}</span>
    </div>
  {/if}

  {#if loading}
    <Skeleton width="100%" height="3.2rem" />
    <Skeleton width="100%" height="4rem" />
  {:else}
    <StatBar items={stats} />

    {#if tiles.length}
      <div class="hoststrip" class:single={tiles.length === 1}>
        {#each tiles.slice(0, TILE_LIMIT) as t (t.id)}
          {@const st = t.status !== 'online' ? 'bad' : (t.cpu ?? 0) >= 80 || (t.mem ?? 0) >= 80 ? 'warn' : 'ok'}
          <a class="hostcard" href="/hosts/{t.id}">
            <span class="hc-top">
              <span class="dm-dot {st}"></span>
              <b class="dm-trunc">{t.name}</b>
              <span class="hc-meta dm-trunc">{kindLabel(t.kind)} · {m.overview_n_stacks({ count: stacksPerHost[t.id] ?? 0 })}</span>
            </span>
            {#if t.status === 'online' && t.cpu != null}
              <span class="bar"><span>CPU</span><i><b class={pctTone(t.cpu)} style="width:{Math.min(100, t.cpu)}%"></b></i><span>{t.cpu}%</span></span>
              <span class="bar"><span>RAM</span>{#if t.mem != null}<i><b class={pctTone(t.mem, 80)} style="width:{Math.min(100, t.mem)}%"></b></i><span>{t.mem}%</span>{:else}<i></i><span>—</span>{/if}</span>
              {#if tiles.length === 1}
                <span class="bar"><span>{m.overview_disk()}</span>{#if t.disk != null}<i><b class={pctTone(t.disk, 80)} style="width:{Math.min(100, t.disk)}%"></b></i><span>{t.disk}%</span>{:else}<i></i><span>—</span>{/if}</span>
              {/if}
            {:else}
              <span class="hc-off dm-trunc">{t.status === 'online' ? m.overview_no_metrics() : t.status}</span>
              <span class="hc-pad"></span>
            {/if}
          </a>
        {/each}
        {#if tiles.length > TILE_LIMIT}
          <a class="hostcard more" href="/hosts">{m.overview_more_hosts({ count: tiles.length - TILE_LIMIT })}</a>
        {/if}
      </div>
    {/if}

    {#if attnSorted}
      <div class="box">
        {#if attnSorted.length}
          <div class="box-h">
            <h3>{m.overview_needs_attention()} <span class="dm-dim dm-num">{attnSorted.length}</span></h3>
          </div>
          {#each attnSorted.slice(0, ATTN_LIMIT) as a (a.id)}
            {@const t = attentionText(a)}
            <div class="attn">
              <StatusChip tone={sevTone(a.severity)} label={alertSeverityLabel(a.severity)} />
              <div class="t dm-trunc" title={t.detail ? `${t.title} — ${t.detail}` : t.title}><b>{t.title}</b>{#if t.detail}<span> — {t.detail}</span>{/if}</div>
              {#if a.since}<span class="dm-num dm-dim" title={fmtDateTime(a.since)}>{fmtAgo(a.since)}</span>{/if}
              {#if inAppLink(a.link)}<a class="dm-btn dm-btn-sm" href={inAppLink(a.link)}>{m.common_open()}</a>{/if}
            </div>
          {/each}
          {#if attnSorted.length > ATTN_LIMIT}
            <div class="attn more"><a class="dm-btn dm-btn-ghost dm-btn-sm" href="/activity">{m.overview_view_all({ count: attnSorted.length })}</a></div>
          {/if}
        {:else}
          <div class="attn">
            <StatusChip tone="ok" label={m.overview_all_good()} />
            <div class="t dm-trunc"><b>{m.overview_nothing_attention()}</b> <span>{m.overview_nothing_attention_hint()}</span></div>
          </div>
        {/if}
      </div>
    {/if}

    <div class="grid2">
      <div class="col">
        {#if allowed('stacks.view')}
          <div>
            <div class="sec-h">
              <h2 class="dm-h2">{m.common_stacks()} <span class="dm-dim dm-num">{stackRows.length}</span></h2>
              <Segmented
                label={m.overview_filter_stacks()}
                size="sm"
                bind:value={stackFilter}
                options={[
                  { key: 'all', label: m.common_all(), count: stackCounts.all },
                  { key: 'running', label: m.common_running(), count: stackCounts.running },
                  { key: 'issues', label: m.overview_issues(), count: stackCounts.issues },
                  { key: 'stopped', label: m.common_stopped(), count: stackCounts.stopped }
                ]}
              />
              <span class="dm-sp"></span>
              <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/stacks">{m.overview_all_stacks()}</a>
            </div>
            {#if stackRows.length === 0}
              <EmptyState title={m.overview_no_stacks()} description={m.overview_no_stacks_hint()}>
                {#snippet action()}<a class="dm-btn dm-btn-primary" href="/stacks">{m.overview_go_to_stacks()}</a>{/snippet}
              </EmptyState>
            {:else}
              <div class="dm-tbl">
                {#each filteredStacks.slice(0, STACK_LIMIT) as s (s.name)}
                  <div
                    class="dm-tr dm-click"
                    role="link"
                    tabindex="0"
                    style="grid-template-columns: minmax(0, 1fr) 190px"
                    onclick={() => goto(`/stacks/${encodeURIComponent(s.name)}`)}
                    onkeydown={(e) => { if (e.key === 'Enter') goto(`/stacks/${encodeURIComponent(s.name)}`); }}
                  >
                    <span class="dm-trunc"><b>{s.name}</b> {#if s.deployed !== false}<span class="dm-dim">· {s.hosts.length ? s.hosts.map(hostName).join(', ') : s.state === 'unknown' ? m.overview_status_unavailable() : m.overview_not_running()}</span>{/if}</span>
                    <StackHealth running={s.running} total={s.total} unhealthy={s.unhealthy} unknown={s.state === 'unknown'} notDeployed={s.deployed === false} />
                  </div>
                {:else}
                  <div class="dm-empty">{m.overview_no_stacks_match()}</div>
                {/each}
              </div>
              {#if filteredStacks.length > STACK_LIMIT}
                <a class="dm-btn dm-btn-ghost dm-btn-sm more" href="/stacks">{m.overview_n_more({ count: filteredStacks.length - STACK_LIMIT })}</a>
              {/if}
            {/if}
          </div>
        {/if}
        {#if sysMetrics && (sysMetrics.mounts?.length || sysMetrics.mounts_unsupported)}
          <div>
            <h2 class="dm-h2">{m.overview_disks()} <span class="dm-dim dm-num">{sysMetrics.mounts?.length ?? 0}</span></h2>
            <MountTable mounts={sysMetrics.mounts} unsupported={sysMetrics.mounts_unsupported} />
          </div>
        {/if}
      </div>

      {#if allowed('audit.view')}
        <div class="col">
          <div>
            <div class="sec-h">
              <h2 class="dm-h2">{m.overview_recent_changes()}</h2>
              <span class="dm-sp"></span>
              <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/audit">{m.overview_audit_log()}</a>
            </div>
            <div class="dm-tbl">
              {#each changes as e (e.id)}
                <div class="dm-tr" style="grid-template-columns: 76px minmax(0, 1fr)" title={fmtDateTime(e.ts)}>
                  <span class="dm-num dm-dim">{fmtAgo(e.ts)}</span>
                  <span class="dm-trunc" title={e.action}>{#each auditSentence(e.action, e.username || m.audit_actor_system(), targetLabel(e.action, e.target, { users: userNames, hosts: hostNames })) as p, i (i)}{#if p.strong}<b>{p.text}</b>{:else}{p.text}{/if}{/each}</span>
                </div>
              {:else}
                <div class="dm-empty">{m.overview_no_changes()}</div>
              {/each}
            </div>
          </div>
        </div>
      {/if}
    </div>
  {/if}
</section>

<style>
  .err { margin: 0; color: var(--bad); font-size: var(--fs-sm); }
  .notice {
    display: flex; gap: 8px; align-items: flex-start;
    border: 1px solid var(--warn-bd); background: var(--warn-bg); color: var(--warn);
    border-radius: var(--r); padding: 10px 14px; font-size: var(--fs-sm);
  }
  .notice.bad { border-color: var(--bad-bd); background: var(--bad-bg); color: var(--bad); }
  .notice span { color: var(--fg); }

  .hoststrip { display: grid; grid-template-columns: repeat(auto-fit, minmax(215px, 1fr)); gap: 10px; }
  .hoststrip.single .hostcard { flex-direction: row; align-items: center; gap: 24px; }
  .hoststrip.single .hc-top { flex: 0 1 340px; }
  .hoststrip.single .bar { flex: 1; }
  .hostcard.more { justify-content: center; align-items: center; color: var(--muted); }
  .hostcard {
    display: flex; flex-direction: column; gap: 6px; padding: 10px 12px; min-width: 0;
    border: 1px solid var(--border); border-radius: var(--r); background: var(--surface);
    color: var(--fg); text-decoration: none;
  }
  .hostcard:hover { border-color: var(--accent); }
  .hc-top { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .hc-meta { margin-left: auto; color: var(--subtle); font-size: 12px; }
  .hc-off { color: var(--bad); font-size: 12px; line-height: 18px; }
  .hc-pad { height: 18px; }
  .bar { display: flex; align-items: center; gap: 8px; font-size: 11.5px; color: var(--muted); line-height: 18px; }
  .bar > span:first-child { width: 30px; }
  .bar i { flex: 1; height: 5px; border-radius: 3px; background: var(--field); overflow: hidden; }
  .bar i b { display: block; height: 100%; background: var(--accent); }
  .bar i b.warn { background: var(--warn); }
  .bar i b.bad { background: var(--bad); }
  .bar > span:last-child { width: 36px; text-align: right; font-family: var(--font-mono); font-variant-numeric: tabular-nums; }

  .box { border: 1px solid var(--border); border-radius: var(--r); background: var(--surface); }
  .box-h { display: flex; align-items: center; gap: 12px; padding: 10px 14px; border-bottom: 1px solid var(--border); }
  .box-h h3 { margin: 0; font-size: 13px; font-weight: 600; }
  .attn { display: flex; align-items: center; gap: 12px; padding: 0 14px; min-height: 46px; border-bottom: 1px solid var(--border); }
  .attn:last-child { border-bottom: 0; }
  .attn .t { flex: 1; min-width: 0; }
  .attn .t b { font-weight: 600; }
  .attn .t span { color: var(--subtle); }
  .attn.more { min-height: 36px; font-size: var(--fs-sm); }

  .grid2 { display: grid; grid-template-columns: minmax(0, 3fr) minmax(300px, 2fr); gap: 28px; align-items: start; }
  @media (max-width: 1100px) { .grid2 { grid-template-columns: minmax(0, 1fr); } }
  .col { display: flex; flex-direction: column; gap: 22px; min-width: 0; }
  .sec-h { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }
  .sec-h .dm-h2 { margin: 0; }
  .more { margin-top: 6px; }
</style>
