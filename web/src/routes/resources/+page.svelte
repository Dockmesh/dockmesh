<script lang="ts">
  // Resources — Images / Volumes / Networks combined.
  // Editorial rebuild of the three legacy pages in one shell. Mockup:
  // `Dockmesh Wizard (6)/resources.jsx`. Tab strip switches the body;
  // the global host picker (topbar) drives the host filter for every
  // tab. Per-tab: SummaryStrip metrics, FilterPills + Search toolbar,
  // resource-table with bulk actions, then per-tab specifics.
  //
  // We intentionally consolidated: the old /images, /volumes, /networks
  // routes still exist as deep-link fallbacks, but the sidebar entry
  // is now a single "Resources". Topology preview footer in Networks
  // links to the existing /networks?topology=1 graph until that gets
  // its own slice.
  import { page } from '$app/stores';
  import {
    api, ApiError, isFanOut,
    type ScanReport, type Severity
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Skeleton, EmptyState, Modal, Button, Input, Badge
  } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import {
    Image as ImageIcon, HardDrive, Network as NetworkIcon,
    Search, Trash2, Download, Plus, Shield, Check,
    AlertTriangle, Package, ArrowUpRight
  } from 'lucide-svelte';

  // Same pattern as /users (Users · Roles tab strip): URL ?tab=… is the
  // initial deep-link only; once mounted, tab is plain $state and the
  // tab buttons set it directly. No URL syncing on click, no goto, no
  // $effect — just like the rest of the app.
  type Tab = 'images' | 'volumes' | 'networks';
  let tab = $state<Tab>(((new URLSearchParams($page.url.search).get('tab')) as Tab) || 'images');

  // ───────── Permissions
  const canImageCreate = $derived(allowed('images.create'));
  const canImageDelete = $derived(allowed('images.delete'));
  const canImageScan   = $derived(allowed('images.scan'));
  const canVolCreate   = $derived(allowed('volumes.create'));
  const canVolDelete   = $derived(allowed('volumes.delete'));
  const canNetCreate   = $derived(allowed('networks.create'));
  const canNetDelete   = $derived(allowed('networks.delete'));

  // ───────── Common state
  const isAll = $derived(hosts.isAll);

  // ===================================================================
  //  Images
  // ===================================================================
  interface ImageSummary {
    Id: string;
    RepoTags: string[] | null;
    Size: number;
    Created: number;
    host_id?: string;
    host_name?: string;
  }
  let images = $state<ImageSummary[]>([]);
  let imagesLoading = $state(true);
  let imagesUnreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let usedImageIds = $state<Set<string>>(new Set());
  let imageSearch = $state('');
  let imageFilter = $state<'all' | 'used' | 'unused' | 'dangling' | 'vulnerable'>('all');
  let imageSelected = $state<Set<string>>(new Set());
  // scanCache holds the most recent scan summary per image-ref (RepoTags[0]
  // or sha256 id when the image is dangling). Populated lazily on image
  // list load — every visible row gets a GetScan probe; 404 means
  // "never scanned" and we render "not scanned" in the cell. Updated
  // in-place when a fresh scan completes.
  let scanCache = $state<Map<string, ScanReport>>(new Map());
  let scanAllBusy = $state(false);

  function imageRef(img: ImageSummary): string {
    return img.RepoTags?.[0] && img.RepoTags[0] !== '<none>:<none>' ? img.RepoTags[0] : img.Id;
  }

  async function loadImages() {
    imagesLoading = true;
    try {
      const [imgRes, ctrRes] = await Promise.all([
        api.images.list(false, hosts.id),
        api.containers.list(true, hosts.id).catch(() => [])
      ]);
      if (isFanOut(imgRes)) {
        images = imgRes.items as ImageSummary[];
        imagesUnreachable = imgRes.unreachable_hosts;
      } else {
        images = imgRes as ImageSummary[];
        imagesUnreachable = [];
      }
      const containers: any[] = isFanOut(ctrRes) ? ctrRes.items : (ctrRes as any[]);
      const used = new Set<string>();
      for (const c of containers) if (c.ImageID) used.add(c.ImageID);
      usedImageIds = used;
      // Backfill scan cache only when the user can scan — otherwise the
      // backend returns 403 for every probe and the console fills with
      // permission errors that aren't actionable.
      if (canImageScan) void backfillScanCache();
    } catch (err) {
      toast.error('Failed to load images', err instanceof ApiError ? err.message : undefined);
    } finally {
      imagesLoading = false;
    }
  }
  // Backend marshals an empty []ScanVuln as JSON null (Go nil-slice
  // semantics), so we have to defensively coerce vulnerabilities to []
  // before storing — otherwise `report.vulnerabilities.length` blows
  // up with "Cannot read properties of null (reading 'length')".
  function normalizeScanReport(rep: ScanReport | null): ScanReport | null {
    if (!rep) return null;
    if (!rep.vulnerabilities) rep.vulnerabilities = [];
    return rep;
  }
  async function backfillScanCache() {
    const next = new Map(scanCache);
    await Promise.all(images.map(async (img) => {
      const ref = imageRef(img);
      if (next.has(ref)) return;
      try {
        const rep = normalizeScanReport(await api.images.getScan(img.Id));
        if (rep) next.set(ref, rep);
      } catch {
        /* 404 / no cache — render as "not scanned" */
      }
    }));
    scanCache = next;
  }

  function imageRepoTag(img: ImageSummary): { repo: string | null; tag: string | null } {
    const t = img.RepoTags?.[0];
    if (!t || t === '<none>:<none>') return { repo: null, tag: null };
    const i = t.lastIndexOf(':');
    if (i < 0) return { repo: t, tag: null };
    return { repo: t.slice(0, i), tag: t.slice(i + 1) };
  }
  function isUsed(img: ImageSummary): boolean { return usedImageIds.has(img.Id); }
  function isDangling(img: ImageSummary): boolean {
    return !img.RepoTags || img.RepoTags.length === 0 || img.RepoTags[0] === '<none>:<none>';
  }

  function isVulnerable(img: ImageSummary): boolean {
    const rep = scanCache.get(imageRef(img));
    if (!rep) return false;
    return rep.summary.critical > 0 || rep.summary.high > 0;
  }
  // Stable sort key — repo:tag alphabetical, dangling images sink to the
  // bottom (sorted by Id so they too keep a stable order between polls).
  // Without this the rows visibly hop around on every 10s auto-refresh
  // because Docker returns image lists in roughly creation order and the
  // fanout response merges per-host slices in an order driven by which
  // host responded first.
  function imageSortKey(img: ImageSummary): string {
    const t = img.RepoTags?.[0];
    if (t && t !== '<none>:<none>') return '0:' + t.toLowerCase() + ':' + img.Id;
    return '1:' + img.Id;
  }
  const visibleImages = $derived(
    images
      .filter((i) => {
        if (imageFilter === 'used') return isUsed(i);
        if (imageFilter === 'unused') return !isUsed(i) && !isDangling(i);
        if (imageFilter === 'dangling') return isDangling(i);
        if (imageFilter === 'vulnerable') return isVulnerable(i);
        return true;
      })
      .filter((i) => {
        if (!imageSearch.trim()) return true;
        const q = imageSearch.toLowerCase();
        return (i.RepoTags?.[0] ?? '').toLowerCase().includes(q) || i.Id.toLowerCase().includes(q);
      })
      .slice().sort((a, b) => imageSortKey(a).localeCompare(imageSortKey(b)))
  );
  const imageCounts = $derived({
    all: images.length,
    used: images.filter(isUsed).length,
    unused: images.filter((i) => !isUsed(i) && !isDangling(i)).length,
    dangling: images.filter(isDangling).length,
    vulnerable: images.filter(isVulnerable).length
  });
  const imageTotalSize = $derived(images.reduce((s, i) => s + i.Size, 0));
  const imageDanglingSize = $derived(
    images.filter(isDangling).reduce((s, i) => s + i.Size, 0)
  );

  function toggleImageAll() {
    if (imageSelected.size === visibleImages.length) imageSelected = new Set();
    else imageSelected = new Set(visibleImages.map((i) => i.Id));
  }
  function toggleImageOne(id: string) {
    const n = new Set(imageSelected);
    if (n.has(id)) n.delete(id); else n.add(id);
    imageSelected = n;
  }

  async function pruneImages() {
    if (!(await confirm.ask({
      title: 'Prune dangling images',
      message: `Remove all ${imageCounts.dangling} dangling image${imageCounts.dangling === 1 ? '' : 's'}?`,
      body: 'Dangling images are layers no tagged image references. Safe to remove — Docker re-pulls what is needed.',
      confirmLabel: 'Prune', danger: true
    }))) return;
    try {
      const r = await api.images.prune();
      toast.success('Pruned', `reclaimed ${fmtBytes(r.SpaceReclaimed)}`);
      await loadImages();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function removeImage(id: string) {
    if (!(await confirm.ask({
      title: 'Remove image',
      message: 'Remove this image?',
      body: 'Docker refuses if any container is still using it — stop those containers first.',
      confirmLabel: 'Remove', danger: true
    }))) return;
    try {
      await api.images.remove(id, true);
      toast.success('Removed');
      await loadImages();
    } catch (err) {
      toast.error('Remove failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkRemoveImages() {
    if (!(await confirm.ask({
      title: 'Remove images',
      message: `Remove ${imageSelected.size} image${imageSelected.size === 1 ? '' : 's'}?`,
      body: 'Images in use by containers are skipped with an error.',
      confirmLabel: 'Remove', danger: true
    }))) return;
    let ok = 0, fail = 0;
    for (const id of imageSelected) {
      try { await api.images.remove(id, true); ok++; } catch { fail++; }
    }
    toast.success(`Removed: ${ok}${fail ? `, ${fail} failed` : ''}`);
    imageSelected = new Set();
    await loadImages();
  }

  // ───────── Pull modal
  let pullOpen = $state(false);
  let pullImageRef = $state('');
  let pullBusy = $state(false);
  let hubResults = $state<Array<{ repo_name: string; short_description: string; star_count: number; is_official: boolean }>>([]);
  let hubTimer: ReturnType<typeof setTimeout> | null = null;
  function onPullInput() {
    if (hubTimer) clearTimeout(hubTimer);
    const q = pullImageRef.trim();
    if (q.length < 2) { hubResults = []; return; }
    hubTimer = setTimeout(async () => {
      try {
        const r = await fetch(`https://hub.docker.com/v2/search/repositories/?query=${encodeURIComponent(q)}&page_size=8`);
        if (r.ok) {
          const data = await r.json();
          hubResults = data.results ?? [];
        }
      } catch { /* hub unreachable */ }
    }, 400);
  }
  async function doPull() {
    if (!pullImageRef.trim()) return;
    pullBusy = true;
    try {
      await api.images.pull(pullImageRef.trim());
      toast.success('Pulled', pullImageRef);
      pullOpen = false;
      pullImageRef = '';
      hubResults = [];
      await loadImages();
    } catch (err) {
      toast.error('Pull failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      pullBusy = false;
    }
  }

  // ───────── Scan modal
  let scanOpen = $state(false);
  let scanBusy = $state(false);
  let scanReport = $state<ScanReport | null>(null);
  let scanRef = $state('');
  let scanFilter = $state<Severity | 'all'>('all');
  async function scanImage(img: ImageSummary) {
    const ref = imageRef(img);
    scanRef = ref;
    scanOpen = true;
    scanBusy = true;
    scanReport = null;
    scanFilter = 'all';
    try {
      const rep = normalizeScanReport(await api.images.scan(ref))!;
      scanReport = rep;
      // Feed the in-page cache so the row's CVE cell flips from
      // "not scanned" to severity badges without a page reload.
      const next = new Map(scanCache);
      next.set(ref, rep);
      scanCache = next;
      toast.success('Scan complete', `${rep.vulnerabilities.length} findings`);
    } catch (err) {
      toast.error('Scan failed', err instanceof ApiError ? err.message : 'Scanner unavailable');
    } finally {
      scanBusy = false;
    }
  }
  // Scan-all: walks every image not yet in the cache, scans serially so
  // we don't pin grype's CPU. Each completed scan immediately feeds the
  // cache so the table flips row-by-row without waiting for the whole
  // batch. Cancellable by closing the page (no in-flight cancel token
  // because grype calls take seconds and the worst case is one stray
  // scan finishing after the user navigates away).
  async function scanAll() {
    if (scanAllBusy) return;
    scanAllBusy = true;
    let scanned = 0, failed = 0;
    try {
      for (const img of images) {
        const ref = imageRef(img);
        if (scanCache.has(ref)) continue;
        try {
          const rep = normalizeScanReport(await api.images.scan(ref))!;
          const next = new Map(scanCache);
          next.set(ref, rep);
          scanCache = next;
          scanned++;
        } catch {
          failed++;
        }
      }
      const detail = scanned > 0 ? `${scanned} scanned${failed > 0 ? `, ${failed} failed` : ''}` : 'all images already cached';
      toast.success('Scan all complete', detail);
    } finally {
      scanAllBusy = false;
    }
  }
  const scanFiltered = $derived(
    scanReport
      ? scanReport.vulnerabilities
          .filter((v) => scanFilter === 'all' || v.severity === scanFilter)
          .sort((a, b) => {
            const r: Record<string, number> = { critical: 5, high: 4, medium: 3, low: 2, negligible: 1, unknown: 0 };
            return (r[b.severity] ?? 0) - (r[a.severity] ?? 0);
          })
      : []
  );

  // ===================================================================
  //  Volumes
  // ===================================================================
  interface VolumeRow {
    Name: string;
    Driver: string;
    Scope: string;
    Mountpoint: string;
    CreatedAt?: string;
    Labels?: Record<string, string>;
    host_id?: string;
    host_name?: string;
  }
  let volumes = $state<VolumeRow[]>([]);
  let volumesLoading = $state(true);
  let volumesUnreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let volumeSearch = $state('');
  let volumeFilter = $state<'all' | 'used' | 'orphan' | 'anon' | 'compose'>('all');
  let volumeSelected = $state<Set<string>>(new Set());
  let volumeUsage = $state<Map<string, number>>(new Map());
  function vKey(v: VolumeRow): string { return `${v.host_id ?? 'local'}/${v.Name}`; }

  async function loadVolumes() {
    volumesLoading = true;
    try {
      const [volRes, ctrRes] = await Promise.all([
        api.volumes.list(hosts.id),
        api.containers.list(true, hosts.id).catch(() => [])
      ]);
      if (isFanOut(volRes)) {
        volumes = volRes.items as VolumeRow[];
        volumesUnreachable = volRes.unreachable_hosts;
      } else {
        volumes = volRes as VolumeRow[];
        volumesUnreachable = [];
      }
      // Build "in use" counts from container Mounts.
      const containers: any[] = isFanOut(ctrRes) ? ctrRes.items : (ctrRes as any[]);
      const usage = new Map<string, number>();
      for (const c of containers) {
        for (const m of (c.Mounts ?? [])) {
          if (m.Type === 'volume' && m.Name) {
            const k = `${c.host_id ?? 'local'}/${m.Name}`;
            usage.set(k, (usage.get(k) ?? 0) + 1);
          }
        }
      }
      volumeUsage = usage;
    } catch (err) {
      toast.error('Failed to load volumes', err instanceof ApiError ? err.message : undefined);
    } finally {
      volumesLoading = false;
    }
  }

  function isAnonVolume(v: VolumeRow): boolean {
    // Docker auto-named volumes are 64-hex-char names. We match the
    // shorter sha-12 form too (some drivers truncate).
    return /^[0-9a-f]{12,64}$/.test(v.Name);
  }
  function volumeStackOwner(v: VolumeRow): string | null {
    return v.Labels?.['com.docker.compose.project'] ?? null;
  }
  function volumeUsedBy(v: VolumeRow): number {
    return volumeUsage.get(vKey(v)) ?? 0;
  }

  const visibleVolumes = $derived(
    volumes
      .filter((v) => {
        if (volumeFilter === 'used') return volumeUsedBy(v) > 0;
        if (volumeFilter === 'orphan') return volumeUsedBy(v) === 0;
        if (volumeFilter === 'anon') return isAnonVolume(v);
        if (volumeFilter === 'compose') return !!volumeStackOwner(v);
        return true;
      })
      .filter((v) => {
        if (!volumeSearch.trim()) return true;
        return v.Name.toLowerCase().includes(volumeSearch.toLowerCase());
      })
      // Stable sort: name first, then host_id as tie-breaker so all-mode
      // doesn't reshuffle volumes on every poll when multiple hosts host
      // a same-named volume.
      .slice().sort((a, b) => {
        const c = a.Name.toLowerCase().localeCompare(b.Name.toLowerCase());
        if (c !== 0) return c;
        return (a.host_id ?? '').localeCompare(b.host_id ?? '');
      })
  );
  const volumeCounts = $derived({
    all: volumes.length,
    used: volumes.filter((v) => volumeUsedBy(v) > 0).length,
    orphan: volumes.filter((v) => volumeUsedBy(v) === 0).length,
    anon: volumes.filter(isAnonVolume).length,
    compose: volumes.filter((v) => !!volumeStackOwner(v)).length
  });
  const volumeDriverCount = $derived(new Set(volumes.map((v) => v.Driver)).size);

  function toggleVolumeAll() {
    if (volumeSelected.size === visibleVolumes.length) volumeSelected = new Set();
    else volumeSelected = new Set(visibleVolumes.map(vKey));
  }
  function toggleVolumeOne(v: VolumeRow) {
    const k = vKey(v);
    const n = new Set(volumeSelected);
    if (n.has(k)) n.delete(k); else n.add(k);
    volumeSelected = n;
  }

  // ───────── Create-volume modal
  let volCreateOpen = $state(false);
  let newVolName = $state('');
  let newVolDriver = $state('local');
  let newVolHost = $state('local');
  let volCreating = $state(false);
  async function createVolume(e: Event) {
    e.preventDefault();
    volCreating = true;
    try {
      await api.volumes.create(newVolName, newVolDriver, newVolHost);
      toast.success('Volume created', newVolName);
      volCreateOpen = false;
      newVolName = '';
      newVolHost = hosts.id && !hosts.isAll ? hosts.id : 'local';
      await loadVolumes();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      volCreating = false;
    }
  }
  async function deleteVolume(v: VolumeRow) {
    if (!(await confirm.ask({
      title: 'Delete volume',
      message: `Delete volume "${v.Name}"?`,
      body: 'Docker refuses if any container still mounts it. Data is unrecoverable once deleted.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.volumes.remove(v.Name, true);
      toast.success('Deleted', v.Name);
      await loadVolumes();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkDeleteVolumes() {
    if (!(await confirm.ask({
      title: 'Delete volumes',
      message: `Delete ${volumeSelected.size} volume${volumeSelected.size === 1 ? '' : 's'}?`,
      body: 'Volumes in use by containers are skipped with an error. Data is unrecoverable.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    let ok = 0, fail = 0;
    for (const v of volumes) {
      if (!volumeSelected.has(vKey(v))) continue;
      try { await api.volumes.remove(v.Name, true); ok++; } catch { fail++; }
    }
    toast.success(`Deleted: ${ok}${fail ? `, ${fail} failed` : ''}`);
    volumeSelected = new Set();
    await loadVolumes();
  }
  async function pruneVolumes() {
    if (!(await confirm.ask({
      title: 'Prune unused volumes',
      message: 'Remove all volumes not used by any container?',
      body: 'This cannot be undone. Volume data is deleted.',
      confirmLabel: 'Prune', danger: true
    }))) return;
    try {
      const r: any = await api.volumes.prune();
      const n = r?.VolumesDeleted?.length ?? 0;
      toast.success('Pruned', `${n} volume(s) removed`);
      await loadVolumes();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // ===================================================================
  //  Networks
  // ===================================================================
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
  let networks = $state<NetworkRow[]>([]);
  let networksLoading = $state(true);
  let networksUnreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let networkSearch = $state('');
  let networkFilter = $state<'user' | 'internal' | 'attachable' | 'empty'>('user');
  let showSystemNetworks = $state(false);
  const SYSTEM_NETWORKS = new Set(['bridge', 'host', 'none']);

  async function loadNetworks() {
    networksLoading = true;
    try {
      const r = await api.networks.list(hosts.id);
      if (isFanOut(r)) {
        networks = r.items as NetworkRow[];
        networksUnreachable = r.unreachable_hosts;
      } else {
        networks = r as NetworkRow[];
        networksUnreachable = [];
      }
    } catch (err) {
      toast.error('Failed to load networks', err instanceof ApiError ? err.message : undefined);
    } finally {
      networksLoading = false;
    }
  }
  function networkContainerCount(n: NetworkRow): number {
    return n.Containers ? Object.keys(n.Containers).length : 0;
  }
  function networkOwner(n: NetworkRow): string | null {
    return n.Labels?.['com.docker.compose.project'] ?? null;
  }
  function isSystem(n: NetworkRow): boolean { return SYSTEM_NETWORKS.has(n.Name); }

  const visibleNetworks = $derived(
    networks
      .filter((n) => showSystemNetworks ? true : !isSystem(n))
      .filter((n) => {
        if (networkFilter === 'internal') return n.Internal;
        if (networkFilter === 'attachable') return n.Attachable;
        if (networkFilter === 'empty') return !isSystem(n) && networkContainerCount(n) === 0;
        return true; // user
      })
      .filter((n) => {
        if (!networkSearch.trim()) return true;
        const q = networkSearch.toLowerCase();
        return n.Name.toLowerCase().includes(q) || n.Driver.toLowerCase().includes(q)
          || (n.IPAM?.Config?.[0]?.Subnet ?? '').includes(q);
      })
      // Stable sort: system networks always first (bridge/host/none then
      // alphabetical within each group), Id as tie-breaker. Keeps rows
      // anchored across the 10s auto-refresh.
      .slice().sort((a, b) => {
        const sa = isSystem(a) ? 0 : 1;
        const sb = isSystem(b) ? 0 : 1;
        if (sa !== sb) return sa - sb;
        const c = a.Name.toLowerCase().localeCompare(b.Name.toLowerCase());
        if (c !== 0) return c;
        return a.Id.localeCompare(b.Id);
      })
  );
  const networkUserList = $derived(networks.filter((n) => !isSystem(n)));
  const networkCounts = $derived({
    user: networkUserList.length,
    internal: networkUserList.filter((n) => n.Internal).length,
    attachable: networkUserList.filter((n) => n.Attachable).length,
    empty: networkUserList.filter((n) => networkContainerCount(n) === 0).length
  });
  const networkSystemCount = $derived(networks.filter(isSystem).length);

  // ───────── Create-network modal
  let netCreateOpen = $state(false);
  let newNetName = $state('');
  let newNetDriver = $state('bridge');
  let netCreating = $state(false);
  async function createNetwork(e: Event) {
    e.preventDefault();
    netCreating = true;
    try {
      await api.networks.create(newNetName, newNetDriver);
      toast.success('Network created', newNetName);
      netCreateOpen = false;
      newNetName = '';
      await loadNetworks();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      netCreating = false;
    }
  }
  async function deleteNetwork(n: NetworkRow) {
    if (!(await confirm.ask({
      title: 'Delete network',
      message: `Delete network "${n.Name}"?`,
      body: 'Docker refuses if any container is still attached.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.networks.remove(n.Id);
      toast.success('Deleted', n.Name);
      await loadNetworks();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function pruneNetworks() {
    if (!(await confirm.ask({
      title: 'Prune empty networks',
      message: 'Remove all empty user networks?',
      body: 'Networks attached to no containers are removed. System networks (bridge / host / none) are kept.',
      confirmLabel: 'Prune', danger: true
    }))) return;
    try {
      await api.networks.prune();
      toast.success('Pruned');
      await loadNetworks();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  function networkDriverPill(d: string): 'success' | 'warning' | 'default' {
    if (d === 'overlay') return 'success';
    if (d === 'macvlan' || d === 'ipvlan') return 'warning';
    return 'default';
  }

  // ===================================================================
  //  Loading orchestrator
  // ===================================================================
  async function loadAll() {
    if (tab === 'images') await loadImages();
    else if (tab === 'volumes') await loadVolumes();
    else await loadNetworks();
  }
  $effect(() => { hosts.id; tab; loadAll(); });
  $effect(() => autoRefresh(loadAll, 10_000));

  // ===================================================================
  //  System cleanup — system-wide prune across images + volumes + networks
  // ===================================================================
  // Mockup pattern: a single "System cleanup…" ghost button right of the
  // tab strip that opens a modal preview-listing how many items each
  // prune-target has, and runs all three Docker prune calls on confirm.
  // Single confirmation, transparent failure-tolerant — partial success
  // is reported via individual toasts.
  let cleanupOpen = $state(false);
  let cleanupBusy = $state(false);
  // Pre-load all three datasets when the modal opens so the preview shows
  // accurate counts even on tabs the user hasn't visited yet.
  async function openCleanup() {
    cleanupOpen = true;
    if (images.length === 0) await loadImages();
    if (volumes.length === 0) await loadVolumes();
    if (networks.length === 0) await loadNetworks();
  }
  async function runSystemCleanup() {
    cleanupBusy = true;
    let totalReclaimed = 0;
    let imagesPruned = 0, volumesPruned = 0, networksPruned = 0;
    let failures: string[] = [];
    try {
      const r = await api.images.prune();
      imagesPruned = r.ImagesDeleted?.length ?? 0;
      totalReclaimed += r.SpaceReclaimed ?? 0;
    } catch (err) {
      failures.push(`images: ${err instanceof ApiError ? err.message : 'failed'}`);
    }
    try {
      const r: any = await api.volumes.prune();
      volumesPruned = r?.VolumesDeleted?.length ?? 0;
      totalReclaimed += r?.SpaceReclaimed ?? 0;
    } catch (err) {
      failures.push(`volumes: ${err instanceof ApiError ? err.message : 'failed'}`);
    }
    try {
      const r: any = await api.networks.prune();
      networksPruned = r?.NetworksDeleted?.length ?? 0;
    } catch (err) {
      failures.push(`networks: ${err instanceof ApiError ? err.message : 'failed'}`);
    }
    cleanupBusy = false;
    cleanupOpen = false;
    if (failures.length > 0) {
      toast.error('Cleanup partial', failures.join(' · '));
    } else {
      toast.success('System cleanup done',
        `${imagesPruned} images · ${volumesPruned} volumes · ${networksPruned} networks · reclaimed ${fmtBytes(totalReclaimed)}`);
    }
    await loadAll();
  }

  // ───────── Helpers
  function fmtBytes(b: number): string {
    if (!b) return '0 B';
    if (b < 1024) return `${b} B`;
    if (b < 1024 ** 2) return `${(b / 1024).toFixed(1)} KB`;
    if (b < 1024 ** 3) return `${(b / 1024 ** 2).toFixed(0)} MB`;
    return `${(b / 1024 ** 3).toFixed(2)} GB`;
  }
  function fmtAge(unix: number): string {
    if (!unix) return '—';
    const d = (Date.now() / 1000 - unix) / 86400;
    if (d < 1) return 'today';
    if (d < 7) return `${Math.floor(d)}d ago`;
    if (d < 30) return `${Math.floor(d / 7)}w ago`;
    if (d < 365) return `${Math.floor(d / 30)}mo ago`;
    return `${Math.floor(d / 365)}y ago`;
  }
  function fmtDate(s?: string): string {
    if (!s) return '—';
    const d = new Date(s);
    if (isNaN(d.getTime())) return s;
    return d.toISOString().slice(0, 10);
  }
  function sevColor(s: Severity): 'danger' | 'warning' | 'info' | 'default' {
    if (s === 'critical' || s === 'high') return 'danger';
    if (s === 'medium') return 'warning';
    if (s === 'low') return 'info';
    return 'default';
  }
</script>

<section class="resources-page">
  <!-- ─── Header ─── -->
  <header class="res-header">
    <h1 class="ed-title res-title">Resources</h1>
    <p class="res-subtitle">
      {images.length} image{images.length === 1 ? '' : 's'} ·
      {volumes.length} volume{volumes.length === 1 ? '' : 's'} ·
      {networks.length} network{networks.length === 1 ? '' : 's'}
    </p>
  </header>

  <!-- ─── Tab strip ─── -->
  <div class="ed-tabs res-tabs">
    <button
      type="button"
      class="ed-tab"
      class:active={tab === 'images'}
      onclick={() => (tab = 'images')}
    >
      <ImageIcon size={13} strokeWidth={1.5} /> Images <span class="count">{imageCounts.all}</span>
    </button>
    <button
      type="button"
      class="ed-tab"
      class:active={tab === 'volumes'}
      onclick={() => (tab = 'volumes')}
    >
      <HardDrive size={13} strokeWidth={1.5} /> Volumes <span class="count">{volumeCounts.all}</span>
    </button>
    <button
      type="button"
      class="ed-tab"
      class:active={tab === 'networks'}
      onclick={() => (tab = 'networks')}
    >
      <NetworkIcon size={13} strokeWidth={1.5} /> Networks <span class="count">{networkCounts.user}</span>
    </button>
    <span class="res-tabs-spacer"></span>
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm res-cleanup-btn" onclick={openCleanup} title="Prune images + volumes + networks in one go">
      <Trash2 size={12} strokeWidth={1.5} /> System cleanup…
    </button>
  </div>

  <!-- ============================================================= -->
  <!--  IMAGES                                                        -->
  <!-- ============================================================= -->
  {#if tab === 'images'}
    <div class="res-summary">
      <div class="ed-metric">
        <span class="ed-metric-label">Total</span>
        <div class="ed-metric-value">{imageCounts.all}</div>
        <span class="ed-metric-meta">{fmtBytes(imageTotalSize)}</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Dangling</span>
        <div class="ed-metric-value" class:warn={imageCounts.dangling > 0}>{imageCounts.dangling}</div>
        <span class="ed-metric-meta">{fmtBytes(imageDanglingSize)}</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Reclaimable</span>
        <div class="ed-metric-value">{fmtBytes(imageDanglingSize)}</div>
        <span class="ed-metric-meta">via prune</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">In use</span>
        <div class="ed-metric-value">{imageCounts.used}</div>
        <span class="ed-metric-meta">attached to a container</span>
      </div>
    </div>

    <div class="res-toolbar">
      <div class="res-toolbar-search">
        <Search size={12} strokeWidth={1.5} class="res-toolbar-search-icon" />
        <input type="text" class="ed-underline-input res-search-input" placeholder="search by repo, tag or sha…" bind:value={imageSearch} />
      </div>
      <span class="ed-metric-meta res-segctrl-label">show</span>
      <div class="res-segctrl">
        {#each [
          ['all','all',imageCounts.all],
          ['used','used',imageCounts.used],
          ['unused','unused',imageCounts.unused],
          ['dangling','dangling',imageCounts.dangling],
          ['vulnerable','CVE',imageCounts.vulnerable]
        ] as [id,label,count] (id)}
          <button type="button" class="res-segctrl-btn" class:active={imageFilter === id} onclick={() => (imageFilter = id as any)}>
            {label}<span class="seg-count">{count}</span>
          </button>
        {/each}
      </div>
      <span class="res-spacer"></span>
      {#if imageSelected.size > 0 && canImageDelete}
        <span class="res-bulk-count font-mono">{imageSelected.size} selected</span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm res-bulk-danger" onclick={bulkRemoveImages}>
          <Trash2 size={12} strokeWidth={1.5} /> Remove
        </button>
      {/if}
      {#if canImageScan}
        <button type="button" class="res-prune-btn" onclick={scanAll} disabled={scanAllBusy || images.length === 0} title="Scan every image with Grype">
          <Shield size={12} strokeWidth={1.5} /> {scanAllBusy ? 'Scanning…' : 'Scan all'}
        </button>
      {/if}
      {#if canImageDelete}
        <button type="button" class="res-prune-btn" onclick={pruneImages} disabled={imageCounts.dangling === 0}>
          <Trash2 size={12} strokeWidth={1.5} /> Prune dangling
          {#if imageCounts.dangling > 0}<span class="dm-pill dm-pill-warning res-action-pill">{imageCounts.dangling}</span>{/if}
        </button>
      {/if}
      {#if canImageCreate}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => { pullOpen = true; pullImageRef = ''; hubResults = []; }}>
          <Download size={12} strokeWidth={1.5} /> Pull image
        </button>
      {/if}
    </div>

    {#if imagesUnreachable.length > 0}
      <div class="res-warn-banner">
        <AlertTriangle size={14} strokeWidth={1.5} />
        <span>{imagesUnreachable.length} host(s) unreachable</span>
        <span class="font-mono res-warn-detail">— {imagesUnreachable.map((u) => u.host_name).join(', ')}</span>
      </div>
    {/if}

    {#if imagesLoading && images.length === 0}
      <div class="dm-card" style="padding: 22px;"><Skeleton width="80%" height="6rem" /></div>
    {:else if visibleImages.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={ImageIcon} title={images.length === 0 ? 'No images' : 'No images match'} description={images.length === 0 ? 'Pull an image or deploy a stack to get started.' : 'Adjust filter or search.'}>
          {#snippet action()}
            {#if canImageCreate && images.length === 0}
              <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => (pullOpen = true)}>
                <Download size={12} strokeWidth={1.5} /> Pull image
              </button>
            {/if}
          {/snippet}
        </EmptyState>
      </div>
    {:else}
      {@const grid = '24px minmax(240px, 1fr) 88px 90px 90px 160px 110px 28px'}
      <div class="res-table">
        <div class="res-row res-row--head" style="grid-template-columns: {grid};">
          <input type="checkbox" disabled={!canImageDelete} checked={imageSelected.size === visibleImages.length && visibleImages.length > 0} onchange={toggleImageAll} />
          <span>repository : tag</span>
          <span>size</span>
          <span>created</span>
          <span>used by</span>
          <span>vulnerabilities</span>
          <span>host</span>
          <span></span>
        </div>
        {#each visibleImages as img (img.Id + (img.host_id ?? ''))}
          {@const rt = imageRepoTag(img)}
          {@const cve = scanCache.get(imageRef(img))}
          <div class="res-row" style="grid-template-columns: {grid};">
            <input type="checkbox" disabled={!canImageDelete} checked={imageSelected.has(img.Id)} onchange={() => toggleImageOne(img.Id)} />
            <div class="res-col-tag">
              {#if rt.repo}
                <a href={`/images/${encodeURIComponent(img.Id)}${img.host_id && img.host_id !== 'local' ? `?host=${encodeURIComponent(img.host_id)}` : ''}`} class="res-col-tag-line res-col-tag-link">
                  {rt.repo}<span class="muted">:</span><span class="accent">{rt.tag}</span>
                </a>
              {:else}
                <a href={`/images/${encodeURIComponent(img.Id)}${img.host_id && img.host_id !== 'local' ? `?host=${encodeURIComponent(img.host_id)}` : ''}`} class="res-col-tag-dangling res-col-tag-link">
                  <span class="dm-pill dm-pill-warning res-mini-pill">dangling</span>
                  <span class="font-mono muted">&lt;none&gt;:&lt;none&gt;</span>
                </a>
              {/if}
              <div class="res-col-tag-sha font-mono">sha256:{img.Id.replace(/^sha256:/, '').slice(0, 12)}</div>
            </div>
            <span class="res-cell-mono right">{fmtBytes(img.Size)}</span>
            <span class="res-cell-mono">{fmtAge(img.Created)}</span>
            <span>
              {#if isUsed(img)}
                <span class="dm-pill dm-pill-success res-mini-pill"><span class="dm-pill-dot"></span> in use</span>
              {:else}
                <span class="font-mono muted">—</span>
              {/if}
            </span>
            <!-- CVE cell. Three states match the mockup:
                 • not scanned (no entry in scanCache): muted text + scan button
                 • clean (scan ran, 0 vulns): green check pill
                 • findings: severity badges per non-zero severity, mockup-style "C N / H N / M N / L N" -->
            <div class="res-cve-cell">
              {#if !cve}
                <span class="font-mono muted res-cve-empty">not scanned</span>
                {#if canImageScan}
                  <button type="button" class="res-cve-scan-btn" onclick={() => scanImage(img)} title="Scan with Grype">
                    <Shield size={10} strokeWidth={1.5} /> scan
                  </button>
                {/if}
              {:else if cve.vulnerabilities.length === 0}
                <button type="button" class="res-cve-result" onclick={() => scanImage(img)} title="Re-scan">
                  <span class="dm-pill dm-pill-success res-mini-pill"><Check size={10} strokeWidth={2} /> clean</span>
                </button>
              {:else}
                <button type="button" class="res-cve-result res-cve-badges" onclick={() => scanImage(img)} title="Open scan report">
                  {#if cve.summary.critical > 0}<span class="cve-badge cve-c">C {cve.summary.critical}</span>{/if}
                  {#if cve.summary.high > 0}<span class="cve-badge cve-h">H {cve.summary.high}</span>{/if}
                  {#if cve.summary.medium > 0}<span class="cve-badge cve-m">M {cve.summary.medium}</span>{/if}
                  {#if cve.summary.low > 0}<span class="cve-badge cve-l">L {cve.summary.low}</span>{/if}
                </button>
              {/if}
            </div>
            <span class="res-cell-mono">{img.host_name ?? img.host_id ?? 'local'}</span>
            <span>
              {#if canImageDelete}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs res-row-danger" onclick={() => removeImage(img.Id)} title="Remove">
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              {/if}
            </span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- ============================================================= -->
  <!--  VOLUMES                                                       -->
  <!-- ============================================================= -->
  {#if tab === 'volumes'}
    <div class="res-summary">
      <div class="ed-metric">
        <span class="ed-metric-label">Volumes</span>
        <div class="ed-metric-value">{volumeCounts.all}</div>
        <span class="ed-metric-meta">{volumeCounts.compose} from stacks</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Orphan</span>
        <div class="ed-metric-value" class:warn={volumeCounts.orphan > 0}>{volumeCounts.orphan}</div>
        <span class="ed-metric-meta">no container attached</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Anonymous</span>
        <div class="ed-metric-value" class:warn={volumeCounts.anon > 0}>{volumeCounts.anon}</div>
        <span class="ed-metric-meta">auto-named, untagged</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Drivers</span>
        <div class="ed-metric-value">{volumeDriverCount}</div>
        <span class="ed-metric-meta">in use</span>
      </div>
    </div>

    <div class="res-toolbar">
      <div class="res-toolbar-search">
        <Search size={12} strokeWidth={1.5} class="res-toolbar-search-icon" />
        <input type="text" class="ed-underline-input res-search-input" placeholder="search volume name…" bind:value={volumeSearch} />
      </div>
      <span class="ed-metric-meta res-segctrl-label">show</span>
      <div class="res-segctrl">
        {#each [
          ['all','all',volumeCounts.all],
          ['used','in use',volumeCounts.used],
          ['orphan','orphan',volumeCounts.orphan],
          ['anon','anon',volumeCounts.anon],
          ['compose','compose',volumeCounts.compose]
        ] as [id,label,count] (id)}
          <button type="button" class="res-segctrl-btn" class:active={volumeFilter === id} onclick={() => (volumeFilter = id as any)}>
            {label}<span class="seg-count">{count}</span>
          </button>
        {/each}
      </div>
      <span class="res-spacer"></span>
      {#if volumeSelected.size > 0 && canVolDelete}
        <span class="res-bulk-count font-mono">{volumeSelected.size} selected</span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm res-bulk-danger" onclick={bulkDeleteVolumes}>
          <Trash2 size={12} strokeWidth={1.5} /> Delete
        </button>
      {/if}
      {#if canVolDelete}
        <button type="button" class="res-prune-btn" onclick={pruneVolumes} disabled={volumeCounts.orphan === 0}>
          <Trash2 size={12} strokeWidth={1.5} /> Prune orphan
          {#if volumeCounts.orphan > 0}<span class="dm-pill dm-pill-warning res-action-pill">{volumeCounts.orphan}</span>{/if}
        </button>
      {/if}
      {#if canVolCreate}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => { volCreateOpen = true; newVolName = ''; newVolHost = hosts.id && !hosts.isAll ? hosts.id : 'local'; }}>
          <Plus size={12} strokeWidth={1.5} /> Create volume
        </button>
      {/if}
    </div>

    {#if volumesUnreachable.length > 0}
      <div class="res-warn-banner">
        <AlertTriangle size={14} strokeWidth={1.5} />
        <span>{volumesUnreachable.length} host(s) unreachable</span>
        <span class="font-mono res-warn-detail">— {volumesUnreachable.map((u) => u.host_name).join(', ')}</span>
      </div>
    {/if}

    {#if volumesLoading && volumes.length === 0}
      <div class="dm-card" style="padding: 22px;"><Skeleton width="80%" height="6rem" /></div>
    {:else if visibleVolumes.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={HardDrive} title={volumes.length === 0 ? 'No volumes yet' : 'No volumes match'} description={volumes.length === 0 ? 'Stacks haven’t created any named volumes. Create one manually if you need shared persistence.' : 'Adjust filter or search.'} />
      </div>
    {:else}
      {@const grid = isAll
        ? '24px minmax(220px, 1.4fr) 110px minmax(220px, 1.6fr) 130px 110px 100px 28px'
        : '24px minmax(220px, 1.4fr) 110px minmax(220px, 1.6fr) 130px 110px 28px'}
      <div class="res-table">
        <div class="res-row res-row--head" style="grid-template-columns: {grid};">
          <input type="checkbox" disabled={!canVolDelete} checked={volumeSelected.size === visibleVolumes.length && visibleVolumes.length > 0} onchange={toggleVolumeAll} />
          <span>name</span>
          <span>driver</span>
          <span>mountpoint</span>
          <span>used by</span>
          <span>created</span>
          {#if isAll}<span>host</span>{/if}
          <span></span>
        </div>
        {#each visibleVolumes as v (vKey(v))}
          {@const used = volumeUsedBy(v)}
          {@const owner = volumeStackOwner(v)}
          {@const anon = isAnonVolume(v)}
          <div class="res-row" style="grid-template-columns: {grid};">
            <input type="checkbox" disabled={!canVolDelete} checked={volumeSelected.has(vKey(v))} onchange={() => toggleVolumeOne(v)} />
            <div class="res-col-tag">
              <a href={`/volumes/${encodeURIComponent(v.Name)}${v.host_id && v.host_id !== 'local' ? `?host=${encodeURIComponent(v.host_id)}` : ''}`} class="res-col-tag-line res-col-tag-link" class:muted-name={anon}>
                <span class="res-name-text" title={v.Name}>{v.Name}</span>
                {#if anon}<span class="dm-pill dm-pill-warning res-mini-pill">anon</span>{/if}
              </a>
              {#if owner}
                <div class="res-col-tag-sha font-mono">stack: <span class="accent">{owner}</span></div>
              {/if}
            </div>
            <div>
              <span class="dm-pill dm-pill-neutral res-mini-pill">{v.Driver}</span>
              {#if v.Scope === 'global'}<span class="dm-pill dm-pill-neutral res-mini-pill" style="margin-left: 4px;">global</span>{/if}
            </div>
            <span class="res-cell-mono ellipsis" title={v.Mountpoint}>{v.Mountpoint}</span>
            <span>
              {#if used > 0}
                <span class="dm-pill dm-pill-success res-mini-pill"><span class="dm-pill-dot"></span> {used} container{used === 1 ? '' : 's'}</span>
              {:else}
                <span class="dm-pill dm-pill-warning res-mini-pill">orphan</span>
              {/if}
            </span>
            <span class="res-cell-mono">{fmtDate(v.CreatedAt)}</span>
            {#if isAll}
              <span class="res-cell-mono">{v.host_name ?? v.host_id ?? '—'}</span>
            {/if}
            <span>
              {#if canVolDelete}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs res-row-danger" onclick={() => deleteVolume(v)} title="Delete">
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              {/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="res-foot">
        Volume size is not exposed by Docker without a per-volume <code>du -s</code>.
        To browse contents, open the volume — every browse is logged in the audit trail.
      </p>
    {/if}
  {/if}

  <!-- ============================================================= -->
  <!--  NETWORKS                                                      -->
  <!-- ============================================================= -->
  {#if tab === 'networks'}
    <div class="res-summary">
      <div class="ed-metric">
        <span class="ed-metric-label">Networks</span>
        <div class="ed-metric-value">{networkCounts.user}</div>
        <span class="ed-metric-meta">+ {networkSystemCount} system {showSystemNetworks ? 'shown' : 'hidden'}</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Internal</span>
        <div class="ed-metric-value">{networkCounts.internal}</div>
        <span class="ed-metric-meta">no external egress</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Attachable</span>
        <div class="ed-metric-value">{networkCounts.attachable}</div>
        <span class="ed-metric-meta">for swarm tasks</span>
      </div>
      <div class="ed-metric">
        <span class="ed-metric-label">Empty</span>
        <div class="ed-metric-value" class:warn={networkCounts.empty > 0}>{networkCounts.empty}</div>
        <span class="ed-metric-meta">0 containers attached</span>
      </div>
    </div>

    <div class="res-toolbar">
      <div class="res-toolbar-search">
        <Search size={12} strokeWidth={1.5} class="res-toolbar-search-icon" />
        <input type="text" class="ed-underline-input res-search-input" placeholder="search network…" bind:value={networkSearch} />
      </div>
      <span class="ed-metric-meta res-segctrl-label">show</span>
      <div class="res-segctrl">
        {#each [
          ['user','user',networkCounts.user],
          ['internal','internal',networkCounts.internal],
          ['attachable','attachable',networkCounts.attachable],
          ['empty','empty',networkCounts.empty]
        ] as [id,label,count] (id)}
          <button type="button" class="res-segctrl-btn" class:active={networkFilter === id} onclick={() => (networkFilter = id as any)}>
            {label}<span class="seg-count">{count}</span>
          </button>
        {/each}
      </div>
      <label class="res-toggle font-mono">
        <input type="checkbox" bind:checked={showSystemNetworks} />
        show system networks
      </label>
      <span class="res-spacer"></span>
      {#if canNetDelete}
        <button type="button" class="res-prune-btn" onclick={pruneNetworks} disabled={networkCounts.empty === 0}>
          <Trash2 size={12} strokeWidth={1.5} /> Prune empty
          {#if networkCounts.empty > 0}<span class="dm-pill dm-pill-warning res-action-pill">{networkCounts.empty}</span>{/if}
        </button>
      {/if}
      {#if canNetCreate}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => { netCreateOpen = true; newNetName = ''; newNetDriver = 'bridge'; }}>
          <Plus size={12} strokeWidth={1.5} /> Create network
        </button>
      {/if}
    </div>

    {#if networksUnreachable.length > 0}
      <div class="res-warn-banner">
        <AlertTriangle size={14} strokeWidth={1.5} />
        <span>{networksUnreachable.length} host(s) unreachable</span>
        <span class="font-mono res-warn-detail">— {networksUnreachable.map((u) => u.host_name).join(', ')}</span>
      </div>
    {/if}

    {#if networksLoading && networks.length === 0}
      <div class="dm-card" style="padding: 22px;"><Skeleton width="80%" height="6rem" /></div>
    {:else if visibleNetworks.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={NetworkIcon} title={networks.length === 0 ? 'No networks' : 'No networks match'} description={networks.length === 0 ? 'Stacks usually create their own network on first deploy.' : 'Adjust filter, search, or toggle "show system networks".'} />
      </div>
    {:else}
      {@const grid = '24px minmax(220px, 1.5fr) 140px 140px 180px 90px 100px 28px'}
      <div class="res-table">
        <div class="res-row res-row--head" style="grid-template-columns: {grid};">
          <span></span>
          <span>name</span>
          <span>driver / scope</span>
          <span>subnet</span>
          <span>flags</span>
          <span>containers</span>
          <span>created</span>
          <span></span>
        </div>
        {#each visibleNetworks as n (n.Id)}
          {@const owner = networkOwner(n)}
          {@const subnet = n.IPAM?.Config?.[0]?.Subnet ?? '—'}
          {@const cnt = networkContainerCount(n)}
          <div class="res-row" style="grid-template-columns: {grid}; opacity: {isSystem(n) ? '0.7' : '1'};">
            <span></span>
            <div class="res-col-tag">
              <a href={`/networks/${n.Id}`} class="res-col-tag-line res-col-tag-link">
                <span class="res-name-text" title={n.Name}>{n.Name}</span>
                {#if isSystem(n)}<span class="dm-pill dm-pill-neutral res-mini-pill">system</span>{/if}
              </a>
              {#if owner}
                <div class="res-col-tag-sha font-mono">stack: <span class="accent">{owner}</span></div>
              {:else}
                <div class="res-col-tag-sha font-mono">id: {n.Id.slice(0, 12)}</div>
              {/if}
            </div>
            <div>
              <span class="dm-pill dm-pill-{networkDriverPill(n.Driver)} res-mini-pill">{n.Driver}</span>
              <span class="res-cell-mono" style="margin-left: 6px;">{n.Scope}</span>
            </div>
            <span class="res-cell-mono">{subnet}</span>
            <div class="res-flags">
              {#if n.Internal}<span class="dm-pill dm-pill-warning res-mini-pill">internal</span>{/if}
              {#if n.Attachable}<span class="dm-pill dm-pill-neutral res-mini-pill">attachable</span>{/if}
              {#if !n.Internal && !n.Attachable}<span class="font-mono muted">—</span>{/if}
            </div>
            <span>
              {#if cnt > 0}
                <span class="dm-pill dm-pill-success res-mini-pill"><span class="dm-pill-dot"></span> {cnt}</span>
              {:else}
                <span class="font-mono muted">0</span>
              {/if}
            </span>
            <span class="res-cell-mono">{fmtDate(n.Created)}</span>
            <span>
              {#if canNetDelete && !isSystem(n)}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs res-row-danger" onclick={() => deleteNetwork(n)} title="Delete">
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              {/if}
            </span>
          </div>
        {/each}
      </div>

      <!-- Topology preview card — links to the standalone /topology page
           where the full dagre graph + side panel + live updates live. -->
      <a href="/topology" class="dm-card res-topology">
        <div class="res-topology-text">
          <Eyebrow>Topology · live</Eyebrow>
          <p>
            See how stacks, containers and networks connect — including which services share an internal network and where traffic exits.
          </p>
          <span class="res-topology-cta">Open topology view <ArrowUpRight size={12} strokeWidth={1.5} /></span>
        </div>
        <div class="res-topology-mini">
          <svg viewBox="0 0 200 100" width="100%" height="100%">
            <path d="M40 30 C 80 30, 80 50, 100 50" stroke="var(--border-strong)" stroke-width="1" fill="none" />
            <path d="M40 70 C 80 70, 80 50, 100 50" stroke="var(--border-strong)" stroke-width="1" fill="none" />
            <path d="M100 50 C 130 50, 130 30, 160 30" stroke="var(--accent)" stroke-width="1.2" fill="none" />
            <path d="M100 50 C 130 50, 130 70, 160 70" stroke="var(--border-strong)" stroke-width="1" fill="none" />
            <rect x="20" y="22" width="40" height="16" rx="2" fill="var(--bg-elevated)" stroke="var(--border-strong)" />
            <rect x="20" y="62" width="40" height="16" rx="2" fill="var(--bg-elevated)" stroke="var(--border-strong)" />
            <circle cx="100" cy="50" r="14" fill="var(--surface)" stroke="var(--accent)" stroke-width="1" />
            <rect x="160" y="22" width="36" height="16" rx="2" fill="var(--bg-elevated)" stroke="var(--border-strong)" />
            <rect x="160" y="62" width="36" height="16" rx="2" fill="var(--bg-elevated)" stroke="var(--border-strong)" />
          </svg>
        </div>
      </a>
    {/if}
  {/if}
</section>

<!-- ─── Pull modal ─── -->
<Modal bind:open={pullOpen} title="Pull image" maxWidth="max-w-lg">
  <div class="space-y-3">
    <div>
      <label for="resource-pull-input" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Image name</label>
      <input
        id="resource-pull-input"
        type="text"
        class="dm-input text-sm"
        placeholder="nginx:alpine, postgres:16, ghcr.io/org/app:latest"
        bind:value={pullImageRef}
        oninput={onPullInput}
        disabled={pullBusy}
      />
    </div>
    {#if hubResults.length > 0 && !pullBusy}
      <div class="border border-[var(--border)] rounded-lg max-h-48 overflow-auto divide-y divide-[var(--border)]">
        {#each hubResults as r (r.repo_name)}
          <button type="button" class="w-full text-left px-3 py-2 hover:bg-[var(--surface-hover)] flex items-start gap-2" onclick={() => { pullImageRef = r.repo_name; hubResults = []; }}>
            <Package class="w-4 h-4 text-[var(--fg-muted)] shrink-0 mt-0.5" />
            <div class="min-w-0 flex-1">
              <div class="text-sm font-mono flex items-center gap-1.5">
                {r.repo_name}
                {#if r.is_official}<Badge variant="info">official</Badge>{/if}
              </div>
              <div class="text-[10px] text-[var(--fg-muted)] truncate">{r.short_description}</div>
            </div>
            <span class="text-[10px] text-[var(--fg-subtle)] shrink-0">★ {r.star_count}</span>
          </button>
        {/each}
      </div>
    {/if}
    <p class="text-xs text-[var(--fg-muted)]">
      Type to search Docker Hub, or enter a full image reference including registry and tag.
    </p>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (pullOpen = false)}>Cancel</Button>
    <Button variant="primary" loading={pullBusy} disabled={pullBusy || !pullImageRef.trim()} onclick={doPull}>
      <Download class="w-4 h-4" /> Pull
    </Button>
  {/snippet}
</Modal>

<!-- ─── Scan modal ─── -->
<Modal bind:open={scanOpen} title="Vulnerability scan" maxWidth="max-w-4xl">
  <div class="space-y-4">
    <div class="flex items-center gap-3">
      <div class="font-mono text-sm">{scanRef}</div>
      {#if scanReport}
        <Badge variant={scanReport.vulnerabilities.length === 0 ? 'success' : 'danger'} dot>
          {scanReport.vulnerabilities.length} finding{scanReport.vulnerabilities.length === 1 ? '' : 's'}
        </Badge>
      {:else if scanBusy}
        <Badge variant="warning" dot>scanning…</Badge>
      {/if}
    </div>

    {#if scanBusy && !scanReport}
      <!-- Independent {#if} so reactivity doesn't get tangled up with the
           report block via {:else if}. Earlier impl had the report in
           an else-branch which sometimes left "Scanning…" stuck on
           Svelte 5 reactivity edge. -->
      <div class="text-center py-8 text-sm text-[var(--fg-muted)]">Scanning…</div>
    {/if}

    {#if scanReport}
      <div class="grid grid-cols-3 sm:grid-cols-6 gap-2">
        {#each [['critical', scanReport.summary.critical], ['high', scanReport.summary.high], ['medium', scanReport.summary.medium], ['low', scanReport.summary.low], ['negligible', scanReport.summary.negligible], ['unknown', scanReport.summary.unknown]] as [sev, count] (sev)}
          <button type="button" class="dm-card p-2 text-center cursor-pointer hover:border-[var(--color-brand-500)]" class:scan-tile-active={scanFilter === sev} onclick={() => (scanFilter = scanFilter === sev ? 'all' : sev as Severity)}>
            <div class="text-lg font-bold tabular-nums">{count}</div>
            <div class="text-[10px] uppercase text-[var(--fg-muted)]">{sev}</div>
          </button>
        {/each}
      </div>
      {#if scanFiltered.length > 0}
        <div class="overflow-x-auto max-h-[50vh] overflow-y-auto">
          <table class="w-full text-xs">
            <thead class="sticky top-0 bg-[var(--bg-elevated)]">
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] uppercase tracking-wider">
                <th class="text-left px-3 py-2">Severity</th>
                <th class="text-left px-3 py-2">Package</th>
                <th class="text-left px-3 py-2">Version</th>
                <th class="text-left px-3 py-2">Fixed in</th>
                <th class="text-left px-3 py-2">CVE</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each scanFiltered as v (v.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-3 py-1.5"><Badge variant={sevColor(v.severity)}>{v.severity}</Badge></td>
                  <td class="px-3 py-1.5 font-mono">{v.package}</td>
                  <td class="px-3 py-1.5 font-mono">{v.version}</td>
                  <td class="px-3 py-1.5 font-mono">{v.fixed_in || '—'}</td>
                  <td class="px-3 py-1.5 font-mono">{v.id}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else}
        <div class="text-sm text-[var(--fg-muted)] text-center py-4">
          {scanReport.vulnerabilities.length === 0 ? 'No vulnerabilities found.' : 'No matches for this severity filter.'}
        </div>
      {/if}
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (scanOpen = false)}>Close</Button>
  {/snippet}
</Modal>

<!-- ─── Create-volume modal ─── -->
<Modal bind:open={volCreateOpen} title="Create volume" maxWidth="max-w-md">
  <form onsubmit={createVolume} class="space-y-3">
    <div>
      <label for="vol-name" class="block text-xs text-[var(--fg-muted)] mb-1.5">Name</label>
      <Input id="vol-name" bind:value={newVolName} placeholder="my-data" required />
    </div>
    <div>
      <label for="vol-driver" class="block text-xs text-[var(--fg-muted)] mb-1.5">Driver</label>
      <Input id="vol-driver" bind:value={newVolDriver} placeholder="local" />
    </div>
    {#if hosts.available.filter((h) => h.kind !== 'all').length > 1}
      <div>
        <label for="vol-host" class="block text-xs text-[var(--fg-muted)] mb-1.5">Host</label>
        <select id="vol-host" bind:value={newVolHost} class="dm-input text-sm w-full">
          {#each hosts.available.filter((h) => h.kind !== 'all') as h (h.id)}
            <option value={h.id}>{h.name}</option>
          {/each}
        </select>
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (volCreateOpen = false)}>Cancel</Button>
    <Button variant="primary" loading={volCreating} disabled={volCreating || !newVolName.trim()} onclick={createVolume}>
      <Plus class="w-4 h-4" /> Create
    </Button>
  {/snippet}
</Modal>

<!-- ─── System cleanup modal ─── -->
<Modal bind:open={cleanupOpen} title="System cleanup" maxWidth="max-w-lg">
  <div class="space-y-4">
    <p class="text-sm text-[var(--fg-muted)]">
      Run all three Docker prune commands in sequence. Operates on the currently
      selected host{isAll ? 's' : ''}. Cannot be undone.
    </p>
    <div class="cleanup-list">
      <div class="cleanup-row">
        <div class="cleanup-row-mark"><ImageIcon size={14} strokeWidth={1.5} /></div>
        <div class="cleanup-row-text">
          <div class="cleanup-row-title">Dangling images</div>
          <div class="cleanup-row-meta font-mono">
            {imageCounts.dangling} target{imageCounts.dangling === 1 ? '' : 's'} · would reclaim {fmtBytes(imageDanglingSize)}
          </div>
        </div>
      </div>
      <div class="cleanup-row">
        <div class="cleanup-row-mark"><HardDrive size={14} strokeWidth={1.5} /></div>
        <div class="cleanup-row-text">
          <div class="cleanup-row-title">Orphan volumes</div>
          <div class="cleanup-row-meta font-mono">
            {volumeCounts.orphan} target{volumeCounts.orphan === 1 ? '' : 's'} · data is unrecoverable once deleted
          </div>
        </div>
      </div>
      <div class="cleanup-row">
        <div class="cleanup-row-mark"><NetworkIcon size={14} strokeWidth={1.5} /></div>
        <div class="cleanup-row-text">
          <div class="cleanup-row-title">Empty networks</div>
          <div class="cleanup-row-meta font-mono">
            {networkCounts.empty} target{networkCounts.empty === 1 ? '' : 's'} · system networks are kept
          </div>
        </div>
      </div>
    </div>
    {#if imageCounts.dangling === 0 && volumeCounts.orphan === 0 && networkCounts.empty === 0}
      <p class="cleanup-empty font-mono">Nothing to prune — system is already clean.</p>
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (cleanupOpen = false)}>Cancel</Button>
    <Button
      variant="danger"
      loading={cleanupBusy}
      disabled={cleanupBusy || (imageCounts.dangling === 0 && volumeCounts.orphan === 0 && networkCounts.empty === 0)}
      onclick={runSystemCleanup}
    >
      <Trash2 class="w-4 h-4" /> Run cleanup
    </Button>
  {/snippet}
</Modal>

<!-- ─── Create-network modal ─── -->
<Modal bind:open={netCreateOpen} title="Create network" maxWidth="max-w-md">
  <form onsubmit={createNetwork} class="space-y-3">
    <div>
      <label for="net-name" class="block text-xs text-[var(--fg-muted)] mb-1.5">Name</label>
      <Input id="net-name" bind:value={newNetName} placeholder="my-net" required />
    </div>
    <div>
      <label for="net-driver" class="block text-xs text-[var(--fg-muted)] mb-1.5">Driver</label>
      <select id="net-driver" bind:value={newNetDriver} class="dm-input text-sm w-full">
        <option value="bridge">bridge</option>
        <option value="overlay">overlay</option>
        <option value="macvlan">macvlan</option>
        <option value="ipvlan">ipvlan</option>
      </select>
    </div>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (netCreateOpen = false)}>Cancel</Button>
    <Button variant="primary" loading={netCreating} disabled={netCreating || !newNetName.trim()} onclick={createNetwork}>
      <Plus class="w-4 h-4" /> Create
    </Button>
  {/snippet}
</Modal>

<style>
  .resources-page { display: block; }

  .res-header { max-width: 70ch; margin-bottom: 24px; }
  .res-title { font-size: 26px; }
  .res-subtitle {
    margin-top: 4px;
    color: var(--fg-muted);
    font-size: 13.5px;
    line-height: 1.6;
  }

  .res-tabs { margin-top: 8px; }
  .res-tabs-spacer { flex: 1; }
  .res-cleanup-btn { color: var(--fg-subtle); margin-bottom: 1px; }
  .res-cleanup-btn:hover { color: var(--fg); }

  /* Summary metric strip — 4 ed-metric tiles. */
  .res-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
    margin-top: 24px;
  }
  .res-summary :global(.ed-metric-value.warn) { color: var(--color-warning-400); }
  @media (max-width: 720px) {
    .res-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }

  /* Toolbar row above the table — search + pills + actions on one
     line with sensible wrap. */
  .res-toolbar {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    margin: 22px 0 14px;
  }
  .res-toolbar-search {
    position: relative;
    flex: 0 1 280px;
    max-width: 320px;
    min-width: 220px;
  }
  :global(.res-toolbar-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
  }
  /* Search input: underline-only style per the editorial language. The
     ed-underline-input class lives in app.css (used by the hosts page
     filter bar too). Padding-left makes room for the absolute icon. */
  .res-search-input { padding-left: 20px; font-size: 12px; }

  /* Segmented filter control — matches the resources mockup. Single
     border around the whole group with internal border-right separators
     between buttons; counts render as inline subtle-grey numbers (NOT
     the .ed-count square badge, which would be too busy in this dense
     row). The square-count convention still applies elsewhere — tab
     counts, sidebar counters etc. */
  .res-segctrl-label { margin-right: 2px; }
  .res-segctrl {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
    background: var(--bg);
  }
  .res-segctrl-btn {
    padding: 5px 10px;
    border: 0;
    border-right: 1px solid var(--border);
    background: transparent;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    transition: color 120ms, background 120ms;
  }
  .res-segctrl-btn:last-child { border-right: 0; }
  .res-segctrl-btn:hover { color: var(--fg-muted); }
  .res-segctrl-btn.active {
    background: var(--bg-elevated);
    color: var(--fg);
  }
  .res-segctrl-btn .seg-count {
    color: var(--fg-subtle);
    margin-left: 6px;
  }
  .res-segctrl-btn.active .seg-count { color: var(--fg-muted); }

  /* Prune button — local override of dm-btn-secondary so the visuals
     line up exactly with the mockup: transparent bg, hairline border,
     subtle grey text, gentle hover. The trailing dm-pill-warning count
     is tinted via res-action-pill below. */
  .res-prune-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 10px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: transparent;
    color: var(--fg-muted);
    font-family: var(--font-sans);
    font-size: 12px;
    cursor: pointer;
    transition: color 120ms, background 120ms, border-color 120ms;
  }
  .res-prune-btn:hover:not(:disabled) {
    color: var(--fg);
    border-color: var(--border-strong);
    background: var(--bg-elevated);
  }
  .res-prune-btn:disabled { opacity: 0.5; cursor: not-allowed; }

  .res-spacer { flex: 1; }
  .res-bulk-count {
    font-size: 11.5px;
    color: var(--fg);
    margin-right: 4px;
  }
  .res-bulk-danger { color: var(--color-danger-400); }
  .res-action-pill { font-size: 9.5px; margin-left: 4px; }

  .res-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .res-toggle input[type="checkbox"] { accent-color: var(--color-brand-500); }

  .res-warn-banner {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    margin-bottom: 12px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 30%, var(--border));
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
    color: var(--color-warning-400);
    font-size: 12px;
  }
  .res-warn-detail { color: var(--fg-muted); }

  /* Per-row layout. The .res-table / .res-row primitives live in
     app.css; below is just per-cell content styling. */
  .res-col-tag { min-width: 0; }
  .res-col-tag-line {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  /* Whole row name is the link target — operator clicks anywhere on the
     repo:tag / volume name / network name area to open the detail page.
     Inheriting underline=none + hover-accent preserves the visual
     consistency with the rest of the editorial system. */
  .res-col-tag-link {
    text-decoration: none;
    color: inherit;
  }
  .res-col-tag-link:hover { color: var(--accent-fg); }
  .res-col-tag-line.muted-name { color: var(--fg-muted); }
  .res-col-tag-line .muted { color: var(--fg-subtle); }
  .res-col-tag-line .accent { color: var(--accent-fg); }
  .res-col-tag-dangling {
    display: flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .res-col-tag-sha {
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 3px;
  }
  .res-col-tag-sha .accent { color: var(--accent-fg); }

  .res-cell-mono.right { text-align: right; }
  .res-cell-mono.ellipsis {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .muted { color: var(--fg-subtle); }
  .res-mini-pill { font-size: 9.5px; padding: 1px 6px; }
  .res-flags { display: flex; gap: 4px; flex-wrap: wrap; }
  .res-row-danger { color: var(--color-danger-400); }

  .res-foot {
    margin-top: 12px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    line-height: 1.6;
  }
  .res-foot code {
    font-family: var(--font-mono);
    color: var(--accent-fg);
    background: var(--bg-elevated);
    padding: 0 4px;
    border-radius: 3px;
  }

  .scan-tile-active { border-color: var(--color-brand-500); }

  /* Topology preview card at the bottom of the Networks tab. Links out
     to the standalone /topology page — keeps this combined view from
     turning into a "everything must fit on one page" stunt. */
  .res-topology {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 220px;
    gap: 24px;
    align-items: center;
    padding: 16px 20px 18px;
    margin-top: 28px;
    text-decoration: none;
    color: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .res-topology:hover {
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }
  .res-topology-text p {
    margin: 8px 0 10px;
    font-size: 13px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .res-topology-cta {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;
    color: var(--accent-fg);
    font-family: var(--font-mono);
  }
  .res-topology-mini {
    height: 100px;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
    background: var(--bg);
  }
  @media (max-width: 720px) {
    .res-topology { grid-template-columns: 1fr; }
    .res-topology-mini { display: none; }
  }

  /* CVE cell — three render modes (not scanned / clean / findings).
     "Findings" shows mockup-style severity counts: "C 1 H 3 M 6 L 14".
     Whole cell is a button so a click anywhere re-opens the scan. */
  .res-cve-cell {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    min-width: 0;
  }
  .res-cve-empty { font-size: 11px; color: var(--fg-subtle); }
  .res-cve-scan-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 1px 6px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    cursor: pointer;
    transition: color 120ms, border-color 120ms;
  }
  .res-cve-scan-btn:hover { color: var(--fg); border-color: var(--border-strong); }
  .res-cve-result {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    background: transparent;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .res-cve-badges { gap: 4px; flex-wrap: wrap; }
  .cve-badge {
    font-family: var(--font-mono);
    font-size: 10.5px;
    padding: 1px 5px;
    border-radius: 3px;
    font-weight: 500;
  }
  .cve-c { color: var(--color-danger-500); background: color-mix(in srgb, var(--color-danger-500) 18%, transparent); }
  .cve-h { color: var(--color-danger-400); background: color-mix(in srgb, var(--color-danger-400) 12%, transparent); }
  .cve-m { color: var(--color-warning-400); background: color-mix(in srgb, var(--color-warning-500) 14%, transparent); }
  .cve-l { color: var(--fg-subtle); background: var(--bg-elevated); }

  /* Volume / network name cell — plain text (the old separate detail
     pages were removed; modernization slice will reintroduce a drawer
     or dedicated page). The title attribute keeps the full name on
     hover for long ones. */
  .res-name-text {
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  /* System cleanup modal preview rows. */
  .cleanup-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px;
  }
  .cleanup-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border-radius: 4px;
  }
  .cleanup-row + .cleanup-row { border-top: 1px solid var(--border-subtle); }
  .cleanup-row-mark {
    width: 28px;
    height: 28px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--fg-subtle);
    flex-shrink: 0;
  }
  .cleanup-row-text { min-width: 0; flex: 1; }
  .cleanup-row-title {
    font-size: 13px;
    color: var(--fg);
  }
  .cleanup-row-meta {
    font-size: 11px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }
  .cleanup-empty {
    text-align: center;
    font-size: 12px;
    color: var(--fg-subtle);
    padding: 8px 0;
  }
</style>
