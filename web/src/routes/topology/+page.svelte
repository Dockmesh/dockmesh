<script lang="ts">
  // Topology: how containers, networks and the reverse proxy connect. Each
  // stack is a group with its networks on top and its containers below;
  // edges are network memberships. Reloads on Docker and stack events.
  import { api, ApiError, type Topology, type TopoNetwork, type TopoContainer } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, EmptyState, StatusChip, Switch, LiveIndicator } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import { Waypoints, Minus, Plus } from 'lucide-svelte';

  type Route = { id: number; host: string; upstream: string; enabled: boolean };

  let topo = $state<Topology | null>(null);
  let routes = $state<Route[]>([]);
  let loading = $state(true);
  let lastLoaded = $state<number | null>(null);
  let showSystem = $state(false);
  let showProxy = $state(false);
  let search = $state('');
  let zoom = $state(1);
  let selected = $state<string | null>(null);
  let connStatus = $state<ConnStatus>('connecting');
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  const GROUP_MIN_W = 230;
  const COL_W = 110;
  const NET_Y = 46;
  const CT_Y0 = 130;
  const ROW_H = 74;
  const GAP = 24;
  const MAX_ROW_W = 980;
  const HUES = ['var(--accent)', 'var(--violet)', 'var(--warn)', 'var(--ok)'];

  interface NetNode { id: string; x: number; y: number; net: TopoNetwork; color: string }
  interface CtNode { id: string; x: number; y: number; ct: TopoContainer }
  interface Group { key: string; label: string; sub: string; x: number; y: number; w: number; h: number; color: string; nets: NetNode[]; cts: CtNode[] }

  // A container is "behind the proxy" when an enabled route points at one
  // of its published host ports (Caddy runs with host networking).
  function upstreamPort(u: string): number | null {
    const m = u.match(/:(\d+)(?:\/.*)?$/);
    return m ? Number(m[1]) : null;
  }
  const proxiedPorts = $derived(
    new Map(routes.filter((r) => r.enabled).map((r) => [upstreamPort(r.upstream), r.host] as const).filter(([p]) => p != null))
  );
  function routesFor(c: TopoContainer): string[] {
    return (c.ports ?? []).map((p) => proxiedPorts.get(p.host_port)).filter((h): h is string => !!h);
  }

  const scene = $derived.by(() => {
    if (!topo) return { groups: [] as Group[], width: 0, height: 0, nets: new Map<string, NetNode>(), cts: new Map<string, CtNode>() };
    const nets = topo.networks.filter((n) => showSystem || !n.system);
    const netIds = new Set(nets.map((n) => n.id));
    const linked = new Set(topo.links.filter((l) => netIds.has(l.network_id)).map((l) => l.container_id));
    const cts = topo.containers.filter((c) => linked.has(c.id) || showSystem);

    const byKey = new Map<string, { label: string; nets: TopoNetwork[]; cts: TopoContainer[] }>();
    const ensure = (key: string, label: string) => {
      if (!byKey.has(key)) byKey.set(key, { label, nets: [], cts: [] });
      return byKey.get(key)!;
    };
    for (const n of nets) ensure(n.stack ? `s:${n.stack}` : n.system ? 'system' : 'shared', n.stack ?? (n.system ? 'System networks' : 'Shared networks')).nets.push(n);
    for (const c of cts) ensure(c.stack ? `s:${c.stack}` : 'standalone', c.stack ?? 'Standalone').cts.push(c);

    const order = [...byKey.entries()].sort(([a, ga], [b, gb]) => {
      const rank = (k: string) => (k.startsWith('s:') ? 0 : k === 'standalone' ? 1 : k === 'shared' ? 2 : 3);
      return rank(a) - rank(b) || ga.label.localeCompare(gb.label);
    });

    const groups: Group[] = [];
    const netMap = new Map<string, NetNode>();
    const ctMap = new Map<string, CtNode>();
    let x = 20;
    let y = 40;
    let rowH = 0;
    order.forEach(([key, g]) => {
      const cols = Math.max(2, g.nets.length);
      const w = Math.max(GROUP_MIN_W, 20 + cols * COL_W);
      const ctRows = Math.ceil(g.cts.length / cols);
      const h = (g.cts.length ? CT_Y0 + (ctRows - 1) * ROW_H + 60 : CT_Y0 - 10);
      if (x > 20 && x + w > MAX_ROW_W) {
        x = 20;
        y += rowH + 48;
        rowH = 0;
      }
      const color = key.startsWith('s:') ? HUES[groups.length % HUES.length] : 'var(--subtle)';
      const group: Group = {
        key,
        label: g.label,
        sub: `${g.cts.length} container${g.cts.length === 1 ? '' : 's'}`,
        x, y, w, h, color, nets: [], cts: []
      };
      const netStep = w / (g.nets.length + 1);
      g.nets.forEach((n, k) => {
        const node = { id: n.id, x: x + netStep * (k + 1), y: y + NET_Y, net: n, color };
        group.nets.push(node);
        netMap.set(n.id, node);
      });
      const ctStep = (w - 20) / cols;
      g.cts.forEach((c, k) => {
        const node = { id: c.id, x: x + 10 + ctStep * (k % cols) + ctStep / 2, y: y + CT_Y0 + Math.floor(k / cols) * ROW_H, ct: c };
        group.cts.push(node);
        ctMap.set(c.id, node);
      });
      groups.push(group);
      x += w + GAP;
      rowH = Math.max(rowH, h);
    });
    const width = Math.max(...groups.map((g) => g.x + g.w), 400) + 20;
    const height = y + rowH + 30;
    return { groups, width, height, nets: netMap, cts: ctMap };
  });

  const edges = $derived(
    (topo?.links ?? [])
      .map((l) => ({ n: scene.nets.get(l.network_id), c: scene.cts.get(l.container_id), l }))
      .filter((e): e is { n: NetNode; c: CtNode; l: (typeof e)['l'] } => !!e.n && !!e.c)
  );

  function matches(text: string): boolean {
    const q = search.trim().toLowerCase();
    return !q || text.toLowerCase().includes(q);
  }
  function ctMatches(c: TopoContainer): boolean {
    return matches(`${c.name} ${c.image} ${c.stack ?? ''}`);
  }
  function netMatches(n: TopoNetwork): boolean {
    return matches(`${n.name} ${n.stack ?? ''}`);
  }
  function groupMatches(g: Group): boolean {
    return matches(g.label) || g.cts.some((c) => ctMatches(c.ct)) || g.nets.some((n) => netMatches(n.net));
  }

  function ctTone(c: TopoContainer): 'ok' | 'warn' | 'bad' | 'off' {
    if (c.state === 'running') return 'ok';
    if (c.state === 'restarting' || c.state === 'paused') return 'warn';
    if (c.state === 'dead') return 'bad';
    return 'off';
  }
  const RING: Record<string, string> = { ok: 'var(--ok)', warn: 'var(--warn)', bad: 'var(--bad)', off: 'var(--off)' };
  function short(s: string, n: number): string {
    return s.length > n ? s.slice(0, n - 1) + '…' : s;
  }
  function initials(name: string): string {
    return name.replace(/^[^a-z0-9]+/i, '').slice(0, 2).toUpperCase();
  }

  const sel = $derived.by(() => {
    if (!selected || !topo) return null;
    const net = topo.networks.find((n) => n.id === selected);
    if (net) {
      const members = topo.links.filter((l) => l.network_id === net.id).map((l) => ({ l, c: topo!.containers.find((c) => c.id === l.container_id) }));
      return { kind: 'network' as const, net, members };
    }
    const ct = topo.containers.find((c) => c.id === selected);
    if (ct) {
      const memberships = topo.links.filter((l) => l.container_id === ct.id).map((l) => ({ l, n: topo!.networks.find((n) => n.id === l.network_id) }));
      return { kind: 'container' as const, ct, memberships };
    }
    return null;
  });

  const totals = $derived({
    groups: scene.groups.filter((g) => g.key.startsWith('s:')).length,
    containers: scene.cts.size,
    networks: scene.nets.size,
    proxied: [...scene.cts.values()].filter((c) => routesFor(c.ct).length > 0).length
  });

  async function load() {
    try {
      const [t, r] = await Promise.all([
        api.networks.topology(),
        allowed('proxy.view') ? api.proxy.listRoutes().catch(() => [] as Route[]) : Promise.resolve([] as Route[])
      ]);
      topo = t;
      routes = r;
      lastLoaded = Date.now();
    } catch (err) {
      toast.error('Failed to load topology', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  const stream = new EventStream({
    onMessage: (msg) => {
      if ((msg.source === 'docker' && (msg.type === 'container' || msg.type === 'network')) || msg.source === 'stacks') {
        if (reloadTimer) clearTimeout(reloadTimer);
        reloadTimer = setTimeout(load, 500);
      }
    },
    onStatus: (s) => (connStatus = s)
  });
  $effect(() => {
    load();
    stream.start();
    return () => {
      stream.stop();
      if (reloadTimer) clearTimeout(reloadTimer);
    };
  });

  function setZoom(z: number) {
    zoom = Math.min(2.5, Math.max(0.5, Math.round(z * 10) / 10));
  }
  function onNodeKey(e: KeyboardEvent, id: string) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      selected = selected === id ? null : id;
    }
  }
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Topology</h1>
    <span class="dm-sub">How containers, networks and the reverse proxy connect</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} paused={connStatus === 'closed'} />
  </div>

  <div class="dm-toolbar">
    <input type="search" class="dm-filter" style="width: 260px" placeholder="Search container, image, stack…" aria-label="Search topology" bind:value={search} />
    <Switch bind:checked={showProxy} label="Show reverse proxy" />
    <Switch bind:checked={showSystem} label="Show system networks" />
    <span class="dm-sp"></span>
    <div class="zoom" role="group" aria-label="Zoom">
      <button type="button" onclick={() => setZoom(zoom - 0.1)} aria-label="Zoom out"><Minus size={13} /></button>
      <button type="button" onclick={() => setZoom(1)} title="Reset zoom">{Math.round(zoom * 100)} %</button>
      <button type="button" onclick={() => setZoom(zoom + 0.1)} aria-label="Zoom in"><Plus size={13} /></button>
    </div>
  </div>

  {#if loading && !topo}
    <Skeleton width="100%" height="520px" />
  {:else if !topo || scene.groups.length === 0}
    <EmptyState icon={Waypoints} title="Nothing to show" description="No containers or user networks on this host yet." />
  {:else}
    <div class="topo">
      <div class="canvas">
        <svg
          viewBox="0 0 {scene.width} {scene.height}"
          style="width: {zoom * 100}%; min-width: {scene.width * zoom * 0.6}px"
          role="group"
          aria-label="Topology graph"
        >
          {#each edges as e (e.n.id + e.c.id)}
            {@const dim = !(ctMatches(e.c.ct) || netMatches(e.n.net))}
            <line x1={e.n.x} y1={e.n.y + 46} x2={e.c.x} y2={e.c.y - 20} class="edge" class:hl={selected === e.n.id || selected === e.c.id} opacity={dim ? 0.2 : 1} />
          {/each}

          {#each scene.groups as g (g.key)}
            <g opacity={groupMatches(g) ? 1 : 0.25}>
              <rect x={g.x} y={g.y} width={g.w} height={g.h} rx="10" fill="none" stroke={g.color} stroke-opacity="0.5" stroke-dasharray="4 4" />
              <text x={g.x + 12} y={g.y - 10} fill={g.color} class="glabel">{short(g.label, 26)} <tspan class="gsub">· {g.sub}</tspan></text>

              {#each g.nets as n (n.id)}
                <g
                  class="tn"
                  class:sel={selected === n.id}
                  role="button"
                  tabindex="0"
                  aria-label="Network {n.net.name}"
                  aria-pressed={selected === n.id}
                  opacity={netMatches(n.net) ? 1 : 0.3}
                  onclick={() => (selected = selected === n.id ? null : n.id)}
                  onkeydown={(e) => onNodeKey(e, n.id)}
                >
                  <circle cx={n.x} cy={n.y} r="22" fill="var(--bg)" stroke={n.color} stroke-width="2" />
                  <text x={n.x} y={n.y + 4} text-anchor="middle" class="nlabel" fill={n.color}>NET</text>
                  <text x={n.x} y={n.y + 38} text-anchor="middle" class="nname">{short(n.net.name, 18)}</text>
                </g>
              {/each}

              {#each g.cts as c (c.id)}
                {@const tone = ctTone(c.ct)}
                {@const pub = (c.ct.ports ?? []).find((p) => p.host_port)}
                {@const proxied = routesFor(c.ct).length > 0}
                <g
                  class="tn"
                  class:sel={selected === c.id}
                  role="button"
                  tabindex="0"
                  aria-label="Container {c.ct.name}, {c.ct.state}"
                  aria-pressed={selected === c.id}
                  opacity={ctMatches(c.ct) ? 1 : 0.3}
                  onclick={() => (selected = selected === c.id ? null : c.id)}
                  onkeydown={(e) => onNodeKey(e, c.id)}
                >
                  {#if showProxy && proxied}
                    <circle cx={c.x} cy={c.y} r="25" fill="none" stroke="var(--accent)" stroke-dasharray="3 3" />
                  {/if}
                  <circle cx={c.x} cy={c.y} r="18" fill="var(--bg)" stroke={RING[tone]} stroke-width="2" />
                  <text x={c.x} y={c.y + 4} text-anchor="middle" class="cinit">{initials(c.ct.name)}</text>
                  <text x={c.x} y={c.y + 33} text-anchor="middle" class="cname">{short(c.ct.name, 16)}</text>
                  {#if pub}
                    <text x={c.x} y={c.y + 46} text-anchor="middle" class="cport">:{pub.host_port}</text>
                  {/if}
                  {#if proxied}
                    <text x={c.x + 16} y={c.y - 14} class="cproxy">⇄</text>
                  {/if}
                </g>
              {/each}
            </g>
          {/each}
        </svg>
      </div>

      <aside class="side" aria-label="Selection">
        {#if !sel}
          <p class="dm-sub">Select a node to see its details.</p>
          <dl class="dm-kv">
            <dt>Stacks</dt><dd>{totals.groups}</dd>
            <dt>Containers</dt><dd>{totals.containers}</dd>
            <dt>Networks</dt><dd>{totals.networks}</dd>
            <dt>Behind the proxy</dt><dd>{totals.proxied} container{totals.proxied === 1 ? '' : 's'}</dd>
          </dl>
        {:else if sel.kind === 'network'}
          <h2 class="dm-h2 dm-mono">{sel.net.name}</h2>
          <dl class="dm-kv">
            <dt>Driver</dt><dd>{sel.net.driver}</dd>
            <dt>Scope</dt><dd>{sel.net.scope}{sel.net.internal ? ' · internal' : ''}</dd>
            {#if sel.net.stack}<dt>Stack</dt><dd><a href="/stacks/{encodeURIComponent(sel.net.stack)}">{sel.net.stack}</a></dd>{/if}
            <dt>Containers</dt>
            <dd>
              {#each sel.members as m (m.l.container_id)}
                <div class="dm-trunc"><span>{m.c?.name ?? m.l.container_id.slice(0, 12)}</span>{#if m.l.ipv4}<span class="dm-mono dm-dim"> {m.l.ipv4}</span>{/if}</div>
              {:else}—{/each}
            </dd>
          </dl>
          <a class="dm-btn dm-btn-sm side-btn" href="/networks/{encodeURIComponent(sel.net.id)}">Open network</a>
        {:else}
          {@const domains = routesFor(sel.ct)}
          <h2 class="dm-h2 dm-mono">{sel.ct.name}</h2>
          <dl class="dm-kv">
            <dt>Status</dt><dd><StatusChip tone={ctTone(sel.ct)} label={sel.ct.state} /></dd>
            <dt>Image</dt><dd class="dm-mono">{sel.ct.image}</dd>
            <dt>Ports</dt>
            <dd class="dm-mono">{(sel.ct.ports ?? []).map((p) => (p.host_port ? `${p.host_port}:${p.container_port}` : `${p.container_port}`) + `/${p.protocol}`).join(', ') || '—'}</dd>
            <dt>Networks</dt>
            <dd>
              {#each sel.memberships as m (m.l.network_id)}
                <div class="dm-trunc">{m.n?.name ?? m.l.network_id.slice(0, 12)}{#if m.l.ipv4}<span class="dm-mono dm-dim"> {m.l.ipv4}</span>{/if}</div>
              {:else}—{/each}
            </dd>
            <dt>Domain</dt><dd>{domains.join(', ') || '—'}</dd>
            {#if sel.ct.stack}<dt>Stack</dt><dd><a href="/stacks/{encodeURIComponent(sel.ct.stack)}">{sel.ct.stack}</a></dd>{/if}
          </dl>
          <a class="dm-btn dm-btn-sm side-btn" href="/containers/{sel.ct.id}">Open container</a>
        {/if}
      </aside>
    </div>

    <div class="legend">
      <span><span class="dm-dot ok"></span> running</span>
      <span><span class="dm-dot warn"></span> restarting or paused</span>
      <span><span class="dm-dot bad"></span> dead</span>
      <span><span class="dm-dot"></span> stopped</span>
      <span class="acc">⇄ published through the reverse proxy</span>
    </div>
  {/if}
</section>

<style>
  .zoom { display: inline-flex; background: var(--field); border-radius: 9px; padding: 2px; }
  .zoom button {
    border: 0;
    background: transparent;
    color: var(--muted);
    font: inherit;
    font-size: var(--fs-sm);
    min-height: 28px;
    min-width: 32px;
    padding: 0 8px;
    border-radius: 7px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .zoom button:hover { background: var(--surface); color: var(--fg); }
  .topo { display: grid; grid-template-columns: minmax(0, 1fr) 300px; gap: 20px; align-items: start; }
  .canvas {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--band);
    overflow: auto;
    height: min(620px, calc(100vh - 300px));
    min-height: 360px;
  }
  svg { display: block; height: auto; }
  .edge { stroke: var(--border-strong); stroke-width: 1.4; }
  .edge.hl { stroke: var(--accent); stroke-width: 2; }
  .glabel { font: 600 12.5px var(--font-mono); }
  .gsub { fill: var(--subtle); font-weight: 400; }
  .nlabel { font: 600 10px var(--font-mono); }
  .nname { font: 11px var(--font-mono); fill: var(--muted); }
  .cinit { font: 700 10px var(--font-mono); fill: var(--fg); }
  .cname { font: 10.5px var(--font-mono); fill: var(--fg); }
  .cport { font: 10px var(--font-mono); fill: var(--accent); }
  .cproxy { font-size: 11px; fill: var(--accent); }
  .tn { cursor: pointer; outline: none; }
  .tn:hover circle,
  .tn.sel circle,
  .tn:focus-visible circle { stroke-width: 3; filter: drop-shadow(0 0 6px var(--accent)); }
  .side { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 12px; min-width: 0; }
  .side p { margin: 0; }
  .side-btn { align-self: flex-start; }
  .legend { display: flex; gap: 16px; flex-wrap: wrap; font-size: 12px; color: var(--muted); }
  .legend .acc { color: var(--accent); }
  @media (max-width: 1100px) {
    .topo { grid-template-columns: minmax(0, 1fr); }
  }
</style>
