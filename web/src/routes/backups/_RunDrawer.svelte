<script lang="ts">
  // Run drawer — right-side aside per backup-wizards.jsx::RunDrawer.
  // Shows status / size / duration / phase summary, the run error if
  // any, and the restore-flow with three modes (in-place / alongside /
  // download). Per-run log streaming isn't backed yet (BackupRun has
  // status + error but no log array) — placeholder block surfaces what
  // we have and notes the rest is a follow-up.
  import { type BackupRun, type BackupJob, type BackupTarget } from '$lib/api';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import {
    X, Copy, RefreshCw, ArrowRight, AlertTriangle, Lock, Undo2, ExternalLink
  } from 'lucide-svelte';

  interface Props {
    run: BackupRun;
    job?: BackupJob;
    target?: BackupTarget;
    onclose: () => void;
    onrestore: (r: BackupRun, mode: 'in-place' | 'alongside' | 'download') => void;
  }
  let { run, job, target, onclose, onrestore }: Props = $props();

  let restoreOpen = $state(false);

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

  const sp = $derived(statusPill(run.status));
  const restoreModes: { id: 'in-place' | 'alongside' | 'download'; title: string; blurb: string; danger?: boolean }[] = [
    { id: 'in-place', title: 'Restore in place', blurb: 'Replace current data on this dockmesh. Stops affected stacks first.', danger: true },
    { id: 'alongside', title: 'Restore alongside', blurb: 'Mount as new stacks with a -restored suffix. Original keeps running.' },
    { id: 'download', title: 'Download archive', blurb: 'Stream the encrypted .tar.age locally for offline inspection.' }
  ];

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window onkeydown={onKey} />

