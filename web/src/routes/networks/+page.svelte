<script lang="ts">
  import { onDestroy, onMount, untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import {
    api,
    ApiError,
    type Topology,
    type TopoNetwork,
    type TopoContainer
  } from '$lib/api';
  import { Card, Button, Skeleton, EmptyState, Badge, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import {
    Network as NetworkIcon,
    RefreshCw,
    Eye,
    EyeOff,
    Search,
    ZoomIn,
    ZoomOut,
    Maximize2
  } from 'lucide-svelte';

  // ---------- data ----------
  let topo = $state<Topology | null>(null);
  let loading = $state(true);
  let showSystem = $state(false);
  let selected = $state<{ kind: 'network' | 'container'; id: string } | null>(null);
  let hovered = $state<{ id: string; clientX: number; clientY: number } | null>(null);
  let search = $state('');
  let live = $state(false);
  let ws: WebSocket | null = null;
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  // ---------- viewport ----------
  // World coordinate space is fixed at WORLD_W x WORLD_H. The transform
  // (panX, panY, zoom) maps it into the viewBox of the SVG. Pan + zoom is
  // implemented entirely on the transform, the layout doesn't know about it.
  const WORLD_W = 1600;
  const WORLD_H = 1100;
  const CENTER_X = WORLD_W / 2;
  const CENTER_Y = WORLD_H / 2;

  let panX = $state(0);
  let panY = $state(0);
  let zoom = $state(1);
  let panning = false;
  let panStart = { x: 0, y: 0, panX: 0, panY: 0 };

  // ---------- simulation data (plain JS, not $state) ----------
  type SimNode = {
    id: string;
    kind: 'network' | 'container';
    label: string;
    x: number;
    y: number;
    vx: number;
    vy: number;
    fixed: boolean;
    radius: number;
    data: TopoNetwork | TopoContainer;
  };
  type SimLink = { source: string; target: string };
  type StackBox = {
    name: string;
    x: number;
    y: number;
    w: number;
    h: number;
    color: string;
  };

  let frame = $state(0);
  let simNodes: SimNode[] = [];
  let simLinks: SimLink[] = [];
  let nodeMap = new Map<string, SimNode>();
  let raf: number | null = null;
  let ticks = 0;

  // Snapshots derived per frame so the SVG re-renders.
  type Edge = { sid: string; tid: string; x1: number; y1: number; x2: number; y2: number };
  const edges = $derived.by<Edge[]>(() => {
    void frame;
    const out: Edge[] = [];
    for (const l of simLinks) {
      const s = nodeMap.get(l.source);
      const t = nodeMap.get(l.target);
      if (!s || !t) continue;
      out.push({ sid: l.source, tid: l.target, x1: s.x, y1: s.y, x2: t.x, y2: t.y });
    }
    return out;
  });

  const renderedNodes = $derived.by(() => {
    void frame;
    return simNodes.map((n) => ({ ...n }));
  });

  // Stack groups: deterministic bounding box around all nodes that share a
  // compose project label. Updated every frame so they shrink/expand as
  // members move under the simulation.
  const stackBoxes = $derived.by<StackBox[]>(() => {
    void frame;
    const groups = new Map<string, SimNode[]>();
    for (const n of simNodes) {
      const stack = (n.data as any).stack as string | undefined;
      if (!stack) continue;
      let g = groups.get(stack);
      if (!g) {
        g = [];
        groups.set(stack, g);
      }
      g.push(n);
    }
    const out: StackBox[] = [];
    const PAD = 32;
    let i = 0;
    for (const [name, members] of groups) {
      if (members.length < 2) continue;
      let minX = Infinity,
        minY = Infinity,
        maxX = -Infinity,
        maxY = -Infinity;
      for (const m of members) {
        if (m.x - m.radius < minX) minX = m.x - m.radius;
        if (m.y - m.radius < minY) minY = m.y - m.radius;
        if (m.x + m.radius > maxX) maxX = m.x + m.radius;
        if (m.y + m.radius > maxY) maxY = m.y + m.radius;
      }
      out.push({
        name,
        x: minX - PAD,
        y: minY - PAD - 8,
        w: maxX - minX + PAD * 2,
        h: maxY - minY + PAD * 2 + 8,
        color: stackColor(i++)
      });
    }
    return out;
  });

  function stackColor(i: number): string {
    const palette = ['#06b6d4', '#a855f7', '#f59e0b', '#ec4899', '#10b981', '#3b82f6', '#f97316'];
    return palette[i % palette.length];
  }

  // ---------- search filter ----------
  const searchMatches = $derived.by(() => {
    const q = search.trim().toLowerCase();
    if (!q) return null;
    const hits = new Set<string>();
    for (const n of simNodes) {
      if (n.label.toLowerCase().includes(q)) hits.add(n.id);
      else if (n.kind === 'container' && (n.data as TopoContainer).image.toLowerCase().includes(q)) hits.add(n.id);
    }
    // Expand by direct neighbours so context is preserved.
    for (const l of simLinks) {
      if (hits.has(l.source) || hits.has(l.target)) {
        hits.add(l.source);
        hits.add(l.target);
      }
    }
    return hits;
  });

  function isDimmed(id: string): boolean {
    if (searchMatches && !searchMatches.has(id)) return true;
    if (selected) {
      if (selected.id === id) return false;
      const touched = simLinks.some(
        (l) =>
          (l.source === selected!.id && (l.target === id || l.source === id)) ||
          (l.target === selected!.id && (l.source === id || l.target === id))
      );
      if (!touched) return true;
    }
    return false;
  }

  // ---------- load + simulate ----------
  async function load(preservePositions = false) {
    loading = true;
    try {
      const next = await api.networks.topology();
      const oldPositions = preservePositions ? snapshotPositions() : null;
      topo = next;
      buildSimulation(oldPositions);
    } catch (err) {
      toast.error('Failed to load topology', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  function snapshotPositions(): Map<string, { x: number; y: number; fixed: boolean }> {
    const out = new Map<string, { x: number; y: number; fixed: boolean }>();
    for (const n of simNodes) out.set(n.id, { x: n.x, y: n.y, fixed: n.fixed });
    return out;
  }

  function buildSimulation(prev?: Map<string, { x: number; y: number; fixed: boolean }> | null) {
    if (!topo) return;
    const nets = showSystem ? topo.networks : topo.networks.filter((n) => !n.system);
    const netIds = new Set(nets.map((n) => n.id));

    const usedContainerIds = new Set<string>();
    for (const l of topo.links) {
      if (netIds.has(l.network_id)) usedContainerIds.add(l.container_id);
    }
    const conts = topo.containers.filter((c) => usedContainerIds.has(c.id));

    simNodes = [];
    simLinks = [];
    nodeMap.clear();

    const radius = Math.min(WORLD_W, WORLD_H) * 0.32;
    nets.forEach((n, i) => {
      const angle = (i / Math.max(1, nets.length)) * Math.PI * 2;
      const seedX = CENTER_X + Math.cos(angle) * radius;
      const seedY = CENTER_Y + Math.sin(angle) * radius;
      const old = prev?.get(n.id);
      const node: SimNode = {
        id: n.id,
        kind: 'network',
        label: n.name,
        x: old ? old.x : seedX,
        y: old ? old.y : seedY,
        vx: 0,
        vy: 0,
        fixed: old ? old.fixed : false,
        radius: 28,
        data: n
      };
      simNodes.push(node);
      nodeMap.set(n.id, node);
    });

    conts.forEach((c) => {
      const firstNet = topo!.links.find((l) => l.container_id === c.id && netIds.has(l.network_id));
      const anchor = firstNet ? nodeMap.get(firstNet.network_id) : null;
      const ax = anchor ? anchor.x : CENTER_X;
      const ay = anchor ? anchor.y : CENTER_Y;
      const old = prev?.get(c.id);
      const node: SimNode = {
        id: c.id,
        kind: 'container',
        label: c.name,
        x: old ? old.x : ax + (Math.random() - 0.5) * 100,
        y: old ? old.y : ay + (Math.random() - 0.5) * 100,
        vx: 0,
        vy: 0,
        fixed: old ? old.fixed : false,
        radius: 18,
        data: c
      };
      simNodes.push(node);
      nodeMap.set(c.id, node);
    });

    for (const l of topo.links) {
      if (netIds.has(l.network_id) && nodeMap.has(l.container_id)) {
        simLinks.push({ source: l.network_id, target: l.container_id });
      }
    }

    frame++;
    ticks = 0;
    if (raf) cancelAnimationFrame(raf);
    raf = requestAnimationFrame(tick);
  }

  // ---------- physics ----------
  const REPEL = 4500;
  const LINK_DIST = 160;
  const LINK_K = 0.04;
  const GRAVITY = 0.008;
  const DAMPING = 0.82;
  const MAX_TICKS = 800;

  function tick() {
    if (simNodes.length === 0) return;
    const n = simNodes.length;

    // Repulsion
    for (let i = 0; i < n; i++) {
      const a = simNodes[i];
      for (let j = i + 1; j < n; j++) {
        const b = simNodes[j];
        const dx = b.x - a.x;
        const dy = b.y - a.y;
        const d2 = dx * dx + dy * dy + 0.01;
        const d = Math.sqrt(d2);
        const f = REPEL / d2;
        const fx = (dx / d) * f;
        const fy = (dy / d) * f;
        a.vx -= fx;
        a.vy -= fy;
        b.vx += fx;
        b.vy += fy;
      }
    }

    // Spring links
    for (const l of simLinks) {
      const s = nodeMap.get(l.source);
      const t = nodeMap.get(l.target);
      if (!s || !t) continue;
      const dx = t.x - s.x;
      const dy = t.y - s.y;
      const d = Math.sqrt(dx * dx + dy * dy) + 0.01;
      const diff = d - LINK_DIST;
      const fx = (dx / d) * diff * LINK_K;
      const fy = (dy / d) * diff * LINK_K;
      s.vx += fx;
      s.vy += fy;
      t.vx -= fx;
      t.vy -= fy;
    }

    // Gravity + integrate
    for (const node of simNodes) {
      node.vx += (CENTER_X - node.x) * GRAVITY;
      node.vy += (CENTER_Y - node.y) * GRAVITY;
      node.vx *= DAMPING;
      node.vy *= DAMPING;
      if (!node.fixed) {
        node.x += node.vx;
        node.y += node.vy;
      }
      node.x = Math.max(60, Math.min(WORLD_W - 60, node.x));
      node.y = Math.max(60, Math.min(WORLD_H - 60, node.y));
    }

    // Collision resolution — push overlapping nodes apart so circles never
    // touch. Ten percent of one frame's worth of position is plenty.
    for (let i = 0; i < n; i++) {
      const a = simNodes[i];
      for (let j = i + 1; j < n; j++) {
        const b = simNodes[j];
        const dx = b.x - a.x;
        const dy = b.y - a.y;
        const d = Math.sqrt(dx * dx + dy * dy) + 0.01;
        const minD = a.radius + b.radius + 14;
        if (d < minD) {
          const overlap = (minD - d) / d;
          const ox = dx * overlap * 0.5;
          const oy = dy * overlap * 0.5;
          if (!a.fixed) {
            a.x -= ox;
            a.y -= oy;
          }
          if (!b.fixed) {
            b.x += ox;
            b.y += oy;
          }
        }
      }
    }

    frame++;
    ticks++;
    if (ticks < MAX_TICKS) {
      raf = requestAnimationFrame(tick);
    } else {
      raf = null;
    }
  }

  function reheat() {
    ticks = 0;
    if (!raf) raf = requestAnimationFrame(tick);
  }

  // ---------- live updates ----------
  async function connectLive() {
    if (ws) return;
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      ws = new WebSocket(`${proto}//${location.host}/api/v1/ws/events?ticket=${ticket}`);
      ws.onopen = () => {
        live = true;
      };
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data);
          if (
            (msg.source === 'docker' && (msg.type === 'container' || msg.type === 'network')) ||
            msg.source === 'stacks'
          ) {
            scheduleReload();
          }
        } catch {
          /* ignore */
        }
      };
      ws.onclose = () => {
        live = false;
        ws = null;
      };
      ws.onerror = () => {
        live = false;
      };
    } catch {
      live = false;
    }
  }

  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(() => load(true), 500);
  }

  function disconnectLive() {
    if (ws) {
      ws.close();
      ws = null;
    }
    if (reloadTimer) clearTimeout(reloadTimer);
    live = false;
  }

  onDestroy(() => {
    if (raf) cancelAnimationFrame(raf);
    disconnectLive();
  });

  // ---------- viewport interaction ----------
  let svgEl: SVGSVGElement | null = $state(null);

  function svgPointWorld(clientX: number, clientY: number): { x: number; y: number } {
    if (!svgEl) return { x: 0, y: 0 };
    const rect = svgEl.getBoundingClientRect();
    // Convert client → SVG viewport
    const vx = ((clientX - rect.left) / rect.width) * WORLD_W;
    const vy = ((clientY - rect.top) / rect.height) * WORLD_H;
    // Reverse the pan/zoom transform to get world coords.
    return { x: (vx - panX) / zoom, y: (vy - panY) / zoom };
  }

  function onWheel(e: WheelEvent) {
    e.preventDefault();
    const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12;
    const newZoom = Math.max(0.3, Math.min(3, zoom * factor));
    if (!svgEl) {
      zoom = newZoom;
      return;
    }
    // Anchor zoom at the cursor.
    const rect = svgEl.getBoundingClientRect();
    const vx = ((e.clientX - rect.left) / rect.width) * WORLD_W;
    const vy = ((e.clientY - rect.top) / rect.height) * WORLD_H;
    panX = vx - (vx - panX) * (newZoom / zoom);
    panY = vy - (vy - panY) * (newZoom / zoom);
    zoom = newZoom;
  }

  function onSvgMouseDown(e: MouseEvent) {
    if (e.target !== svgEl && !(e.target as Element)?.classList?.contains('bg-rect')) return;
    panning = true;
    panStart = { x: e.clientX, y: e.clientY, panX, panY };
  }

  function onSvgClick(e: MouseEvent) {
    // Deselect only if user clicked the actual background, not a node child.
    if (e.target === svgEl || (e.target as Element)?.classList?.contains('bg-rect')) {
      selected = null;
    }
  }

  function resetView() {
    panX = 0;
    panY = 0;
    zoom = 1;
  }

  function zoomIn() {
    zoom = Math.min(3, zoom * 1.2);
  }
  function zoomOut() {
    zoom = Math.max(0.3, zoom / 1.2);
  }

  // ---------- node drag ----------
  let dragging: SimNode | null = null;

  function onNodeMouseDown(snapshot: SimNode, e: MouseEvent) {
    e.stopPropagation();
    const real = nodeMap.get(snapshot.id);
    if (!real) return;
    selected = { kind: real.kind, id: real.id };
    dragging = real;
    real.fixed = true;
    reheat();
  }

  function onWindowMouseMove(e: MouseEvent) {
    if (panning) {
      const dx = e.clientX - panStart.x;
      const dy = e.clientY - panStart.y;
      // Convert client px delta to viewport units
      if (svgEl) {
        const rect = svgEl.getBoundingClientRect();
        panX = panStart.panX + (dx / rect.width) * WORLD_W;
        panY = panStart.panY + (dy / rect.height) * WORLD_H;
      }
      return;
    }
    if (dragging) {
      const p = svgPointWorld(e.clientX, e.clientY);
      dragging.x = p.x;
      dragging.y = p.y;
      frame++;
    }
  }

  function onWindowMouseUp() {
    if (dragging) {
      dragging.fixed = false;
      dragging = null;
    }
    panning = false;
  }

  function onNodeMouseEnter(node: SimNode, e: MouseEvent) {
    hovered = { id: node.id, clientX: e.clientX, clientY: e.clientY };
  }
  function onNodeMouseLeave() {
    hovered = null;
  }
  function onNodeMouseMove(e: MouseEvent) {
    if (hovered) hovered = { ...hovered, clientX: e.clientX, clientY: e.clientY };
  }

  // ---------- styling helpers ----------
  function networkColor(n: TopoNetwork): string {
    if (n.system) return '#6b7280';
    if (n.driver === 'overlay') return '#a855f7';
    return '#06b6d4';
  }
  function containerColor(c: TopoContainer): string {
    if (c.state !== 'running') return '#6b7280';
    return '#22c55e';
  }

  // Service icon: emoji for known popular images, else first 2 letters.
  // Avoids shipping a 100 KB icon set; all ASCII/emoji is bundled by the
  // browser already.
  const ICON_MAP: Record<string, string> = {
    nginx: 'NX',
    caddy: 'CA',
    traefik: 'TR',
    apache: 'AP',
    httpd: 'AP',
    postgres: '🐘',
    postgresql: '🐘',
    mysql: '🐬',
    mariadb: '🐬',
    redis: '🟥',
    mongo: '🍃',
    mongodb: '🍃',
    elasticsearch: 'ES',
    grafana: '📊',
    prometheus: '🔥',
    influxdb: '📈',
    rabbitmq: '🐰',
    nextcloud: '☁️',
    jellyfin: '🎬',
    plex: '🎞',
    homeassistant: '🏠',
    vaultwarden: '🔐',
    bitwarden: '🔐',
    gitea: '🍵',
    drone: '✈️',
    jenkins: '🤖',
    portainer: '⚓',
    minio: 'M',
    alpine: '🏔'
  };

  function serviceIcon(image: string): string {
    const name = imageBaseName(image);
    for (const key of Object.keys(ICON_MAP)) {
      if (name.includes(key)) return ICON_MAP[key];
    }
    return name.slice(0, 2).toUpperCase();
  }

  function imageBaseName(image: string): string {
    let s = image;
    const at = s.indexOf('@');
    if (at >= 0) s = s.slice(0, at);
    const colon = s.lastIndexOf(':');
    if (colon >= 0) s = s.slice(0, colon);
    const slash = s.lastIndexOf('/');
    if (slash >= 0) s = s.slice(slash + 1);
    return s.toLowerCase();
  }

  // ---------- side panel data ----------
  const selectedNode = $derived(selected ? nodeMap.get(selected.id) ?? null : null);
  const selectedLinks = $derived(
    selected
      ? simLinks.filter((l) => l.source === selected!.id || l.target === selected!.id)
      : []
  );
  const hoveredNode = $derived(hovered ? nodeMap.get(hovered.id) ?? null : null);

  function neighbourLabel(id: string): string {
    const n = nodeMap.get(id);
    return n ? n.label : id.slice(0, 12);
  }

  // ---------- mount + filter effect ----------
  onMount(() => {
    load();
    connectLive();
  });

  let prevShowSystem = showSystem;
  $effect(() => {
    const current = showSystem;
    if (current === prevShowSystem) return;
    prevShowSystem = current;
    untrack(() => {
      if (topo) buildSimulation(snapshotPositions());
    });
  });
