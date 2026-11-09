<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { vulnSeverityLabel } from '$lib/labels';
  // Images, volumes and networks of the hosts in the lens, one tab each.
  import { fmtBytes } from '$lib/format';
  import { fmtDate as fmtLocaleDate } from '$lib/i18n';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import {
    api, ApiError, isFanOut,
    type ScanReport, type Severity
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { latest } from '$lib/latest';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Skeleton, EmptyState, Modal, Button, Input, Badge, Tabs, StatBar } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import {
    Image as ImageIcon, HardDrive, Network as NetworkIcon,
    Search, Trash2, Download, Plus, Shield, Check,
    AlertTriangle, Package, ArrowUpRight
  } from 'lucide-svelte';

  // ?tab= picks the initial tab only.
  type Tab = 'images' | 'volumes' | 'networks';
  let tab = $state<Tab>(((new URLSearchParams(page.url.search).get('tab')) as Tab) || 'images');

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
  const realHosts = $derived(hosts.available.filter((h) => h.kind !== 'all'));

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
  // Latest scan report per host and image ref (see scanKey).
  let scanCache = $state<Map<string, ScanReport>>(new Map());
  let scanAllBusy = $state(false);

  function iKey(img: ImageSummary): string { return `${img.host_id ?? 'local'}/${img.Id}`; }
  // A report belongs to one host: the same tag can be other content elsewhere.
  function scanKey(img: ImageSummary): string { return `${rowHost(img)}/${imageRef(img)}`; }

  function imageRef(img: ImageSummary): string {
    return img.RepoTags?.[0] && img.RepoTags[0] !== '<none>:<none>' ? img.RepoTags[0] : img.Id;
  }

  const nextImages = latest();
  async function loadImages() {
    const current = nextImages();
    imagesLoading = true;
    try {
      const [imgRes, ctrRes] = await Promise.all([
        api.images.list(false, hosts.id),
        api.containers.list(true, hosts.id).catch(() => [])
      ]);
      if (!current()) return;
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
      if (current()) toast.error(m.resources_load_images_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      if (current()) imagesLoading = false;
    }
  }
  // GET /images/{id}/scan answers {"scanned": false} for an image that was
  // never scanned; that is "not scanned", not a report without findings.
  function normalizeScanReport(rep: ScanReport | null): ScanReport | null {
    if (!rep || rep.scanned === false || !rep.summary) return null;
    // An empty finding list arrives as null.
    if (!rep.vulnerabilities) rep.vulnerabilities = [];
    return rep;
  }
  async function backfillScanCache() {
    const next = new Map(scanCache);
    await Promise.all(images.map(async (img) => {
      const key = scanKey(img);
      if (next.has(key)) return;
      try {
        const rep = normalizeScanReport(await api.images.getScan(img.Id, rowHost(img)));
        if (rep) next.set(key, rep);
      } catch {
        /* host unreachable or image gone — render as "not scanned" */
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
    const rep = scanCache.get(scanKey(img));
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
    else imageSelected = new Set(visibleImages.map(iKey));
  }
  function toggleImageOne(img: ImageSummary) {
    const k = iKey(img);
    const n = new Set(imageSelected);
    if (n.has(k)) n.delete(k); else n.add(k);
    imageSelected = n;
  }

  async function pruneImages() {
    if (!(await confirm.ask({
      title: m.resources_prune_images_title(),
      message: m.resources_prune_images_message({ count: imageCounts.dangling }),
      body: m.resources_prune_images_body(),
      confirmLabel: m.resources_prune(), danger: true
    }))) return;
    const { done, failed } = await pruneOnHosts((h) => api.images.prune(h));
    if (failed.length) toast.error(m.resources_prune_failed(), failed.join(' · '));
    else toast.success(m.resources_pruned(), m.resources_reclaimed({ size: fmtBytes(done.reduce((s, r) => s + (r.SpaceReclaimed ?? 0), 0)) }));
    await loadImages();
  }
  async function removeImage(img: ImageSummary) {
    if (!(await confirm.ask({
      title: m.resources_remove_image_title(),
      message: m.resources_remove_image_message(),
      body: m.resources_remove_image_body(),
      confirmLabel: m.common_remove(), danger: true
    }))) return;
    try {
      await api.images.remove(img.Id, true, rowHost(img));
      toast.success(m.resources_removed());
      await loadImages();
    } catch (err) {
      toast.error(m.resources_remove_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkRemoveImages() {
    if (!(await confirm.ask({
      title: m.resources_remove_images_title(),
      message: m.resources_remove_images_message({ count: imageSelected.size }),
      body: m.resources_remove_images_body(),
      confirmLabel: m.common_remove(), danger: true
    }))) return;
    let ok = 0, fail = 0;
    for (const img of images) {
      if (!imageSelected.has(iKey(img))) continue;
      try { await api.images.remove(img.Id, true, rowHost(img)); ok++; } catch { fail++; }
    }
    toast.success(fail ? m.resources_removed_n_failed({ ok, fail }) : m.resources_removed_n({ ok }));
    imageSelected = new Set();
    await loadImages();
  }

  // ───────── Pull modal
  let pullOpen = $state(false);
  let pullImageRef = $state('');
  let pullBusy = $state(false);
  let pullHost = $state('local');
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
      await api.images.pull(pullImageRef.trim(), isAll ? pullHost : hosts.id || 'local');
      toast.success(m.resources_pulled(), pullImageRef);
      pullOpen = false;
      pullImageRef = '';
      hubResults = [];
      await loadImages();
    } catch (err) {
      toast.error(m.resources_pull_failed(), err instanceof ApiError ? err.message : undefined);
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
      const rep = normalizeScanReport(await api.images.scan(ref, rowHost(img)))!;
      scanReport = rep;
      // Feed the in-page cache so the row's CVE cell flips from
      // "not scanned" to severity badges without a page reload.
      const next = new Map(scanCache);
      next.set(scanKey(img), rep);
      scanCache = next;
      toast.success(m.resources_scan_complete(), m.resources_n_findings({ n: rep.vulnerabilities.length }));
    } catch (err) {
      toast.error(m.resources_scan_failed(), err instanceof ApiError ? err.message : m.resources_scanner_unavailable());
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
        const key = scanKey(img);
        if (scanCache.has(key)) continue;
        try {
          const rep = normalizeScanReport(await api.images.scan(imageRef(img), rowHost(img)))!;
          const next = new Map(scanCache);
          next.set(key, rep);
          scanCache = next;
          scanned++;
        } catch {
          failed++;
        }
      }
      const detail = scanned > 0 ? (failed > 0 ? m.resources_scanned_n_failed({ scanned, failed }) : m.resources_scanned_n({ scanned })) : m.resources_all_cached();
      toast.success(m.resources_scan_all_complete(), detail);
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

  const nextVolumes = latest();
  async function loadVolumes() {
    const current = nextVolumes();
    volumesLoading = true;
    try {
      const [volRes, ctrRes] = await Promise.all([
        api.volumes.list(hosts.id),
        api.containers.list(true, hosts.id).catch(() => [])
      ]);
      if (!current()) return;
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
        for (const mt of (c.Mounts ?? [])) {
          if (mt.Type === 'volume' && mt.Name) {
            const k = `${c.host_id ?? 'local'}/${mt.Name}`;
            usage.set(k, (usage.get(k) ?? 0) + 1);
          }
        }
      }
      volumeUsage = usage;
    } catch (err) {
      if (current()) toast.error(m.resources_load_volumes_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      if (current()) volumesLoading = false;
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
      toast.success(m.resources_volume_created(), newVolName);
      volCreateOpen = false;
      newVolName = '';
      newVolHost = hosts.id && !hosts.isAll ? hosts.id : 'local';
      await loadVolumes();
    } catch (err) {
      toast.error(m.resources_create_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      volCreating = false;
    }
  }
  // Rows of a single-host list carry no host_id; they belong to the host
  // the list was loaded for.
  function rowHost(r: { host_id?: string }): string {
    return r.host_id ?? (isAll ? 'local' : hosts.id || 'local');
  }
  function hostQuery(r: { host_id?: string }): string {
    const h = rowHost(r);
    return h !== 'local' ? `?host=${encodeURIComponent(h)}` : '';
  }

  // A prune acts on the selected host; under All hosts on every reachable
  // host in the lens, one after the other.
  function pruneHosts(): string[] {
    if (!isAll) return [hosts.id || 'local'];
    return hosts.available
      .filter((h) => h.kind !== 'all' && (h.id === 'local' || h.status === 'online'))
      .map((h) => h.id);
  }
  async function pruneOnHosts<T>(fn: (host: string) => Promise<T>): Promise<{ done: T[]; failed: string[] }> {
    const done: T[] = [];
    const failed: string[] = [];
    for (const id of pruneHosts()) {
      try {
        done.push(await fn(id));
      } catch (err) {
        const name = hosts.available.find((h) => h.id === id)?.name ?? id;
        failed.push(`${name}: ${err instanceof ApiError ? err.message : m.resources_failed_lc()}`);
      }
    }
    return { done, failed };
  }

  async function deleteVolume(v: VolumeRow) {
    if (!(await confirm.ask({
      title: m.resources_delete_volume_title(),
      message: m.resources_delete_volume_message({ name: v.Name }),
      body: m.resources_delete_volume_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    try {
      await api.volumes.remove(v.Name, true, rowHost(v));
      toast.success(m.resources_deleted(), v.Name);
      await loadVolumes();
    } catch (err) {
      toast.error(m.resources_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkDeleteVolumes() {
    if (!(await confirm.ask({
      title: m.resources_delete_volumes_title(),
      message: m.resources_delete_volumes_message({ count: volumeSelected.size }),
      body: m.resources_delete_volumes_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    let ok = 0, fail = 0;
    for (const v of volumes) {
      if (!volumeSelected.has(vKey(v))) continue;
      try { await api.volumes.remove(v.Name, true, rowHost(v)); ok++; } catch { fail++; }
    }
    toast.success(fail ? m.resources_deleted_n_failed({ ok, fail }) : m.resources_deleted_n({ ok }));
    volumeSelected = new Set();
    await loadVolumes();
  }
  async function pruneVolumes() {
    if (!(await confirm.ask({
      title: m.resources_prune_volumes_title(),
      message: m.resources_prune_volumes_message(),
      body: m.resources_prune_volumes_body(),
      confirmLabel: m.resources_prune(), danger: true
    }))) return;
    const { done, failed } = await pruneOnHosts((h) => api.volumes.prune(h));
    if (failed.length) toast.error(m.resources_prune_failed(), failed.join(' · '));
    else toast.success(m.resources_pruned(), m.resources_volumes_removed({ n: done.reduce((s, r) => s + (r?.VolumesDeleted?.length ?? 0), 0) }));
    await loadVolumes();
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

  const nextNetworks = latest();
  async function loadNetworks() {
    const current = nextNetworks();
    networksLoading = true;
    try {
      const r = await api.networks.list(hosts.id);
      if (!current()) return;
      if (isFanOut(r)) {
        networks = r.items as NetworkRow[];
        networksUnreachable = r.unreachable_hosts;
      } else {
        networks = r as NetworkRow[];
        networksUnreachable = [];
      }
    } catch (err) {
      if (current()) toast.error(m.resources_load_networks_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      if (current()) networksLoading = false;
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
  let newNetHost = $state('local');
  let netCreating = $state(false);
  async function createNetwork(e: Event) {
    e.preventDefault();
    netCreating = true;
    try {
      await api.networks.create(newNetName, newNetDriver, newNetHost);
      toast.success(m.resources_network_created(), newNetName);
      netCreateOpen = false;
      newNetName = '';
      await loadNetworks();
    } catch (err) {
      toast.error(m.resources_create_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      netCreating = false;
    }
  }
  async function deleteNetwork(n: NetworkRow) {
    if (!(await confirm.ask({
      title: m.resources_delete_network_title(),
      message: m.resources_delete_network_message({ name: n.Name }),
      body: m.resources_delete_network_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    try {
      await api.networks.remove(n.Id, rowHost(n));
      toast.success(m.resources_deleted(), n.Name);
      await loadNetworks();
    } catch (err) {
      toast.error(m.resources_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function pruneNetworks() {
    if (!(await confirm.ask({
      title: m.resources_prune_networks_title(),
      message: m.resources_prune_networks_message(),
      body: m.resources_prune_networks_body(),
      confirmLabel: m.resources_prune(), danger: true
    }))) return;
    const { failed } = await pruneOnHosts((h) => api.networks.prune(h));
    if (failed.length) toast.error(m.resources_prune_failed(), failed.join(' · '));
    else toast.success(m.resources_pruned());
    await loadNetworks();
  }
  function networkDriverPill(d: string): 'success' | 'warning' | 'default' {
    if (d === 'overlay') return 'success';
    if (d === 'macvlan' || d === 'ipvlan') return 'warning';
    return 'default';
  }

  // ===================================================================
  //  Loading orchestrator
  // ===================================================================
  // Fetch all three lists in parallel so the Images / Volumes /
  // Networks tab counters show real numbers from the first paint
  // instead of "0" until each tab is clicked. Re-runs whenever the
  // host selection changes (single-host vs. all-mode).
  async function loadAll() {
    await Promise.all([loadImages(), loadVolumes(), loadNetworks()]);
  }
  $effect(() => { hosts.id; untrack(loadAll); });
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
    const failures: string[] = [];
    const img = await pruneOnHosts((h) => api.images.prune(h));
    for (const r of img.done) {
      imagesPruned += r.ImagesDeleted?.length ?? 0;
      totalReclaimed += r.SpaceReclaimed ?? 0;
    }
    failures.push(...img.failed.map((f) => `${m.resources_cleanup_images_lc()}: ${f}`));
    const vol = await pruneOnHosts((h) => api.volumes.prune(h));
    for (const r of vol.done) {
      volumesPruned += r?.VolumesDeleted?.length ?? 0;
      totalReclaimed += r?.SpaceReclaimed ?? 0;
    }
    failures.push(...vol.failed.map((f) => `${m.resources_cleanup_volumes_lc()}: ${f}`));
    const net = await pruneOnHosts((h) => api.networks.prune(h));
    for (const r of net.done) networksPruned += r?.NetworksDeleted?.length ?? 0;
    failures.push(...net.failed.map((f) => `${m.resources_cleanup_networks_lc()}: ${f}`));
    cleanupBusy = false;
    cleanupOpen = false;
    if (failures.length > 0) {
      toast.error(m.resources_cleanup_partial(), failures.join(' · '));
    } else {
      toast.success(m.resources_cleanup_done(),
        m.resources_cleanup_done_detail({ images: imagesPruned, volumes: volumesPruned, networks: networksPruned, size: fmtBytes(totalReclaimed) }));
    }
    await loadAll();
  }

  // ───────── Helpers
  function fmtAge(unix: number): string {
    if (!unix) return '—';
    const d = (Date.now() / 1000 - unix) / 86400;
    if (d < 1) return m.resources_age_today();
    if (d < 7) return m.resources_age_d({ n: Math.floor(d) });
    if (d < 30) return m.resources_age_w({ n: Math.floor(d / 7) });
    if (d < 365) return m.resources_age_mo({ n: Math.floor(d / 30) });
    return m.resources_age_y({ n: Math.floor(d / 365) });
  }
  function fmtDate(s?: string): string {
    if (!s) return '—';
    const d = new Date(s);
    if (isNaN(d.getTime())) return s;
    return fmtLocaleDate(d);
  }
  function sevColor(s: Severity): 'danger' | 'warning' | 'info' | 'default' {
    if (s === 'critical' || s === 'high') return 'danger';
    if (s === 'medium') return 'warning';
    if (s === 'low') return 'info';
    return 'default';
  }
</script>

<section class="dm-page resources-page">
  <div class="dm-ph">
    <h1>{m.resources_title()}</h1>
    <span class="dm-sub">{m.resources_n_images({ count: images.length })} · {m.resources_n_volumes({ count: volumes.length })} · {m.resources_n_networks({ count: networks.length })}</span>
    <span class="dm-sp"></span>
    <button type="button" class="dm-btn" onclick={openCleanup} title={m.resources_cleanup_hint()}>
      <Trash2 size={13} strokeWidth={1.8} /> {m.resources_cleanup_ellipsis()}
    </button>
  </div>

  <Tabs
    label={m.resources_types()}
    active={tab}
    onchange={(k) => (tab = k as Tab)}
    tabs={[
      { key: 'images', label: m.common_images(), count: imageCounts.all },
      { key: 'volumes', label: m.common_volumes(), count: volumeCounts.all },
      { key: 'networks', label: m.common_networks(), count: networkCounts.user }
    ]}
  />

  <!-- ============================================================= -->
  <!--  IMAGES                                                        -->
  <!-- ============================================================= -->
  {#if tab === 'images'}
    <StatBar
      items={[
        { label: m.resources_stat_total(), value: imageCounts.all, note: fmtBytes(imageTotalSize) },
        { label: m.resources_stat_dangling(), value: imageCounts.dangling, note: fmtBytes(imageDanglingSize), tone: imageCounts.dangling > 0 ? 'warn' : '' },
        { label: m.resources_stat_reclaimable(), value: fmtBytes(imageDanglingSize), note: m.resources_stat_via_prune() },
        { label: m.resources_stat_in_use(), value: imageCounts.used, note: m.resources_stat_attached() }
      ]}
    />

    <div class="res-toolbar">
      <div class="res-toolbar-search">
        <Search size={12} strokeWidth={1.5} class="res-toolbar-search-icon" />
        <input type="text" class="ed-underline-input res-search-input" placeholder={m.resources_images_search_ph()} bind:value={imageSearch} />
      </div>
      <span class="ed-metric-meta res-segctrl-label">{m.resources_show()}</span>
      <div class="res-segctrl">
        {#each [
          ['all',m.resources_f_all(),imageCounts.all],
          ['used',m.resources_f_used(),imageCounts.used],
          ['unused',m.resources_f_unused(),imageCounts.unused],
          ['dangling',m.resources_f_dangling(),imageCounts.dangling],
          ['vulnerable','CVE',imageCounts.vulnerable]
        ] as [id,label,count] (id)}
          <button type="button" class="res-segctrl-btn" class:active={imageFilter === id} onclick={() => (imageFilter = id as any)}>
            {label}<span class="seg-count">{count}</span>
          </button>
        {/each}
      </div>
      <span class="res-spacer"></span>
      {#if imageSelected.size > 0 && canImageDelete}
        <span class="res-bulk-count font-mono">{m.resources_selected({ n: imageSelected.size })}</span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm res-bulk-danger" onclick={bulkRemoveImages}>
          <Trash2 size={12} strokeWidth={1.5} /> {m.common_remove()}
        </button>
      {/if}
      {#if canImageScan}
        <button type="button" class="res-prune-btn" onclick={scanAll} disabled={scanAllBusy || images.length === 0} title={m.resources_scan_all_hint()}>
          <Shield size={12} strokeWidth={1.5} /> {scanAllBusy ? m.resources_scanning() : m.resources_scan_all()}
        </button>
      {/if}
      {#if canImageDelete}
        <button type="button" class="res-prune-btn" onclick={pruneImages} disabled={imageCounts.dangling === 0}>
          <Trash2 size={12} strokeWidth={1.5} /> {m.resources_prune_dangling()}
          {#if imageCounts.dangling > 0}<span class="dm-pill dm-pill-warning res-action-pill">{imageCounts.dangling}</span>{/if}
        </button>
      {/if}
      {#if canImageCreate}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => { pullOpen = true; pullImageRef = ''; pullHost = realHosts[0]?.id ?? 'local'; hubResults = []; }}>
          <Download size={12} strokeWidth={1.5} /> {m.resources_pull_image()}
        </button>
      {/if}
    </div>

    {#if imagesUnreachable.length > 0}
      <div class="res-warn-banner">
        <AlertTriangle size={14} strokeWidth={1.5} />
        <span>{m.resources_unreachable({ n: imagesUnreachable.length })}</span>
        <span class="font-mono res-warn-detail">— {imagesUnreachable.map((u) => u.host_name).join(', ')}</span>
      </div>
    {/if}

    {#if imagesLoading && images.length === 0}
      <div class="dm-card" style="padding: 22px;"><Skeleton width="80%" height="6rem" /></div>
    {:else if visibleImages.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={ImageIcon} title={images.length === 0 ? m.resources_no_images() : m.resources_no_images_match()} description={images.length === 0 ? m.resources_no_images_hint() : m.resources_adjust_filter()}>
          {#snippet action()}
            {#if canImageCreate && images.length === 0}
              <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => (pullOpen = true)}>
                <Download size={12} strokeWidth={1.5} /> {m.resources_pull_image()}
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
          <span>{m.resources_col_repo_tag()}</span>
          <span>{m.common_size()}</span>
          <span>{m.common_created()}</span>
          <span>{m.resources_col_used_by()}</span>
          <span>{m.resources_col_vulns()}</span>
          <span>{m.common_host()}</span>
          <span></span>
        </div>
        {#each visibleImages as img (img.Id + (img.host_id ?? ''))}
          {@const rt = imageRepoTag(img)}
          {@const cve = scanCache.get(scanKey(img))}
          <div class="res-row" style="grid-template-columns: {grid};">
            <input type="checkbox" disabled={!canImageDelete} checked={imageSelected.has(iKey(img))} onchange={() => toggleImageOne(img)} />
            <div class="res-col-tag">
              {#if rt.repo}
                <a href={`/images/${encodeURIComponent(img.Id)}${img.host_id && img.host_id !== 'local' ? `?host=${encodeURIComponent(img.host_id)}` : ''}`} class="res-col-tag-line res-col-tag-link">
                  {rt.repo}<span class="muted">:</span><span class="accent">{rt.tag}</span>
                </a>
              {:else}
                <a href={`/images/${encodeURIComponent(img.Id)}${img.host_id && img.host_id !== 'local' ? `?host=${encodeURIComponent(img.host_id)}` : ''}`} class="res-col-tag-dangling res-col-tag-link">
                  <span class="dm-pill dm-pill-warning res-mini-pill">{m.resources_stat_dangling()}</span>
                  <span class="font-mono muted">&lt;none&gt;:&lt;none&gt;</span>
                </a>
              {/if}
              <div class="res-col-tag-sha font-mono">sha256:{img.Id.replace(/^sha256:/, '').slice(0, 12)}</div>
            </div>
            <span class="res-cell-mono right">{fmtBytes(img.Size)}</span>
            <span class="res-cell-mono">{fmtAge(img.Created)}</span>
            <span>
              {#if isUsed(img)}
                <span class="dm-pill dm-pill-success res-mini-pill"><span class="dm-pill-dot"></span> {m.resources_in_use_lc()}</span>
              {:else}
                <span class="font-mono muted">—</span>
              {/if}
            </span>
            <div class="res-cve-cell">
              {#if !cve}
                <span class="font-mono muted res-cve-empty">{m.resources_not_scanned()}</span>
                {#if canImageScan}
                  <button type="button" class="res-cve-scan-btn" onclick={() => scanImage(img)} title={m.resources_scan_with_grype()}>
                    <Shield size={10} strokeWidth={1.5} /> {m.resources_scan_lc()}
                  </button>
                {/if}
              {:else if cve.vulnerabilities.length === 0}
                <button type="button" class="res-cve-result" onclick={() => scanImage(img)} title={m.resources_rescan()}>
                  <span class="dm-pill dm-pill-success res-mini-pill"><Check size={10} strokeWidth={2} /> {m.resources_clean()}</span>
                </button>
              {:else}
                <button type="button" class="res-cve-result res-cve-badges" onclick={() => scanImage(img)} title={m.resources_open_report()}>
                  {#if cve.summary.critical > 0}<span class="cve-badge cve-c">{m.resources_cve_c({ n: cve.summary.critical })}</span>{/if}
                  {#if cve.summary.high > 0}<span class="cve-badge cve-h">{m.resources_cve_h({ n: cve.summary.high })}</span>{/if}
                  {#if cve.summary.medium > 0}<span class="cve-badge cve-m">{m.resources_cve_m({ n: cve.summary.medium })}</span>{/if}
                  {#if cve.summary.low > 0}<span class="cve-badge cve-l">{m.resources_cve_l({ n: cve.summary.low })}</span>{/if}
                </button>
              {/if}
            </div>
            <span class="res-cell-mono">{img.host_name ?? img.host_id ?? m.resources_local_lc()}</span>
            <span>
              {#if canImageDelete}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs res-row-danger" onclick={() => removeImage(img)} title={m.common_remove()}>
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
    <StatBar
      items={[
        { label: m.common_volumes(), value: volumeCounts.all, note: m.resources_from_stacks({ n: volumeCounts.compose }) },
        { label: m.resources_stat_orphaned(), value: volumeCounts.orphan, note: m.resources_stat_no_container(), tone: volumeCounts.orphan > 0 ? 'warn' : '' },
        { label: m.resources_stat_anonymous(), value: volumeCounts.anon, note: m.resources_stat_auto_named(), tone: volumeCounts.anon > 0 ? 'warn' : '' },
        { label: m.resources_stat_drivers(), value: volumeDriverCount, note: m.resources_in_use_lc() }
      ]}
    />

    <div class="res-toolbar">
      <div class="res-toolbar-search">
        <Search size={12} strokeWidth={1.5} class="res-toolbar-search-icon" />
        <input type="text" class="ed-underline-input res-search-input" placeholder={m.resources_volumes_search_ph()} bind:value={volumeSearch} />
      </div>
      <span class="ed-metric-meta res-segctrl-label">{m.resources_show()}</span>
      <div class="res-segctrl">
        {#each [
          ['all',m.resources_f_all(),volumeCounts.all],
          ['used',m.resources_in_use_lc(),volumeCounts.used],
          ['orphan',m.resources_f_orphan(),volumeCounts.orphan],
          ['anon',m.resources_f_anon(),volumeCounts.anon],
          ['compose','compose',volumeCounts.compose]
        ] as [id,label,count] (id)}
          <button type="button" class="res-segctrl-btn" class:active={volumeFilter === id} onclick={() => (volumeFilter = id as any)}>
            {label}<span class="seg-count">{count}</span>
          </button>
        {/each}
      </div>
      <span class="res-spacer"></span>
      {#if volumeSelected.size > 0 && canVolDelete}
        <span class="res-bulk-count font-mono">{m.resources_selected({ n: volumeSelected.size })}</span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm res-bulk-danger" onclick={bulkDeleteVolumes}>
          <Trash2 size={12} strokeWidth={1.5} /> {m.common_delete()}
        </button>
      {/if}
      {#if canVolDelete}
        <button type="button" class="res-prune-btn" onclick={pruneVolumes} disabled={volumeCounts.orphan === 0}>
          <Trash2 size={12} strokeWidth={1.5} /> {m.resources_prune_orphan()}
          {#if volumeCounts.orphan > 0}<span class="dm-pill dm-pill-warning res-action-pill">{volumeCounts.orphan}</span>{/if}
        </button>
      {/if}
      {#if canVolCreate}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => { volCreateOpen = true; newVolName = ''; newVolHost = hosts.id && !hosts.isAll ? hosts.id : 'local'; }}>
          <Plus size={12} strokeWidth={1.5} /> {m.resources_create_volume()}
        </button>
      {/if}
    </div>

    {#if volumesUnreachable.length > 0}
      <div class="res-warn-banner">
        <AlertTriangle size={14} strokeWidth={1.5} />
        <span>{m.resources_unreachable({ n: volumesUnreachable.length })}</span>
        <span class="font-mono res-warn-detail">— {volumesUnreachable.map((u) => u.host_name).join(', ')}</span>
      </div>
    {/if}

    {#if volumesLoading && volumes.length === 0}
      <div class="dm-card" style="padding: 22px;"><Skeleton width="80%" height="6rem" /></div>
    {:else if visibleVolumes.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={HardDrive} title={volumes.length === 0 ? m.resources_no_volumes() : m.resources_no_volumes_match()} description={volumes.length === 0 ? m.resources_no_volumes_hint() : m.resources_adjust_filter()} />
      </div>
    {:else}
      {@const grid = isAll
        ? '24px minmax(200px, 1.4fr) 90px minmax(180px, 1.6fr) 120px 100px 100px 28px'
        : '24px minmax(200px, 1.4fr) 90px minmax(180px, 1.6fr) 120px 100px 28px'}
      <div class="res-table">
        <div class="res-row res-row--head" style="grid-template-columns: {grid};">
          <input type="checkbox" disabled={!canVolDelete} checked={volumeSelected.size === visibleVolumes.length && visibleVolumes.length > 0} onchange={toggleVolumeAll} />
          <span>{m.common_name()}</span>
          <span>{m.resources_col_driver()}</span>
          <span>{m.resources_col_mountpoint()}</span>
          <span>{m.resources_col_used_by()}</span>
          <span>{m.common_created()}</span>
          {#if isAll}<span>{m.common_host()}</span>{/if}
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
                {#if anon}<span class="dm-pill dm-pill-warning res-mini-pill">{m.resources_f_anon()}</span>{/if}
              </a>
              {#if owner}
                <div class="res-col-tag-sha font-mono">{m.resources_stack_prefix()} <span class="accent">{owner}</span></div>
              {/if}
            </div>
            <div>
              <span class="dm-pill dm-pill-neutral res-mini-pill">{v.Driver}</span>
              {#if v.Scope === 'global'}<span class="dm-pill dm-pill-neutral res-mini-pill" style="margin-left: 4px;">global</span>{/if}
            </div>
            <span class="res-cell-mono ellipsis" title={v.Mountpoint}>{v.Mountpoint}</span>
            <span>
              {#if used > 0}
                <span class="dm-pill dm-pill-success res-mini-pill"><span class="dm-pill-dot"></span> {m.resources_n_containers({ count: used })}</span>
              {:else}
                <span class="dm-pill dm-pill-warning res-mini-pill">{m.resources_orphan()}</span>
              {/if}
            </span>
            <span class="res-cell-mono">{fmtDate(v.CreatedAt)}</span>
            {#if isAll}
              <span class="res-cell-mono">{v.host_name ?? v.host_id ?? '—'}</span>
            {/if}
            <span>
              {#if canVolDelete}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs res-row-danger" onclick={() => deleteVolume(v)} title={m.common_delete()}>
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              {/if}
            </span>
          </div>
        {/each}
      </div>
      <p class="res-foot">
        {m.resources_vol_foot_before()} <code>du -s</code>{m.resources_vol_foot_after()}
      </p>
    {/if}
  {/if}

  <!-- ============================================================= -->
  <!--  NETWORKS                                                      -->
  <!-- ============================================================= -->
  {#if tab === 'networks'}
    <StatBar
      items={[
        { label: m.common_networks(), value: networkCounts.user, note: showSystemNetworks ? m.resources_system_shown({ n: networkSystemCount }) : m.resources_system_hidden({ n: networkSystemCount }) },
        { label: m.resources_stat_internal(), value: networkCounts.internal, note: m.resources_stat_no_egress() },
        { label: m.resources_stat_attachable(), value: networkCounts.attachable, note: m.resources_stat_swarm() },
        { label: m.resources_stat_empty(), value: networkCounts.empty, note: m.resources_stat_zero_attached(), tone: networkCounts.empty > 0 ? 'warn' : '' }
      ]}
    />

    <div class="res-toolbar">
      <div class="res-toolbar-search">
        <Search size={12} strokeWidth={1.5} class="res-toolbar-search-icon" />
        <input type="text" class="ed-underline-input res-search-input" placeholder={m.resources_networks_search_ph()} bind:value={networkSearch} />
      </div>
      <span class="ed-metric-meta res-segctrl-label">{m.resources_show()}</span>
      <div class="res-segctrl">
        {#each [
          ['user',m.resources_f_user(),networkCounts.user],
          ['internal',m.resources_f_internal(),networkCounts.internal],
          ['attachable',m.resources_f_attachable(),networkCounts.attachable],
          ['empty',m.resources_f_empty(),networkCounts.empty]
        ] as [id,label,count] (id)}
          <button type="button" class="res-segctrl-btn" class:active={networkFilter === id} onclick={() => (networkFilter = id as any)}>
            {label}<span class="seg-count">{count}</span>
          </button>
        {/each}
      </div>
      <label class="res-toggle font-mono">
        <input type="checkbox" bind:checked={showSystemNetworks} />
        {m.resources_show_system()}
      </label>
      <span class="res-spacer"></span>
      {#if canNetDelete}
        <button type="button" class="res-prune-btn" onclick={pruneNetworks} disabled={networkCounts.empty === 0}>
          <Trash2 size={12} strokeWidth={1.5} /> {m.resources_prune_empty()}
          {#if networkCounts.empty > 0}<span class="dm-pill dm-pill-warning res-action-pill">{networkCounts.empty}</span>{/if}
        </button>
      {/if}
      {#if canNetCreate}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => { netCreateOpen = true; newNetName = ''; newNetDriver = 'bridge'; newNetHost = hosts.id && !hosts.isAll ? hosts.id : 'local'; }}>
          <Plus size={12} strokeWidth={1.5} /> {m.resources_create_network()}
        </button>
      {/if}
    </div>

    {#if networksUnreachable.length > 0}
      <div class="res-warn-banner">
        <AlertTriangle size={14} strokeWidth={1.5} />
        <span>{m.resources_unreachable({ n: networksUnreachable.length })}</span>
        <span class="font-mono res-warn-detail">— {networksUnreachable.map((u) => u.host_name).join(', ')}</span>
      </div>
    {/if}

    {#if networksLoading && networks.length === 0}
      <div class="dm-card" style="padding: 22px;"><Skeleton width="80%" height="6rem" /></div>
    {:else if visibleNetworks.length === 0}
      <div class="dm-card" style="padding: 36px;">
        <EmptyState icon={NetworkIcon} title={networks.length === 0 ? m.resources_no_networks() : m.resources_no_networks_match()} description={networks.length === 0 ? m.resources_no_networks_hint() : m.resources_no_networks_match_hint()} />
      </div>
    {:else}
      {@const grid = '24px minmax(200px, 1.5fr) 120px 130px 150px 90px 100px 28px'}
      <div class="res-table">
        <div class="res-row res-row--head" style="grid-template-columns: {grid};">
          <span></span>
          <span>{m.common_name()}</span>
          <span>{m.resources_col_driver_scope()}</span>
          <span>{m.resources_col_subnet()}</span>
          <span>{m.resources_col_flags()}</span>
          <span>{m.common_containers()}</span>
          <span>{m.common_created()}</span>
          <span></span>
        </div>
        {#each visibleNetworks as n (n.Id)}
          {@const owner = networkOwner(n)}
          {@const subnet = n.IPAM?.Config?.[0]?.Subnet ?? '—'}
          {@const cnt = networkContainerCount(n)}
          <div class="res-row" style="grid-template-columns: {grid}; opacity: {isSystem(n) ? '0.7' : '1'};">
            <span></span>
            <div class="res-col-tag">
              <a href={`/networks/${n.Id}${hostQuery(n)}`} class="res-col-tag-line res-col-tag-link">
                <span class="res-name-text" title={n.Name}>{n.Name}</span>
                {#if isSystem(n)}<span class="dm-pill dm-pill-neutral res-mini-pill">{m.resources_system()}</span>{/if}
              </a>
              {#if owner}
                <div class="res-col-tag-sha font-mono">{m.resources_stack_prefix()} <span class="accent">{owner}</span></div>
              {:else}
                <div class="res-col-tag-sha font-mono">{m.resources_id_prefix()} {n.Id.slice(0, 12)}</div>
              {/if}
            </div>
            <div>
              <span class="dm-pill dm-pill-{networkDriverPill(n.Driver)} res-mini-pill">{n.Driver}</span>
              <span class="res-cell-mono" style="margin-left: 6px;">{n.Scope}</span>
            </div>
            <span class="res-cell-mono">{subnet}</span>
            <div class="res-flags">
              {#if n.Internal}<span class="dm-pill dm-pill-warning res-mini-pill">{m.resources_f_internal()}</span>{/if}
              {#if n.Attachable}<span class="dm-pill dm-pill-neutral res-mini-pill">{m.resources_f_attachable()}</span>{/if}
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
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs res-row-danger" onclick={() => deleteNetwork(n)} title={m.common_delete()}>
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              {/if}
            </span>
          </div>
        {/each}
      </div>

      <a href="/topology" class="dm-card res-topology">
        <div class="res-topology-text">
          <Eyebrow>{m.resources_topology_eyebrow()}</Eyebrow>
          <p>
            {m.resources_topology_text()}
          </p>
          <span class="res-topology-cta">{m.resources_topology_cta()} <ArrowUpRight size={12} strokeWidth={1.5} /></span>
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
<Modal bind:open={pullOpen} title={m.resources_pull_image()} maxWidth="max-w-lg">
  <div class="space-y-3">
    <div>
      <label for="resource-pull-input" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.resources_image_name()}</label>
      <input
        id="resource-pull-input"
        type="text"
        class="dm-input text-sm"
        placeholder={m.resources_pull_ph()}
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
                {#if r.is_official}<Badge variant="info">{m.resources_official()}</Badge>{/if}
              </div>
              <div class="text-[10px] text-[var(--fg-muted)] truncate">{r.short_description}</div>
            </div>
            <span class="text-[10px] text-[var(--fg-subtle)] shrink-0">★ {r.star_count}</span>
          </button>
        {/each}
      </div>
    {/if}
    <p class="text-xs text-[var(--fg-muted)]">
      {m.resources_pull_hint()}
    </p>
    {#if isAll && realHosts.length > 1}
      <div>
        <label for="resource-pull-host" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.common_host()}</label>
        <select id="resource-pull-host" bind:value={pullHost} class="dm-input text-sm w-full" disabled={pullBusy}>
          {#each realHosts as h (h.id)}
            <option value={h.id}>{h.name}</option>
          {/each}
        </select>
      </div>
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (pullOpen = false)}>{m.common_cancel()}</Button>
    <Button variant="primary" loading={pullBusy} disabled={pullBusy || !pullImageRef.trim()} onclick={doPull}>
      <Download class="w-4 h-4" /> {m.resources_pull()}
    </Button>
  {/snippet}
</Modal>

<!-- ─── Scan modal ─── -->
<Modal bind:open={scanOpen} title={m.resources_scan_title()} maxWidth="max-w-4xl">
  <div class="space-y-4">
    <div class="flex items-center gap-3">
      <div class="font-mono text-sm">{scanRef}</div>
      {#if scanReport}
        <Badge variant={scanReport.vulnerabilities.length === 0 ? 'success' : 'danger'} dot>
          {m.resources_findings({ count: scanReport.vulnerabilities.length })}
        </Badge>
      {:else if scanBusy}
        <Badge variant="warning" dot>{m.resources_scanning_lc()}</Badge>
      {/if}
    </div>

    {#if scanBusy && !scanReport}
      <!-- Independent {#if} so reactivity doesn't get tangled up with the
           report block via {:else if}. Earlier impl had the report in
           an else-branch which sometimes left "Scanning…" stuck on
           Svelte 5 reactivity edge. -->
      <div class="text-center py-8 text-sm text-[var(--fg-muted)]">{m.resources_scanning()}</div>
    {/if}

    {#if scanReport}
      <div class="grid grid-cols-3 sm:grid-cols-6 gap-2">
        {#each [['critical', scanReport.summary.critical], ['high', scanReport.summary.high], ['medium', scanReport.summary.medium], ['low', scanReport.summary.low], ['negligible', scanReport.summary.negligible], ['unknown', scanReport.summary.unknown]] as [sev, count] (sev)}
          <button type="button" class="dm-card p-2 text-center cursor-pointer hover:border-[var(--color-brand-500)]" class:scan-tile-active={scanFilter === sev} onclick={() => (scanFilter = scanFilter === sev ? 'all' : sev as Severity)}>
            <div class="text-lg font-bold tabular-nums">{count}</div>
            <div class="text-[10px] uppercase text-[var(--fg-muted)]">{vulnSeverityLabel(String(sev))}</div>
          </button>
        {/each}
      </div>
      {#if scanFiltered.length > 0}
        <div class="overflow-x-auto max-h-[50vh] overflow-y-auto">
          <table class="w-full text-xs">
            <thead class="sticky top-0 bg-[var(--bg-elevated)]">
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] uppercase tracking-wider">
                <th class="text-left px-3 py-2">{m.resources_col_severity()}</th>
                <th class="text-left px-3 py-2">{m.resources_col_package()}</th>
                <th class="text-left px-3 py-2">{m.resources_col_version()}</th>
                <th class="text-left px-3 py-2">{m.resources_col_fixed_in()}</th>
                <th class="text-left px-3 py-2">CVE</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each scanFiltered as v (v.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-3 py-1.5"><Badge variant={sevColor(v.severity)}>{vulnSeverityLabel(v.severity)}</Badge></td>
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
          {scanReport.vulnerabilities.length === 0 ? m.resources_no_vulns() : m.resources_no_sev_match()}
        </div>
      {/if}
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (scanOpen = false)}>{m.common_close()}</Button>
  {/snippet}
</Modal>

<!-- ─── Create-volume modal ─── -->
<Modal bind:open={volCreateOpen} title={m.resources_create_volume()} maxWidth="max-w-md">
  <form onsubmit={createVolume} class="space-y-3">
    <div>
      <label for="vol-name" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.common_name()}</label>
      <Input id="vol-name" bind:value={newVolName} placeholder="my-data" required />
    </div>
    <div>
      <label for="vol-driver" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.resources_col_driver()}</label>
      <Input id="vol-driver" bind:value={newVolDriver} placeholder="local" />
    </div>
    {#if realHosts.length > 1}
      <div>
        <label for="vol-host" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.common_host()}</label>
        <select id="vol-host" bind:value={newVolHost} class="dm-input text-sm w-full">
          {#each realHosts as h (h.id)}
            <option value={h.id}>{h.name}</option>
          {/each}
        </select>
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (volCreateOpen = false)}>{m.common_cancel()}</Button>
    <Button variant="primary" loading={volCreating} disabled={volCreating || !newVolName.trim()} onclick={createVolume}>
      <Plus class="w-4 h-4" /> {m.common_create()}
    </Button>
  {/snippet}
</Modal>

<!-- ─── System cleanup modal ─── -->
<Modal bind:open={cleanupOpen} title={m.resources_cleanup_title()} maxWidth="max-w-lg">
  <div class="space-y-4">
    <p class="text-sm text-[var(--fg-muted)]">
      {isAll ? m.resources_cleanup_intro_all() : m.resources_cleanup_intro()}
    </p>
    <div class="cleanup-list">
      <div class="cleanup-row">
        <div class="cleanup-row-mark"><ImageIcon size={14} strokeWidth={1.5} /></div>
        <div class="cleanup-row-text">
          <div class="cleanup-row-title">{m.resources_cleanup_dangling()}</div>
          <div class="cleanup-row-meta font-mono">
            {m.resources_targets({ count: imageCounts.dangling })} · {m.resources_would_reclaim({ size: fmtBytes(imageDanglingSize) })}
          </div>
        </div>
      </div>
      <div class="cleanup-row">
        <div class="cleanup-row-mark"><HardDrive size={14} strokeWidth={1.5} /></div>
        <div class="cleanup-row-text">
          <div class="cleanup-row-title">{m.resources_cleanup_orphan()}</div>
          <div class="cleanup-row-meta font-mono">
            {m.resources_targets({ count: volumeCounts.orphan })} · {m.resources_unrecoverable()}
          </div>
        </div>
      </div>
      <div class="cleanup-row">
        <div class="cleanup-row-mark"><NetworkIcon size={14} strokeWidth={1.5} /></div>
        <div class="cleanup-row-text">
          <div class="cleanup-row-title">{m.resources_cleanup_empty()}</div>
          <div class="cleanup-row-meta font-mono">
            {m.resources_targets({ count: networkCounts.empty })} · {m.resources_system_kept()}
          </div>
        </div>
      </div>
    </div>
    {#if imageCounts.dangling === 0 && volumeCounts.orphan === 0 && networkCounts.empty === 0}
      <p class="cleanup-empty font-mono">{m.resources_nothing_to_prune()}</p>
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (cleanupOpen = false)}>{m.common_cancel()}</Button>
    <Button
      variant="danger"
      loading={cleanupBusy}
      disabled={cleanupBusy || (imageCounts.dangling === 0 && volumeCounts.orphan === 0 && networkCounts.empty === 0)}
      onclick={runSystemCleanup}
    >
      <Trash2 class="w-4 h-4" /> {m.resources_run_cleanup()}
    </Button>
  {/snippet}
</Modal>

<!-- ─── Create-network modal ─── -->
<Modal bind:open={netCreateOpen} title={m.resources_create_network()} maxWidth="max-w-md">
  <form onsubmit={createNetwork} class="space-y-3">
    <div>
      <label for="net-name" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.common_name()}</label>
      <Input id="net-name" bind:value={newNetName} placeholder="my-net" required />
    </div>
    <div>
      <label for="net-driver" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.resources_col_driver()}</label>
      <select id="net-driver" bind:value={newNetDriver} class="dm-input text-sm w-full">
        <option value="bridge">bridge</option>
        <option value="overlay">overlay</option>
        <option value="macvlan">macvlan</option>
        <option value="ipvlan">ipvlan</option>
      </select>
    </div>
    {#if realHosts.length > 1}
      <div>
        <label for="net-host" class="block text-xs text-[var(--fg-muted)] mb-1.5">{m.common_host()}</label>
        <select id="net-host" bind:value={newNetHost} class="dm-input text-sm w-full">
          {#each realHosts as h (h.id)}
            <option value={h.id}>{h.name}</option>
          {/each}
        </select>
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (netCreateOpen = false)}>{m.common_cancel()}</Button>
    <Button variant="primary" loading={netCreating} disabled={netCreating || !newNetName.trim()} onclick={createNetwork}>
      <Plus class="w-4 h-4" /> {m.common_create()}
    </Button>
  {/snippet}
</Modal>

<style>
  .resources-page { display: block; }

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
  .res-search-input { padding-left: 20px; font-size: 12px; }

  .res-segctrl-label { margin-right: 2px; }
  .res-segctrl {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
    background: var(--bg-elevated);
  }
  .res-segctrl-btn {
    padding: 5px 10px;
    border: 0;
    border-right: 1px solid var(--border);
    background: transparent;
    cursor: pointer;
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
  .res-action-pill { font-size: 11.5px; margin-left: 4px; }

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
    font-size: 12px;
  }
  .res-col-tag-sha {
    font-size: 11.5px;
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
  .res-mini-pill { font-size: 11.5px; padding: 1px 6px; }
  .res-flags { display: flex; gap: 4px; flex-wrap: wrap; }
  .res-row-danger { color: var(--color-danger-400); }

  .res-foot {
    margin-top: 12px;
    font-size: 11.5px;
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
  }
  .res-topology-mini {
    height: 100px;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
    background: var(--bg-elevated);
  }
  @media (max-width: 720px) {
    .res-topology { grid-template-columns: 1fr; }
    .res-topology-mini { display: none; }
  }

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
    font-size: 11.5px;
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
    font-size: 11.5px;
    padding: 1px 5px;
    border-radius: 3px;
    font-weight: 500;
  }
  .cve-c { color: var(--color-danger-500); background: color-mix(in srgb, var(--color-danger-500) 18%, transparent); }
  .cve-h { color: var(--color-danger-400); background: color-mix(in srgb, var(--color-danger-400) 12%, transparent); }
  .cve-m { color: var(--color-warning-400); background: color-mix(in srgb, var(--color-warning-500) 14%, transparent); }
  .cve-l { color: var(--fg-subtle); background: var(--bg-elevated); }

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