<button type="button" class="bk-drawer-backdrop" onclick={onclose} aria-label="Close drawer"></button>
<aside class="bk-drawer" role="dialog" aria-modal="true">
  <header class="bk-drawer-head">
    <div class="bk-drawer-head-text">
      <Eyebrow>Run · #{run.id}</Eyebrow>
      <h2 class="ed-title bk-drawer-title font-mono">
        {run.job_name}
        <span class="bk-drawer-title-meta">· {fmtDay(run.started_at)}, {fmtTimeOnly(run.started_at)}</span>
      </h2>
      <p class="bk-drawer-subtitle">
        {#if run.status === 'failed'}
          <span class="bk-drawer-fail">Failed in {fmtDuration(run.started_at, run.finished_at)} — no data written.</span>
        {:else if run.status === 'running'}
          Running… size + duration update on completion.
        {:else if run.status === 'success'}
          Wrote <em class="ed-accent">{fmtBytes(run.size_bytes)}</em>
          {#if target} to <em class="ed-accent">{target.name}</em>{/if}
          in {fmtDuration(run.started_at, run.finished_at)}.
        {:else}
          {run.status}
        {/if}
      </p>
    </div>
    <a href="/backups/runs/{run.id}" class="bk-drawer-pop" title="Open as page" aria-label="Open as page">
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
        <span class="dm-pill {sp.cls} bk-mini-pill"><span class="dm-pill-dot"></span> {sp.label}</span>
        {#if run.encrypted}<span class="bk-drawer-stat-mono"><Lock size={9} strokeWidth={1.5} /> encrypted</span>{/if}
      </div>
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Size</span>
        <span class="bk-drawer-stat-value">{run.size_bytes ? fmtBytes(run.size_bytes) : '—'}</span>
      </div>
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Duration</span>
        <span class="bk-drawer-stat-value">{fmtDuration(run.started_at, run.finished_at)}</span>
      </div>
    </div>

    {#if run.error}
      <div class="bk-drawer-error">
        <AlertTriangle size={12} strokeWidth={1.5} class="bk-drawer-error-icon" />
        <div class="bk-drawer-error-text font-mono">{run.error}</div>
      </div>
    {/if}

    <!-- Run metadata -->
    <section class="bk-drawer-section">
      <Eyebrow>Run metadata</Eyebrow>
      <dl class="bk-drawer-kv">
        <dt>started</dt>
        <dd class="font-mono">{fmtTime(run.started_at)}</dd>
        {#if run.finished_at}
          <dt>finished</dt>
          <dd class="font-mono">{fmtTime(run.finished_at)}</dd>
        {/if}
        {#if run.target_path}
          <dt>target path</dt>
          <dd class="font-mono bk-drawer-kv-break">{run.target_path}</dd>
        {/if}
        {#if run.sha256}
          <dt>sha-256</dt>
          <dd class="font-mono bk-drawer-kv-hash">{run.sha256}</dd>
        {/if}
        <dt>encrypted</dt>
        <dd class="font-mono">{run.encrypted ? 'yes (age)' : 'no'}</dd>
        {#if run.sources?.length}
          <dt>sources</dt>
          <dd>
            <div class="bk-drawer-source-chips">
              {#each run.sources as s, i (i)}
                <span class="dm-pill dm-pill-neutral bk-mini-pill">{s.type}: <span class="font-mono">{s.name}</span></span>
              {/each}
            </div>
          </dd>
        {/if}
      </dl>
    </section>

    <!-- Logs (placeholder until backend records them) -->
    <section class="bk-drawer-section">
      <Eyebrow>Log</Eyebrow>
      <div class="bk-drawer-log-empty">
        <p class="bk-drawer-section-empty">
          Per-run log capture isn't recorded by the backend yet — only status + error message survive the run.
          <a href="#" class="bk-drawer-link" onclick={(e) => { e.preventDefault(); copyDetails(); }}>Copy run metadata</a>
          for an audit trail.
        </p>
      </div>
    </section>

    <!-- Restore flow -->
    {#if run.status === 'success'}
      <section class="bk-drawer-section bk-drawer-restore">
        <div class="bk-drawer-section-head">
          <Eyebrow>Restore</Eyebrow>
          {#if !restoreOpen}
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (restoreOpen = true)}>show options</button>
          {/if}
        </div>
        {#if !restoreOpen}
          <p class="bk-drawer-section-empty">
            This run produced a recoverable snapshot.
            <a href="#" class="bk-drawer-link" onclick={(e) => { e.preventDefault(); restoreOpen = true; }}>Open restore options</a>
            — three modes available.
          </p>
        {:else}
          <div class="bk-drawer-restore-list">
            {#each restoreModes as m (m.id)}
              <button type="button" class="bk-drawer-restore-row" class:danger={m.danger} onclick={() => onrestore(run, m.id)}>
                <div class="bk-drawer-restore-text">
                  <span class="bk-drawer-restore-title">{m.title}</span>
                  <span class="bk-drawer-restore-blurb">{m.blurb}</span>
                </div>
                <ArrowRight size={12} strokeWidth={1.5} class="bk-drawer-restore-arrow" />
              </button>
            {/each}
          </div>
        {/if}
      </section>
    {/if}
  </div>

  <footer class="bk-drawer-foot">
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={onclose}>Close</button>
    <span class="bk-drawer-foot-spacer"></span>
    <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={copyDetails}>
      <Copy size={11} strokeWidth={1.5} /> Copy details
    </button>
    {#if run.status === 'success'}
      <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={() => (restoreOpen = true)}>
        <Undo2 size={11} strokeWidth={1.5} /> Restore
      </button>
    {/if}
  </footer>
</aside>

<style>
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
    width: min(640px, 100vw);
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
    font-size: 20px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .bk-drawer-title-meta { color: var(--fg-subtle); font-weight: 400; font-size: 14px; }
  .bk-drawer-subtitle {
    margin-top: 4px;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.5;
  }
  .bk-drawer-fail { color: var(--color-danger-400); }
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
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .bk-mini-pill { font-size: 9.5px; padding: 1px 6px; }

  .bk-drawer-error {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    border-radius: 5px;
    color: var(--color-danger-400);
  }
  :global(.bk-drawer-error-icon) { color: var(--color-danger-400); flex-shrink: 0; margin-top: 2px; }
  .bk-drawer-error-text {
    font-size: 11.5px;
    line-height: 1.55;
    word-break: break-all;
    flex: 1;
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
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-drawer-link {
    color: var(--accent-fg);
    text-decoration: none;
    cursor: pointer;
  }
  .bk-drawer-link:hover { text-decoration: underline; }

  .bk-drawer-kv {
    display: grid;
    grid-template-columns: 110px 1fr;
    row-gap: 6px;
    column-gap: 12px;
    margin: 0;
    font-size: 12px;
    padding: 12px 14px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
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
    color: var(--fg);
    word-break: break-all;
  }
  .bk-drawer-kv-break { word-break: break-all; }
  .bk-drawer-kv-hash {
    font-size: 10.5px;
    color: var(--fg-subtle);
    word-break: break-all;
  }

  .bk-drawer-log-empty {
    padding: 18px 16px;
    text-align: center;
    border: 1px dashed var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
  }

  .bk-drawer-source-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }

  /* Restore flow — mockup pattern: dashed top border separating from
     the rest of the drawer body, no card chrome. Each row is a button
     with hairline border, accent on hover. */
  .bk-drawer-restore {
    border-top: 1px dashed var(--border-subtle);
    padding-top: 16px;
  }
  .bk-drawer-restore-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .bk-drawer-restore-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-elevated);
    cursor: pointer;
    text-align: left;
    width: 100%;
    transition: border-color 120ms ease, background 120ms ease;
  }
  .bk-drawer-restore-row:hover {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 5%, var(--bg-elevated));
  }
  .bk-drawer-restore-row.danger:hover {
    border-color: var(--color-danger-500);
    background: color-mix(in srgb, var(--color-danger-500) 5%, var(--bg-elevated));
  }
  .bk-drawer-restore-text {
    display: flex;
    flex-direction: column;
    gap: 4px;
    flex: 1;
    min-width: 0;
  }
  .bk-drawer-restore-title {
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
  }
  .bk-drawer-restore-row.danger .bk-drawer-restore-title { color: var(--color-danger-400); }
  .bk-drawer-restore-blurb {
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.5;
  }
  :global(.bk-drawer-restore-arrow) { color: var(--fg-subtle); flex-shrink: 0; }

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
</style>
