<script lang="ts">
  // Target drawer — right-side aside. Mockup didn't author one (only
  // Job + Run drawers exist in backup-wizards.jsx), so this follows
  // the same primitives + adds target-specific blocks: full config
  // dump (passwords masked), storage drill-down with bar, jobs that
  // reference this target, last-test result.
  import { api, ApiError, type BackupTarget, type BackupJob } from '$lib/api';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import {
    X, Edit, Trash2, RefreshCw, AlertTriangle, ArrowRight, HardDrive, ExternalLink
  } from 'lucide-svelte';

  interface Props {
    target: BackupTarget;
    jobs: BackupJob[];
    onclose: () => void;
    onedit: (t: BackupTarget) => void;
    ondelete: (t: BackupTarget) => void;
    ontest: (t: BackupTarget) => Promise<void>;
  }
  let { target, jobs, onclose, onedit, ondelete, ontest }: Props = $props();

  let testing = $state(false);
  async function runTest() {
    testing = true;
    try { await ontest(target); } finally { testing = false; }
  }

  const usedPct = $derived(target.total_bytes > 0 ? (target.used_bytes / target.total_bytes) * 100 : 0);
  const jobsHere = $derived(jobs.filter((j) => j.target_type === target.type));

  function fmtBytes(n: number): string {
    if (!n) return '—';
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }
  function fmtTime(ts?: string): string {
    if (!ts) return 'never';
    const d = (Date.now() - new Date(ts).getTime()) / 1000;
    if (d < 60) return 'just now';
    if (d < 3600) return `${Math.floor(d / 60)}m ago`;
    if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
    return new Date(ts).toLocaleString();
  }
  function targetTypeMeta(type: string): { label: string; glyph: string } {
    if (type === 's3') return { label: 'S3 / object store', glyph: 'S3' };
    if (type === 'sftp') return { label: 'SFTP over SSH', glyph: 'FTP' };
    if (type === 'smb') return { label: 'SMB / NAS share', glyph: 'NAS' };
    if (type === 'webdav') return { label: 'WebDAV / Nextcloud', glyph: 'DAV' };
    return { label: 'Local directory', glyph: 'LOC' };
  }
  function isSecretKey(k: string): boolean {
    const lk = k.toLowerCase();
    return lk.includes('secret') || lk.includes('password') || lk.includes('token');
  }
  const meta = $derived(targetTypeMeta(target.type));
  const cfgEntries = $derived(
    target.config && typeof target.config === 'object'
      ? Object.entries(target.config as Record<string, any>)
      : []
  );

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window onkeydown={onKey} />