</script>

<svelte:window onmousemove={onWindowMouseMove} onmouseup={onWindowMouseUp} />

<section class="space-y-4">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight flex items-center gap-2">
        Network topology
        {#if live}
          <span class="inline-flex items-center gap-1 text-xs text-[var(--color-success-400)] font-normal">
            <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)] animate-pulse"></span>live
          </span>
        {/if}
      </h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Drag the background to pan, scroll to zoom, drag a node to pin it. Stack labels group containers by compose project.
      </p>
    </div>
    <div class="flex items-center gap-2 flex-wrap">
      <div class="relative">
        <Search class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--fg-subtle)] pointer-events-none" />
        <input
          type="text"
          bind:value={search}
          placeholder="Search…"
          class="dm-input !py-1.5 !pl-8 !pr-3 text-sm w-48"
        />
      </div>
      <Button variant="secondary" size="sm" onclick={() => (showSystem = !showSystem)}>
        {#if showSystem}<EyeOff class="w-3.5 h-3.5" />{:else}<Eye class="w-3.5 h-3.5" />{/if}
        {showSystem ? 'Hide' : 'Show'} system
      </Button>
      <Button variant="secondary" size="sm" onclick={() => load(true)}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
    </div>
  </div>

  {#if loading && !topo}
    <Card><Skeleton class="m-5" width="80%" height="20rem" /></Card>
  {:else if !topo || topo.networks.length === 0}
    <Card>
      <EmptyState
        icon={NetworkIcon}
        title="No networks"
        description="Deploy a stack or create a network to see the topology."
      />
    </Card>
  {:else}
    <div class="grid grid-cols-1 lg:grid-cols-4 gap-4">
      <Card class="lg:col-span-3 overflow-hidden relative">
        <!-- Zoom controls -->
        <div class="absolute top-3 right-3 z-10 flex flex-col gap-1 bg-[var(--surface)] border border-[var(--border)] rounded-lg p-1 shadow-lg">
          <button
            class="w-7 h-7 flex items-center justify-center rounded text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]"
            onclick={zoomIn}
            aria-label="Zoom in"
            title="Zoom in"
          >
            <ZoomIn class="w-3.5 h-3.5" />
          </button>
          <button
            class="w-7 h-7 flex items-center justify-center rounded text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]"
            onclick={zoomOut}
            aria-label="Zoom out"
            title="Zoom out"
          >
            <ZoomOut class="w-3.5 h-3.5" />
          </button>
          <button
            class="w-7 h-7 flex items-center justify-center rounded text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]"
            onclick={resetView}
            aria-label="Reset view"
            title="Reset view"
          >
            <Maximize2 class="w-3.5 h-3.5" />
          </button>
          <div class="text-[10px] text-center text-[var(--fg-subtle)] font-mono px-1">
            {(zoom * 100).toFixed(0)}%
          </div>
        </div>

        <svg
          bind:this={svgEl}
          viewBox="0 0 {WORLD_W} {WORLD_H}"
          class="w-full h-[72vh] {panning ? 'cursor-grabbing' : 'cursor-grab'}"
          onmousedown={onSvgMouseDown}
          onclick={onSvgClick}
          onwheel={onWheel}
          role="application"
          aria-label="Network topology graph"
        >
          <!-- Background hit target so SVG-level events work -->
          <rect class="bg-rect" x="0" y="0" width={WORLD_W} height={WORLD_H} fill="transparent" />

          <g transform="translate({panX} {panY}) scale({zoom})">
            <!-- Stack groups (rendered behind everything) -->
            {#each stackBoxes as box (box.name)}
              <g>
                <rect
                  x={box.x}
                  y={box.y}
                  width={box.w}
                  height={box.h}
                  rx="14"
                  fill="color-mix(in srgb, {box.color} 6%, transparent)"
                  stroke="color-mix(in srgb, {box.color} 50%, transparent)"
                  stroke-width="1.5"
                  stroke-dasharray="6 4"
                  pointer-events="none"
                />
                <text
                  x={box.x + 14}
                  y={box.y + 18}
                  font-size="13"
                  font-weight="600"
                  fill={box.color}
                  font-family="var(--font-mono)"
                  pointer-events="none"
                >
                  {box.name}
                </text>
              </g>
            {/each}

            <!-- Edges -->
            <g fill="none">
              {#each edges as e (e.sid + '|' + e.tid)}
                {@const isSel = selected && (selected.id === e.sid || selected.id === e.tid)}
                {@const dim = (selected || searchMatches) && !isSel && (isDimmed(e.sid) || isDimmed(e.tid))}
                <line
                  x1={e.x1}
                  y1={e.y1}
                  x2={e.x2}
                  y2={e.y2}
                  stroke={isSel ? 'var(--color-brand-400)' : 'var(--border-strong)'}
                  stroke-width={isSel ? 2.5 : 1.4}
                  opacity={dim ? 0.1 : isSel ? 1 : 0.55}
                />
              {/each}
            </g>

            <!-- Nodes -->
            {#each renderedNodes as n (n.id)}
              {@const isSel = selected?.id === n.id}
              {@const dim = isDimmed(n.id)}
              <g
                transform="translate({n.x},{n.y})"
                opacity={dim ? 0.15 : 1}
                class="cursor-grab active:cursor-grabbing transition-opacity"
                onmousedown={(e) => onNodeMouseDown(n, e)}
                onmouseenter={(e) => onNodeMouseEnter(n, e)}
                onmouseleave={onNodeMouseLeave}
                onmousemove={onNodeMouseMove}
                role="button"
                tabindex="0"
              >
                {#if n.kind === 'network'}
                  {@const net = n.data as TopoNetwork}
                  <circle
                    r={n.radius}
                    fill="color-mix(in srgb, {networkColor(net)} 22%, var(--bg-elevated))"
                    stroke={networkColor(net)}
                    stroke-width={isSel ? 3.5 : 2.5}
                  />
                  <text
                    text-anchor="middle"
                    dy="-2"
                    font-size="9"
                    font-weight="700"
                    fill={networkColor(net)}
                    font-family="var(--font-mono)"
                    pointer-events="none"
                  >NET</text>
                  <text
                    text-anchor="middle"
                    dy="11"
                    font-size="8"
                    fill="var(--fg-muted)"
                    font-family="var(--font-mono)"
                    pointer-events="none"
                  >{net.driver}</text>
                  <text
                    text-anchor="middle"
                    y={n.radius + 14}
                    font-size="11"
                    font-weight="500"
                    fill="var(--fg)"
                    font-family="var(--font-mono)"
                    pointer-events="none"
                  >{n.label}</text>
                {:else}
                  {@const c = n.data as TopoContainer}
                  <circle
                    r={n.radius}
                    fill="color-mix(in srgb, {containerColor(c)} 24%, var(--bg-elevated))"
                    stroke={containerColor(c)}
                    stroke-width={isSel ? 3 : 2}
                  />
                  <text
                    text-anchor="middle"
                    dy="5"
                    font-size="13"
                    font-weight="700"
                    fill="var(--fg)"
                    font-family="var(--font-sans)"
                    pointer-events="none"
                  >{serviceIcon(c.image)}</text>
                  <text
                    text-anchor="middle"
                    y={n.radius + 13}
                    font-size="10"
                    font-weight="500"
                    fill="var(--fg)"
                    font-family="var(--font-mono)"
                    pointer-events="none"
                  >{n.label.length > 22 ? n.label.slice(0, 21) + '…' : n.label}</text>
                  {#if c.ports && c.ports.length > 0}
                    <text
                      text-anchor="middle"
                      y={n.radius + 25}
                      font-size="8"
                      fill="var(--color-brand-400)"
                      font-family="var(--font-mono)"
                      pointer-events="none"
                    >:{c.ports[0].host_port}{c.ports.length > 1 ? ` +${c.ports.length - 1}` : ''}</text>
                  {/if}
                {/if}
              </g>
            {/each}
          </g>
        </svg>

        <!-- Tooltip -->
        {#if hovered && hoveredNode}
          {@const n = hoveredNode}
          <div
            class="absolute pointer-events-none z-20 bg-[var(--bg-elevated)] border border-[var(--border-strong)] rounded-lg px-3 py-2 shadow-2xl text-xs"
            style="left: {hovered.clientX - (svgEl?.getBoundingClientRect().left ?? 0) + 14}px; top: {hovered.clientY - (svgEl?.getBoundingClientRect().top ?? 0) + 14}px; max-width: 280px"
          >
            {#if n.kind === 'network'}
              {@const net = n.data as TopoNetwork}
              <div class="font-semibold font-mono">{net.name}</div>
              <div class="text-[var(--fg-muted)] mt-0.5 font-mono">{net.driver} · {net.scope}</div>
              {#if net.stack}
                <div class="text-[var(--fg-muted)] mt-0.5">stack: <span class="font-mono">{net.stack}</span></div>
              {/if}
              {#if net.system}<div class="text-[var(--color-warning-400)] mt-0.5">system network</div>{/if}
            {:else}
              {@const c = n.data as TopoContainer}
              <div class="font-semibold font-mono">{c.name}</div>
              <div class="text-[var(--fg-muted)] mt-0.5 font-mono truncate">{c.image}</div>
              <div class="flex gap-2 mt-1 items-center">
                <span class="w-1.5 h-1.5 rounded-full {c.state === 'running' ? 'bg-[var(--color-success-500)]' : 'bg-[var(--fg-subtle)]'}"></span>
                <span class="text-[var(--fg-muted)]">{c.state}</span>
                {#if c.stack}<span class="text-[var(--fg-muted)]">·</span><span class="font-mono">{c.stack}</span>{/if}
              </div>
              {#if c.ports && c.ports.length > 0}
                <div class="mt-1 font-mono text-[var(--color-brand-400)]">
                  {c.ports.map((p) => `${p.host_port}→${p.container_port}/${p.protocol}`).join(' · ')}
                </div>
              {/if}
            {/if}
          </div>
        {/if}

        <!-- Legend -->
        <div class="flex items-center gap-4 px-5 py-3 border-t border-[var(--border)] text-xs text-[var(--fg-muted)] flex-wrap">
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#06b6d4"></span>bridge / user</div>
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#a855f7"></span>overlay</div>
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#22c55e"></span>running</div>
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#6b7280"></span>stopped / system</div>
          <div class="ml-auto">{renderedNodes.length} nodes · {edges.length} edges{#if stackBoxes.length > 0} · {stackBoxes.length} stacks{/if}</div>
        </div>
      </Card>

      <!-- Side panel -->
      <Card class="p-5 lg:col-span-1">
        {#if !selectedNode}
          <h3 class="font-semibold text-sm mb-2">Selection</h3>
          <p class="text-xs text-[var(--fg-muted)]">Click a node to inspect it. Hover for a quick preview.</p>
          <div class="mt-4 space-y-1 text-xs text-[var(--fg-muted)]">
            <div>{topo.networks.length} network(s) total</div>
            <div>{topo.containers.length} container(s) total</div>
            <div>{topo.links.length} link(s) total</div>
          </div>
        {:else if selectedNode.kind === 'network'}
          {@const net = selectedNode.data as TopoNetwork}
          <h3 class="font-semibold text-sm mb-2 flex items-center gap-2 truncate">
            {net.name}
            {#if net.system}<Badge variant="default">system</Badge>{/if}
          </h3>
          <div class="space-y-1 text-xs">
            <div><span class="text-[var(--fg-muted)]">Driver:</span> <span class="font-mono">{net.driver}</span></div>
            <div><span class="text-[var(--fg-muted)]">Scope:</span> <span class="font-mono">{net.scope}</span></div>
            {#if net.internal}<div><Badge variant="warning">internal</Badge></div>{/if}
            {#if net.stack}<div><span class="text-[var(--fg-muted)]">Stack:</span> <Badge variant="info">{net.stack}</Badge></div>{/if}
            <div class="font-mono text-[var(--fg-subtle)] break-all pt-1">{net.id.slice(0, 12)}</div>
          </div>
          <div class="mt-4">
            <div class="text-xs text-[var(--fg-muted)] mb-2">{selectedLinks.length} attached container(s)</div>
            <div class="space-y-1 max-h-72 overflow-auto -mx-2">
              {#each selectedLinks as l}
                {@const cid = l.source === selectedNode.id ? l.target : l.source}
                <button
                  class="block w-full text-left px-2 py-1.5 text-xs font-mono rounded hover:bg-[var(--surface-hover)]"
                  onclick={() => goto(`/containers/${cid}`)}
                >
                  {neighbourLabel(cid)}
                </button>
              {/each}
            </div>
          </div>
        {:else}
          {@const c = selectedNode.data as TopoContainer}
          <h3 class="font-semibold text-sm mb-2 flex items-center gap-2 truncate">
            {c.name}
            <Badge variant={c.state === 'running' ? 'success' : 'default'} dot>{c.state}</Badge>
          </h3>
          <div class="space-y-1 text-xs">
            <div class="font-mono truncate text-[var(--fg-muted)]">{c.image}</div>
            {#if c.stack}<div><span class="text-[var(--fg-muted)]">Stack:</span> <Badge variant="info">{c.stack}</Badge></div>{/if}
            {#if c.ports && c.ports.length > 0}
              <div class="pt-2">
                <div class="text-[var(--fg-muted)] mb-1">Published ports</div>
                <div class="space-y-0.5">
                  {#each c.ports as p}
                    <div class="font-mono text-[var(--color-brand-400)]">
                      {p.host_port} → {p.container_port}/{p.protocol}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}
            <div class="font-mono text-[var(--fg-subtle)] break-all pt-1">{c.id.slice(0, 12)}</div>
          </div>
          <div class="mt-4">
            <div class="text-xs text-[var(--fg-muted)] mb-2">{selectedLinks.length} network(s)</div>
            <div class="space-y-1 max-h-48 overflow-auto -mx-2">
              {#each selectedLinks as l}
                {@const nid = l.source === selectedNode.id ? l.target : l.source}
                <div class="px-2 py-1 text-xs font-mono rounded text-[var(--fg)]">
                  {neighbourLabel(nid)}
                </div>
              {/each}
            </div>
          </div>
          <Button class="mt-4 w-full" variant="secondary" size="sm" onclick={() => goto(`/containers/${c.id}`)}>
            Open container
          </Button>
        {/if}
      </Card>
    </div>
  {/if}
</section>
