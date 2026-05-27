<script lang="ts">
  // Job drawer — right-side 560px aside. Mockup pattern from
  // backup-wizards.jsx::JobDrawer (similar UX to Edit-Role drawer used
  // on /users). Shows full job detail + run-history + actions in-place
  // so the operator doesn't context-switch out of the jobs grid.
  import { api, ApiError, type BackupJob, type BackupRun } from '$lib/api';
  import { Eyebrow } from '$lib/components/editorial';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    X, Play, Edit, Trash2, Copy, RefreshCw, ArrowRight, AlertTriangle,
    Clock, Lock, ExternalLink
  } from 'lucide-svelte';

  interface Props {
    job: BackupJob;
    runs: BackupRun[];
    onclose: () => void;
    onedit: (j: BackupJob) => void;
    ondelete: (j: BackupJob) => void;
    onrun: (j: BackupJob) => Promise<void>;
    ontoggle: (j: BackupJob) => Promise<void>;
    onopenrun: (r: BackupRun) => void;
  }
  let { job, runs, onclose, onedit, ondelete, onrun, ontoggle, onopenrun }: Props = $props();

  const jobRuns = $derived(runs.filter((r) => r.job_name === job.name).slice(0, 14));
  const lastRun = $derived(jobRuns[0] ?? null);
  const failed7 = $derived(jobRuns.filter((r) => r.status === 'failed').length);
  const successRuns = $derived(jobRuns.filter((r) => r.status === 'success'));
  const avgSize = $derived(
    successRuns.length > 0
      ? successRuns.reduce((s, r) => s + r.size_bytes, 0) / successRuns.length
      : 0
  );

  let busy = $state(false);
  async function runNow() {
    busy = true;
    try { await onrun(job); } finally { busy = false; }
  }

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
  function hookSummary(h: { container: string; cmd: string[] }): string {
    return h.cmd.join(' ');
  }

  // Close on Escape
  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window onkeydown={onKey} />

