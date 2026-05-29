<script lang="ts">
  // Topology — editorial standalone. Reuses the dagre layout + pan/zoom +
  // live-reload from the legacy /networks?topology=1 view (preserved at
  // commit 7448720) but rewraps the chrome in editorial primitives:
  // breadcrumb + Eyebrow + Title, underline search, segmented filter
  // for show-system, side panel rendered on a flat dm-card. Live event
  // stream re-runs the layout when the Docker / stacks layer emits a
  // change event.
  import * as dagre from '@dagrejs/dagre';
  import { goto } from '$app/navigation';
  import { api, ApiError, type Topology, type TopoNetwork, type TopoContainer } from '$lib/api';
  import { Skeleton, EmptyState, Badge } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import {
    ChevronLeft, RefreshCw, Search, ZoomIn, ZoomOut, Maximize2, Network as NetworkIcon, Box
  } from 'lucide-svelte';

  let topo = $state<Topology | null>(null);
  let loading = $state(true);
  let topoShowSystem = $state(false);
  let topoSelected = $state<{ kind: 'network' | 'container'; id: string } | null>(null);
  let hovered = $state<{ id: string; clientX: number; clientY: number } | null>(null);
  let topoSearch = $state('');
  let connStatus = $state<ConnStatus>('connecting');
  const live = $derived(connStatus === 'live');
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  const NETWORK_W = 110;
  const NETWORK_H = 100;
  const CONTAINER_W = 150;
  const CONTAINER_H = 90;
  const NETWORK_R = 28;
  const CONTAINER_R = 18;

  type LaidNode = {
    id: string;
    kind: 'network' | 'container';
    label: string;
    x: number;
    y: number;
    data: TopoNetwork | TopoContainer;
  };
  type LaidEdge = { sid: string; tid: string; points: { x: number; y: number }[] };
  type StackBox = { name: string; x: number; y: number; w: number; h: number; color: string };

  let scene = $state<{ nodes: LaidNode[]; edges: LaidEdge[]; stacks: StackBox[]; width: number; height: number }>({
    nodes: [], edges: [], stacks: [], width: 0, height: 0
  });
  let nodeIndex = new Map<string, LaidNode>();

  function stackColor(i: number): string {
    const palette = ['#06b6d4', '#a855f7', '#f59e0b', '#ec4899', '#10b981', '#3b82f6', '#f97316'];
    return palette[i % palette.length];
  }

  function relayout() {
    if (!topo) {
      scene = { nodes: [], edges: [], stacks: [], width: 0, height: 0 };
      nodeIndex.clear();
      return;
    }
    const nets = topoShowSystem ? topo.networks : topo.networks.filter((n) => !n.system);
    const netIds = new Set(nets.map((n) => n.id));
    const usedContainerIds = new Set<string>();
    for (const l of topo.links) {
      if (netIds.has(l.network_id)) usedContainerIds.add(l.container_id);
    }
    const conts = topo.containers.filter((c) => usedContainerIds.has(c.id));

    const g = new dagre.graphlib.Graph({ compound: true, multigraph: false });
    g.setGraph({ rankdir: 'TB', nodesep: 30, ranksep: 55, marginx: 20, marginy: 20 });
    g.setDefaultEdgeLabel(() => ({}));

    const stackNames = new Set<string>();
    for (const n of nets) if (n.stack) stackNames.add(n.stack);
    for (const c of conts) if (c.stack) stackNames.add(c.stack);
    const stackList = [...stackNames].sort();
    const stackKey = (name: string) => `stack:${name}`;
    for (const name of stackList) {
      g.setNode(stackKey(name), { label: name, clusterLabelPos: 'top', padding: 18 });
    }
    for (const n of nets) {
      g.setNode(n.id, { label: n.name, width: NETWORK_W, height: NETWORK_H, kind: 'network', data: n });
      if (n.stack) g.setParent(n.id, stackKey(n.stack));
    }
    for (const c of conts) {
      g.setNode(c.id, { label: c.name, width: CONTAINER_W, height: CONTAINER_H, kind: 'container', data: c });
      if (c.stack) g.setParent(c.id, stackKey(c.stack));
    }
    for (const l of topo.links) {
      if (netIds.has(l.network_id) && usedContainerIds.has(l.container_id)) {
        g.setEdge(l.network_id, l.container_id);
      }
    }
    dagre.layout(g);

    const nodes: LaidNode[] = [];
    const edges: LaidEdge[] = [];
    const stacks: StackBox[] = [];
    let stackIdx = 0;
    for (const id of g.nodes()) {
      const meta: any = g.node(id);
      if (id.startsWith('stack:')) {
        stacks.push({
          name: id.slice('stack:'.length),
          x: meta.x - meta.width / 2,
          y: meta.y - meta.height / 2,
          w: meta.width,
          h: meta.height,
          color: stackColor(stackIdx++)
        });
        continue;
      }
      nodes.push({ id, kind: meta.kind, label: meta.label, x: meta.x, y: meta.y, data: meta.data });
    }
    for (const e of g.edges()) {
      const ed: any = g.edge(e);
      edges.push({
        sid: e.v,
        tid: e.w,
        points: (ed.points || []).map((p: any) => ({ x: p.x, y: p.y }))
      });
    }
    const graphMeta: any = g.graph();
    nodeIndex = new Map(nodes.map((n) => [n.id, n]));
    scene = { nodes, edges, stacks, width: graphMeta.width || 800, height: graphMeta.height || 600 };
    if (zoom === 1 && panX === 0 && panY === 0) fitToView();
  }

  // ───────── Viewport
  let panX = $state(0);
  let panY = $state(0);
  let zoom = $state(1);
  let svgEl: SVGSVGElement | null = $state(null);
  let panning = $state(false);
  let panStart = { clientX: 0, clientY: 0, panX: 0, panY: 0 };

  function fitToView() {
    if (!svgEl) return;
    const rect = svgEl.getBoundingClientRect();
    if (rect.width === 0 || scene.width === 0) return;
    const pad = 60;
    const sx = (rect.width - pad * 2) / scene.width;
    const sy = (rect.height - pad * 2) / scene.height;
    zoom = Math.min(2, Math.max(0.3, Math.min(sx, sy)));
    panX = (rect.width - scene.width * zoom) / 2;
    panY = (rect.height - scene.height * zoom) / 2;
  }
  function onWheel(e: WheelEvent) {
    e.preventDefault();
    if (!svgEl) return;
    const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12;
    const newZoom = Math.max(0.3, Math.min(3, zoom * factor));
    const rect = svgEl.getBoundingClientRect();
    const cx = e.clientX - rect.left;
    const cy = e.clientY - rect.top;
    panX = cx - (cx - panX) * (newZoom / zoom);
    panY = cy - (cy - panY) * (newZoom / zoom);
    zoom = newZoom;
  }
  function onSvgMouseDown(e: MouseEvent) {
    const target = e.target as Element;
    if (target === svgEl || target.classList?.contains('bg-rect') || target.tagName === 'svg') {
      panning = true;
      panStart = { clientX: e.clientX, clientY: e.clientY, panX, panY };
    }
  }
  function onWindowMouseMove(e: MouseEvent) {
    if (!panning) return;
    panX = panStart.panX + (e.clientX - panStart.clientX);
    panY = panStart.panY + (e.clientY - panStart.clientY);
  }
  function onWindowMouseUp() { panning = false; }
  function onSvgClick(e: MouseEvent) {
    const target = e.target as Element;
    if (target === svgEl || target.classList?.contains('bg-rect')) topoSelected = null;
  }
  function resetView() { fitToView(); }
  function zoomIn() {
    if (!svgEl) return;
    const rect = svgEl.getBoundingClientRect();
    const cx = rect.width / 2, cy = rect.height / 2;
    const newZoom = Math.min(3, zoom * 1.2);
    panX = cx - (cx - panX) * (newZoom / zoom);
    panY = cy - (cy - panY) * (newZoom / zoom);
    zoom = newZoom;
  }
  function zoomOut() {
    if (!svgEl) return;
    const rect = svgEl.getBoundingClientRect();
    const cx = rect.width / 2, cy = rect.height / 2;
    const newZoom = Math.max(0.3, zoom / 1.2);
    panX = cx - (cx - panX) * (newZoom / zoom);
    panY = cy - (cy - panY) * (newZoom / zoom);
    zoom = newZoom;
  }

  // ───────── Interaction
  function onNodeClick(node: LaidNode, e: MouseEvent) {
    e.stopPropagation();
    topoSelected = { kind: node.kind, id: node.id };
  }
  function onNodeMouseEnter(node: LaidNode, e: MouseEvent) {
    hovered = { id: node.id, clientX: e.clientX, clientY: e.clientY };
  }
  function onNodeMouseLeave() { hovered = null; }
  function onNodeMouseMove(e: MouseEvent) {
    if (hovered) hovered = { ...hovered, clientX: e.clientX, clientY: e.clientY };
  }

  // ───────── Search filter
  const searchMatches = $derived.by(() => {
    const q = topoSearch.trim().toLowerCase();
    if (!q) return null;
    const hits = new Set<string>();
    for (const n of scene.nodes) {
      if (n.label.toLowerCase().includes(q)) hits.add(n.id);
      else if (n.kind === 'container' && (n.data as TopoContainer).image.toLowerCase().includes(q)) hits.add(n.id);
    }
    for (const e of scene.edges) {
      if (hits.has(e.sid) || hits.has(e.tid)) {
        hits.add(e.sid);
        hits.add(e.tid);
      }
    }
    return hits;
  });
  function isDimmed(id: string): boolean {
    if (searchMatches && !searchMatches.has(id)) return true;
    if (topoSelected) {
      if (topoSelected.id === id) return false;
      const touched = scene.edges.some(
        (e) =>
          (e.sid === topoSelected!.id && (e.tid === id || e.sid === id)) ||
          (e.tid === topoSelected!.id && (e.sid === id || e.tid === id))
      );
      if (!touched) return true;
    }
    return false;
  }

  // ───────── Styling
  function networkColor(n: TopoNetwork): string {
    if (n.system) return '#6b7280';
    if (n.driver === 'overlay') return '#a855f7';
    return '#06b6d4';
  }
  function containerColor(c: TopoContainer): string {
    if (c.state !== 'running') return '#6b7280';
    return '#22c55e';
  }
  const ICON_MAP: Record<string, string> = {
    nginx: 'NX', caddy: 'CA', traefik: 'TR', apache: 'AP', httpd: 'AP',
    postgres: 'PG', postgresql: 'PG', mysql: 'MY', mariadb: 'MY',
    redis: 'RE', mongo: 'MG', mongodb: 'MG', elasticsearch: 'ES',
    grafana: 'GR', prometheus: 'PM', influxdb: 'IF', rabbitmq: 'RM',
    nextcloud: 'NC', jellyfin: 'JF', plex: 'PX', homeassistant: 'HA',
    vaultwarden: 'VW', bitwarden: 'BW', gitea: 'GT', drone: 'DR',
    jenkins: 'JK', portainer: 'PT', minio: 'MN', alpine: 'AL'
  };
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
  function serviceIcon(image: string): string {
    const name = imageBaseName(image);
    for (const key of Object.keys(ICON_MAP)) {
      if (name.includes(key)) return ICON_MAP[key];
    }
    return name.slice(0, 2).toUpperCase();
  }

  // ───────── Side panel data
  const selectedNode = $derived(topoSelected ? nodeIndex.get(topoSelected.id) ?? null : null);
  const selectedLinks = $derived(
    topoSelected
      ? scene.edges.filter((e) => e.sid === topoSelected!.id || e.tid === topoSelected!.id)
      : []
  );
  const hoveredNode = $derived(hovered ? nodeIndex.get(hovered.id) ?? null : null);
  function neighbourLabel(id: string): string {
    const n = nodeIndex.get(id);
    return n ? n.label : id.slice(0, 12);
  }
  function edgePath(e: LaidEdge): string {
    const pts = e.points;
    if (pts.length === 0) return '';
    if (pts.length === 1) return `M${pts[0].x},${pts[0].y}`;
    if (pts.length === 2) return `M${pts[0].x},${pts[0].y} L${pts[1].x},${pts[1].y}`;
    let d = `M${pts[0].x},${pts[0].y}`;
    for (let i = 1; i < pts.length - 1; i++) {
      const mx = (pts[i].x + pts[i + 1].x) / 2;
      const my = (pts[i].y + pts[i + 1].y) / 2;
      d += ` Q${pts[i].x},${pts[i].y} ${mx},${my}`;
    }
    const last = pts[pts.length - 1];
    d += ` L${last.x},${last.y}`;
    return d;
  }

  // ───────── Load + live
  async function load() {
    loading = true;
    try {
      topo = await api.networks.topology();
      relayout();
    } catch (err) {
      toast.error('Failed to load topology', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  const stream = new EventStream({
    onMessage: (msg) => {
      if (
        (msg.source === 'docker' && (msg.type === 'container' || msg.type === 'network')) ||
        msg.source === 'stacks'
      ) {
        scheduleReload();
      }
    },
    onStatus: (s) => { connStatus = s; }
  });
  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(() => load(), 500);
  }
  $effect(() => {
    load();
    stream.start();
    return () => {
      stream.stop();
      if (reloadTimer) clearTimeout(reloadTimer);
    };
  });
  let prevShowSystem: boolean | null = null;
  $effect(() => {
    const cur = topoShowSystem;
    if (prevShowSystem === null) {
      prevShowSystem = cur;
      return;
    }
    if (cur !== prevShowSystem) {
      prevShowSystem = cur;
      relayout();
    }
  });
</script>

<svelte:window onmousemove={onWindowMouseMove} onmouseup={onWindowMouseUp} />

<section class="topo-page">
  <header class="topo-header">
    <div class="topo-back-row">
      <a href="/resources?tab=networks" class="topo-back" aria-label="Back to resources">
        <ChevronLeft size={14} strokeWidth={1.5} /> resources / networks
      </a>
    </div>
    <div class="topo-title-row">
      <div class="topo-title-text">
        <h1 class="ed-title topo-title">Topology</h1>
        <p class="topo-subtitle">
          {topo?.containers.length ?? 0} container{(topo?.containers.length ?? 0) === 1 ? '' : 's'} ·
          {topo?.networks.length ?? 0} network{(topo?.networks.length ?? 0) === 1 ? '' : 's'}
          <span class="topo-livebadge font-mono" class:on={live}>
            <span class="topo-livedot" class:on={live}></span> {live ? 'live' : connStatus}
          </span>
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={load}>
          <RefreshCw size={12} strokeWidth={1.5} class={loading ? 'ed-spin' : ''} /> Refresh
        </button>
      </div>
    </div>
  </header>

  <!-- ─── Toolbar ─── -->
  <div class="topo-toolbar">
    <div class="topo-search">
      <Search size={12} strokeWidth={1.5} class="topo-search-icon" />
      <input type="text" class="ed-underline-input topo-search-input" placeholder="search nodes, image, stack…" bind:value={topoSearch} />
    </div>
    <label class="topo-toggle font-mono">
      <input type="checkbox" bind:checked={topoShowSystem} />
      show system networks
    </label>
    <span class="topo-spacer"></span>
    <div class="topo-zoom-bar">
      <button type="button" class="topo-zoom-btn" onclick={zoomIn} title="Zoom in" aria-label="Zoom in">
        <ZoomIn size={12} strokeWidth={1.5} />
      </button>
      <button type="button" class="topo-zoom-btn" onclick={zoomOut} title="Zoom out" aria-label="Zoom out">
        <ZoomOut size={12} strokeWidth={1.5} />
      </button>
      <button type="button" class="topo-zoom-btn" onclick={resetView} title="Fit to view" aria-label="Fit to view">
        <Maximize2 size={12} strokeWidth={1.5} />
      </button>
      <span class="topo-zoom-pct font-mono">{(zoom * 100).toFixed(0)}%</span>
    </div>
  </div>

  {#if loading && !topo}
    <div class="dm-card topo-stage-loading"><Skeleton width="80%" height="12rem" /></div>
  {:else if !topo || (topo.networks.length === 0 && topo.containers.length === 0)}
    <div class="dm-card topo-stage-loading">
      <EmptyState icon={NetworkIcon} title="Empty fleet" description="No networks, no containers — deploy a stack to populate the topology." />
    </div>
  {:else}
    <div class="topo-grid">
      <!-- Graph card -->
      <div class="dm-card topo-stage">
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <svg
          bind:this={svgEl}
          class="topo-svg {panning ? 'panning' : ''}"
          onmousedown={onSvgMouseDown}
          onclick={onSvgClick}
          onwheel={onWheel}
          role="application"
          aria-label="Network topology graph"
        >
          <rect class="bg-rect" x="0" y="0" width="100%" height="100%" fill="transparent" />

          <g style="transform: translate({panX}px, {panY}px) scale({zoom}); transform-origin: 0 0;">
            <!-- Stack groups -->
            {#each scene.stacks as box (box.name)}
              <g>
                <rect
                  x={box.x} y={box.y} width={box.w} height={box.h} rx="14"
                  fill="color-mix(in srgb, {box.color} 6%, transparent)"
                  stroke="color-mix(in srgb, {box.color} 50%, transparent)"
                  stroke-width="1.5"
                  stroke-dasharray="6 4"
                  pointer-events="none"
                />
                <rect
                  x={box.x + 10} y={box.y - 12} width={box.name.length * 8 + 16} height="22" rx="6"
                  fill="var(--bg-elevated)"
                  stroke="color-mix(in srgb, {box.color} 50%, transparent)"
                  stroke-width="1"
                  pointer-events="none"
                />
                <text
                  x={box.x + 18} y={box.y + 4}
                  font-size="13" font-weight="600" fill={box.color}
                  font-family="var(--font-mono)" pointer-events="none"
                >{box.name}</text>
              </g>
            {/each}

            <!-- Edges -->
            <g fill="none">
              {#each scene.edges as e (e.sid + '|' + e.tid)}
                {@const isSel = topoSelected && (topoSelected.id === e.sid || topoSelected.id === e.tid)}
                {@const dim = (topoSelected || searchMatches) && !isSel && (isDimmed(e.sid) || isDimmed(e.tid))}
                <path
                  d={edgePath(e)}
                  stroke={isSel ? 'var(--color-brand-400)' : 'var(--border-strong)'}
                  stroke-width={isSel ? 2.5 : 1.5}
                  opacity={dim ? 0.1 : isSel ? 1 : 0.6}
                  style="transition: stroke 200ms, opacity 200ms"
                />
              {/each}
            </g>

            <!-- Nodes -->
            {#each scene.nodes as n (n.id)}
              {@const isSel = topoSelected?.id === n.id}
              {@const dim = isDimmed(n.id)}
              <g
                style="transform: translate({n.x}px, {n.y}px); transition: transform 300ms ease, opacity 200ms;"
                opacity={dim ? 0.15 : 1}
                class="topo-node"
                onclick={(e) => onNodeClick(n, e)}
                onmouseenter={(e) => onNodeMouseEnter(n, e)}
                onmouseleave={onNodeMouseLeave}
                onmousemove={onNodeMouseMove}
                role="button"
                tabindex="0"
                aria-label="{n.kind === 'network' ? 'Network' : 'Container'} {n.id}"
              >
                {#if n.kind === 'network'}
                  {@const net = n.data as TopoNetwork}
                  <circle r={NETWORK_R}
                    fill="color-mix(in srgb, {networkColor(net)} 22%, var(--bg-elevated))"
                    stroke={networkColor(net)} stroke-width={isSel ? 3.5 : 2.5} />
                  <text text-anchor="middle" dy="-2" font-size="9" font-weight="700"
                    fill={networkColor(net)} font-family="var(--font-mono)" pointer-events="none">NET</text>
                  <text text-anchor="middle" dy="11" font-size="8"
                    fill="var(--fg-muted)" font-family="var(--font-mono)" pointer-events="none">{net.driver}</text>
                  <text text-anchor="middle" y={NETWORK_R + 14} font-size="11" font-weight="500"
                    fill="var(--fg)" font-family="var(--font-mono)" pointer-events="none">{net.name}</text>
                {:else}
                  {@const c = n.data as TopoContainer}
                  <circle r={CONTAINER_R}
                    fill="color-mix(in srgb, {containerColor(c)} 24%, var(--bg-elevated))"
                    stroke={containerColor(c)} stroke-width={isSel ? 3 : 2} />
                  <text text-anchor="middle" dy="5" font-size="11" font-weight="700"
                    fill="var(--fg)" font-family="var(--font-mono)" pointer-events="none">{serviceIcon(c.image)}</text>
                  <text text-anchor="middle" y={CONTAINER_R + 13} font-size="10" font-weight="500"
                    fill="var(--fg)" font-family="var(--font-mono)" pointer-events="none">
                    {c.name.length > 22 ? c.name.slice(0, 21) + '…' : c.name}
                  </text>
                  {#if c.ports && c.ports.length > 0}
                    <text text-anchor="middle" y={CONTAINER_R + 25} font-size="8"
                      fill="var(--color-brand-400)" font-family="var(--font-mono)" pointer-events="none">
                      :{c.ports[0].host_port}{c.ports.length > 1 ? ` +${c.ports.length - 1}` : ''}
                    </text>
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
            class="topo-tooltip"
            style="left: {hovered.clientX - (svgEl?.getBoundingClientRect().left ?? 0) + 14}px; top: {hovered.clientY - (svgEl?.getBoundingClientRect().top ?? 0) + 14}px;"
          >
            {#if n.kind === 'network'}
              {@const net = n.data as TopoNetwork}
              <div class="topo-tooltip-name font-mono">{net.name}</div>
              <div class="topo-tooltip-meta font-mono">{net.driver} · {net.scope}</div>
              {#if net.stack}<div class="topo-tooltip-meta">stack: <span class="font-mono">{net.stack}</span></div>{/if}
              {#if net.system}<div class="topo-tooltip-warn">system network</div>{/if}
            {:else}
              {@const c = n.data as TopoContainer}
              <div class="topo-tooltip-name font-mono">{c.name}</div>
              <div class="topo-tooltip-meta font-mono">{c.image}</div>
              <div class="topo-tooltip-state">
                <span class="topo-tooltip-dot" class:running={c.state === 'running'}></span>
                {c.state}
                {#if c.stack}· <span class="font-mono">{c.stack}</span>{/if}
              </div>
              {#if c.ports && c.ports.length > 0}
                <div class="topo-tooltip-ports font-mono">
                  {c.ports.map((p) => `${p.host_port}→${p.container_port}/${p.protocol}`).join(' · ')}
                </div>
              {/if}
            {/if}
          </div>
        {/if}

        <!-- Legend -->
        <div class="topo-legend">
          <div class="topo-legend-item"><span class="topo-legend-dot" style="background:#06b6d4"></span>bridge / user</div>
          <div class="topo-legend-item"><span class="topo-legend-dot" style="background:#a855f7"></span>overlay</div>
          <div class="topo-legend-item"><span class="topo-legend-dot" style="background:#22c55e"></span>running</div>
          <div class="topo-legend-item"><span class="topo-legend-dot" style="background:#6b7280"></span>stopped / system</div>
          <div class="topo-legend-counts font-mono">
            {scene.nodes.length} nodes · {scene.edges.length} edges{#if scene.stacks.length > 0} · {scene.stacks.length} stacks{/if}
          </div>
        </div>
      </div>

      <!-- Side panel -->
      <div class="dm-card topo-side">
        {#if !selectedNode}
          <Eyebrow>Selection</Eyebrow>
          <p class="topo-side-hint">Click a node to inspect it. Hover for a quick preview.</p>
          <div class="topo-side-stats">
            <div class="topo-side-stat">
              <span class="topo-side-stat-label">Networks</span>
              <span class="topo-side-stat-num">{topo.networks.length}</span>
            </div>
            <div class="topo-side-stat">
              <span class="topo-side-stat-label">Containers</span>
              <span class="topo-side-stat-num">{topo.containers.length}</span>
            </div>
            <div class="topo-side-stat">
              <span class="topo-side-stat-label">Links</span>
              <span class="topo-side-stat-num">{topo.links.length}</span>
            </div>
          </div>
        {:else if selectedNode.kind === 'network'}
          {@const net = selectedNode.data as TopoNetwork}
          <Eyebrow>Network</Eyebrow>
          <h3 class="topo-side-title font-mono">{net.name}</h3>
          <div class="topo-side-meta">
            <span class="dm-pill dm-pill-neutral topo-side-pill">{net.driver}</span>
            <span class="dm-pill dm-pill-neutral topo-side-pill">{net.scope}</span>
            {#if net.system}<span class="dm-pill dm-pill-neutral topo-side-pill">system</span>{/if}
            {#if net.internal}<span class="dm-pill dm-pill-warning topo-side-pill">internal</span>{/if}
          </div>
          {#if net.stack}
            <div class="topo-side-row font-mono">stack: <span class="ed-accent">{net.stack}</span></div>
          {/if}
          <div class="topo-side-row font-mono muted">id: {net.id.slice(0, 12)}</div>
          <div class="topo-side-section-head">
            <Eyebrow>Attached containers</Eyebrow>
            <span class="topo-side-section-count font-mono">{selectedLinks.length}</span>
          </div>
          <div class="topo-side-list">
            {#each selectedLinks as l (l.sid + l.tid)}
              {@const cid = l.sid === selectedNode.id ? l.tid : l.sid}
              <button class="topo-side-list-item font-mono" onclick={() => goto(`/containers/${cid}`)}>
                {neighbourLabel(cid)}
              </button>
            {/each}
          </div>
          <a class="dm-btn dm-btn-secondary dm-btn-sm topo-side-cta" href={`/networks/${net.id}`}>Open detail</a>
        {:else}
          {@const c = selectedNode.data as TopoContainer}
          <Eyebrow>Container</Eyebrow>
          <h3 class="topo-side-title font-mono">{c.name}</h3>
          <div class="topo-side-meta">
            <Badge variant={c.state === 'running' ? 'success' : 'default'} dot>{c.state}</Badge>
            {#if c.stack}<span class="dm-pill dm-pill-neutral topo-side-pill">{c.stack}</span>{/if}
          </div>
          <div class="topo-side-row font-mono muted">{c.image}</div>
          <div class="topo-side-row font-mono muted">id: {c.id.slice(0, 12)}</div>
          {#if c.ports && c.ports.length > 0}
            <div class="topo-side-section-head"><Eyebrow>Published ports</Eyebrow></div>
            <div class="topo-side-ports">
              {#each c.ports as p, i (i)}
                <div class="topo-side-port font-mono">
                  <span class="ed-accent">{p.host_port}</span> → {p.container_port}/{p.protocol}
                </div>
              {/each}
            </div>
          {/if}
          <div class="topo-side-section-head">
            <Eyebrow>Networks</Eyebrow>
            <span class="topo-side-section-count font-mono">{selectedLinks.length}</span>
          </div>
          <div class="topo-side-list">
            {#each selectedLinks as l (l.sid + l.tid)}
              {@const nid = l.sid === selectedNode.id ? l.tid : l.sid}
              <div class="topo-side-list-static font-mono">{neighbourLabel(nid)}</div>
            {/each}
          </div>
          <a class="dm-btn dm-btn-secondary dm-btn-sm topo-side-cta" href={`/containers/${c.id}`}>Open container</a>
        {/if}
      </div>
    </div>
  {/if}
</section>

<style>
  .topo-page { display: block; }

  /* ─── Header ─── */
  .topo-header { margin-bottom: 22px; }
  .topo-back-row { margin-bottom: 12px; }
  .topo-back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-decoration: none;
    letter-spacing: 0.04em;
  }
  .topo-back:hover { color: var(--fg); }
  .topo-title-row {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .topo-title-text { min-width: 0; max-width: 70ch; }
  .topo-title { font-size: 26px; }
  .topo-subtitle {
    margin-top: 6px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .topo-livebadge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-left: 10px;
    padding: 1px 8px;
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 10.5px;
    color: var(--fg-subtle);
    background: var(--bg-elevated);
  }
  .topo-livebadge.on { color: var(--color-success-400); border-color: color-mix(in srgb, var(--color-success-500) 40%, var(--border)); }
  .topo-livedot {
    width: 5px; height: 5px; border-radius: 999px;
    background: var(--fg-subtle);
  }
  .topo-livedot.on { background: var(--color-success-400); box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success-500) 22%, transparent); }

  /* ─── Toolbar ─── */
  .topo-toolbar {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    margin: 18px 0 14px;
  }
  .topo-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 240px;
  }
  :global(.topo-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
  }
  .topo-search-input { padding-left: 20px; font-size: 12px; }
  .topo-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .topo-toggle input[type="checkbox"] { accent-color: var(--color-brand-500); }
  .topo-spacer { flex: 1; }
  .topo-zoom-bar {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 2px;
    background: var(--bg-elevated);
  }
  .topo-zoom-btn {
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    width: 24px;
    height: 24px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    border-radius: 3px;
  }
  .topo-zoom-btn:hover { color: var(--fg); background: var(--surface-hover); }
  .topo-zoom-pct { font-size: 10px; color: var(--fg-subtle); padding: 0 6px; }

  /* ─── Stage ─── */
  .topo-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 320px;
    gap: 14px;
  }
  @media (max-width: 1100px) {
    .topo-grid { grid-template-columns: 1fr; }
  }
  .topo-stage {
    padding: 0;
    overflow: hidden;
    position: relative;
  }
  .topo-stage-loading { padding: 28px; }
  .topo-svg {
    width: 100%;
    height: 72vh;
    display: block;
    cursor: grab;
    background: var(--bg-elevated);
  }
  .topo-svg.panning { cursor: grabbing; }
  .topo-node { cursor: pointer; }
  .topo-tooltip {
    position: absolute;
    z-index: 20;
    pointer-events: none;
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    padding: 8px 10px;
    box-shadow: 0 10px 30px rgba(0, 0, 0, 0.35);
    font-size: 11.5px;
    max-width: 280px;
  }
  .topo-tooltip-name { color: var(--fg); font-weight: 500; font-size: 12px; }
  .topo-tooltip-meta { color: var(--fg-muted); font-size: 11px; margin-top: 2px; }
  .topo-tooltip-state {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 4px;
    color: var(--fg-muted);
    font-size: 11px;
  }
  .topo-tooltip-dot { width: 6px; height: 6px; border-radius: 999px; background: var(--fg-subtle); }
  .topo-tooltip-dot.running { background: var(--color-success-500); }
  .topo-tooltip-ports { color: var(--color-brand-400); margin-top: 4px; font-size: 11px; }
  .topo-tooltip-warn { color: var(--color-warning-400); font-size: 11px; margin-top: 2px; }

  .topo-legend {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 8px 16px;
    border-top: 1px solid var(--border);
    color: var(--fg-muted);
    font-size: 11px;
    flex-wrap: wrap;
  }
  .topo-legend-item { display: inline-flex; align-items: center; gap: 6px; }
  .topo-legend-dot { width: 9px; height: 9px; border-radius: 999px; }
  .topo-legend-counts { margin-left: auto; color: var(--fg-subtle); }

  /* ─── Side panel ─── */
  .topo-side {
    padding: 18px 18px 22px;
  }
  .topo-side-hint {
    margin: 8px 0 0;
    font-size: 12px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .topo-side-stats {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 18px;
  }
  .topo-side-stat {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .topo-side-stat-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
  }
  .topo-side-stat-num {
    font-size: 18px;
    font-weight: 600;
    color: var(--fg);
    font-variant-numeric: tabular-nums;
  }
  .topo-side-title {
    margin: 6px 0 6px;
    font-size: 14px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .topo-side-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-bottom: 8px;
  }
  .topo-side-pill { font-size: 9.5px; padding: 1px 6px; }
  .topo-side-row {
    font-size: 11.5px;
    color: var(--fg-muted);
    margin-top: 4px;
    word-break: break-all;
  }
  .topo-side-row.muted { color: var(--fg-subtle); }
  .topo-side-section-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-top: 16px;
    margin-bottom: 6px;
  }
  .topo-side-section-count {
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .topo-side-list {
    display: flex;
    flex-direction: column;
    gap: 1px;
    max-height: 220px;
    overflow-y: auto;
  }
  .topo-side-list-item {
    background: transparent;
    border: 0;
    padding: 6px 10px;
    text-align: left;
    cursor: pointer;
    color: var(--fg);
    font-size: 11.5px;
    border-radius: 3px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .topo-side-list-item:hover { background: var(--surface-hover); color: var(--accent-fg); }
  .topo-side-list-static {
    padding: 6px 10px;
    color: var(--fg);
    font-size: 11.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .topo-side-ports {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .topo-side-port {
    font-size: 11.5px;
    color: var(--fg-muted);
    padding: 4px 10px;
    border: 1px solid var(--border);
    border-radius: 3px;
    background: var(--bg-elevated);
  }
  .topo-side-cta {
    margin-top: 16px;
    width: 100%;
    justify-content: center;
  }

  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
