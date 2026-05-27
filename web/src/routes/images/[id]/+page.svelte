<script lang="ts">
  // Image detail — editorial. Header + summary tiles + tabs (Overview ·
  // Vulnerabilities · Layers · History · Containers). Backend GET
  // /images/{id} returns the full Docker ImageInspect; on remote agents
  // it currently 501s (agent protocol gap) and we fall back to data
  // already on hand from the list payload + a banner.
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError, isFanOut, type ScanReport, type Severity } from '$lib/api';
  import { Skeleton, EmptyState, Badge } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import {
    ChevronLeft, Image as ImageIcon, Trash2, Copy, Box, Shield, Check,
    AlertTriangle, RefreshCw, Download
  } from 'lucide-svelte';

  const id = $derived($page.params.id);
  const hostId = $derived($page.url.searchParams.get('host') || 'local');

  type Tab = 'overview' | 'cve' | 'layers' | 'history' | 'containers';
  let tab = $state<Tab>(((new URLSearchParams($page.url.search).get('tab')) as Tab) || 'overview');

  let inspect = $state<any>(null);
  let inspectLoading = $state(true);
  let inspectError = $state<string | null>(null);
  let notSupported = $state(false);
  async function loadInspect() {
    inspectLoading = true;
    inspectError = null;
    notSupported = false;
    try {
      inspect = await api.images.inspect(id, hostId);
    } catch (err) {
      if (err instanceof ApiError && err.status === 501) {
        notSupported = true;
      } else {
        inspectError = err instanceof ApiError ? err.message : 'failed to load';
      }
    } finally {
      inspectLoading = false;
    }
  }

  // Containers using this image. Filtered from /containers list by
  // ImageID match. Cheaper than another endpoint and matches what the
  // resources page uses for the "in use" pill.
  interface UsingContainer {
    id: string;
    name: string;
    state: string;
    started: number;
  }
  let using = $state<UsingContainer[]>([]);
  let usingLoading = $state(false);
  async function loadUsing() {
    usingLoading = true;
    try {
      const r = await api.containers.list(true, hostId);
      const list: any[] = isFanOut(r) ? r.items : (r as any[]);
      const out: UsingContainer[] = [];
      const wantId = inspect?.Id ?? id;
      for (const c of list) {
        if (c.ImageID === wantId || c.ImageID === id) {
          out.push({
            id: c.Id,
            name: (c.Names?.[0] ?? c.Id).replace(/^\//, ''),
            state: c.State,
            started: c.Created
          });
        }
      }
      using = out;
    } catch (err) {
      toast.error('Failed to load containers', err instanceof ApiError ? err.message : undefined);
    } finally {
      usingLoading = false;
    }
  }

  // Scan state — cached scan loaded on-mount via getScan; fresh scan via
  // POST. Same render shape as /resources CVE cell + scan modal merged
  // into a tab. Defensive about Go's null-vs-empty slice (see resources
  // page comment for the same fix).
  let scanReport = $state<ScanReport | null>(null);
  let scanBusy = $state(false);
  let scanFilter = $state<Severity | 'all'>('all');
  function imageRef(): string {
    if (inspect?.RepoTags && inspect.RepoTags.length > 0 && inspect.RepoTags[0] !== '<none>:<none>') {
      return inspect.RepoTags[0];
    }
    return inspect?.Id ?? id;
  }
  function normalizeReport(rep: ScanReport | null): ScanReport | null {
    if (!rep) return null;
    if (!rep.vulnerabilities) rep.vulnerabilities = [];
    return rep;
  }
  async function loadCachedScan() {
    try {
      scanReport = normalizeReport(await api.images.getScan(id));
    } catch {
      /* 404 = never scanned */
      scanReport = null;
    }
  }
  async function runScan() {
    scanBusy = true;
    try {
      scanReport = normalizeReport(await api.images.scan(imageRef()))!;
      toast.success('Scan complete', `${scanReport.vulnerabilities.length} findings`);
    } catch (err) {
      toast.error('Scan failed', err instanceof ApiError ? err.message : 'Scanner unavailable');
    } finally {
      scanBusy = false;
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

  $effect(() => { id; hostId; loadInspect(); loadCachedScan(); });
  $effect(() => {
    if (tab === 'containers' && inspect) loadUsing();
  });

  function fmtBytes(n: number): string {
    if (!n) return '0 B';
    if (n < 1024) return `${n} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let v = n / 1024, i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return `${v.toFixed(1)} ${units[i]}`;
  }
  function fmtDate(iso?: string | number): string {
    if (!iso) return '—';
    const d = typeof iso === 'number' ? new Date(iso * 1000) : new Date(iso);
    if (isNaN(d.getTime())) return String(iso);
    return d.toISOString().slice(0, 10);
  }
  function shortSha(s: string): string {
    return s.replace(/^sha256:/, '').slice(0, 12);
  }
  function copy(s: string) {
    navigator.clipboard.writeText(s).then(
      () => toast.success('Copied'),
      () => toast.error('Copy failed')
    );
  }
  function sevColor(s: Severity): 'danger' | 'warning' | 'info' | 'default' {
    if (s === 'critical' || s === 'high') return 'danger';
    if (s === 'medium') return 'warning';
    if (s === 'low') return 'info';
    return 'default';
  }

  const repoTag = $derived.by(() => {
    const t = inspect?.RepoTags?.[0];
    if (!t || t === '<none>:<none>') return null;
    const i = t.lastIndexOf(':');
    return { repo: t.slice(0, i), tag: t.slice(i + 1) };
  });
  const labelEntries = $derived(inspect?.Config?.Labels ? Object.entries(inspect.Config.Labels) : []);
  const layers = $derived<string[]>(inspect?.RootFS?.Layers ?? []);
  const history = $derived<any[]>(inspect?.History ?? []);
  const canPull = $derived(allowed('images.create'));
  const canDelete = $derived(allowed('images.delete'));
  const canScan = $derived(allowed('images.scan'));

  $effect(() => {
    if (repoTag) {
      pageContext.set(`${repoTag.repo}:${repoTag.tag}`);
    } else {
      pageContext.set(shortSha(inspect?.Id ?? id));
    }
    return () => pageContext.clear();
  });

  async function deleteImage() {
    if (!(await confirm.ask({
      title: 'Remove image',
      message: 'Remove this image?',
      body: 'Docker refuses if any container is still using it — stop those containers first.',
      confirmLabel: 'Remove', danger: true
    }))) return;
    try {
      await api.images.remove(id, true);
      toast.success('Removed');
      goto('/resources?tab=images');
    } catch (err) {
      toast.error('Remove failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function pullLatest() {
    if (!repoTag) {
      toast.error('Cannot pull', 'image has no repo:tag');
      return;
    }
    try {
      await api.images.pull(`${repoTag.repo}:${repoTag.tag}`);
      toast.success('Pull queued', `${repoTag.repo}:${repoTag.tag}`);
      await loadInspect();
    } catch (err) {
      toast.error('Pull failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<section class="img-detail">
  <header class="img-detail-header">
    <div class="img-detail-back-row">
      <a href="/resources?tab=images" class="img-detail-back" aria-label="Back to resources">
        <ChevronLeft size={14} strokeWidth={1.5} /> resources / images
      </a>
    </div>
    <div class="img-detail-title-row">
      <div class="img-detail-title-text">
        <h1 class="ed-title img-detail-title">
          {#if repoTag}
            {repoTag.repo}:{repoTag.tag}
          {:else if inspect}
            &lt;none&gt;:&lt;none&gt;
            <span class="dm-pill dm-pill-warning img-detail-pill">dangling</span>
          {:else}
            {shortSha(id)}
          {/if}
        </h1>
        <p class="ed-subtitle img-detail-subtitle">
          sha256:{inspect?.Id ? shortSha(inspect.Id) : shortSha(id)} · {fmtBytes(inspect?.Size ?? 0)} · host {hostId}
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={loadInspect}>
          <RefreshCw size={12} strokeWidth={1.5} class={inspectLoading ? 'ed-spin' : ''} /> Refresh
        </button>
        {#if canPull && repoTag}
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={pullLatest}>
            <Download size={12} strokeWidth={1.5} /> Re-pull
          </button>
        {/if}
        {#if canDelete}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm img-detail-danger" onclick={deleteImage}>
            <Trash2 size={12} strokeWidth={1.5} /> Remove image
          </button>
        {/if}
      </div>
    </div>
  </header>

  {#if notSupported}
    <div class="img-detail-banner">
      <AlertTriangle size={14} strokeWidth={1.5} />
      <div>
        <strong>Inspect not supported on this host.</strong>
        Remote agents don't carry the image-inspect frame yet.
        Layers, history, and config require running the inspect on the local docker socket.
      </div>
    </div>
  {/if}
  {#if inspectError}
    <div class="img-detail-banner img-detail-banner--err">
      <AlertTriangle size={14} strokeWidth={1.5} />
      <div>{inspectError}</div>
    </div>
  {/if}

  <!-- ─── Summary tiles ─── -->
  <div class="img-detail-summary">
    <div class="ed-metric">
      <span class="ed-metric-label">Size</span>
      <div class="ed-metric-value">{fmtBytes(inspect?.Size ?? 0)}</div>
      <span class="ed-metric-meta">{layers.length || '—'} layer{layers.length === 1 ? '' : 's'}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">In use</span>
      <div class="ed-metric-value">{using.length || (usingLoading ? '…' : '—')}</div>
      <span class="ed-metric-meta">container{using.length === 1 ? '' : 's'}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">Created</span>
      <div class="ed-metric-value img-detail-tile-text">{fmtDate(inspect?.Created)}</div>
      <span class="ed-metric-meta">{inspect?.Architecture ?? '—'}/{inspect?.Os ?? '—'}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">Vulnerabilities</span>
      {#if scanReport}
        {@const sum = scanReport.summary}
        {@const total = sum.critical + sum.high + sum.medium + sum.low}
        <div class="ed-metric-value" class:warn={sum.critical > 0 || sum.high > 0}>
          {total === 0 ? '0' : total}
        </div>
        <span class="ed-metric-meta">
          {#if total === 0}clean{:else}
            {#if sum.critical > 0}C {sum.critical} {/if}
            {#if sum.high > 0}H {sum.high} {/if}
            {#if sum.medium > 0}M {sum.medium} {/if}
            {#if sum.low > 0}L {sum.low}{/if}
          {/if}
        </span>
      {:else}
        <div class="ed-metric-value muted">—</div>
        <span class="ed-metric-meta">not scanned</span>
      {/if}
    </div>
  </div>

  <!-- ─── Tabs ─── -->
  <div class="ed-tabs img-detail-tabs">
    <button type="button" class="ed-tab" class:active={tab === 'overview'} onclick={() => (tab = 'overview')}>
      Overview
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'cve'} onclick={() => (tab = 'cve')}>
      Vulnerabilities
      {#if scanReport}
        <span class="count">{scanReport.vulnerabilities.length}</span>
      {/if}
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'layers'} onclick={() => (tab = 'layers')}>
      Layers <span class="count">{layers.length}</span>
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'history'} onclick={() => (tab = 'history')}>
      History <span class="count">{history.length}</span>
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'containers'} onclick={() => (tab = 'containers')}>
      Containers <span class="count">{using.length}</span>
    </button>
  </div>

  <!-- ============================================================== -->
  <!--  OVERVIEW                                                       -->
  <!-- ============================================================== -->
  {#if tab === 'overview'}
    {#if inspectLoading && !inspect}
      <div class="dm-card img-detail-card-pad"><Skeleton width="80%" height="6rem" /></div>
    {:else if inspect}
      <div class="dm-card img-detail-overview">
        <dl class="img-detail-dl">
          <dt>ID</dt>
          <dd class="img-detail-id-line">
            <span class="font-mono muted">{inspect.Id}</span>
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => copy(inspect.Id)} title="Copy">
              <Copy size={11} strokeWidth={1.5} />
            </button>
          </dd>
          {#if inspect.RepoTags && inspect.RepoTags.length > 0}
            <dt>Tags</dt>
            <dd>
              <div class="img-detail-chips">
                {#each inspect.RepoTags as t (t)}
                  <span class="img-detail-chip font-mono">{t}</span>
                {/each}
              </div>
            </dd>
          {/if}
          {#if inspect.RepoDigests && inspect.RepoDigests.length > 0}
            <dt>Digests</dt>
            <dd>
              <div class="img-detail-chips">
                {#each inspect.RepoDigests as d (d)}
                  <span class="img-detail-chip img-detail-chip--digest font-mono">{d}</span>
                {/each}
              </div>
            </dd>
          {/if}
          <dt>Architecture</dt>
          <dd class="font-mono">{inspect.Architecture}/{inspect.Os}{inspect.Variant ? `/${inspect.Variant}` : ''}</dd>
          <dt>Created</dt>
          <dd class="font-mono">{inspect.Created}</dd>
          <dt>Author</dt>
          <dd class="font-mono">{inspect.Author || '—'}</dd>
          <dt>Size</dt>
          <dd class="font-mono">{fmtBytes(inspect.Size)} ({fmtBytes(inspect.VirtualSize ?? inspect.Size)} on disk virtual)</dd>
          {#if inspect.Config?.Cmd}
            <dt>Cmd</dt>
            <dd class="font-mono img-detail-cmd">{(inspect.Config.Cmd ?? []).join(' ')}</dd>
          {/if}
          {#if inspect.Config?.Entrypoint}
            <dt>Entrypoint</dt>
            <dd class="font-mono img-detail-cmd">{(inspect.Config.Entrypoint ?? []).join(' ')}</dd>
          {/if}
          {#if inspect.Config?.WorkingDir}
            <dt>WorkingDir</dt>
            <dd class="font-mono">{inspect.Config.WorkingDir}</dd>
          {/if}
          {#if inspect.Config?.User}
            <dt>User</dt>
            <dd class="font-mono">{inspect.Config.User}</dd>
          {/if}
          {#if inspect.Config?.ExposedPorts && Object.keys(inspect.Config.ExposedPorts).length > 0}
            <dt>Exposed ports</dt>
            <dd>
              <div class="img-detail-chips">
                {#each Object.keys(inspect.Config.ExposedPorts) as p (p)}
                  <span class="img-detail-chip font-mono">{p}</span>
                {/each}
              </div>
            </dd>
          {/if}
          {#if inspect.Config?.Env && inspect.Config.Env.length > 0}
            <dt>Env</dt>
            <dd>
              <pre class="img-detail-env font-mono">{inspect.Config.Env.join('\n')}</pre>
            </dd>
          {/if}
          {#if labelEntries.length > 0}
            <dt>Labels</dt>
            <dd>
              <div class="img-detail-chips">
                {#each labelEntries as [k, v] (k)}
                  <span class="img-detail-chip font-mono">
                    <span class="muted">{k}</span>=<span>{v}</span>
                  </span>
                {/each}
              </div>
            </dd>
          {/if}
        </dl>
      </div>
    {/if}
  {/if}

  <!-- ============================================================== -->
  <!--  VULNERABILITIES                                                -->
  <!-- ============================================================== -->
  {#if tab === 'cve'}
    <div class="dm-card img-detail-card-pad">
      <div class="img-detail-cve-head">
        <Eyebrow>Vulnerability scan</Eyebrow>
        <div class="ed-actions">
          {#if canScan}
            <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" disabled={scanBusy} onclick={runScan}>
              <Shield size={12} strokeWidth={1.5} /> {scanBusy ? 'Scanning…' : (scanReport ? 'Re-scan' : 'Scan now')}
            </button>
          {/if}
        </div>
      </div>

      {#if scanBusy && !scanReport}
        <p class="img-detail-cve-empty font-mono">Scanning… grype CLI typically takes 5-30 seconds.</p>
      {:else if !scanReport}
        <div class="img-detail-cve-cta">
          <Shield size={28} strokeWidth={1.5} />
          <p>Image has not been scanned. Run a scan with grype to see CVE findings, severity breakdown, and fix versions.</p>
          {#if canScan}
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={runScan}>
              <Shield size={12} strokeWidth={1.5} /> Run scan
            </button>
          {/if}
        </div>
      {:else}
        {@const sum = scanReport.summary}
        <div class="img-detail-cve-tiles">
          {#each [['critical',sum.critical],['high',sum.high],['medium',sum.medium],['low',sum.low],['negligible',sum.negligible],['unknown',sum.unknown]] as [sev,count] (sev)}
            <button
              type="button"
              class="img-detail-cve-tile"
              class:active={scanFilter === sev}
              onclick={() => (scanFilter = scanFilter === sev ? 'all' : sev as Severity)}
            >
              <div class="img-detail-cve-tile-num">{count}</div>
              <div class="img-detail-cve-tile-label">{sev}</div>
            </button>
          {/each}
        </div>
        {#if scanReport.vulnerabilities.length === 0}
          <div class="img-detail-cve-clean">
            <Check size={20} strokeWidth={2} />
            <span>Clean — no vulnerabilities found by grype.</span>
          </div>
        {:else if scanFiltered.length === 0}
          <p class="img-detail-cve-empty font-mono">No matches for this severity filter.</p>
        {:else}
          <div class="img-detail-cve-table">
            <div class="img-detail-cve-row img-detail-cve-row--head">
              <span>severity</span>
              <span>package</span>
              <span>version</span>
              <span>fixed in</span>
              <span>cve</span>
            </div>
            {#each scanFiltered as v (v.id + v.package)}
              <div class="img-detail-cve-row">
                <span><Badge variant={sevColor(v.severity)}>{v.severity}</Badge></span>
                <span class="font-mono">{v.package}</span>
                <span class="font-mono">{v.version}</span>
                <span class="font-mono">{v.fixed_in || '—'}</span>
                <span class="font-mono">
                  {#if v.url}<a href={v.url} target="_blank" rel="noopener" class="img-detail-cve-link">{v.id}</a>{:else}{v.id}{/if}
                </span>
              </div>
            {/each}
          </div>
          <p class="img-detail-cve-foot font-mono">
            Scanned {scanReport.scanned_at} · grype {scanReport.scanner_version || ''}
          </p>
        {/if}
      {/if}
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  LAYERS                                                         -->
  <!-- ============================================================== -->
  {#if tab === 'layers'}
    {#if !inspect}
      <div class="dm-card img-detail-card-pad"><Skeleton width="80%" height="5rem" /></div>
    {:else if layers.length === 0}
      <div class="dm-card img-detail-card-pad">
        <EmptyState icon={ImageIcon} title="No layers" description="Image has no rootfs layers — probably a scratch base." />
      </div>
    {:else}
      <div class="img-detail-layer-table">
        <div class="img-detail-layer-row img-detail-layer-row--head">
          <span>#</span>
          <span>digest</span>
        </div>
        {#each layers as digest, i (digest + i)}
          <div class="img-detail-layer-row">
            <span class="font-mono muted">{i + 1}</span>
            <span class="font-mono img-detail-layer-digest">{digest}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- ============================================================== -->
  <!--  HISTORY                                                        -->
  <!-- ============================================================== -->
  {#if tab === 'history'}
    {#if !inspect}
      <div class="dm-card img-detail-card-pad"><Skeleton width="80%" height="5rem" /></div>
    {:else if history.length === 0}
      <div class="dm-card img-detail-card-pad">
        <EmptyState icon={ImageIcon} title="No history" description="Docker did not record build history for this image." />
      </div>
    {:else}
      <div class="img-detail-history">
        {#each history as h, i (i)}
          <div class="img-detail-history-row">
            <div class="img-detail-history-meta">
              <span class="font-mono img-detail-history-idx">{history.length - i}</span>
              <span class="font-mono img-detail-history-when">{fmtDate(h.created)}</span>
              <span class="font-mono img-detail-history-size">{fmtBytes(h.size ?? 0)}</span>
            </div>
            <div class="img-detail-history-cmd font-mono">
              {h.created_by ?? '—'}
            </div>
            {#if h.comment}
              <div class="img-detail-history-comment font-mono">{h.comment}</div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- ============================================================== -->
  <!--  CONTAINERS USING                                               -->
  <!-- ============================================================== -->
  {#if tab === 'containers'}
    {#if usingLoading && using.length === 0}
      <div class="dm-card img-detail-card-pad"><Skeleton width="80%" height="5rem" /></div>
    {:else if using.length === 0}
      <div class="dm-card img-detail-card-pad">
        <EmptyState icon={Box} title="Not in use" description="No running container is based on this image right now." />
      </div>
    {:else}
      <div class="img-detail-using-table">
        <div class="img-detail-using-row img-detail-using-row--head">
          <span>name</span>
          <span>state</span>
          <span>started</span>
        </div>
        {#each using as c (c.id)}
          <a href={`/containers/${c.id}`} class="img-detail-using-row img-detail-using-row--data">
            <span class="font-mono img-detail-using-name">{c.name}</span>
            <span>
              {#if c.state === 'running'}
                <span class="dm-pill dm-pill-success img-detail-pill"><span class="dm-pill-dot"></span> running</span>
              {:else}
                <span class="dm-pill dm-pill-neutral img-detail-pill">{c.state}</span>
              {/if}
            </span>
            <span class="font-mono">{fmtDate(c.started)}</span>
          </a>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .img-detail { display: block; }

  /* ─── Header ─── */
  .img-detail-header { margin-bottom: 18px; }
  .img-detail-back-row { margin-bottom: 12px; }
  .img-detail-back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-decoration: none;
    letter-spacing: 0.04em;
  }
  .img-detail-back:hover { color: var(--fg); }
  .img-detail-title-row {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .img-detail-title-text { min-width: 0; max-width: 70ch; }
  .img-detail-title {
    font-size: 24px;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .img-detail-subtitle {
    margin-top: 6px;
    font-size: 12px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .img-detail-pill { font-size: 9.5px; padding: 1px 6px; }
  .img-detail-danger { color: var(--color-danger-400); }

  /* ─── Banners ─── */
  .img-detail-banner {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    margin-bottom: 16px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 30%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
    color: var(--color-warning-400);
    border-radius: 5px;
    font-size: 12.5px;
  }
  .img-detail-banner--err {
    border-color: color-mix(in srgb, var(--color-danger-500) 30%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    color: var(--color-danger-400);
  }
  .img-detail-banner strong { color: inherit; }

  /* ─── Summary ─── */
  .img-detail-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
    margin: 18px 0 14px;
  }
  @media (max-width: 720px) {
    .img-detail-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  .img-detail-summary :global(.ed-metric-value.warn) { color: var(--color-warning-400); }
  .img-detail-tile-text {
    font-size: 14px;
    font-family: var(--font-mono);
  }

  .img-detail-tabs { margin-top: 14px; }
  .img-detail-card-pad { padding: 22px 24px; margin-top: 18px; }

  /* ─── Overview dl/dt/dd ─── */
  .img-detail-overview { padding: 22px 24px 24px; margin-top: 18px; }
  .img-detail-dl {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 10px 18px;
    font-size: 13px;
    margin: 0;
  }
  .img-detail-dl dt {
    font-family: var(--font-mono);
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
    padding-top: 4px;
  }
  .img-detail-dl dd { margin: 0; color: var(--fg); }
  .img-detail-id-line {
    display: flex;
    align-items: center;
    gap: 8px;
    word-break: break-all;
  }
  .img-detail-chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .img-detail-chip {
    display: inline-flex;
    align-items: center;
    padding: 2px 6px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-size: 10.5px;
    color: var(--fg-muted);
    background: var(--surface);
  }
  .img-detail-chip--digest {
    color: var(--fg-subtle);
    word-break: break-all;
  }
  .img-detail-cmd {
    word-break: break-all;
    color: var(--fg-muted);
  }
  .img-detail-env {
    margin: 0;
    padding: 10px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 4px;
    font-size: 11.5px;
    overflow: auto;
    max-height: 220px;
    color: var(--fg-muted);
  }

  /* ─── Vulnerabilities tab ─── */
  .img-detail-cve-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 14px;
  }
  .img-detail-cve-empty {
    text-align: center;
    color: var(--fg-subtle);
    font-size: 12px;
    padding: 14px 0;
  }
  .img-detail-cve-cta {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 32px 16px;
    color: var(--fg-subtle);
    text-align: center;
  }
  .img-detail-cve-cta p {
    font-size: 13px;
    color: var(--fg-muted);
    max-width: 50ch;
    line-height: 1.55;
    margin: 0;
  }
  .img-detail-cve-tiles {
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    gap: 6px;
    margin-bottom: 16px;
  }
  @media (max-width: 720px) {
    .img-detail-cve-tiles { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  }
  .img-detail-cve-tile {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 4px;
    padding: 8px 4px;
    text-align: center;
    cursor: pointer;
    transition: border-color 120ms, background 120ms;
  }
  .img-detail-cve-tile:hover { border-color: var(--border-strong); }
  .img-detail-cve-tile.active {
    border-color: var(--color-brand-500);
    background: color-mix(in srgb, var(--color-brand-500) 8%, transparent);
  }
  .img-detail-cve-tile-num {
    font-size: 18px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    color: var(--fg);
  }
  .img-detail-cve-tile-label {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
    margin-top: 2px;
  }
  .img-detail-cve-clean {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 18px;
    color: var(--color-success-400);
    background: color-mix(in srgb, var(--color-success-500) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--color-success-500) 30%, var(--border));
    border-radius: 4px;
    font-size: 13px;
  }
  .img-detail-cve-table {
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }
  .img-detail-cve-row {
    display: grid;
    grid-template-columns: 100px minmax(120px, 1.4fr) 110px 110px minmax(140px, 1fr);
    gap: 12px;
    align-items: center;
    padding: 6px 12px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
  }
  .img-detail-cve-row:last-child { border-bottom: 0; }
  .img-detail-cve-row--head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .img-detail-cve-link {
    color: var(--accent-fg);
    text-decoration: none;
  }
  .img-detail-cve-link:hover { text-decoration: underline; }
  .img-detail-cve-foot {
    margin-top: 10px;
    font-size: 10.5px;
    color: var(--fg-subtle);
  }

  /* ─── Layers tab ─── */
  .img-detail-layer-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin-top: 18px;
    overflow: hidden;
  }
  .img-detail-layer-row {
    display: grid;
    grid-template-columns: 30px minmax(0, 1fr);
    gap: 14px;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
  }
  .img-detail-layer-row:last-child { border-bottom: 0; }
  .img-detail-layer-row--head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .img-detail-layer-digest {
    color: var(--fg-muted);
    word-break: break-all;
  }

  /* ─── History tab ─── */
  .img-detail-history {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin-top: 18px;
    overflow: hidden;
  }
  .img-detail-history-row {
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
  }
  .img-detail-history-row:last-child { border-bottom: 0; }
  .img-detail-history-meta {
    display: flex;
    gap: 12px;
    align-items: baseline;
    color: var(--fg-subtle);
    font-size: 11px;
    margin-bottom: 4px;
  }
  .img-detail-history-idx { color: var(--fg-muted); }
  .img-detail-history-when { color: var(--fg-subtle); }
  .img-detail-history-size { margin-left: auto; }
  .img-detail-history-cmd {
    color: var(--fg);
    word-break: break-all;
    line-height: 1.5;
  }
  .img-detail-history-comment {
    margin-top: 4px;
    font-size: 11px;
    color: var(--fg-subtle);
    font-style: normal;
  }

  /* ─── Containers using tab ─── */
  .img-detail-using-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin-top: 18px;
    overflow: hidden;
  }
  .img-detail-using-row {
    display: grid;
    grid-template-columns: minmax(180px, 2fr) 110px 120px;
    gap: 14px;
    align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    text-decoration: none;
    color: var(--fg);
  }
  .img-detail-using-row:last-child { border-bottom: 0; }
  .img-detail-using-row--head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .img-detail-using-row--data:hover { background: var(--surface-hover); }
  .img-detail-using-name {
    font-size: 12.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .muted { color: var(--fg-subtle); }
  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