<button type="button" class="bk-drawer-backdrop" onclick={onclose} aria-label="Close drawer"></button>
<aside class="bk-drawer" role="dialog" aria-modal="true">
  <header class="bk-drawer-head">
    <div class="bk-drawer-head-text">
      <Eyebrow>Backup job</Eyebrow>
      <h2 class="ed-title bk-drawer-title font-mono">{job.name}</h2>
      <p class="bk-drawer-subtitle">
        {#if job.sources.length === 0}<em>no source</em>{:else}{job.sources[0].type}: {job.sources[0].name}{#if job.sources.length > 1} <span class="muted">+ {job.sources.length - 1} more</span>{/if}{/if}
        {' '}→ <em class="ed-accent">{job.target_type}</em>
      </p>
    </div>
    <a href="/backups/jobs/{job.id}" class="bk-drawer-pop" title="Open as page" aria-label="Open as page">
      <ExternalLink size={13} strokeWidth={1.5} />
    </a>
    <button type="button" onclick={onclose} class="bk-drawer-close" aria-label="Close">
      <X size={14} strokeWidth={1.5} />
    </button>
  </header>

  <div class="bk-drawer-body">
    <!-- Stat row -->
    <div class="bk-drawer-stat-row">
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Status</span>
        {#if lastRun}
          {@const sp = statusPill(lastRun.status)}
          <span class="dm-pill {sp.cls} bk-mini-pill"><span class="dm-pill-dot"></span> {sp.label}</span>
        {:else if job.enabled}
          <span class="dm-pill dm-pill-neutral bk-mini-pill">idle</span>
        {:else}
          <span class="dm-pill dm-pill-neutral bk-mini-pill">disabled</span>
        {/if}
        {#if lastRun}
          <span class="bk-drawer-stat-mono">{fmtTime(lastRun.started_at)}</span>
        {/if}
      </div>
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Schedule</span>
        <span class="bk-drawer-stat-value">{cronHuman(job.schedule)}</span>
        <span class="bk-drawer-stat-mono">{job.schedule}</span>
      </div>
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Avg size</span>
        <span class="bk-drawer-stat-value">{fmtBytes(avgSize)}</span>
        <span class="bk-drawer-stat-mono">{jobRuns.length} runs · {failed7} failed</span>
      </div>
    </div>

    <!-- Source detail -->
    <section class="bk-drawer-section">
      <Eyebrow>Source · {job.sources.length}</Eyebrow>
      {#if job.sources.length === 0}
        <p class="bk-drawer-section-empty">No source configured — backup would be empty.</p>
      {:else}
        <ul class="bk-drawer-list">
          {#each job.sources as s, i (i)}
            <li class="bk-drawer-list-row">
              <span class="dm-pill dm-pill-neutral bk-mini-pill">{s.type}</span>
              <span class="font-mono bk-drawer-list-name">{s.name}</span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <!-- Target detail -->
    <section class="bk-drawer-section">
      <Eyebrow>Target</Eyebrow>
      <div class="bk-drawer-target">
        <div class="bk-drawer-target-head">
          <span class="dm-pill dm-pill-neutral bk-mini-pill">{job.target_type}</span>
          {#if job.encrypt}<span class="dm-pill bk-mini-pill bk-pill-encrypt"><Lock size={9} strokeWidth={1.5} /> age</span>{/if}
        </div>
        {#if job.target_config && Object.keys(job.target_config).length > 0}
          <dl class="bk-drawer-kv">
            {#each Object.entries(job.target_config) as [k, v] (k)}
              <dt>{k}</dt>
              <dd class="font-mono">{k.includes('secret') || k.includes('password') ? '••••••••' : String(v ?? '—')}</dd>
            {/each}
          </dl>
        {/if}
      </div>
    </section>

    <!-- Run history -->
    <section class="bk-drawer-section">
      <div class="bk-drawer-section-head">
        <Eyebrow>Run history · last {jobRuns.length}</Eyebrow>
      </div>
      {#if jobRuns.length === 0}
        <p class="bk-drawer-section-empty">No runs recorded yet.</p>
      {:else}
        <div class="bk-drawer-runs">
          <div class="bk-drawer-runs-row bk-drawer-runs-head">
            <span>when</span>
            <span class="right">size</span>
            <span class="right">duration</span>
            <span>status</span>
            <span></span>
          </div>
          {#each jobRuns as r (r.id)}
            {@const sp = statusPill(r.status)}
            <button type="button" class="bk-drawer-runs-row bk-drawer-runs-row-data" onclick={() => onopenrun(r)}>
              <span class="font-mono bk-drawer-runs-when">
                <span class="bk-drawer-runs-day">{fmtDay(r.started_at)}</span>
                <span class="bk-drawer-runs-time">{fmtTimeOnly(r.started_at)}</span>
              </span>
              <span class="font-mono right bk-drawer-runs-size">{r.size_bytes ? fmtBytes(r.size_bytes) : '—'}</span>
              <span class="font-mono right bk-drawer-runs-dur">{fmtDuration(r.started_at, r.finished_at)}</span>
              <span><span class="dm-pill {sp.cls} bk-mini-pill"><span class="dm-pill-dot"></span> {sp.label}</span></span>
              <ArrowRight size={11} strokeWidth={1.5} class="bk-drawer-runs-arrow" />
            </button>
          {/each}
        </div>
      {/if}
    </section>

    <!-- Retention + hooks -->
    <section class="bk-drawer-grid">
      <div class="bk-drawer-block">
        <Eyebrow>Retention</Eyebrow>
        <p class="bk-drawer-block-body">
          Keep <em class="ed-accent">{job.retention_count}</em> newest runs.
          {#if job.retention_days > 0}
            Drop runs older than <em class="ed-accent">{job.retention_days}d</em>.
          {:else}
            No age limit.
          {/if}
        </p>
      </div>
      <div class="bk-drawer-block">
        <Eyebrow>Hooks · {(job.pre_hooks?.length ?? 0) + (job.post_hooks?.length ?? 0)}</Eyebrow>
        {#if (job.pre_hooks?.length ?? 0) + (job.post_hooks?.length ?? 0) === 0}
          <p class="bk-drawer-block-body bk-drawer-block-muted">
            No hooks configured. Volumes are paused via fsfreeze before snapshot.
          </p>
        {:else}
          <ul class="bk-drawer-block-list">
            {#each job.pre_hooks ?? [] as h, i (`pre-${i}`)}
              <li class="font-mono bk-drawer-block-hook">
                <span class="bk-drawer-block-hook-tag">pre</span>
                <span class="bk-drawer-block-hook-ctr">{h.container}</span>
                <span class="bk-drawer-block-hook-cmd">{hookSummary(h)}</span>
              </li>
            {/each}
            {#each job.post_hooks ?? [] as h, i (`post-${i}`)}
              <li class="font-mono bk-drawer-block-hook">
                <span class="bk-drawer-block-hook-tag bk-drawer-block-hook-tag-post">post</span>
                <span class="bk-drawer-block-hook-ctr">{h.container}</span>
                <span class="bk-drawer-block-hook-cmd">{hookSummary(h)}</span>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    </section>
  </div>

  <footer class="bk-drawer-foot">
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={onclose}>Close</button>
    <span class="bk-drawer-foot-spacer"></span>
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm bk-drawer-danger" onclick={() => ondelete(job)}>
      <Trash2 size={11} strokeWidth={1.5} /> Delete
    </button>
    <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={() => onedit(job)}>
      <Edit size={11} strokeWidth={1.5} /> Edit
    </button>
    <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={!job.enabled || busy} onclick={runNow}>
      <Play size={11} strokeWidth={1.5} /> {busy ? 'Starting…' : 'Run now'}
    </button>
  </footer>
</aside>

<style>
  /* Backdrop covers the whole viewport. We render at z-index 100 so it
     sits above the topbar (which has its own z-index 9 + backdrop-filter,
     same trick as the host-picker — `position:fixed` inside a backdrop-
     filtered ancestor only works if it goes above the offending parent). */
  .bk-drawer-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.55);
    backdrop-filter: blur(2px);
    border: 0;
    cursor: default;
    z-index: 100;
  }
  .bk-drawer {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    width: min(580px, 100vw);
    background: var(--bg-elevated);
    border-left: 1px solid var(--border-strong);
    box-shadow: -20px 0 60px rgba(0, 0, 0, 0.4);
    z-index: 101;
    display: flex;
    flex-direction: column;
    animation: bk-drawer-in 200ms ease-out;
  }
  @keyframes bk-drawer-in {
    from { transform: translateX(20px); opacity: 0; }
    to { transform: translateX(0); opacity: 1; }
  }

  .bk-drawer-head {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 22px 24px 16px;
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }
  .bk-drawer-head-text { flex: 1; min-width: 0; }
  .bk-drawer-title {
    margin-top: 8px;
    font-size: 22px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-drawer-subtitle {
    margin-top: 4px;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.5;
  }
  .bk-drawer-close,
  .bk-drawer-pop {
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 4px;
    border-radius: 3px;
    text-decoration: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .bk-drawer-close:hover,
  .bk-drawer-pop:hover { color: var(--fg); background: var(--surface-hover); }

  .bk-drawer-body {
    flex: 1;
    overflow-y: auto;
    padding: 22px 24px 28px;
    display: flex;
    flex-direction: column;
    gap: 24px;
  }

  /* Stat row — connected hairline grid (mockup pattern). The gap is
     1px and the parent's bg leaks through, so the cells appear
     separated by a hairline rather than each carrying its own border. */
  .bk-drawer-stat-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    gap: 1px;
    background: var(--border-subtle);
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    overflow: hidden;
  }
  .bk-drawer-stat {
    display: flex;
    flex-direction: column;
    padding: 12px 14px;
    background: var(--bg-elevated);
  }
  .bk-drawer-stat-label {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .bk-drawer-stat-value {
    font-size: 13px;
    color: var(--fg);
    margin-top: 2px;
  }
  .bk-drawer-stat-mono {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }

  .bk-mini-pill { font-size: 9.5px; padding: 1px 6px; }
  .bk-pill-encrypt {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--color-brand-500) 35%, var(--border));
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }

  .bk-drawer-section { display: flex; flex-direction: column; gap: 10px; }
  .bk-drawer-section-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .bk-drawer-section-empty {
    margin: 0;
    font-size: 12px;
    color: var(--fg-subtle);
    line-height: 1.55;
  }

  /* Source list */
  .bk-drawer-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .bk-drawer-list-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
  }
  .bk-drawer-list-name {
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Target block */
  .bk-drawer-target {
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    padding: 12px 14px;
  }
  .bk-drawer-target-head {
    display: flex;
    gap: 6px;
    margin-bottom: 10px;
  }
  .bk-drawer-kv {
    display: grid;
    grid-template-columns: 110px 1fr;
    row-gap: 6px;
    column-gap: 12px;
    margin: 0;
    font-size: 12px;
  }
  .bk-drawer-kv dt {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .bk-drawer-kv dd {
    margin: 0;
    color: var(--fg-muted);
    word-break: break-all;
  }

  /* Run history table */
  .bk-drawer-runs {
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    overflow: hidden;
    background: var(--bg-elevated);
  }
  .bk-drawer-runs-row {
    display: grid;
    grid-template-columns: minmax(120px, 1.4fr) 80px 90px 100px 16px;
    gap: 12px;
    align-items: center;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
    text-align: left;
    background: transparent;
    border-left: 0; border-right: 0; border-top: 0;
    color: var(--fg);
    width: 100%;
  }
  .bk-drawer-runs-row:last-child { border-bottom: 0; }
  .bk-drawer-runs-head {
    background: var(--surface);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .bk-drawer-runs-row-data { cursor: pointer; }
  .bk-drawer-runs-row-data:hover { background: var(--surface-hover); }
  .bk-drawer-runs-row .right { text-align: right; }
  .bk-drawer-runs-when { display: flex; flex-direction: column; gap: 1px; }
  .bk-drawer-runs-day { font-size: 11.5px; color: var(--fg); }
  .bk-drawer-runs-time { font-size: 10px; color: var(--fg-subtle); }
  .bk-drawer-runs-size { font-size: 11.5px; color: var(--fg); }
  .bk-drawer-runs-dur { font-size: 11px; color: var(--fg-muted); }
  :global(.bk-drawer-runs-arrow) { color: var(--fg-subtle); }

  /* Two-column block grid (retention + hooks) */
  .bk-drawer-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 14px;
  }
  @media (max-width: 520px) {
    .bk-drawer-grid { grid-template-columns: 1fr; }
  }
  .bk-drawer-block {
    padding: 12px 14px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
  }
  .bk-drawer-block-body {
    margin: 8px 0 0;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-drawer-block-muted { color: var(--fg-subtle); }
  .bk-drawer-block-list {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .bk-drawer-block-hook {
    font-size: 11px;
    color: var(--fg-muted);
    line-height: 1.5;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: baseline;
  }
  .bk-drawer-block-hook-tag {
    font-size: 9.5px;
    padding: 1px 5px;
    border-radius: 2px;
    background: color-mix(in srgb, var(--color-brand-500) 16%, transparent);
    color: var(--accent-fg);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .bk-drawer-block-hook-tag-post {
    background: color-mix(in srgb, var(--color-warning-500) 16%, transparent);
    color: var(--color-warning-400);
  }
  .bk-drawer-block-hook-ctr { color: var(--fg); }
  .bk-drawer-block-hook-cmd {
    color: var(--fg-subtle);
    word-break: break-all;
    flex: 1;
    min-width: 0;
  }

  .bk-drawer-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 14px 24px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
    flex-shrink: 0;
  }
  .bk-drawer-foot-spacer { flex: 1; }
  .bk-drawer-danger { color: var(--color-danger-400); }

  .muted { color: var(--fg-subtle); }
</style>
