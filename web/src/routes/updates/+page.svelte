<script lang="ts">
  import { api, apiRequest, ApiError, isFanOut, type Operation, type UpdateStatus } from '$lib/api';
  import { StatusChip, StatBar, EmptyState, Skeleton, LiveIndicator } from '$lib/components/ui';
  import type { Stat } from '$lib/components/ui/StatBar.svelte';
  import OpProgress from '$lib/components/operations/OpProgress.svelte';
  import { operations, isActive } from '$lib/stores/operations.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { fmtVersion } from '$lib/version';
  import { autoRefresh } from '$lib/autorefresh';

  interface Container {
    Id: string;
    Names: string[];
    Image: string;
    State: string;
    Labels?: Record<string, string>;
    image_update_available?: boolean;
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
  interface SystemRow {
    name: string;
    note: string;
    from: string;
    to: string;
    href: string;
  }
  interface ProxyVersion {
    current: string;
    default: string;
    managed: boolean;
  }

  const canUpdate = $derived(allowed('containers.update'));
  const canDeploy = $derived(allowed('stacks.deploy'));

  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let apps = $state<AppRow[]>([]);
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
  function bump(from: string, to: string): 'major' | 'minor' | 'patch' {
    const a = from.replace(/^v/, '').split('.'), b = to.replace(/^v/, '').split('.');
    if (a[0] !== b[0]) return 'major';
    if (a[1] !== b[1]) return 'minor';
    return 'patch';
  }
  function ago(iso?: string): string {
    if (!iso) return '—';
    const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
    if (s < 90) return 'just now';
    if (s < 3600) return `${Math.round(s / 60)} min ago`;
    if (s < 86400) return `${Math.round(s / 3600)} h ago`;
    return `${Math.round(s / 86400)} d ago`;
  }

  async function loadSystem(serverVersion: string) {
    const out: SystemRow[] = [];
    const [st, pv, agents] = await Promise.all([
      api.system.updateStatus().catch(() => null as UpdateStatus | null),
      allowed('proxy.view') ? apiRequest<ProxyVersion>('/proxy/version').catch(() => null) : Promise.resolve(null),
      allowed('hosts.view') ? api.agents.list().catch(() => []) : Promise.resolve([])
    ]);
    if (st?.update_available && !st.is_dev_build) {
      out.push({ name: 'Dockmesh', note: 'Server; agents follow their upgrade policy', from: fmtVersion(st.current_version), to: fmtVersion(st.latest_version), href: '/settings/updates' });
    }
    if (pv?.managed && pv.current && pv.default && newer(pv.default, pv.current)) {
      out.push({ name: 'Caddy (reverse proxy)', note: 'Version tested with this Dockmesh release', from: pv.current, to: pv.default, href: '/proxy?tab=caddy' });
    }
    for (const a of agents as Array<{ id: string; name: string; status: string; version?: string }>) {
      if (a.status !== 'online' || !a.version || !serverVersion || a.version === serverVersion) continue;
      out.push({ name: `Agent ${a.name}`, note: 'Follows the agent upgrade policy', from: fmtVersion(a.version), to: fmtVersion(serverVersion), href: `/hosts/${encodeURIComponent(a.id)}` });
    }
    system = out;
  }

  async function load() {
    refreshing = true;
    try {
      const info = await api.system.info().catch(() => null);
      const [list, imgs, ops] = await Promise.all([
        allowed('containers.view') ? api.containers.list(true, 'local').catch(() => []) : Promise.resolve([]),
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
  $effect(() => autoRefresh(load, 60_000));

  // Reload once tracked updates end, so finished rows leave the list.
  let wasActive = 0;
  $effect(() => {
    const n = Object.keys(started).filter((id) => {
      const op = operations.latest(`container:${id}`, 'image.update');
      return op && isActive(op);
    }).length;
    if (n < wasActive) void load();
    wasActive = n;
  });

  const rows = $derived.by<AppRow[]>(() => {
    const ids = new Set(apps.map((a) => a.id));
    return [...apps, ...Object.values(started).filter((r) => !ids.has(r.id))];
  });
  const opFor = (id: string) => (started[id] ? operations.latest(`container:${id}`, 'image.update') : undefined);
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
    if (!canUpdate) return 'Needs the containers.update permission';
    if (r.stack && !canDeploy) return `Updating a container of stack ${r.stack} rebuilds it from compose.yaml and needs stacks.deploy`;
    return null;
  }

  async function updateOne(r: AppRow) {
    started = { ...started, [r.id]: r };
    const since = Date.now();
    const found = operations.find(`container:${r.id}`, 'image.update', since);
    api.containers.doUpdate(r.id).then(
      (res) => {
        if (!res.updated) toast.info(`${r.name} is already up to date`);
      },
      (e) => toast.error(`Update of ${r.name} failed`, e instanceof ApiError ? e.message : undefined)
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
      toast.info(`${list.length} update${list.length === 1 ? '' : 's'} queued`, 'They run one after another on the server; you can leave this page.');
    } catch (e) {
      toast.error('Could not queue the updates', e instanceof ApiError ? e.message : undefined);
    } finally {
      bulkBusy = false;
    }
  }

  const stats = $derived<Stat[]>([
    { label: 'App images', value: apps.length, note: apps.length ? 'newer image available' : 'all up to date', tone: apps.length ? 'warn' : '' },
    { label: 'System', value: system.length, note: system.length ? 'components to update' : 'all current' },
    { label: 'Last check', value: ago(Object.values(checks).map((c) => c.checked_at).sort().pop()), note: 'registries are checked every 30 min' },
    { label: 'Updated · this week', value: recentOps.filter((o) => Date.now() - Date.parse(o.finished_at ?? o.created_at) < 7 * 86400_000).length }
  ]);

  const APP_COLS = '22px minmax(180px, 1.3fr) minmax(200px, 1.6fr) minmax(150px, 1fr) 110px 190px';
  const SYS_COLS = 'minmax(180px, 1.3fr) minmax(200px, 1.4fr) 80px minmax(160px, 1fr) 100px';
  const REC_COLS = 'minmax(180px, 1.3fr) minmax(200px, 1.4fr) 140px';
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Updates</h1>
    <span class="dm-sub">{apps.length} app image{apps.length === 1 ? '' : 's'} · {system.length} system component{system.length === 1 ? '' : 's'}</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if canUpdate}
      <button type="button" class="dm-btn dm-btn-primary" disabled={!selected.size || bulkBusy} onclick={updateSelected}>
        Update selected{selected.size ? ` (${selected.size})` : ''}
      </button>
    {/if}
  </div>

  <StatBar items={stats} />

  {#if system.length}
    <div>
      <h2 class="dm-h2">System</h2>
      <div class="dm-tbl" role="table" aria-label="System updates">
        {#each system as s (s.name)}
          {@const kind = bump(s.from, s.to)}
          <div class="dm-tr" role="row" style="grid-template-columns: {SYS_COLS}">
            <span role="cell" class="dm-nm"><span class="dm-t">{s.name}</span><small>{s.note}</small></span>
            <span role="cell" class="dm-mono up-ver">{s.from} → <b>{s.to}</b></span>
            <span role="cell"><StatusChip tone={kind === 'major' ? 'warn' : 'off'} label={kind} /></span>
            <span role="cell"></span>
            <span role="cell" class="up-act"><a class="dm-btn dm-btn-sm" href={s.href}>Open</a></span>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <div>
    <h2 class="dm-h2">Apps</h2>
    {#if hosts.id !== 'local' && hosts.id !== 'all'}
      <p class="dm-sub up-note">Image updates run on the server host only; containers on agents are not listed here.</p>
    {/if}
    {#if loading}
      <div class="dm-tbl">
        {#each Array(4) as _, i (i)}
          <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="55%" height="0.9rem" /></div>
        {/each}
      </div>
    {:else if rows.length === 0}
      <EmptyState title="All app images are up to date" description="The server checks the registries of running containers every 30 minutes." />
    {:else}
      <div class="dm-tbl" role="table" aria-label="App image updates">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {APP_COLS}">
          <span role="columnheader">
            {#if canUpdate}<input type="checkbox" aria-label="Select all" checked={allSelected} onchange={toggleAll} />{/if}
          </span>
          <span role="columnheader">Container</span>
          <span role="columnheader">Image</span>
          <span role="columnheader">Digest</span>
          <span role="columnheader">Checked</span>
          <span role="columnheader"></span>
        </div>
        {#each rows as r (r.id)}
          {@const op = opFor(r.id)}
          {@const why = denied(r)}
          <div class="dm-tr" role="row" class:dm-sel={selected.has(r.id)} style="grid-template-columns: {APP_COLS}">
            <span role="cell">
              {#if canUpdate && (!op || !isActive(op))}
                <input type="checkbox" aria-label="Select {r.name}" checked={selected.has(r.id)} disabled={!!why} onchange={() => toggle(r.id)} />
              {/if}
            </span>
            <span role="cell" class="dm-nm">
              <a href="/containers/{encodeURIComponent(r.id)}">{r.name}</a>
              <small>{r.stack ? `stack ${r.stack}` : 'standalone'}</small>
            </span>
            <span role="cell" class="dm-mono up-ver" title={r.image}>{r.image}</span>
            <span role="cell" class="dm-num dm-dim" title={r.check ? `${r.check.local_digest ?? ''} → ${r.check.remote_digest ?? ''}` : ''}>
              {shortDigest(r.check?.local_digest)} → <b class="up-new">{shortDigest(r.check?.remote_digest)}</b>
            </span>
            <span role="cell" class="dm-num dm-dim">{ago(r.check?.checked_at)}</span>
            <span role="cell" class="up-act">
              {#if op}
                <OpProgress {op} />
              {:else if canUpdate}
                <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/containers/{encodeURIComponent(r.id)}?tab=updates">Notes</a>
                <button type="button" class="dm-btn dm-btn-sm" disabled={!!why} title={why ?? `Pull the newer image and replace ${r.name}`} onclick={() => updateOne(r)}>Update</button>
              {:else}
                <a class="dm-btn dm-btn-ghost dm-btn-sm" href="/containers/{encodeURIComponent(r.id)}?tab=updates">Notes</a>
              {/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="dm-sub up-note">An update pulls the image, keeps the current one as rollback target and replaces the container once the new one runs. Stack containers are rebuilt from the stack's compose.yaml.</p>
    {/if}
  </div>

  {#if recentOps.length}
    <div>
      <h2 class="dm-h2">Recently updated</h2>
      <div class="dm-tbl" role="table" aria-label="Recently updated">
        {#each recentOps as o (o.id)}
          {@const pull = o.steps.find((s) => s.key === 'pull')}
          <div class="dm-tr dm-click" role="row" style="grid-template-columns: {REC_COLS}" onclick={() => operations.open(o.id)}
            onkeydown={(e) => { if (e.key === 'Enter') operations.open(o.id); }} tabindex="0">
            <span role="cell" class="dm-nm"><span class="dm-t">{o.target}</span><small>{o.stack ? `stack ${o.stack}` : 'standalone'}{o.actor_name ? ` · by ${o.actor_name}` : ''}</small></span>
            <span role="cell" class="dm-mono dm-dim dm-trunc">new image {pull?.detail ?? ''}</span>
            <span role="cell" class="dm-num dm-dim">{ago(o.finished_at)}</span>
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