<button type="button" class="bk-drawer-backdrop" onclick={onclose} aria-label="Close drawer"></button>
<aside class="bk-drawer" role="dialog" aria-modal="true">
  <header class="bk-drawer-head">
    <span class="bk-drawer-glyph">{meta.glyph}</span>
    <div class="bk-drawer-head-text">
      <Eyebrow>Backup target</Eyebrow>
      <h2 class="ed-title bk-drawer-title font-mono">{target.name}</h2>
      <p class="bk-drawer-subtitle">{meta.label}</p>
    </div>
    <a href="/backups/targets/{target.id}" class="bk-drawer-pop" title="Open as page" aria-label="Open as page">
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
        {#if target.status === 'connected'}
          <span class="dm-pill dm-pill-success bk-mini-pill"><span class="dm-pill-dot"></span> connected</span>
        {:else if target.status === 'error'}
          <span class="dm-pill dm-pill-danger bk-mini-pill"><span class="dm-pill-dot"></span> error</span>
        {:else}
          <span class="dm-pill dm-pill-neutral bk-mini-pill">{target.status || 'unknown'}</span>
        {/if}
      </div>
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Tested</span>
        <span class="bk-drawer-stat-value">{fmtTime(target.last_checked_at)}</span>
      </div>
      <div class="bk-drawer-stat">
        <span class="bk-drawer-stat-label">Jobs</span>
        <span class="bk-drawer-stat-value">{jobsHere.length}</span>
        <span class="bk-drawer-stat-mono">writing here</span>
      </div>
    </div>

    <!-- Storage drill-down -->
    <section class="bk-drawer-section">
      <Eyebrow>Storage</Eyebrow>
      <div class="bk-drawer-storage" class:warn={usedPct > 80}>
        <div class="bk-drawer-storage-head">
          <span class="bk-drawer-storage-label">Used</span>
          {#if target.total_bytes > 0}
            <span class="font-mono bk-drawer-storage-num">
              {fmtBytes(target.used_bytes)} <span class="bk-drawer-storage-num-total">/ {fmtBytes(target.total_bytes)}</span>
            </span>
          {:else}
            <span class="bk-drawer-storage-num-total">—</span>
          {/if}
        </div>
        <div class="bk-drawer-storage-bar">
          <span class="bk-drawer-storage-bar-fill" style="width: {usedPct}%"></span>
        </div>
        <div class="bk-drawer-storage-meta font-mono">
          {#if target.total_bytes > 0}
            <span>{usedPct.toFixed(1)}% used</span>
            <span class="muted">·</span>
            <span>{fmtBytes(target.free_bytes)} free</span>
          {:else}
            <span class="muted">storage size unknown — run a test to discover</span>
          {/if}
        </div>
      </div>
    </section>

    <!-- Configuration -->
    <section class="bk-drawer-section">
      <Eyebrow>Configuration</Eyebrow>
      {#if cfgEntries.length === 0}
        <p class="bk-drawer-section-empty">No configuration recorded.</p>
      {:else}
        <dl class="bk-drawer-kv">
          {#each cfgEntries as [k, v] (k)}
            <dt>{k}</dt>
            <dd class="font-mono">
              {#if isSecretKey(k)}
                <span class="bk-drawer-secret">••••••••</span>
              {:else}
                {String(v ?? '—')}
              {/if}
            </dd>
          {/each}
        </dl>
      {/if}
    </section>

    <!-- Jobs writing here -->
    <section class="bk-drawer-section">
      <div class="bk-drawer-section-head">
        <Eyebrow>Jobs · {jobsHere.length}</Eyebrow>
      </div>
      {#if jobsHere.length === 0}
        <p class="bk-drawer-section-empty">No backup jobs are configured to write to this target.</p>
      {:else}
        <ul class="bk-drawer-list">
          {#each jobsHere as j (j.id)}
            <li class="bk-drawer-list-row">
              <span class="dm-pill dm-pill-neutral bk-mini-pill">{j.enabled ? 'on' : 'off'}</span>
              <span class="font-mono bk-drawer-list-name">{j.name}</span>
              <span class="font-mono bk-drawer-list-meta">{j.schedule}</span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  </div>

  <footer class="bk-drawer-foot">
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={onclose}>Close</button>
    <span class="bk-drawer-foot-spacer"></span>
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm bk-drawer-danger" onclick={() => ondelete(target)}>
      <Trash2 size={11} strokeWidth={1.5} /> Delete
    </button>
    <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={() => onedit(target)}>
      <Edit size={11} strokeWidth={1.5} /> Edit
    </button>
    <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={runTest} disabled={testing}>
      <RefreshCw size={11} strokeWidth={1.5} class={testing ? 'ed-spin' : ''} /> {testing ? 'Testing…' : 'Test connection'}
    </button>
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
    gap: 14px;
    padding: 22px 24px 16px;
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }
  .bk-drawer-glyph {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 40px;
    height: 40px;
    border-radius: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.04em;
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

  /* Storage section */
  .bk-drawer-storage {
    padding: 14px 16px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
  }
  .bk-drawer-storage.warn { border-left: 3px solid var(--color-warning-500); }
  .bk-drawer-storage-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 8px;
  }
  .bk-drawer-storage-label {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .bk-drawer-storage-num { font-size: 13px; color: var(--fg); }
  .bk-drawer-storage-num-total { color: var(--fg-subtle); font-size: 12px; }
  .bk-drawer-storage-bar {
    height: 6px;
    background: var(--bg-elevated);
    border-radius: 3px;
    overflow: hidden;
    position: relative;
  }
  .bk-drawer-storage-bar-fill {
    position: absolute;
    inset: 0;
    right: auto;
    background: var(--accent);
    border-radius: 3px;
    transition: width 200ms;
  }
  .bk-drawer-storage.warn .bk-drawer-storage-bar-fill { background: var(--color-warning-400); }
  .bk-drawer-storage-meta {
    margin-top: 6px;
    font-size: 11px;
    color: var(--fg-muted);
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }

  /* Configuration */
  .bk-drawer-kv {
    display: grid;
    grid-template-columns: 110px 1fr;
    row-gap: 8px;
    column-gap: 12px;
    margin: 0;
    padding: 12px 14px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    font-size: 12px;
  }
  .bk-drawer-kv dt {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    padding-top: 1px;
  }
  .bk-drawer-kv dd {
    margin: 0;
    color: var(--fg);
    word-break: break-all;
  }
  .bk-drawer-secret { color: var(--fg-subtle); letter-spacing: 0.2em; }

  /* Jobs list */
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
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-drawer-list-meta {
    font-size: 10.5px;
    color: var(--fg-subtle);
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
  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
