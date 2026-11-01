<script lang="ts">
  import { untrack } from 'svelte';
  import { sevTone } from './alerts/ruleKinds';
  import { fmtAgo, fmtMem, pctTone } from '$lib/format';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, isFanOut, type AttentionItem, type SystemMetrics, type ContainerSummary, type HostInfo, type BackupRun } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { latest } from '$lib/latest';
  import { fmtVersion } from '$lib/version';
  import { Skeleton, StatBar, StatusChip, Segmented, LiveIndicator, EmptyState } from '$lib/components/ui';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import type { Stat } from '$lib/components/ui/StatBar.svelte';
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
  }

  let health = $state<{ status: string; version: string; docker: boolean } | null>(null);
  let sysMetrics = $state<SystemMetrics | null>(null);
  let perHostMetrics = $state<PerHostMetrics[]>([]);
  let summary = $state<ContainerSummary | null>(null);
  let stackRows = $state<StackRow[]>([]);
  let changes = $state<Awaited<ReturnType<typeof api.audit.list>>>([]);
  let attention = $state<AttentionItem[] | null>(null);
  let runs = $state<BackupRun[] | null>(null);
  let hostList = $state<HostInfo[]>([]);
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let error = $state('');
  let stackFilter = $state<'all' | 'running' | 'issues' | 'stopped'>('all');

  const nextLoad = latest();
  async function load() {
    const current = nextLoad();
    refreshing = true;
    error = '';
    try {
      const [h, sysRaw, sum, stacksList, audit, hostListRaw, att, runList] = await Promise.all([
        api.health(),
        allowed('system.view') ? api.system.metrics(hosts.id).catch(() => null) : Promise.resolve(null),
        allowed('containers.view') ? api.containers.summary(hosts.id).catch(() => null) : Promise.resolve(null),
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
      stackRows = stacksList.map((s: any): StackRow => {
        if (!sum) return { name: s.name, state: 'unknown', total: 0, running: 0, unhealthy: 0, hosts: [] };
        const r = sum.by_stack[s.name];
        if (!r) return { name: s.name, state: 'stopped', total: 0, running: 0, unhealthy: 0, hosts: [] };
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
      if (current()) error = err.message ?? 'Failed to load';
    } finally {
      if (current()) {
        loading = false;
        refreshing = false;
      }
    }
  }

  $effect(() => {
    hosts.id;
    untrack(load);
  });
  $effect(() => autoRefresh(load, 10_000));

  const ORDER: Record<StackState, number> = { unhealthy: 0, partial: 1, running: 2, stopped: 3, unknown: 4 };
  function stackTone(s: StackState): Tone {
    return s === 'running' ? 'ok' : s === 'unhealthy' ? 'bad' : s === 'partial' ? 'warn' : 'off';
  }
  function stackLabel(s: StackState): string {
    return s === 'running' ? 'running' : s === 'unhealthy' ? 'unhealthy' : s === 'partial' ? 'degraded' : s === 'unknown' ? 'no status' : 'stopped';
  }
  const hostName = (id: string) => hostList.find((h) => h.id === id)?.name ?? id;

  // Hosts in the lens: every host in all-mode, else the selected one. A
  // host without metrics is offline or unreachable.
  interface Tile { id: string; name: string; kind: string; status: string; cpu: number | null; mem: number | null; disk: number | null }
  const tiles = $derived.by<Tile[]>(() => {
    const byId = new Map<string, SystemMetrics>();
    for (const m of perHostMetrics) byId.set(m.host_id, m);
    const list = hosts.isAll ? hostList : hostList.filter((h) => h.id === hosts.id);
    if (!hosts.isAll && sysMetrics && list[0]) byId.set(list[0].id, sysMetrics);
    return list.map((h) => {
      const m = byId.get(h.id);
      return {
        id: h.id,
        name: h.name,
        kind: h.kind,
        status: h.status,
        cpu: m ? Math.round(m.cpu_percent) : null,
        mem: m && m.mem_total > 0 ? Math.round(m.mem_percent) : null,
        disk: m && m.disk_total > 0 ? Math.round(m.disk_percent) : null
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
  const SEV: Record<string, number> = { critical: 0, warning: 1, info: 2 };
  const ATTN_LIMIT = 5;
  const attnAll = $derived(page.url.searchParams.get('attention') === 'all');
  const attnSev = $derived(page.url.searchParams.get('severity') ?? '');
  const attnSorted = $derived(
    (attention ?? [])
      .slice()
      .sort((a, b) => (SEV[a.severity] ?? 3) - (SEV[b.severity] ?? 3) || Date.parse(b.since ?? '0') - Date.parse(a.since ?? '0'))
  );
  const attnShown = $derived(attnAll ? attnSorted.filter((a) => !attnSev || a.severity === attnSev) : attnSorted.slice(0, ATTN_LIMIT));
  const attnCounts = $derived({
    '': attnSorted.length,
    critical: attnSorted.filter((a) => a.severity === 'critical').length,
    warning: attnSorted.filter((a) => a.severity === 'warning').length,
    info: attnSorted.filter((a) => a.severity === 'info').length
  });
  const fleetCpu = $derived.by(() => {
    if (sysMetrics) return Math.round(sysMetrics.cpu_percent);
    if (!perHostMetrics.length) return null;
    return Math.round(perHostMetrics.reduce((a, m) => a + m.cpu_percent, 0) / perHostMetrics.length);
  });

  const stats = $derived.by<Stat[]>(() => {
    const out: Stat[] = [
      {
        label: 'Hosts',
        value: `${online} / ${lensHosts.length}`,
        note: offline.length ? `${offline.length} offline` : 'all online',
        tone: offline.length ? 'bad' : '',
        href: '/hosts'
      },
      {
        label: 'Stacks',
        value: stackRows.length,
        note: issues.length ? `${issues.length} need attention` : 'all healthy',
        tone: issues.length ? 'warn' : '',
        href: '/stacks'
      },
      {
        label: 'Containers',
        value: summary ? `${summary.running} / ${summary.total}` : '—',
        note: summary?.unhealthy ? `${summary.unhealthy} unhealthy` : 'running',
        tone: summary?.unhealthy ? 'warn' : ''
      },
      {
        label: hosts.isAll ? 'Fleet CPU' : sysMetrics?.docker_limited ? 'CPU · Docker limit' : 'CPU',
        value: fleetCpu != null ? `${fleetCpu} %` : '—',
        note: hosts.isAll ? `average of ${perHostMetrics.length} host${perHostMetrics.length === 1 ? '' : 's'}` : sysMetrics ? `${sysMetrics.cpu_used_cores.toFixed(1)} of ${sysMetrics.cpu_cores.toFixed(0)} cores` : ''
      }
    ];
    if (sysMetrics && sysMetrics.mem_total > 0) {
      out.push({
        label: 'Memory',
        value: `${Math.round(sysMetrics.mem_percent)} %`,
        note: `${fmtMem(sysMetrics.mem_used)} of ${fmtMem(sysMetrics.mem_total)}`
      });
    }
    if (runs24) {
      out.push({
        label: 'Backups · 24 h',
        value: runs24.length,
        note: failed24.length ? `${failed24.length} failed` : runs24.length ? 'all succeeded' : 'none ran',
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
    const parts = [`${online} of ${lensHosts.length} host${lensHosts.length === 1 ? '' : 's'} online`];
    if (summary) parts.push(`${summary.running} of ${summary.total} containers running`);
    if (health?.version) parts.push(`dockmesh ${fmtVersion(health.version)}`);
    return parts.join(' · ');
  });
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Overview</h1>
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
      <span><b>Docker is not responding.</b> Container, stack, image and volume pages show errors until it is back. Dockmesh checks again every 10 seconds; no restart is needed.</span>
    </div>
  {/if}

  {#if loading}
    <Skeleton width="100%" height="3.2rem" />
    <Skeleton width="100%" height="4rem" />
  {:else}
    <StatBar items={stats} />

    {#if tiles.length}
      <div class="hoststrip">
        {#each tiles as t (t.id)}
          {@const st = t.status !== 'online' ? 'bad' : (t.cpu ?? 0) >= 80 || (t.mem ?? 0) >= 80 ? 'warn' : 'ok'}
          <a class="hostcard" href="/hosts/{t.id}">
            <span class="hc-top">
              <span class="dm-dot {st}"></span>
              <b class="dm-trunc">{t.name}</b>
              <span class="hc-meta dm-trunc">{t.kind} · {stacksPerHost[t.id] ?? 0} stacks</span>
            </span>
            {#if t.status === 'online' && t.cpu != null}
              <span class="bar"><span>CPU</span><i><b class={pctTone(t.cpu)} style="width:{Math.min(100, t.cpu)}%"></b></i><span>{t.cpu}%</span></span>
              <span class="bar"><span>RAM</span>{#if t.mem != null}<i><b class={pctTone(t.mem, 80)} style="width:{Math.min(100, t.mem)}%"></b></i><span>{t.mem}%</span>{:else}<i></i><span>—</span>{/if}</span>
            {:else}
              <span class="hc-off dm-trunc">{t.status === 'online' ? 'no metrics' : t.status}</span>
              <span class="hc-pad"></span>
            {/if}
          </a>
        {/each}
      </div>
    {/if}

    {#if attention}
      <div class="box">
        {#if attention.length}
          <div class="box-h">
            <h3>Needs attention <span class="dm-dim">{attention.length}</span></h3>
            {#if attnAll}
              <Segmented
                label="Severity"
                size="sm"
                value={attnSev}
                options={[
                  { key: '', label: 'All', count: attnCounts[''] },
                  { key: 'critical', label: 'Critical', count: attnCounts.critical },
                  { key: 'warning', label: 'Warning', count: attnCounts.warning },
                  { key: 'info', label: 'Info', count: attnCounts.info }
                ]}
                onchange={(v: string) => goto(v ? `/?attention=all&severity=${v}` : '/?attention=all', { replaceState: true })}
              />
              <span class="dm-sp"></span>
              <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/">Show top {ATTN_LIMIT}</a>
            {/if}
          </div>
          {#each attnShown as a (a.id)}
            <div class="attn">
              <StatusChip tone={sevTone(a.severity)} label={a.severity} />
              <div class="t dm-trunc"><b>{a.title}</b>{#if a.detail}<span> — {a.detail}</span>{/if}</div>
              {#if a.since}<span class="dm-num dm-dim">{fmtAgo(a.since)}</span>{/if}
              <a class="dm-btn dm-btn-sm" href={a.link}>Open</a>
            </div>
          {/each}
          {#if !attnAll && attention.length > ATTN_LIMIT}
            <div class="attn more"><a class="dm-btn dm-btn-ghost dm-btn-sm" href="/?attention=all">+{attention.length - ATTN_LIMIT} more</a></div>
          {/if}
        {:else}
          <div class="attn">
            <StatusChip tone="ok" label="all good" />
            <div class="t dm-trunc"><b>Nothing needs attention.</b> <span>Hosts online, containers healthy, last backups succeeded.</span></div>
          </div>
        {/if}
      </div>
    {/if}

    <div class="grid2">
      <div class="col">
        {#if allowed('stacks.view')}
          <div>
            <div class="sec-h">
              <h2 class="dm-h2">Stacks <span class="dm-dim dm-num">{stackRows.length}</span></h2>
              <Segmented
                label="Filter stacks"
                size="sm"
                bind:value={stackFilter}
                options={[
                  { key: 'all', label: 'All', count: stackCounts.all },
                  { key: 'running', label: 'Running', count: stackCounts.running },
                  { key: 'issues', label: 'Issues', count: stackCounts.issues },
                  { key: 'stopped', label: 'Stopped', count: stackCounts.stopped }
                ]}
              />
              <span class="dm-sp"></span>
              <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/stacks">All stacks →</a>
            </div>
            {#if stackRows.length === 0}
              <EmptyState title="No stacks yet" description="Deploy a compose file to get started.">
                {#snippet action()}<a class="dm-btn dm-btn-primary" href="/stacks">Go to stacks</a>{/snippet}
              </EmptyState>
            {:else}
              <div class="dm-tbl">
                {#each filteredStacks.slice(0, STACK_LIMIT) as s (s.name)}
                  {@const tone = stackTone(s.state)}
                  <div
                    class="dm-tr dm-click"
                    role="link"
                    tabindex="0"
                    style="grid-template-columns: 14px minmax(0, 1fr) auto 52px 90px"
                    onclick={() => goto(`/stacks/${encodeURIComponent(s.name)}`)}
                    onkeydown={(e) => { if (e.key === 'Enter') goto(`/stacks/${encodeURIComponent(s.name)}`); }}
                  >
                    <span class="dm-dot {tone === 'off' ? '' : tone}"></span>
                    <span class="dm-trunc"><b>{s.name}</b> <span class="dm-dim">· {s.hosts.length ? s.hosts.map(hostName).join(', ') : s.state === 'unknown' ? 'container status not available' : 'not running'}</span></span>
                    <span class="dm-mini" title="{s.running} of {s.total} running{s.unhealthy ? `, ${s.unhealthy} unhealthy` : ''}">
                      {#each Array(Math.min(s.total, 12)) as _, i (i)}<i class={i < s.unhealthy ? 'warn' : i < s.running ? '' : 'off'}></i>{/each}
                    </span>
                    <span class="dm-num dm-dim" style="text-align: right">{s.total ? `${s.running}/${s.total}` : '—'}</span>
                    <span><StatusChip {tone} label={stackLabel(s.state)} /></span>
                  </div>
                {:else}
                  <div class="dm-empty">No stacks match.</div>
                {/each}
              </div>
              {#if filteredStacks.length > STACK_LIMIT}
                <a class="dm-btn dm-btn-ghost dm-btn-sm more" href="/stacks">{filteredStacks.length - STACK_LIMIT} more →</a>
              {/if}
            {/if}
          </div>
        {/if}
        {#if sysMetrics && (sysMetrics.mounts?.length || sysMetrics.mounts_unsupported)}
          <div>
            <h2 class="dm-h2">Disks <span class="dm-dim dm-num">{sysMetrics.mounts?.length ?? 0}</span></h2>
            <MountTable mounts={sysMetrics.mounts} unsupported={sysMetrics.mounts_unsupported} />
          </div>
        {/if}
      </div>

      {#if allowed('audit.view')}
        <div class="col">
          <div>
            <div class="sec-h">
              <h2 class="dm-h2">Recent changes</h2>
              <span class="dm-sp"></span>
              <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/audit">Audit log →</a>
            </div>
            <div class="dm-tbl">
              {#each changes as e (e.id)}
                <div class="dm-tr" style="grid-template-columns: 52px minmax(0, 1fr)" title={new Date(e.ts).toLocaleString()}>
                  <span class="dm-num dm-dim">{fmtAgo(e.ts)}</span>
                  <span class="dm-trunc"><b>{e.username || 'system'}</b> <span class="dm-mono act">{e.action}</span> {e.target ?? ''}</span>
                </div>
              {:else}
                <div class="dm-empty">No changes recorded yet.</div>
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

  .hoststrip { display: grid; grid-template-columns: repeat(auto-fill, minmax(215px, 1fr)); gap: 10px; }
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
  .act { font-size: 12px; color: var(--muted); }
</style>
