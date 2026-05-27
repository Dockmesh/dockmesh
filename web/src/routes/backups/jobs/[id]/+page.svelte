<script lang="ts">
  // Standalone detail page for one backup job. Same data + sections as
  // _JobDrawer but with full-page editorial chrome (back-link in topbar
  // line + page header with actions). Lets operators bookmark / link
  // to a specific job and gives the run-history room to breathe on
  // wide screens.
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError, type BackupJob, type BackupRun, type BackupTarget } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Eyebrow } from '$lib/components/editorial';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import {
    ChevronLeft, Play, Edit, Trash2, ArrowRight, AlertTriangle, Lock,
    LayoutDashboard, Layers, HardDrive, Box
  } from 'lucide-svelte';

  const id = $derived(parseInt($page.params.id, 10));

  $effect(() => {
    if (job) pageContext.set(job.name);
    return () => pageContext.clear();
  });

  let job = $state<BackupJob | null>(null);
  let runs = $state<BackupRun[]>([]);
  let targets = $state<BackupTarget[]>([]);
  let loading = $state(true);
  let notFound = $state(false);

  async function load() {
    loading = true;
    try {
      const [j, r, t] = await Promise.all([
        api.backups.getJob(id),
        api.backups.listRuns(200),
        api.backups.listTargets()
      ]);
      job = j;
      runs = r;
      targets = t;
      notFound = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        notFound = true;
      } else {
        toast.error('Failed to load job', err instanceof ApiError ? err.message : undefined);
      }
    } finally {
      loading = false;
    }
  }
  $effect(() => { id; load(); });
  // Poll every 10s so a "Run now" started elsewhere is reflected here.
  $effect(() => autoRefresh(load, 10_000));

  // Filter runs to this job (matched by name — same join as the
  // legacy list page does).
  const jobRuns = $derived(job ? runs.filter((r) => r.job_name === job!.name).slice(0, 14) : []);
  const lastRun = $derived(jobRuns[0] ?? null);
  const failed7 = $derived(jobRuns.filter((r) => r.status === 'failed').length);
  const successRuns = $derived(jobRuns.filter((r) => r.status === 'success'));
  const avgSize = $derived(
    successRuns.length > 0
      ? successRuns.reduce((s, r) => s + r.size_bytes, 0) / successRuns.length
      : 0
  );

  function fmtBytes(n: number): string {
    if (!n) return '—';
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }
  function fmtTime(ts?: string): string {
    if (!ts) return '—';
    const d = (Date.now() - new Date(ts).getTime()) / 1000;
    if (d < 60) return 'just now';
    if (d < 3600) return `${Math.floor(d / 60)}m ago`;
    if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
    return new Date(ts).toLocaleString();
  }
  function fmtDuration(start: string, end?: string): string {
    if (!end) return 'running…';
    const secs = Math.floor((new Date(end).getTime() - new Date(start).getTime()) / 1000);
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
    return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
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
  function statusDot(s: string): 'ok-dot' | 'warn-dot' | 'fail-dot' | 'neutral-dot' {
    if (s === 'success' || s === 'running') return 'ok-dot';
    if (s === 'failed') return 'fail-dot';
    return 'neutral-dot';
  }
  function cronHuman(c: string): string {
    const m: Record<string, string> = {
      '0 3 * * *': 'Daily at 03:00',
      '0 0 * * *': 'Daily at midnight',
      '0 */6 * * *': 'Every 6 hours',
      '0 */12 * * *': 'Every 12 hours',
      '0 2 * * 0': 'Weekly Sunday 02:00',
      '0 3 1 * *': 'Monthly 1st at 03:00'
    };
    return m[c] ?? c;
  }
  function jobSourceLabel(j: BackupJob): string {
    if (j.sources.length === 0) return 'no source';
    const s = j.sources[0];
    const t = s.type as string;
    if (t === 'system') return 'dockmesh-system';
    return `${t}: ${s.name}`;
  }
  function jobSourceIcon(j: BackupJob) {
    const t = (j.sources[0]?.type as string | undefined) ?? '';
    if (t === 'stack') return Layers;
    if (t === 'volume') return HardDrive;
    if (t === 'system') return LayoutDashboard;
    return Box;
  }

  let busy = $state(false);
  async function runNow() {
    if (!job) return;
    if (!(await confirm.ask({
      title: 'Run backup now',
      message: `Run "${job.name}" now?`,
      body: 'The backup starts in the background. Progress appears in the Runs section below.',
      confirmLabel: 'Run'
    }))) return;
    busy = true;
    try {
      await api.backups.runJob(job.id);
      toast.success('Backup started');
      await load();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
  async function deleteJob() {
    if (!job) return;
    if (!(await confirm.ask({
      title: 'Delete backup job',
      message: `Delete backup job "${job.name}"?`,
      body: 'Existing backup runs are kept. The schedule is removed and no new runs will be triggered.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.backups.deleteJob(job.id);
      toast.success('Deleted');
      goto('/backups?tab=jobs');
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

{#if loading && !job}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=jobs" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / jobs
      </a>
    </div>
    <div class="dm-card bk-detail-loading"><Skeleton width="60%" height="6rem" /></div>
  </section>
{:else if notFound || !job}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=jobs" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / jobs
      </a>
    </div>
    <div class="dm-card bk-detail-loading">
      <EmptyState icon={Box} title="Job not found" description="This backup job has been deleted or you don't have access. Go back to the jobs list." />
    </div>
  </section>
{:else}
  {@const SrcIcon = jobSourceIcon(job)}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=jobs" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / jobs
      </a>
    </div>

    <header class="bk-detail-header">
      <div class="bk-detail-header-text">
        <h1 class="ed-title bk-detail-title">{job.name}</h1>
        <p class="ed-subtitle bk-detail-subtitle">
          {jobSourceLabel(job)} → {job.target_type}{!job.enabled ? ' · disabled' : ''}
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm bk-detail-danger" onclick={deleteJob}>
          <Trash2 size={11} strokeWidth={1.5} /> Delete
        </button>
        <a href="/backups?tab=jobs&edit={job.id}" class="dm-btn dm-btn-secondary dm-btn-sm">
          <Edit size={11} strokeWidth={1.5} /> Edit
        </a>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={!job.enabled || busy} onclick={runNow}>
          <Play size={11} strokeWidth={1.5} /> {busy ? 'Starting…' : 'Run now'}
        </button>
      </div>
    </header>

    <!-- Stat row -->
    <div class="bk-detail-stat-row">
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Status</span>
        {#if lastRun}
          {@const sp = statusPill(lastRun.status)}
          <span class="dm-pill {sp.cls} bk-detail-pill"><span class="dm-pill-dot"></span> {sp.label}</span>
          <span class="bk-detail-stat-mono">{fmtTime(lastRun.started_at)}</span>
        {:else if job.enabled}
          <span class="dm-pill dm-pill-neutral bk-detail-pill">idle</span>
          <span class="bk-detail-stat-mono">no runs yet</span>
        {:else}
          <span class="dm-pill dm-pill-neutral bk-detail-pill">disabled</span>
        {/if}
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Schedule</span>
        <span class="bk-detail-stat-value">{cronHuman(job.schedule)}</span>
        <span class="bk-detail-stat-mono">{job.schedule}</span>
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Avg size</span>
        <span class="bk-detail-stat-value">{fmtBytes(avgSize)}</span>
        <span class="bk-detail-stat-mono">{jobRuns.length} runs · {failed7} failed</span>
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Next</span>
        <span class="bk-detail-stat-value">{fmtTime(job.next_run_at)}</span>
        <span class="bk-detail-stat-mono">retention {job.retention_count}{job.retention_days > 0 ? ` · ${job.retention_days}d` : ''}</span>
      </div>
    </div>

    <div class="bk-detail-grid">
      <!-- Left column — source + target -->
      <div class="bk-detail-col">
        <section class="bk-detail-section dm-card">
          <Eyebrow>Source · {job.sources.length}</Eyebrow>
          {#if job.sources.length === 0}
            <p class="bk-detail-empty">No source configured.</p>
          {:else}
            <ul class="bk-detail-list">
              {#each job.sources as s, i (i)}
                <li class="bk-detail-list-row">
                  <SrcIcon size={12} strokeWidth={1.5} />
                  <span class="dm-pill dm-pill-neutral bk-detail-pill">{s.type}</span>
                  <span class="font-mono bk-detail-list-name">{s.name}</span>
                </li>
              {/each}
            </ul>
          {/if}
        </section>

        <section class="bk-detail-section dm-card">
          <Eyebrow>Target</Eyebrow>
          <div class="bk-detail-target">
            <div class="bk-detail-target-head">
              <span class="dm-pill dm-pill-neutral bk-detail-pill">{job.target_type}</span>
              {#if job.encrypt}<span class="dm-pill bk-detail-pill bk-detail-pill-encrypt"><Lock size={9} strokeWidth={1.5} /> age</span>{/if}
            </div>
            {#if job.target_config && Object.keys(job.target_config).length > 0}
              <dl class="bk-detail-kv">
                {#each Object.entries(job.target_config) as [k, v] (k)}
                  <dt>{k}</dt>
                  <dd class="font-mono">{k.includes('secret') || k.includes('password') ? '••••••••' : String(v ?? '—')}</dd>
                {/each}
              </dl>
            {/if}
          </div>
        </section>

        <section class="bk-detail-section dm-card">
          <Eyebrow>Retention</Eyebrow>
          <p class="bk-detail-block-body">
            Keep <em class="ed-accent">{job.retention_count}</em> newest runs.
            {#if job.retention_days > 0}
              Drop runs older than <em class="ed-accent">{job.retention_days}d</em>.
            {:else}
              No age limit.
            {/if}
          </p>
        </section>

        <section class="bk-detail-section dm-card">
          <Eyebrow>Hooks · {(job.pre_hooks?.length ?? 0) + (job.post_hooks?.length ?? 0)}</Eyebrow>
          {#if (job.pre_hooks?.length ?? 0) + (job.post_hooks?.length ?? 0) === 0}
            <p class="bk-detail-empty">No hooks configured. Volumes are paused via fsfreeze before snapshot.</p>
          {:else}
            <ul class="bk-detail-hook-list">
              {#each job.pre_hooks ?? [] as h, i (`pre-${i}`)}
                <li class="font-mono bk-detail-hook">
                  <span class="bk-detail-hook-tag">pre</span>
                  <span class="bk-detail-hook-ctr">{h.container}</span>
                  <span class="bk-detail-hook-cmd">{h.cmd.join(' ')}</span>
                </li>
              {/each}
              {#each job.post_hooks ?? [] as h, i (`post-${i}`)}
                <li class="font-mono bk-detail-hook">
                  <span class="bk-detail-hook-tag bk-detail-hook-tag-post">post</span>
                  <span class="bk-detail-hook-ctr">{h.container}</span>
                  <span class="bk-detail-hook-cmd">{h.cmd.join(' ')}</span>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
      </div>

      <!-- Right column — run history -->
      <div class="bk-detail-col">
        <section class="bk-detail-section dm-card">
          <Eyebrow>Run history · last {jobRuns.length}</Eyebrow>
          {#if jobRuns.length === 0}
            <p class="bk-detail-empty">No runs recorded yet. Click "Run now" to trigger one.</p>
          {:else}
            <div class="bk-detail-runs">
              <div class="bk-detail-runs-row bk-detail-runs-head">
                <span>when</span>
                <span class="right">size</span>
                <span class="right">duration</span>
                <span>status</span>
                <span></span>
              </div>
              {#each jobRuns as r (r.id)}
                {@const sp = statusPill(r.status)}
                <a href="/backups/runs/{r.id}" class="bk-detail-runs-row bk-detail-runs-row-data">
                  <span class="font-mono bk-detail-runs-when">
                    <span class="bk-detail-runs-day">{fmtDay(r.started_at)}</span>
                    <span class="bk-detail-runs-time">{fmtTimeOnly(r.started_at)}</span>
                  </span>
                  <span class="font-mono right bk-detail-runs-size">{r.size_bytes ? fmtBytes(r.size_bytes) : '—'}</span>
                  <span class="font-mono right bk-detail-runs-dur">{fmtDuration(r.started_at, r.finished_at)}</span>
                  <span><span class="dm-pill {sp.cls} bk-detail-pill"><span class="dm-pill-dot"></span> {sp.label}</span></span>
                  <ArrowRight size={11} strokeWidth={1.5} class="bk-detail-runs-arrow" />
                </a>
              {/each}
            </div>
          {/if}
        </section>
      </div>
    </div>
  </section>
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
    font-size: 26px;
    margin-top: 8px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .bk-detail-subtitle {
    margin-top: 6px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-detail-disabled { color: var(--fg-subtle); }
  .bk-detail-danger { color: var(--color-danger-400); }

  /* Connected hairline stat-row (mockup pattern). */
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
  }
  .bk-detail-pill { font-size: 9.5px; padding: 1px 6px; }
  .bk-detail-pill-encrypt {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--color-brand-500) 35%, var(--border));
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }

  /* Two-col grid below the stat row */
  .bk-detail-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr);
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
  .bk-detail-section {
    padding: 18px 20px 20px;
  }
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

  /* Source list */
  .bk-detail-list {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .bk-detail-list-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg);
  }
  .bk-detail-list-name {
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Target block */
  .bk-detail-target {
    margin-top: 8px;
  }
  .bk-detail-target-head {
    display: flex;
    gap: 6px;
    margin-bottom: 10px;
  }
  .bk-detail-kv {
    display: grid;
    grid-template-columns: 110px 1fr;
    row-gap: 6px;
    column-gap: 12px;
    margin: 0;
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
    color: var(--fg-muted);
    word-break: break-all;
  }

  /* Hooks */
  .bk-detail-hook-list {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .bk-detail-hook {
    font-size: 11px;
    color: var(--fg-muted);
    line-height: 1.5;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: baseline;
  }
  .bk-detail-hook-tag {
    font-size: 9.5px;
    padding: 1px 5px;
    border-radius: 2px;
    background: color-mix(in srgb, var(--color-brand-500) 16%, transparent);
    color: var(--accent-fg);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .bk-detail-hook-tag-post {
    background: color-mix(in srgb, var(--color-warning-500) 16%, transparent);
    color: var(--color-warning-400);
  }
  .bk-detail-hook-ctr { color: var(--fg); }
  .bk-detail-hook-cmd {
    color: var(--fg-subtle);
    word-break: break-all;
    flex: 1;
    min-width: 0;
  }

  /* Run-history table */
  .bk-detail-runs {
    border: 1px solid var(--border);
    border-radius: 5px;
    overflow: hidden;
    background: var(--bg);
    margin-top: 8px;
  }
  .bk-detail-runs-row {
    display: grid;
    grid-template-columns: minmax(120px, 1.4fr) 80px 100px 110px 16px;
    gap: 12px;
    align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
    text-decoration: none;
    color: var(--fg);
  }
  .bk-detail-runs-row:last-child { border-bottom: 0; }
  .bk-detail-runs-head {
    background: var(--surface);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .bk-detail-runs-row-data { cursor: pointer; }
  .bk-detail-runs-row-data:hover { background: var(--surface-hover); }
  .bk-detail-runs-row .right { text-align: right; }
  .bk-detail-runs-when { display: flex; flex-direction: column; gap: 1px; }
  .bk-detail-runs-day { font-size: 12px; color: var(--fg); }
  .bk-detail-runs-time { font-size: 10px; color: var(--fg-subtle); }
  .bk-detail-runs-size { font-size: 12px; color: var(--fg); }
  .bk-detail-runs-dur { font-size: 11px; color: var(--fg-muted); }
  :global(.bk-detail-runs-arrow) { color: var(--fg-subtle); }
</style>
