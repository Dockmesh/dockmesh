<script lang="ts">
  import { onDestroy, onMount, untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError, type Topology, type TopoNetwork, type TopoContainer } from '$lib/api';
  import { Card, Button, Skeleton, EmptyState, Badge } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Network as NetworkIcon, RefreshCw, Eye, EyeOff } from 'lucide-svelte';

  // ---------- data ----------
  let topo = $state<Topology | null>(null);
  let loading = $state(true);
  let showSystem = $state(false);
  let selected = $state<{ kind: 'network' | 'container'; id: string } | null>(null);

  async function load() {
    loading = true;
    try {
      const next = await api.networks.topology();
      topo = next;
      // Build the simulation eagerly so the first frame is already laid out.
      // Any subsequent showSystem toggle goes through the dedicated effect
      // below; we do NOT rebuild from a $effect that reads `topo` because
      // buildSimulation reads topo deeply and would form a tracked cycle.
      buildSimulation();
    } catch (err) {
      toast.error('Failed to load topology', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  // ---------- force simulation ----------
  // Lightweight Verlet-style force layout. Networks are heavier nodes that
  // attract their containers via spring links. Containers repel each other
  // via Coulomb forces. A small gravity pull keeps everything centred.
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

  const W = 1200;
  const H = 800;
  const CENTER_X = W / 2;
  const CENTER_Y = H / 2;

  let nodes = $state<SimNode[]>([]);
  let links = $state<SimLink[]>([]);
  let nodeMap = new Map<string, SimNode>();
  let raf: number | null = null;
  let ticks = 0;

  function buildSimulation() {
    if (!topo) return;
    const nets = showSystem ? topo.networks : topo.networks.filter((n) => !n.system);
    const netIds = new Set(nets.map((n) => n.id));

    // Only containers that participate in at least one visible network.
    const usedContainerIds = new Set<string>();
    for (const l of topo.links) {
      if (netIds.has(l.network_id)) usedContainerIds.add(l.container_id);
    }
    const conts = topo.containers.filter((c) => usedContainerIds.has(c.id));

    nodes = [];
    links = [];
    nodeMap.clear();

    // Seed positions: networks on a circle, containers near their first network.
    const radius = Math.min(W, H) * 0.32;
    nets.forEach((n, i) => {
      const angle = (i / Math.max(1, nets.length)) * Math.PI * 2;
      const node: SimNode = {
        id: n.id,
        kind: 'network',
        label: n.name,
        x: CENTER_X + Math.cos(angle) * radius,
        y: CENTER_Y + Math.sin(angle) * radius,
        vx: 0,
        vy: 0,
        fixed: false,
        radius: 22,
        data: n
      };
      nodes.push(node);
      nodeMap.set(n.id, node);
    });

    conts.forEach((c, i) => {
      // Place near a random network for a smoother start.
      const firstNet = topo!.links.find((l) => l.container_id === c.id && netIds.has(l.network_id));
      const anchor = firstNet ? nodeMap.get(firstNet.network_id) : null;
      const ax = anchor ? anchor.x : CENTER_X;
      const ay = anchor ? anchor.y : CENTER_Y;
      const node: SimNode = {
        id: c.id,
        kind: 'container',
        label: c.name,
        x: ax + (Math.random() - 0.5) * 80,
        y: ay + (Math.random() - 0.5) * 80,
        vx: 0,
        vy: 0,
        fixed: false,
        radius: 12,
        data: c
      };
      nodes.push(node);
      nodeMap.set(c.id, node);
    });

    for (const l of topo.links) {
      if (netIds.has(l.network_id) && nodeMap.has(l.container_id)) {
        links.push({ source: l.network_id, target: l.container_id });
      }
    }

    ticks = 0;
    if (raf) cancelAnimationFrame(raf);
    raf = requestAnimationFrame(tick);
  }

  // Constants tuned for ~50 nodes max
  const REPEL = 1800;
  const LINK_DIST = 120;
  const LINK_K = 0.04;
  const GRAVITY = 0.012;
  const DAMPING = 0.82;
  const MAX_TICKS = 600;

  function tick() {
    if (nodes.length === 0) return;
    // Repulsion: O(n²) is fine for our sizes.
    for (let i = 0; i < nodes.length; i++) {
      const a = nodes[i];
      for (let j = i + 1; j < nodes.length; j++) {
        const b = nodes[j];
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
    for (const l of links) {
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
    // Gravity toward centre
    for (const n of nodes) {
      n.vx += (CENTER_X - n.x) * GRAVITY;
      n.vy += (CENTER_Y - n.y) * GRAVITY;
      n.vx *= DAMPING;
      n.vy *= DAMPING;
      if (!n.fixed) {
        n.x += n.vx;
        n.y += n.vy;
      }
      // Soft bounds
      n.x = Math.max(40, Math.min(W - 40, n.x));
      n.y = Math.max(40, Math.min(H - 40, n.y));
    }
    // Force-publish a new array reference so the {#each} block re-renders.
    // Mutating n.x/n.y on existing items isn't enough because the state
    // proxy doesn't observe property writes on the contained objects when
    // they're plain (non-state) records.
    nodes = [...nodes];
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

  onDestroy(() => {
    if (raf) cancelAnimationFrame(raf);
  });

  // ---------- drag ----------
  let dragging: SimNode | null = null;
  let svgEl: SVGSVGElement | null = $state(null);

  function svgPoint(e: MouseEvent | TouchEvent): { x: number; y: number } {
    if (!svgEl) return { x: 0, y: 0 };
    const rect = svgEl.getBoundingClientRect();
    const p = 'touches' in e ? e.touches[0] : e;
    const sx = (p.clientX - rect.left) * (W / rect.width);
    const sy = (p.clientY - rect.top) * (H / rect.height);
    return { x: sx, y: sy };
  }

  function onMouseDown(node: SimNode, e: MouseEvent) {
    e.stopPropagation();
    selected = { kind: node.kind, id: node.id };
    dragging = node;
    node.fixed = true;
    reheat();
  }

  function onMouseMove(e: MouseEvent) {
    if (!dragging) return;
    const p = svgPoint(e);
    dragging.x = p.x;
    dragging.y = p.y;
    nodes = [...nodes];
  }

  function onMouseUp() {
    if (dragging) {
      dragging.fixed = false;
      dragging = null;
    }
  }

  function onBackgroundClick() {
    selected = null;
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

  // For the side panel
  const selectedNode = $derived(selected ? nodeMap.get(selected.id) ?? null : null);
  const selectedLinks = $derived(
    selected
      ? links.filter((l) => l.source === selected!.id || l.target === selected!.id)
      : []
  );

  function neighbourLabel(linkSourceOrTarget: string): string {
    const n = nodeMap.get(linkSourceOrTarget);
    return n ? n.label : linkSourceOrTarget.slice(0, 12);
  }

  // Initial fetch — call directly, not via $effect, so we don't accidentally
  // turn the load() body into a tracked dep graph.
  onMount(() => {
    load();
  });

  // Re-layout on system-network filter toggle. We track *only* showSystem
  // and call buildSimulation in untrack() so its internal reads of `topo`
  // don't become deps of this effect (which would form a cycle the moment
  // buildSimulation writes to nodes/links).
  let prevShowSystem = showSystem;
  $effect(() => {
    const current = showSystem;
    if (current === prevShowSystem) return;
    prevShowSystem = current;
    untrack(() => {
      if (topo) buildSimulation();
    });
  });
</script>

<svelte:window onmousemove={onMouseMove} onmouseup={onMouseUp} />

<section class="space-y-4">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Network topology</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Live graph of docker networks and the containers attached to them. Drag nodes to rearrange.
      </p>
    </div>
    <div class="flex items-center gap-2">
      <Button variant="secondary" size="sm" onclick={() => (showSystem = !showSystem)}>
        {#if showSystem}<EyeOff class="w-3.5 h-3.5" />{:else}<Eye class="w-3.5 h-3.5" />{/if}
        {showSystem ? 'Hide' : 'Show'} system
      </Button>
      <Button variant="secondary" size="sm" onclick={reheat}>Reheat</Button>
      <Button variant="secondary" size="sm" onclick={load}>
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
      <Card class="lg:col-span-3 overflow-hidden">
        <svg
          bind:this={svgEl}
          viewBox="0 0 {W} {H}"
          class="w-full h-[70vh] cursor-default"
          onclick={onBackgroundClick}
        >
          <!-- Edges -->
          <g stroke="var(--border-strong)" stroke-width="1.5" fill="none">
            {#each links as l}
              {@const s = nodeMap.get(l.source)}
              {@const t = nodeMap.get(l.target)}
              {#if s && t}
                {@const isSel = selected && (selected.id === l.source || selected.id === l.target)}
                <line
                  x1={s.x}
                  y1={s.y}
                  x2={t.x}
                  y2={t.y}
                  stroke={isSel ? 'var(--color-brand-400)' : 'var(--border-strong)'}
                  stroke-width={isSel ? 2 : 1.2}
                  opacity={selected && !isSel ? 0.25 : 0.7}
                />
              {/if}
            {/each}
          </g>

          <!-- Nodes -->
          {#each nodes as n (n.id)}
            {@const isSel = selected?.id === n.id}
            {@const dim = selected && !isSel && !selectedLinks.some((l) => l.source === n.id || l.target === n.id)}
            <g
              transform="translate({n.x},{n.y})"
              opacity={dim ? 0.3 : 1}
              class="cursor-grab active:cursor-grabbing"
              onmousedown={(e) => onMouseDown(n, e)}
              role="button"
              tabindex="0"
            >
              {#if n.kind === 'network'}
                <circle
                  r={n.radius}
                  fill="color-mix(in srgb, {networkColor(n.data as TopoNetwork)} 25%, var(--bg-elevated))"
                  stroke={networkColor(n.data as TopoNetwork)}
                  stroke-width={isSel ? 3 : 2}
                />
                <text
                  text-anchor="middle"
                  dy="4"
                  font-size="10"
                  font-weight="600"
                  fill="var(--fg)"
                  font-family="var(--font-mono)"
                  pointer-events="none"
                >NET</text>
                <text
                  text-anchor="middle"
                  y={n.radius + 14}
                  font-size="11"
                  fill="var(--fg)"
                  font-family="var(--font-mono)"
                  pointer-events="none"
                >{n.label}</text>
              {:else}
                <circle
                  r={n.radius}
                  fill="color-mix(in srgb, {containerColor(n.data as TopoContainer)} 30%, var(--bg-elevated))"
                  stroke={containerColor(n.data as TopoContainer)}
                  stroke-width={isSel ? 2.5 : 1.5}
                />
                <text
                  text-anchor="middle"
                  y={n.radius + 12}
                  font-size="10"
                  fill="var(--fg-muted)"
                  font-family="var(--font-mono)"
                  pointer-events="none"
                >{n.label.length > 18 ? n.label.slice(0, 17) + '…' : n.label}</text>
              {/if}
            </g>
          {/each}
        </svg>

        <!-- Legend -->
        <div class="flex items-center gap-4 px-5 py-3 border-t border-[var(--border)] text-xs text-[var(--fg-muted)]">
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#06b6d4"></span>bridge / user network</div>
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#a855f7"></span>overlay</div>
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#22c55e"></span>running container</div>
          <div class="flex items-center gap-1.5"><span class="w-3 h-3 rounded-full" style="background:#6b7280"></span>stopped / system</div>
          <div class="ml-auto">{nodes.length} nodes · {links.length} edges</div>
        </div>
      </Card>

      <!-- Side panel -->
      <Card class="p-5 lg:col-span-1">
        {#if !selectedNode}
          <h3 class="font-semibold text-sm mb-2">Selection</h3>
          <p class="text-xs text-[var(--fg-muted)]">Click a node to inspect it.</p>
          <div class="mt-4 space-y-2 text-xs text-[var(--fg-muted)]">
            <div>{topo.networks.length} network(s) total</div>
            <div>{topo.containers.length} container(s) total</div>
            <div>{topo.links.length} link(s) total</div>
          </div>
        {:else if selectedNode.kind === 'network'}
          {@const n = selectedNode.data as TopoNetwork}
          <h3 class="font-semibold text-sm mb-2 flex items-center gap-2">
            {n.name}
            {#if n.system}<Badge variant="default">system</Badge>{/if}
          </h3>
          <div class="space-y-2 text-xs">
            <div><span class="text-[var(--fg-muted)]">Driver:</span> <span class="font-mono">{n.driver}</span></div>
            <div><span class="text-[var(--fg-muted)]">Scope:</span> <span class="font-mono">{n.scope}</span></div>
            {#if n.internal}<Badge variant="warning">internal</Badge>{/if}
            {#if n.stack}<div><span class="text-[var(--fg-muted)]">Stack:</span> <Badge variant="info">{n.stack}</Badge></div>{/if}
            <div class="font-mono text-[var(--fg-subtle)] break-all">{n.id.slice(0, 12)}</div>
          </div>
          <div class="mt-4">
            <div class="text-xs text-[var(--fg-muted)] mb-2">{selectedLinks.length} attached container(s)</div>
            <div class="space-y-1 max-h-64 overflow-auto">
              {#each selectedLinks as l}
                {@const cid = l.source === selectedNode.id ? l.target : l.source}
                <button
                  class="block w-full text-left px-2 py-1 text-xs font-mono rounded hover:bg-[var(--surface-hover)]"
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
          <div class="space-y-2 text-xs">
            <div class="font-mono truncate text-[var(--fg-muted)]">{c.image}</div>
            {#if c.stack}<div><span class="text-[var(--fg-muted)]">Stack:</span> <Badge variant="info">{c.stack}</Badge></div>{/if}
            <div class="font-mono text-[var(--fg-subtle)] break-all">{c.id.slice(0, 12)}</div>
          </div>
          <div class="mt-4">
            <div class="text-xs text-[var(--fg-muted)] mb-2">{selectedLinks.length} network(s)</div>
            <div class="space-y-1 max-h-48 overflow-auto">
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
