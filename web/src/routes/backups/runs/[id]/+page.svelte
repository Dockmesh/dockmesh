<script lang="ts">
  // Standalone detail page for one backup run. Same data + restore-flow
  // as _RunDrawer but full-page chrome. Backend doesn't currently
  // expose a single-run getter; we pull listRuns(500) and pluck the
  // match. Cheap enough for a homelab and avoids a backend slice
  // before this UI lands.
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError, type BackupRun, type BackupJob, type BackupTarget } from '$lib/api';
  import { Eyebrow } from '$lib/components/editorial';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import {
    ChevronLeft, Copy, RefreshCw, ArrowRight, AlertTriangle, Lock, Undo2, Box
  } from 'lucide-svelte';

  const id = $derived(parseInt($page.params.id, 10));

  $effect(() => {
    pageContext.set(`Run #${id}`);
    return () => pageContext.clear();
  });

  let run = $state<BackupRun | null>(null);
  let job = $state<BackupJob | null>(null);
  let loading = $state(true);
  let notFound = $state(false);

  async function load() {
    loading = true;
    try {
      const [allRuns, jobs] = await Promise.all([
        api.backups.listRuns(500),
        api.backups.listJobs()
      ]);
      const r = allRuns.find((x) => x.id === id);
      if (!r) { notFound = true; run = null; return; }
      run = r;
      job = jobs.find((j) => j.name === r.job_name) ?? null;
    } catch (err) {
      toast.error('Failed to load run', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  $effect(() => { id; load(); });

  let restoreOpen = $state(false);
  let restoreVolume = $state('');
  let restoreConfirm = $state('');
  let restoreBusy = $state(false);

  async function doRestore(mode: 'in-place' | 'alongside' | 'download') {
    if (!run) return;
    if (mode === 'download') {
      try {
        await api.backups.downloadArchive(run.id);
        toast.success('Download started', `backup-run-${run.id}.tar.gz`);
      } catch (err) {
        toast.error('Download failed', err instanceof ApiError ? err.message : undefined);
      }
      return;
    }
    restoreOpen = true;
  }

  async function confirmRestore() {
    if (!run) return;
    if (restoreConfirm !== restoreVolume || !restoreVolume.trim()) return;
    restoreBusy = true;
    try {
      await api.backups.restore(run.id, restoreVolume.trim());
      toast.success('Restore queued');
      restoreOpen = false;
    } catch (err) {
      toast.error('Restore failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      restoreBusy = false;
    }
  }

  function fmtBytes(n: number): string {
    if (!n) return '—';
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }
  function fmtDuration(start: string, end?: string): string {
    if (!end) return 'running…';
    const secs = Math.floor((new Date(end).getTime() - new Date(start).getTime()) / 1000);
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
    return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
  }
  function fmtTime(ts: string): string {
    return new Date(ts).toLocaleString();
  }
  function fmtDay(ts: string): string {
    const d = new Date(ts);
    const today = new Date();
    if (d.toDateString() === today.toDateString()) return 'Today';
    const y = new Date(today); y.setDate(y.getDate() - 1);
    if (d.toDateString() === y.toDateString()) return 'Yesterday';
    return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
  }
  function fmtTimeOnly(ts: string): string {
    return new Date(ts).toTimeString().slice(0, 5);
  }
  function statusPill(s: string): { cls: string; label: string } {
    if (s === 'success') return { cls: 'dm-pill-success', label: 'success' };
    if (s === 'failed') return { cls: 'dm-pill-danger', label: 'failed' };
    if (s === 'running') return { cls: 'dm-pill', label: 'running' };
    return { cls: 'dm-pill-neutral', label: s };
  }

  function copyDetails() {
    if (!run) return;
    const lines = [
      `Run #${run.id}`,
      `Job: ${run.job_name}`,
      `Status: ${run.status}`,
      `Started: ${run.started_at}`,
      run.finished_at ? `Finished: ${run.finished_at}` : '',
      `Duration: ${fmtDuration(run.started_at, run.finished_at)}`,
      `Size: ${fmtBytes(run.size_bytes)}`,
      run.target_path ? `Target path: ${run.target_path}` : '',
      run.sha256 ? `SHA-256: ${run.sha256}` : '',
      run.encrypted ? 'Encrypted: yes (age)' : '',
      run.error ? `Error: ${run.error}` : ''
    ].filter(Boolean).join('\n');
    navigator.clipboard.writeText(lines).then(
      () => toast.success('Copied'),
      () => toast.error('Copy failed')
    );
  }

  const restoreModes: { id: 'in-place' | 'alongside' | 'download'; title: string; blurb: string; danger?: boolean }[] = [
    { id: 'in-place', title: 'Restore in place', blurb: 'Replace current data on this dockmesh. Stops affected stacks first.', danger: true },
    { id: 'alongside', title: 'Restore alongside', blurb: 'Mount as new stacks with a -restored suffix. Original keeps running.' },
    { id: 'download', title: 'Download archive', blurb: 'Stream the encrypted .tar.age locally for offline inspection.' }
  ];
</script>

{#if loading && !run}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=runs" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / runs
      </a>
    </div>
    <div class="dm-card bk-detail-loading"><Skeleton width="60%" height="6rem" /></div>
  </section>
{:else if notFound || !run}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=runs" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / runs
      </a>
    </div>
    <div class="dm-card bk-detail-loading">
      <EmptyState icon={Box} title="Run not found" description="This run has been pruned or you don't have access." />
    </div>
  </section>
{:else}
  {@const sp = statusPill(run.status)}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=runs" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / runs
      </a>
    </div>

    <header class="bk-detail-header">
      <div class="bk-detail-header-text">
        <h1 class="ed-title bk-detail-title">Run #{run.id}</h1>
        <p class="ed-subtitle bk-detail-subtitle">
          <a href="/backups/jobs/{job?.id ?? ''}" class="bk-detail-title-job">{run.job_name}</a> ·
          {fmtDay(run.started_at)} {fmtTimeOnly(run.started_at)} ·
          {#if run.status === 'failed'}
            <span class="bk-detail-fail">failed in {fmtDuration(run.started_at, run.finished_at)}</span>
          {:else if run.status === 'running'}
            running…
          {:else if run.status === 'success'}
            {fmtBytes(run.size_bytes)} in {fmtDuration(run.started_at, run.finished_at)}
          {:else}
            {run.status}
          {/if}
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={copyDetails}>
          <Copy size={11} strokeWidth={1.5} /> Copy details
        </button>
        {#if run.status === 'success'}
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => doRestore('alongside')}>
            <Undo2 size={11} strokeWidth={1.5} /> Restore
          </button>
        {/if}
      </div>
    </header>

    <!-- Stat row -->
    <div class="bk-detail-stat-row">
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Status</span>
        <span class="dm-pill {sp.cls} bk-detail-pill"><span class="dm-pill-dot"></span> {sp.label}</span>
        {#if run.encrypted}
          <span class="bk-detail-stat-mono"><Lock size={9} strokeWidth={1.5} /> encrypted</span>
        {/if}
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Size</span>
        <span class="bk-detail-stat-value">{run.size_bytes ? fmtBytes(run.size_bytes) : '—'}</span>
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Duration</span>
        <span class="bk-detail-stat-value">{fmtDuration(run.started_at, run.finished_at)}</span>
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Started</span>
        <span class="bk-detail-stat-value">{fmtDay(run.started_at)}, {fmtTimeOnly(run.started_at)}</span>
        <span class="bk-detail-stat-mono">{fmtTime(run.started_at)}</span>
      </div>
    </div>

    {#if run.error}
      <div class="bk-detail-error">
        <AlertTriangle size={14} strokeWidth={1.5} class="bk-detail-error-icon" />
        <div class="bk-detail-error-text font-mono">{run.error}</div>
      </div>
    {/if}

    <div class="bk-detail-grid">
      <!-- Left column — metadata + log -->
      <div class="bk-detail-col">
        <section class="bk-detail-section dm-card">
          <Eyebrow>Run metadata</Eyebrow>
          <dl class="bk-detail-kv">
            <dt>started</dt>
            <dd class="font-mono">{fmtTime(run.started_at)}</dd>
            {#if run.finished_at}
              <dt>finished</dt>
              <dd class="font-mono">{fmtTime(run.finished_at)}</dd>
            {/if}
            {#if run.target_path}
              <dt>target path</dt>
              <dd class="font-mono bk-detail-kv-break">{run.target_path}</dd>
            {/if}
            {#if run.sha256}
              <dt>sha-256</dt>
              <dd class="font-mono bk-detail-kv-hash">{run.sha256}</dd>
            {/if}
            <dt>encrypted</dt>
            <dd class="font-mono">{run.encrypted ? 'yes (age)' : 'no'}</dd>
            {#if run.sources?.length}
              <dt>sources</dt>
              <dd>
                <div class="bk-detail-source-chips">
                  {#each run.sources as s, i (i)}
                    <span class="dm-pill dm-pill-neutral bk-detail-pill">{s.type}: <span class="font-mono">{s.name}</span></span>
                  {/each}
                </div>
              </dd>
            {/if}
          </dl>
        </section>

        <section class="bk-detail-section dm-card">
          <Eyebrow>Log</Eyebrow>
          <div class="bk-detail-log-empty">
            <p class="bk-detail-empty">
              Per-run log capture isn't recorded by the backend yet — only status + error message survive the run.
              <button type="button" class="bk-detail-link" onclick={copyDetails}>Copy run metadata</button>
              for an audit trail.
            </p>
          </div>
        </section>
      </div>

      <!-- Right column — restore flow -->
      <div class="bk-detail-col">
        {#if run.status === 'success'}
          <section class="bk-detail-section dm-card">
            <Eyebrow>Restore</Eyebrow>
            <p class="bk-detail-block-body">
              Pick how to restore this run. <strong>In-place</strong> replaces the current data;
              <strong>alongside</strong> mounts as a copy with a <code>-restored</code> suffix;
              <strong>download</strong> streams the archive for offline inspection.
            </p>
            <div class="bk-detail-restore-list">
              {#each restoreModes as m (m.id)}
                <button type="button" class="bk-detail-restore-row" class:danger={m.danger} onclick={() => doRestore(m.id)}>
                  <div class="bk-detail-restore-text">
                    <span class="bk-detail-restore-title">{m.title}</span>
                    <span class="bk-detail-restore-blurb">{m.blurb}</span>
                  </div>
                  <ArrowRight size={12} strokeWidth={1.5} class="bk-detail-restore-arrow" />
                </button>
              {/each}
            </div>
          </section>
        {:else if run.status === 'failed'}
          <section class="bk-detail-section dm-card">
            <Eyebrow>Restore</Eyebrow>
            <p class="bk-detail-empty">No data was written — restore is unavailable for failed runs.</p>
          </section>
        {/if}

        {#if job}
          <section class="bk-detail-section dm-card">
            <Eyebrow>Job</Eyebrow>
            <a href="/backups/jobs/{job.id}" class="bk-detail-job-link">
              <span class="font-mono bk-detail-job-link-name">{job.name}</span>
              <ArrowRight size={11} strokeWidth={1.5} class="bk-detail-job-link-arrow" />
            </a>
            <p class="bk-detail-block-body bk-detail-block-meta font-mono">
              schedule {job.schedule} · target {job.target_type}
            </p>
          </section>
        {/if}
      </div>
    </div>
  </section>

  <!-- Inline restore-confirm modal (volume-name typing matches the
       legacy flow's destructive-confirm pattern). Wired only for
       in-place + alongside; download is a future endpoint. -->
  {#if restoreOpen && run}
    <button type="button" class="bk-restore-backdrop" onclick={() => (restoreOpen = false)} aria-label="Close"></button>
    <div class="bk-restore-modal" role="dialog" aria-modal="true">
      <header class="bk-restore-head">
        <Eyebrow>Confirm restore</Eyebrow>
        <h2 class="ed-title bk-restore-title">Type the volume name to confirm</h2>
        <p class="bk-detail-block-body">
          Restoring run <em class="ed-accent">#{run.id}</em> from <em>{run.job_name}</em>.
          Type the destination volume name below — must match exactly to proceed.
        </p>
      </header>
      <div class="bk-restore-body">
        <label class="bk-restore-label" for="restore-vol">Destination volume name</label>
        <input id="restore-vol" class="bk-restore-input font-mono" bind:value={restoreVolume} placeholder="my-restored-volume" />
        <label class="bk-restore-label" for="restore-confirm" style="margin-top: 14px;">Type the same name to confirm</label>
        <input id="restore-confirm" class="bk-restore-input font-mono" bind:value={restoreConfirm} placeholder="must match exactly" />
      </div>
      <footer class="bk-restore-foot">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (restoreOpen = false)}>Cancel</button>
        <span class="bk-detail-spacer"></span>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={restoreBusy || !restoreVolume.trim() || restoreConfirm !== restoreVolume} onclick={confirmRestore}>
          <Undo2 size={11} strokeWidth={1.5} /> {restoreBusy ? 'Restoring…' : 'Restore'}
        </button>
      </footer>
    </div>
  {/if}
{/if}

<style>
  .bk-detail-page { display: block; }
  .bk-detail-back-row { margin-bottom: 12px; }
  .bk-detail-back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-decoration: none;
    letter-spacing: 0.04em;
  }
  .bk-detail-back:hover { color: var(--fg); }

  .bk-detail-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
    margin-bottom: 22px;
  }
  .bk-detail-header-text { min-width: 0; max-width: 70ch; }
  .bk-detail-title {
    font-size: 24px;
    margin-top: 8px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .bk-detail-title-job {
    color: var(--fg);
    text-decoration: none;
  }
  .bk-detail-title-job:hover { color: var(--accent-fg); }
  .bk-detail-title-meta { color: var(--fg-subtle); font-weight: 400; font-size: 16px; }
  .bk-detail-subtitle {
    margin-top: 6px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-detail-fail { color: var(--color-danger-400); }

  /* Connected hairline grid (mockup pattern) — same look as the
     drawer stat-rows. Cells separated by 1px gap with parent-bg
     bleeding through, no per-cell borders. */
  .bk-detail-stat-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 1px;
    background: var(--border-subtle);
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    overflow: hidden;
    margin-bottom: 22px;
  }
  .bk-detail-stat {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 14px 16px;
    background: var(--bg-elevated);
  }
  .bk-detail-stat-label {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .bk-detail-stat-value {
    font-size: 14px;
    color: var(--fg);
    margin-top: 2px;
  }
  .bk-detail-stat-mono {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .bk-detail-pill { font-size: 9.5px; padding: 1px 6px; }

  .bk-detail-error {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px 14px;
    margin-bottom: 22px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    border-radius: 5px;
    color: var(--color-danger-400);
  }
  :global(.bk-detail-error-icon) { color: var(--color-danger-400); flex-shrink: 0; margin-top: 2px; }
  .bk-detail-error-text {
    font-size: 12px;
    line-height: 1.55;
    word-break: break-all;
    flex: 1;
  }

  .bk-detail-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
    gap: 16px;
  }
  @media (max-width: 1000px) {
    .bk-detail-grid { grid-template-columns: 1fr; }
  }
  .bk-detail-col {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .bk-detail-section { padding: 18px 20px 20px; }
  .bk-detail-loading { padding: 28px; margin-bottom: 24px; }
  .bk-detail-empty {
    margin: 8px 0 0;
    font-size: 12.5px;
    color: var(--fg-subtle);
    line-height: 1.55;
  }
  .bk-detail-block-body {
    margin: 8px 0 0;
    font-size: 13px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-detail-block-body strong { color: var(--fg); font-weight: 500; }
  .bk-detail-block-body code {
    font-family: var(--font-mono);
    color: var(--accent-fg);
    background: var(--bg-elevated);
    padding: 0 4px;
    border-radius: 3px;
  }
  .bk-detail-block-meta { color: var(--fg-subtle); font-size: 11px; }

  .bk-detail-link {
    color: var(--accent-fg);
    background: transparent;
    border: 0;
    padding: 0;
    cursor: pointer;
    font-size: inherit;
    text-decoration: underline;
  }

  .bk-detail-kv {
    display: grid;
    grid-template-columns: 110px 1fr;
    row-gap: 6px;
    column-gap: 12px;
    margin: 8px 0 0;
    font-size: 12px;
  }
  .bk-detail-kv dt {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .bk-detail-kv dd {
    margin: 0;
    color: var(--fg);
    word-break: break-all;
  }
  .bk-detail-kv-break { word-break: break-all; }
  .bk-detail-kv-hash {
    font-size: 10.5px;
    color: var(--fg-subtle);
    word-break: break-all;
  }

  .bk-detail-log-empty {
    padding: 18px 16px;
    margin-top: 8px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 5px;
    background: var(--bg);
  }

  .bk-detail-source-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  /* Restore flow */
  .bk-detail-restore-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 14px;
  }
  .bk-detail-restore-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
    cursor: pointer;
    text-align: left;
    width: 100%;
    transition: border-color 120ms, background 120ms;
  }
  .bk-detail-restore-row:hover { border-color: var(--border-strong); background: var(--surface-hover); }
  .bk-detail-restore-row.danger {
    border-color: color-mix(in srgb, var(--color-danger-500) 30%, var(--border));
  }
  .bk-detail-restore-row.danger:hover {
    border-color: var(--color-danger-500);
    background: color-mix(in srgb, var(--color-danger-500) 6%, transparent);
  }
  .bk-detail-restore-text {
    display: flex;
    flex-direction: column;
    gap: 4px;
    flex: 1;
    min-width: 0;
  }
  .bk-detail-restore-title {
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
  }
  .bk-detail-restore-row.danger .bk-detail-restore-title { color: var(--color-danger-400); }
  .bk-detail-restore-blurb {
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.5;
  }
  :global(.bk-detail-restore-arrow) { color: var(--fg-subtle); flex-shrink: 0; }

  .bk-detail-job-link {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    margin-top: 8px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
    text-decoration: none;
    color: var(--fg);
    transition: border-color 120ms, background 120ms;
  }
  .bk-detail-job-link:hover { border-color: var(--border-strong); background: var(--surface-hover); }
  .bk-detail-job-link-name {
    font-size: 12.5px;
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  :global(.bk-detail-job-link-arrow) { color: var(--fg-subtle); }

  /* Restore-confirm modal (matches the existing flow's destructive
     volume-name-typing pattern). Centred + 480px wide. */
  .bk-restore-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.66);
    backdrop-filter: blur(4px);
    border: 0;
    cursor: default;
    z-index: 100;
  }
  .bk-restore-modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(480px, calc(100vw - 48px));
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    box-shadow: 0 25px 80px rgba(0, 0, 0, 0.5);
    z-index: 101;
  }
  .bk-restore-head { padding: 22px 24px 12px; }
  .bk-restore-title { margin-top: 6px; font-size: 18px; }
  .bk-restore-body { padding: 0 24px 16px; }
  .bk-restore-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    display: block;
    margin-bottom: 4px;
  }
  .bk-restore-input {
    width: 100%;
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg);
    color: var(--fg);
    font-size: 13px;
  }
  .bk-restore-input:focus { outline: none; border-color: var(--accent); }
  .bk-restore-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 14px 24px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
    border-bottom-left-radius: 8px;
    border-bottom-right-radius: 8px;
  }
  .bk-detail-spacer { flex: 1; }
</style>
