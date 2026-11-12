<script lang="ts">
  import { untrack } from 'svelte';
  import { api, ApiError, isFanOut, type Operation, type UpdateStatus } from '$lib/api';
  import { fmtAgo } from '$lib/format';
  import { StatusChip, StatBar, EmptyState, Skeleton, LiveIndicator, InfoTip } from '$lib/components/ui';
  import type { Stat } from '$lib/components/ui/StatBar.svelte';
  import OpProgress from '$lib/components/operations/OpProgress.svelte';
  import CancelOpButton from '$lib/components/operations/CancelOpButton.svelte';
  import { operations, isActive, opRef } from '$lib/stores/operations.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { fmtVersion, semverBump } from '$lib/version';
  import { autoRefresh } from '$lib/autorefresh';
  import { m } from '$lib/paraglide/messages.js';

  interface Container {
    Id: string;
    Names: string[];
    Image: string;
    State: string;
    Labels?: Record<string, string>;
    image_update_available?: boolean;
    swarm_service?: string;
  }
  interface ImageCheck {
    image: string;
    update_available: boolean;
    local_digest?: string;
    remote_digest?: string;
    checked_at: string;
    error?: string;
  }
  interface AppRow {
    id: string;
    name: string;
    stack: string;
    image: string;
    check?: ImageCheck;
  }
  interface SwarmRow {
    id: string;
    name: string;
    image: string;
    service: string;
  }
  interface SystemRow {
    name: string;
    note: string;
    from: string;
    to: string;
    href: string;
  }

  const canUpdate = $derived(allowed('containers.upgrade'));
  const canDeploy = $derived(allowed('stacks.deploy'));

  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let apps = $state<AppRow[]>([]);
  let swarmTasks = $state<SwarmRow[]>([]);
  let checks = $state<Record<string, ImageCheck>>({});
  let system = $state<SystemRow[]>([]);
  let recentOps = $state<Operation[]>([]);
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);
  // Rows whose update this page started stay visible with their progress
  // after the container is replaced and drops out of the list.
  let started = $state<Record<string, AppRow>>({});

  const nameOf = (c: Container) => (c.Names?.[0] ?? c.Id.slice(0, 12)).replace(/^\//, '');
  const shortDigest = (d?: string) => (d ? d.replace(/^sha256:/, '').slice(0, 12) : '—');

  // "1.2.3" > "1.2.2"; anything not numeric compares as equal.
  function newer(a: string, b: string): boolean {
    const pa = a.replace(/^v/, '').split(/[.-]/).map(Number);
    const pb = b.replace(/^v/, '').split(/[.-]/).map(Number);
    for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
      const x = pa[i] ?? 0, y = pb[i] ?? 0;
      if (Number.isNaN(x) || Number.isNaN(y)) return false;
      if (x !== y) return x > y;
    }
    return false;
  }

  async function loadSystem(serverVersion: string) {
    const out: SystemRow[] = [];
    const [st, pv, agents] = await Promise.all([
      api.system.updateStatus().catch(() => null as UpdateStatus | null),
      allowed('proxy.view') ? api.proxy.version().catch(() => null) : Promise.resolve(null),
      allowed('hosts.view') ? api.agents.list().catch(() => []) : Promise.resolve([])
    ]);
    if (st?.update_available && !st.is_dev_build) {
      out.push({ name: 'Dockmesh', note: m.updates_sys_dockmesh_note(), from: fmtVersion(st.current_version), to: fmtVersion(st.latest_version), href: '/settings/updates' });
    }
    if (pv?.managed && pv.current && pv.default && newer(pv.default, pv.current)) {
      out.push({ name: m.updates_sys_caddy_name(), note: m.updates_sys_caddy_note(), from: pv.current, to: pv.default, href: '/proxy?tab=caddy' });
    }
    for (const a of agents as Array<{ id: string; name: string; status: string; version?: string }>) {
      if (a.status !== 'online' || !a.version || !serverVersion || a.version === serverVersion) continue;
      out.push({ name: m.updates_sys_agent_name({ name: a.name }), note: m.updates_sys_agent_note(), from: fmtVersion(a.version), to: fmtVersion(serverVersion), href: `/hosts/${encodeURIComponent(a.id)}` });
    }
    system = out;
  }

  // Image checks cover the server's own containers, which a caller limited
  // to agent hosts may not read.
  const seesLocal = () => hosts.available.length === 0 || hosts.available.some((h) => h.id === 'local');

  async function load() {
    refreshing = true;
    try {
      const info = await api.system.info().catch(() => null);
      const [list, imgs, ops] = await Promise.all([
        allowed('containers.view') && seesLocal() ? api.containers.list(true, 'local').catch(() => []) : Promise.resolve([]),
        allowed('images.view') ? api.images.updates().catch(() => ({})) : Promise.resolve({}),
        api.operations.list({ kind: 'image.update', limit: 50 }).catch(() => [] as Operation[]),
        loadSystem(info?.version ?? '')
      ]);
      const items = (isFanOut(list) ? list.items : list) as Container[];
      checks = imgs as Record<string, ImageCheck>;
      apps = items
        .filter((c) => c.image_update_available)
        .map((c) => ({
          id: c.Id,
          name: nameOf(c),
          stack: c.Labels?.['com.docker.compose.project'] ?? '',
          image: c.Image,
          check: checks[c.Image]
        }))
        .sort((a, b) => (a.stack || '~').localeCompare(b.stack || '~') || a.name.localeCompare(b.name));
      swarmTasks = items
        .filter((c) => c.swarm_service)
        .map((c) => ({ id: c.Id, name: nameOf(c), image: c.Image, service: c.swarm_service! }))
        .sort((a, b) => a.service.localeCompare(b.service) || a.name.localeCompare(b.name));
      operations.track(ops);
      recentOps = ops
        .filter((o) => o.status === 'succeeded' && o.steps.some((s) => s.key === 'swap' && s.status === 'succeeded'))
        .slice(0, 10);
      const ids = new Set(apps.map((a) => a.id));
      selected = new Set([...selected].filter((id) => ids.has(id)));
      lastLoaded = Date.now();
    } finally {
      loading = false;
      refreshing = false;
    }
  }
  $effect(() => {
    untrack(load);
  });
  $effect(() => autoRefresh(load, 60_000));

  const rows = $derived.by<AppRow[]>(() => {
    const ids = new Set(apps.map((a) => a.id));
    return [...apps, ...Object.values(started).filter((r) => !ids.has(r.id))];
  });
  // Running and queued updates show on their row wherever they were
  // started; finished ones only on rows this page started.
  const opFor = (id: string) => {
    const op = operations.latest(opRef.container(id), 'image.update');
    return op && (started[id] || isActive(op)) ? op : undefined;
  };

  // Reload once updates end, so finished rows leave the list.
  let wasActive = 0;
  $effect(() => {
    const n = rows.filter((r) => {
      const op = opFor(r.id);
      return op && isActive(op);
    }).length;
    if (n < wasActive) void load();
    wasActive = n;
  });
  const selectable = $derived(rows.filter((r) => !opFor(r.id) || !isActive(opFor(r.id)!)));
  const allSelected = $derived(selectable.length > 0 && selectable.every((r) => selected.has(r.id)));
  function toggleAll() {
    selected = allSelected ? new Set() : new Set(selectable.map((r) => r.id));
  }
  function toggle(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selected = next;
  }

  function denied(r: AppRow): string | null {
    if (!canUpdate) return m.updates_denied_perm();
    if (r.stack && !canDeploy) return m.updates_denied_stack({ stack: r.stack });
    return null;
  }

  async function updateOne(r: AppRow) {
    started = { ...started, [r.id]: r };
    const since = Date.now();
    const found = operations.find(opRef.container(r.id), 'image.update', since);
    api.containers.doUpdate(r.id).then(
      (res) => {
        if (!res.updated) toast.info(m.updates_already_current({ name: r.name }));
      },
      (e) => toast.error(m.updates_one_failed({ name: r.name }), e instanceof ApiError ? e.message : undefined)
    );
    await found;
  }

  async function updateSelected() {
    const list = rows.filter((r) => selected.has(r.id));
    if (!list.length) return;
    bulkBusy = true;
    try {
      const res = await api.containers.updateBulk(list.map((r) => r.id));
      const next = { ...started };
      for (const r of list) next[r.id] = r;
      started = next;
      operations.track(res.operations);
      selected = new Set();
      toast.info(m.updates_queued({ count: res.operations.length }), m.updates_queued_body());
      if (res.skipped?.length) {
        const skipped = new Set(res.skipped.map((s) => s.container_id));
        toast.info(m.updates_swarm_skipped({ names: list.filter((r) => skipped.has(r.id)).map((r) => r.name).join(', ') }));
      }
    } catch (e) {
      toast.error(m.updates_queue_failed(), e instanceof ApiError ? e.message : undefined);
    } finally {
      bulkBusy = false;
    }
  }

  const stats = $derived<Stat[]>([
    { label: m.updates_stat_app_images(), value: apps.length, note: apps.length ? m.updates_stat_newer() : m.updates_stat_all_up_to_date(), tone: apps.length ? 'warn' : '' },
    { label: m.updates_stat_system(), value: system.length, note: system.length ? m.updates_stat_components() : m.updates_stat_all_current() },
    { label: m.updates_stat_last_check(), value: fmtAgo(Object.values(checks).map((c) => c.checked_at).sort().pop()), note: m.updates_stat_last_check_note() },
    { label: m.updates_stat_week(), value: recentOps.filter((o) => Date.now() - Date.parse(o.finished_at ?? o.created_at) < 7 * 86400_000).length }
  ]);

  function kindLabel(kind: string): string {
    if (kind === 'major') return m.updates_kind_major();
    if (kind === 'minor') return m.updates_kind_minor();
    if (kind === 'patch') return m.updates_kind_patch();
    return kind;
  }

  const APP_COLS = '22px minmax(180px, 1.3fr) minmax(200px, 1.6fr) minmax(150px, 1fr) 110px 190px';
  const SYS_COLS = 'minmax(180px, 1.3fr) minmax(200px, 1.4fr) 80px minmax(160px, 1fr) 100px';
  const REC_COLS = 'minmax(180px, 1.3fr) minmax(200px, 1.4fr) 140px';
  const SWARM_COLS = 'minmax(180px, 1.3fr) minmax(200px, 1.6fr) minmax(200px, 1.4fr)';
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>{m.updates_title()}</h1>
    <span class="dm-sub">{m.updates_sub_apps({ count: apps.length })} · {m.updates_sub_system({ count: system.length })}</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if canUpdate}
      <button type="button" class="dm-btn dm-btn-primary" disabled={!selected.size || bulkBusy} onclick={updateSelected}>
        {m.updates_update_selected()}{selected.size ? ` (${selected.size})` : ''}
      </button>
    {/if}
  </div>

  <StatBar items={stats} />

  {#if system.length}
    <div>
      <h2 class="dm-h2">{m.updates_system()}</h2>
      <div class="dm-tbl" role="table" aria-label={m.updates_system_label()}>
        {#each system as s (s.name)}
          {@const kind = semverBump(s.from.replace(/^v/, ''), s.to.replace(/^v/, ''))}
          <div class="dm-tr" role="row" style="grid-template-columns: {SYS_COLS}">
            <span role="cell" class="dm-nm"><span class="dm-t">{s.name}</span><small>{s.note}</small></span>
            <span role="cell" class="dm-mono up-ver" title="{s.from} → {s.to}">{s.from} → <b>{s.to}</b></span>
            <span role="cell">{#if kind}<StatusChip tone={kind === 'major' ? 'warn' : 'off'} label={kindLabel(kind)} />{:else}<span class="dm-dim" title={m.updates_no_kind()}>—</span>{/if}</span>
            <span role="cell"></span>
            <span role="cell" class="up-act"><a class="dm-btn dm-btn-sm" href={s.href}>{m.common_open()}</a></span>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <div>
    <h2 class="dm-h2">{m.updates_apps()}</h2>
    {#if hosts.id !== 'local' && hosts.id !== 'all'}
      <p class="dm-sub up-note">{m.updates_agents_note()}</p>
    {/if}
    {#if loading}
      <div class="dm-tbl">
        {#each Array(4) as _, i (i)}
          <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="55%" height="0.9rem" /></div>
        {/each}
      </div>
    {:else if rows.length === 0}
      <EmptyState title={m.updates_empty_title()} description={m.updates_empty_desc()} />
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.updates_apps_label()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {APP_COLS}">
          <span role="columnheader">
            {#if canUpdate}<input type="checkbox" aria-label={m.updates_select_all()} checked={allSelected} onchange={toggleAll} />{/if}
          </span>
          <span role="columnheader">{m.common_container()}</span>
          <span role="columnheader">{m.common_image()}</span>
          <span role="columnheader">{m.updates_col_digest()}</span>
          <span role="columnheader">{m.updates_col_checked()}</span>
          <span role="columnheader"></span>
        </div>
        {#each rows as r (r.id)}
          {@const op = opFor(r.id)}
          {@const why = denied(r)}
          <div class="dm-tr" role="row" class:dm-sel={selected.has(r.id)} style="grid-template-columns: {APP_COLS}">
            <span role="cell">
              {#if canUpdate && (!op || !isActive(op))}
                <input type="checkbox" aria-label={m.updates_select_named({ name: r.name })} checked={selected.has(r.id)} disabled={!!why} onchange={() => toggle(r.id)} />
              {/if}
            </span>
            <span role="cell" class="dm-nm">
              <a href="/containers/{encodeURIComponent(r.id)}">{r.name}</a>
              <small>{r.stack ? m.updates_in_stack({ stack: r.stack }) : m.updates_standalone()}</small>
            </span>
            <span role="cell" class="dm-mono up-ver" title={r.image}>{r.image}</span>
            <span role="cell" class="dm-mono dm-dim" title={r.check ? `${r.check.local_digest ?? ''} → ${r.check.remote_digest ?? ''}` : ''}>
              {shortDigest(r.check?.local_digest)} → <b class="up-new">{shortDigest(r.check?.remote_digest)}</b>
            </span>
            <span role="cell" class="dm-num dm-dim">{fmtAgo(r.check?.checked_at)}</span>
            <span role="cell" class="up-act">
              {#if op}
                <OpProgress {op} />
                <CancelOpButton {op} />
              {:else if canUpdate}
                <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/containers/{encodeURIComponent(r.id)}?tab=updates">{m.updates_notes()}</a>
                <button type="button" class="dm-btn dm-btn-sm" disabled={!!why} title={why ?? m.updates_update_hint({ name: r.name })} onclick={() => updateOne(r)}>{m.updates_update()}</button>
              {:else}
                <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/containers/{encodeURIComponent(r.id)}?tab=updates">{m.updates_notes()}</a>
              {/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="dm-sub up-note">{m.updates_footer_note()} <InfoTip text={m.updates_footer_tip()} /></p>
    {/if}
  </div>

  {#if swarmTasks.length}
    <div>
      <h2 class="dm-h2">{m.updates_swarm_title()}</h2>
      <div class="dm-tbl" role="table" aria-label={m.updates_swarm_label()}>
        {#each swarmTasks as s (s.id)}
          <div class="dm-tr" role="row" style="grid-template-columns: {SWARM_COLS}">
            <span role="cell" class="dm-nm"><a href="/containers/{encodeURIComponent(s.id)}">{s.name}</a></span>
            <span role="cell" class="dm-mono up-ver dm-trunc" title={s.image}>{s.image}</span>
            <span role="cell" class="dm-dim">{m.updates_swarm_reason({ service: s.service })}</span>
          </div>
        {/each}
      </div>
      <p class="dm-sub up-note">{m.updates_swarm_note()}</p>
    </div>
  {/if}

  {#if recentOps.length}
    <div>
      <h2 class="dm-h2">{m.updates_recent()}</h2>
      <div class="dm-tbl" role="table" aria-label={m.updates_recent()}>
        {#each recentOps as o (o.id)}
          {@const pull = o.steps.find((s) => s.key === 'pull')}
          <div class="dm-tr dm-click" role="row" style="grid-template-columns: {REC_COLS}" onclick={() => operations.open(o.id)}
            onkeydown={(e) => { if (e.key === 'Enter') operations.open(o.id); }} tabindex="0">
            <span role="cell" class="dm-nm"><span class="dm-t">{o.target}</span><small>{o.stack ? m.updates_in_stack({ stack: o.stack }) : m.updates_standalone()}{o.actor_name ? ` · ${m.updates_by({ name: o.actor_name })}` : ''}</small></span>
            <span role="cell" class="dm-mono dm-dim dm-trunc">{m.updates_new_image({ detail: pull?.detail ?? '' })}</span>
            <span role="cell" class="dm-num dm-dim">{fmtAgo(o.finished_at)}</span>
          </div>
        {/each}
      </div>
    </div>
  {/if}
</section>

<style>
  .up-ver { font-size: 12.5px; }
  .up-new { color: var(--fg); font-weight: 600; }
  .up-act { display: flex; justify-content: flex-end; align-items: center; gap: 6px; overflow: visible; }
  .up-note { margin: 8px 0 0; }
</style>
