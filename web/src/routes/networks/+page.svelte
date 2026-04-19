<script lang="ts">
  import { goto } from '$app/navigation';
  import * as dagre from '@dagrejs/dagre';
  import {
    api,
    ApiError,
    isFanOut,
    type Topology,
    type TopoNetwork,
    type TopoContainer
  } from '$lib/api';
  import { Card, Button, Skeleton, EmptyState, Badge, Modal, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import {
    Network as NetworkIcon,
    RefreshCw,
    Eye,
    EyeOff,
    Search,
    ZoomIn,
    ZoomOut,
    Maximize2,
    GitBranch,
    Server,
    Plus,
    Trash2,
    ChevronUp,
    ChevronDown,
    Lock,
    Shield
  } from 'lucide-svelte';

  const canWrite = $derived(allowed('network.write'));
  const isAll = $derived(hosts.isAll);

  // ---------- view mode ----------
  let showTopology = $state(false);

  // ---------- list data ----------
  interface NetworkRow {
    Name: string;
    Id: string;
    Driver: string;
    Scope: string;
    Internal: boolean;
    Attachable: boolean;
    IPAM?: { Config?: Array<{ Subnet?: string; Gateway?: string }> };
    Containers?: Record<string, any>;
    Labels?: Record<string, string>;
    Created?: string;
    host_id?: string;
    host_name?: string;
  }
  let networkList = $state<NetworkRow[]>([]);
  let listLoading = $state(false);
  let listSearch = $state('');
  let showSystem = $state(false);

  // Sort
  type SortKey = 'name' | 'driver' | 'scope' | 'containers';
  let sortKey = $state<SortKey>('name');
  let sortAsc = $state(true);

  // Bulk
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);

  // Create modal
  let showCreate = $state(false);
  let newName = $state('');
  let newDriver = $state('bridge');
  let newSubnet = $state('');
  let newInternal = $state(false);
  let creating = $state(false);

  const systemNetworks = new Set(['bridge', 'host', 'none']);

  async function loadList() {
    listLoading = true;
    try {
      const raw = await api.networks.list(hosts.id);
      if (isFanOut(raw)) {
        networkList = raw.items as NetworkRow[];
      } else {
        networkList = (raw as any[]) as NetworkRow[];
      }
    } catch (err) {
      toast.error('Failed to load networks', err instanceof ApiError ? err.message : undefined);
    } finally {
      listLoading = false;
    }
  }

  const visibleNetworks = $derived(
    networkList
      .filter(n => {
        if (!showSystem && systemNetworks.has(n.Name)) return false;
        if (!listSearch.trim()) return true;
        const q = listSearch.toLowerCase();
        return n.Name.toLowerCase().includes(q) || n.Driver.toLowerCase().includes(q)
          || (n.IPAM?.Config?.[0]?.Subnet ?? '').includes(q);
      })
      .sort((a, b) => {
        let cmp = 0;
        switch (sortKey) {
          case 'name': cmp = a.Name.localeCompare(b.Name); break;
          case 'driver': cmp = a.Driver.localeCompare(b.Driver); break;
          case 'scope': cmp = a.Scope.localeCompare(b.Scope); break;
          case 'containers': cmp = containerCount(a) - containerCount(b); break;
        }
        return sortAsc ? cmp : -cmp;
      })
  );

  const allSelected = $derived(visibleNetworks.length > 0 && visibleNetworks.every(n => selected.has(n.Id)));
  function toggleAll() {
    if (allSelected) { selected = new Set(); }
    else { selected = new Set(visibleNetworks.map(n => n.Id)); }
  }
  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  function toggleSort(key: SortKey) {
    if (sortKey === key) { sortAsc = !sortAsc; }
    else { sortKey = key; sortAsc = true; }
  }

  function containerCount(n: NetworkRow): number {
    return n.Containers ? Object.keys(n.Containers).length : 0;
  }
  function subnet(n: NetworkRow): string {
    return n.IPAM?.Config?.[0]?.Subnet ?? '—';
  }
  function gateway(n: NetworkRow): string {
    return n.IPAM?.Config?.[0]?.Gateway ?? '—';
  }
  function stackOf(n: NetworkRow): string | null {
    return n.Labels?.['com.docker.compose.project'] ?? null;
  }

  // Actions
  async function createNetwork(e: Event) {
    e.preventDefault();
    creating = true;
    try {
      await api.networks.create(newName, newDriver);
      toast.success('Network created', newName);
      showCreate = false;
      newName = '';
      await loadList();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }
  async function deleteNetwork(n: NetworkRow) {
    if (!(await confirm.ask({ title: 'Delete network', message: `Delete network "${n.Name}"?`, body: 'Docker refuses the delete if any container is still attached — disconnect those first.', confirmLabel: 'Delete', danger: true }))) return;
    try {
      await api.networks.remove(n.Id);
      toast.success('Deleted', n.Name);
      await loadList();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkDelete() {
    if (!(await confirm.ask({ title: 'Delete networks', message: `Delete ${selected.size} network(s)?`, body: 'Networks with attached containers are skipped with an error; the rest are removed.', confirmLabel: 'Delete', danger: true }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const n of networkList.filter(n => selected.has(n.Id))) {
      try { await api.networks.remove(n.Id); ok++; } catch { fail++; }
    }
    toast.success(`Deleted: ${ok}${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await loadList();
  }
  async function pruneNetworks() {
    if (!(await confirm.ask({ title: 'Prune unused networks', message: 'Remove all networks with no attached containers?', body: 'This cannot be undone. Default Docker networks (bridge, host, none) are always kept.', confirmLabel: 'Prune', danger: true }))) return;
    try {
      const res = await api.networks.prune();
      toast.success('Pruned', `${res?.NetworksDeleted?.length ?? 0} network(s) removed`);
      await loadList();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  $effect(() => {
    hosts.id;
    loadList();
  });

  // ---------- topology tab data ----------
  let topo = $state<Topology | null>(null);
  let loading = $state(true);
  let topoShowSystem = $state(false);
  let topoSelected = $state<{ kind: 'network' | 'container'; id: string } | null>(null);
  let hovered = $state<{ id: string; clientX: number; clientY: number } | null>(null);
  let topoSearch = $state('');
  let connStatus = $state<ConnStatus>('connecting');
  const live = $derived(connStatus === 'live');
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  // ---------- node sizing ----------
  // The dagre layout treats each node as a fixed-size rectangle. We give it
  // dimensions that match the SVG visuals (circle + bottom labels) so the
  // resulting placement leaves enough room for everything we draw.
  const NETWORK_W = 110;
  const NETWORK_H = 100;
  const CONTAINER_W = 150;
  const CONTAINER_H = 90;
  const NETWORK_R = 28;
  const CONTAINER_R = 18;

  // ---------- laid out scene ----------
  type LaidNode = {
    id: string;
    kind: 'network' | 'container';
    label: string;
    x: number;
    y: number;
    data: TopoNetwork | TopoContainer;
  };
  type LaidEdge = {
    sid: string;
    tid: string;
    points: { x: number; y: number }[];
  };
  type StackBox = {
    name: string;
    x: number;
    y: number;
    w: number;
    h: number;
    color: string;
  };

  let scene = $state<{
    nodes: LaidNode[];
    edges: LaidEdge[];
    stacks: StackBox[];
    width: number;
    height: number;
  }>({
    nodes: [],
    edges: [],
    stacks: [],
    width: 0,
    height: 0
  });

  // Lookup map (rebuilt with each layout)
  let nodeIndex = new Map<string, LaidNode>();

  function stackColor(i: number): string {
    const palette = ['#06b6d4', '#a855f7', '#f59e0b', '#ec4899', '#10b981', '#3b82f6', '#f97316'];
    return palette[i % palette.length];
  }

  // ---------- the layout ----------
  // Build a dagre compound graph: each compose stack becomes a parent node
  // with its network + containers as children. Standalone networks /
  // containers go at the root level. dagre runs Sugiyama layered layout
  // and gives us non-overlapping rectangles by construction.
  function relayout() {
    if (!topo) {
      scene = { nodes: [], edges: [], stacks: [], width: 0, height: 0 };
      nodeIndex.clear();
      return;
    }

    const nets = topoShowSystem ? topo.networks : topo.networks.filter((n) => !n.system);
    const netIds = new Set(nets.map((n) => n.id));

    // Only containers that participate in at least one visible network
    const usedContainerIds = new Set<string>();
    for (const l of topo.links) {
      if (netIds.has(l.network_id)) usedContainerIds.add(l.container_id);
    }
    const conts = topo.containers.filter((c) => usedContainerIds.has(c.id));

    const g = new dagre.graphlib.Graph({ compound: true, multigraph: false });
    g.setGraph({
      rankdir: 'TB',
      nodesep: 30,
      ranksep: 55,
      marginx: 20,
      marginy: 20
    });
    g.setDefaultEdgeLabel(() => ({}));

    // Discover stacks and create one compound parent per stack
    const stackNames = new Set<string>();
    for (const n of nets) if (n.stack) stackNames.add(n.stack);
    for (const c of conts) if (c.stack) stackNames.add(c.stack);
    const stackList = [...stackNames].sort();
    const stackKey = (name: string) => `stack:${name}`;
    for (const name of stackList) {
      g.setNode(stackKey(name), {
        label: name,
        clusterLabelPos: 'top',
        // Padding pushes children away from the parent's borders so
        // there's room for the dashed rect + label we draw on top of it.
        padding: 18
      });
    }

    // Add network nodes
    for (const n of nets) {
      g.setNode(n.id, {
        label: n.name,
        width: NETWORK_W,
        height: NETWORK_H,
        kind: 'network',
        data: n
      });
      if (n.stack) g.setParent(n.id, stackKey(n.stack));
    }

    // Add container nodes
    for (const c of conts) {
      g.setNode(c.id, {
        label: c.name,
        width: CONTAINER_W,
        height: CONTAINER_H,
        kind: 'container',
        data: c
      });
      if (c.stack) g.setParent(c.id, stackKey(c.stack));
    }

    // Add edges (network → container)
    for (const l of topo.links) {
      if (netIds.has(l.network_id) && usedContainerIds.has(l.container_id)) {
        g.setEdge(l.network_id, l.container_id);
      }
    }

    // Run layout
    dagre.layout(g);

    // Read back positions
    const nodes: LaidNode[] = [];
    const edges: LaidEdge[] = [];
    const stacks: StackBox[] = [];
    let stackIdx = 0;

    for (const id of g.nodes()) {
      const meta: any = g.node(id);
      if (id.startsWith('stack:')) {
        // dagre returns center coords + dims for compound nodes
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
      nodes.push({
        id,
        kind: meta.kind,
        label: meta.label,
        x: meta.x,
        y: meta.y,
        data: meta.data
      });
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

    scene = {
      nodes,
      edges,
      stacks,
      width: graphMeta.width || 800,
      height: graphMeta.height || 600
    };

    // Reset viewport on first layout if zoomed too far in
    if (zoom === 1 && panX === 0 && panY === 0) {
      fitToView();
    }
  }

  // ---------- viewport ----------
  let panX = $state(0);
  let panY = $state(0);
  let zoom = $state(1);
  let svgEl: SVGSVGElement | null = $state(null);
  // `panning` needs $state so the cursor style reacts to mouse-down/up —
  // without it Svelte warns and any derived CSS class wouldn't update.
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
    // Only pan when grabbing the background, not a node.
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

  function onWindowMouseUp() {
    panning = false;
  }

  function onSvgClick(e: MouseEvent) {
    const target = e.target as Element;
    if (target === svgEl || target.classList?.contains('bg-rect')) {
      topoSelected = null;
    }
  }

  function resetView() {
    fitToView();
  }
  function zoomIn() {
    if (!svgEl) return;
    const rect = svgEl.getBoundingClientRect();
    const cx = rect.width / 2;
    const cy = rect.height / 2;
    const newZoom = Math.min(3, zoom * 1.2);
    panX = cx - (cx - panX) * (newZoom / zoom);
    panY = cy - (cy - panY) * (newZoom / zoom);
    zoom = newZoom;
  }
  function zoomOut() {
    if (!svgEl) return;
    const rect = svgEl.getBoundingClientRect();
    const cx = rect.width / 2;
    const cy = rect.height / 2;
    const newZoom = Math.max(0.3, zoom / 1.2);
    panX = cx - (cx - panX) * (newZoom / zoom);
    panY = cy - (cy - panY) * (newZoom / zoom);
    zoom = newZoom;
  }

  // ---------- interaction ----------
  function onNodeClick(node: LaidNode, e: MouseEvent) {
    e.stopPropagation();
    topoSelected = { kind: node.kind, id: node.id };
  }

  function onNodeMouseEnter(node: LaidNode, e: MouseEvent) {
    hovered = { id: node.id, clientX: e.clientX, clientY: e.clientY };
  }
  function onNodeMouseLeave() {
    hovered = null;
  }
  function onNodeMouseMove(e: MouseEvent) {
    if (hovered) hovered = { ...hovered, clientX: e.clientX, clientY: e.clientY };
  }

  // ---------- search filter ----------
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

  // ---------- styling ----------
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
  const selectedNode = $derived(topoSelected ? nodeIndex.get(topoSelected.id) ?? null : null);
  const selectedLinks = $derived(
    selected
      ? scene.edges.filter((e) => e.sid === topoSelected!.id || e.tid === topoSelected!.id)
      : []
  );
  const hoveredNode = $derived(hovered ? nodeIndex.get(hovered.id) ?? null : null);

  function neighbourLabel(id: string): string {
    const n = nodeIndex.get(id);
    return n ? n.label : id.slice(0, 12);
  }

  // ---------- edge path ----------
  // dagre returns a list of waypoints; render them as a smooth curve via
  // SVG path commands. For 2 points we use a straight line; for ≥3 we use
  // a quadratic curve through the midpoints (catmull-rom-ish smoothing).
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

  // ---------- load + live ----------
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

  function disconnectLive() {
    stream.stop();
    if (reloadTimer) clearTimeout(reloadTimer);
  }

  $effect(() => {
    load();
    stream.start();
    return disconnectLive;
  });

  // Re-layout when the show-system filter toggles. topo doesn't change,
  // only the included subset does. `prevShowSystem` is initialised to
  // null and set on first effect run so svelte-check doesn't flag it as
  // a stale module-level snapshot of a reactive.
  {
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
  }
</script>

<svelte:window onmousemove={onWindowMouseMove} onmouseup={onWindowMouseUp} />

<section class="space-y-4">
  <!-- Header -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Networks</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {networkList.length} network{networkList.length === 1 ? '' : 's'}
        {#if isAll}across all hosts{:else if hosts.selected?.name && hosts.id !== 'local'}on {hosts.selected.name}{/if}
      </p>
    </div>
    <div class="flex items-center gap-2">
      <button
        class="dm-btn dm-btn-sm {showTopology ? 'dm-btn-primary' : 'dm-btn-secondary'}"
        onclick={() => (showTopology = !showTopology)}
        title="Toggle topology view"
      >
        <GitBranch class="w-3.5 h-3.5" /> Topology
      </button>
      {#if canWrite}
        <Button variant="secondary" size="sm" onclick={pruneNetworks}>
          <Trash2 class="w-3.5 h-3.5" /> Prune
        </Button>
        <Button variant="primary" size="sm" onclick={() => (showCreate = true)}>
          <Plus class="w-3.5 h-3.5" /> Create
        </Button>
      {/if}
      <Button variant="secondary" size="sm" onclick={loadList}>
        <RefreshCw class="w-3.5 h-3.5 {listLoading ? 'animate-spin' : ''}" /> Refresh
      </Button>
    </div>
  </div>

  <!-- Search + filters -->
  {#if networkList.length > 0}
    <div class="flex flex-wrap items-center gap-3">
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input type="search" placeholder="Search by name, driver, subnet…" bind:value={listSearch} class="dm-input pl-8 pr-3 py-1.5 text-sm w-full" />
      </div>
      <label class="flex items-center gap-2 text-xs text-[var(--fg-muted)] cursor-pointer">
        <input type="checkbox" bind:checked={showSystem} class="accent-[var(--color-brand-500)]" />
        system networks
      </label>
    </div>
  {/if}

  <!-- Bulk action bar -->
  {#if selected.size > 0 && canWrite}
    <div class="flex items-center gap-3 px-4 py-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-sm font-medium">{selected.size} selected</span>
      <div class="flex gap-1.5 ml-auto">
        <Button size="xs" variant="danger" onclick={bulkDelete} disabled={bulkBusy}>
          <Trash2 class="w-3.5 h-3.5" /> Delete
        </Button>
        <button class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] ml-2" onclick={() => (selected = new Set())}>Clear</button>
      </div>
    </div>
  {/if}

  <!-- Table -->
  {#if listLoading && networkList.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(5) as _}
          <div class="px-5 py-3.5 flex items-center gap-4">
            <Skeleton width="1rem" height="1rem" />
            <Skeleton width="30%" height="0.85rem" />
            <Skeleton width="15%" height="0.75rem" />
          </div>
        {/each}
      </div>
    </Card>
  {:else if networkList.length === 0}
    <Card>
      <EmptyState icon={NetworkIcon} title="No networks" description="Docker networks will appear here when containers are running." />
    </Card>
  {:else if visibleNetworks.length === 0}
    <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No networks match this search.</Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              {#if canWrite}
                <th class="w-10 px-3 py-3">
                  <input type="checkbox" checked={allSelected} onchange={toggleAll} class="accent-[var(--color-brand-500)]" />
                </th>
              {/if}
              {#snippet sortHeader(key: SortKey, label: string)}
                <th class="text-left px-3 py-3 cursor-pointer select-none hover:text-[var(--fg)]" onclick={() => toggleSort(key)}>
                  <span class="inline-flex items-center gap-1">
                    {label}
                    {#if sortKey === key}
                      {#if sortAsc}<ChevronUp class="w-3 h-3" />{:else}<ChevronDown class="w-3 h-3" />{/if}
                    {/if}
                  </span>
                </th>
              {/snippet}
              {@render sortHeader('name', 'Name')}
              <th class="text-left px-3 py-3">ID</th>
              <th class="text-left px-3 py-3">Stack</th>
              {@render sortHeader('driver', 'Driver')}
              {@render sortHeader('scope', 'Scope')}
              <th class="text-left px-3 py-3">Subnet</th>
              <th class="text-left px-3 py-3">Gateway</th>
              <th class="text-center px-3 py-3" title="Internal">Int</th>
              {@render sortHeader('containers', 'Containers')}
              {#if isAll}
                <th class="text-left px-3 py-3">Host</th>
              {/if}
              {#if canWrite}
                <th class="text-right px-3 py-3 w-20">Actions</th>
              {/if}
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visibleNetworks as n (n.Id || n.Name)}
              {@const stack = stackOf(n)}
              {@const isSystem = systemNetworks.has(n.Name)}
              <tr class="hover:bg-[var(--surface-hover)] transition-colors {selected.has(n.Id) ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_5%,transparent)]' : ''}">
                {#if canWrite}
                  <td class="w-10 px-3 py-2.5">
                    {#if !isSystem}
                      <input type="checkbox" checked={selected.has(n.Id)} onchange={() => toggleOne(n.Id)} class="accent-[var(--color-brand-500)]" />
                    {/if}
                  </td>
                {/if}
                <td class="px-3 py-2.5">
                  <span class="font-mono text-sm truncate block max-w-[180px]" title={n.Name}>{n.Name}</span>
                </td>
                <td class="px-3 py-2.5 text-[10px] text-[var(--fg-muted)] font-mono">{(n.Id ?? '').slice(0, 12)}</td>
                <td class="px-3 py-2.5">
                  {#if stack}
                    <a href="/stacks/{stack}" class="text-xs text-[var(--color-brand-400)] hover:underline font-mono">{stack}</a>
                  {:else}
                    <span class="text-xs text-[var(--fg-subtle)]">—</span>
                  {/if}
                </td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{n.Driver}</td>
                <td class="px-3 py-2.5"><Badge variant="default">{n.Scope}</Badge></td>
                <td class="px-3 py-2.5 font-mono text-xs text-[var(--fg-muted)]">{subnet(n)}</td>
                <td class="px-3 py-2.5 font-mono text-xs text-[var(--fg-muted)]">{gateway(n)}</td>
                <td class="px-3 py-2.5 text-center">
                  {#if n.Internal}<span title="Internal network"><Lock class="w-3 h-3 text-[var(--color-warning-400)] inline" /></span>{/if}
                </td>
                <td class="px-3 py-2.5 text-center tabular-nums">{containerCount(n)}</td>
                {#if isAll}
                  <td class="px-3 py-2.5">
                    <span class="inline-flex items-center gap-1 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                      <Server class="w-2.5 h-2.5" />
                      {n.host_name || n.host_id || 'local'}
                    </span>
                  </td>
                {/if}
                {#if canWrite}
                  <td class="px-3 py-2.5 text-right">
                    {#if !isSystem}
                      <button
                        class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]"
                        title="Delete network"
                        onclick={() => deleteNetwork(n)}
                      >
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    {/if}
                  </td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}

  <!-- Topology (inline toggle, not a tab) -->
  {#if showTopology}
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h3 class="text-lg font-semibold tracking-tight flex items-center gap-2">
        Topology
        {#if live}
          <span class="inline-flex items-center gap-1 text-xs text-[var(--color-success-400)] font-normal">
            <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)] animate-pulse"></span>live
          </span>
        {:else if connStatus === 'reconnecting'}
          <span class="inline-flex items-center gap-1 text-xs text-[var(--color-warning-400)] font-normal">
            <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-warning-500)] animate-pulse"></span>reconnecting…
          </span>
        {/if}
      </h3>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Hierarchical layout via dagre. Drag to pan, scroll to zoom.
      </p>
    </div>
    <div class="flex items-center gap-2 flex-wrap">
      <div class="relative">
        <Search class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--fg-subtle)] pointer-events-none" />
        <input
          type="text"
          bind:value={topoSearch}
          placeholder="Search…"
          class="dm-input !py-1.5 !pl-8 !pr-3 text-sm w-48"
        />
      </div>
      <Button variant="secondary" size="sm" onclick={() => (topoShowSystem = !topoShowSystem)}>
        {#if topoShowSystem}<EyeOff class="w-3.5 h-3.5" />{:else}<Eye class="w-3.5 h-3.5" />{/if}
        {topoShowSystem ? 'Hide' : 'Show'} system
      </Button>
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
      <Card class="lg:col-span-3 overflow-hidden relative">
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
            aria-label="Fit to view"
            title="Fit to view"
          >
            <Maximize2 class="w-3.5 h-3.5" />
          </button>
          <div class="text-[10px] text-center text-[var(--fg-subtle)] font-mono px-1">
            {(zoom * 100).toFixed(0)}%
          </div>
        </div>

        <!-- The graph is a pan+zoom+click interaction surface that doesn't
             have a keyboard equivalent (panning a graph with arrow keys is
             a separate feature). role="application" tells assistive tech
             this is a complex widget; individual nodes below have
             role="button" + onkeydown for keyboard-accessible selection. -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <svg
          bind:this={svgEl}
          class="w-full h-[72vh] {panning ? 'cursor-grabbing' : 'cursor-grab'} block"
          onmousedown={onSvgMouseDown}
          onclick={onSvgClick}
          onwheel={onWheel}
          role="application"
          aria-label="Network topology graph"
        >
          <rect class="bg-rect" x="0" y="0" width="100%" height="100%" fill="transparent" />

          <g style="transform: translate({panX}px, {panY}px) scale({zoom}); transform-origin: 0 0; transition: transform 0ms;">
            <!-- Stack groups (rendered behind nodes) -->
            {#each scene.stacks as box (box.name)}
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
                <rect
                  x={box.x + 10}
                  y={box.y - 12}
                  width={box.name.length * 8 + 16}
                  height="22"
                  rx="6"
                  fill="var(--bg-elevated)"
                  stroke="color-mix(in srgb, {box.color} 50%, transparent)"
                  stroke-width="1"
                  pointer-events="none"
                />
                <text
                  x={box.x + 18}
                  y={box.y + 4}
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
                style="transform: translate({n.x}px, {n.y}px); transform-box: fill-box; transition: transform 300ms ease, opacity 200ms;"
                opacity={dim ? 0.15 : 1}
                class="cursor-pointer"
                onclick={(e) => onNodeClick(n, e)}
                onkeydown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onNodeClick(n, e as unknown as MouseEvent);
                  }
                }}
                onmouseenter={(e) => onNodeMouseEnter(n, e)}
                onmouseleave={onNodeMouseLeave}
                onmousemove={onNodeMouseMove}
                role="button"
                tabindex="0"
                aria-label="{n.kind === 'network' ? 'Network' : 'Container'} {n.id}"
              >
                {#if n.kind === 'network'}
                  {@const net = n.data as TopoNetwork}
                  <circle
                    r={NETWORK_R}
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
                    y={NETWORK_R + 14}
                    font-size="11"
                    font-weight="500"
                    fill="var(--fg)"
                    font-family="var(--font-mono)"
                    pointer-events="none"
                  >{net.name}</text>
                {:else}
                  {@const c = n.data as TopoContainer}
                  <circle
                    r={CONTAINER_R}
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
                    y={CONTAINER_R + 13}
                    font-size="10"
                    font-weight="500"
                    fill="var(--fg)"
                    font-family="var(--font-mono)"
                    pointer-events="none"
                  >{c.name.length > 22 ? c.name.slice(0, 21) + '…' : c.name}</text>
                  {#if c.ports && c.ports.length > 0}
                    <text
                      text-anchor="middle"
                      y={CONTAINER_R + 25}
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
          <div class="ml-auto">{scene.nodes.length} nodes · {scene.edges.length} edges{#if scene.stacks.length > 0} · {scene.stacks.length} stacks{/if}</div>
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
                {@const cid = l.sid === selectedNode.id ? l.tid : l.sid}
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
                {@const nid = l.sid === selectedNode.id ? l.tid : l.sid}
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
  {/if}
</section>

<!-- Create network modal -->
<Modal bind:open={showCreate} title="Create network" maxWidth="max-w-sm">
  <form onsubmit={createNetwork} id="create-net-form" class="space-y-3">
    <Input label="Name" placeholder="my-network" bind:value={newName} disabled={creating} />
    <Input label="Driver" bind:value={newDriver} disabled={creating} hint="bridge, overlay, macvlan, host, none" />
    <Input label="Subnet (optional)" placeholder="172.20.0.0/16" bind:value={newSubnet} disabled={creating} />
    <label class="flex items-center gap-2 text-sm cursor-pointer">
      <input type="checkbox" bind:checked={newInternal} class="accent-[var(--color-brand-500)]" />
      <span>Internal (no external access)</span>
    </label>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="create-net-form" loading={creating} disabled={creating || !newName.trim()}>Create</Button>
  {/snippet}
</Modal>
