<script lang="ts">
  // Standalone detail page for one backup target. Same layout as the
  // _TargetDrawer but with full-page editorial chrome — better for
  // copy-paste into runbooks ("here's the SFTP target we use…").
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError, type BackupTarget, type BackupJob } from '$lib/api';
  import { Eyebrow } from '$lib/components/editorial';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import {
    ChevronLeft, Edit, Trash2, RefreshCw, AlertTriangle, ArrowRight, Box
  } from 'lucide-svelte';

  const id = $derived(parseInt($page.params.id, 10));

  $effect(() => {
    if (target) pageContext.set(target.name);
    return () => pageContext.clear();
  });

  let target = $state<BackupTarget | null>(null);
  let jobs = $state<BackupJob[]>([]);
  let loading = $state(true);
  let notFound = $state(false);

  async function load() {
    loading = true;
    try {
      const [allTargets, allJobs] = await Promise.all([
        api.backups.listTargets(),
        api.backups.listJobs()
      ]);
      const t = allTargets.find((x) => x.id === id);
      if (!t) { notFound = true; target = null; return; }
      target = t;
      jobs = allJobs;
    } catch (err) {
      toast.error('Failed to load target', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  $effect(() => { id; load(); });
  $effect(() => autoRefresh(load, 15_000));

  let testing = $state(false);
  async function runTest() {
    if (!target) return;
    testing = true;
    toast.info('Testing connection…', target.name);
    try {
      const res = await api.backups.testTarget(target.id);
      if (res.status === 'connected') {
        toast.success('Connected', res.total_bytes > 0 ? `${fmtBytes(res.free_bytes)} free of ${fmtBytes(res.total_bytes)}` : 'OK');
      } else {
        toast.error('Connection failed', res.error);
      }
      await load();
    } catch (err) {
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      testing = false;
    }
  }
  async function deleteTarget() {
    if (!target) return;
    if (!(await confirm.ask({
      title: 'Delete backup target',
      message: `Delete target "${target.name}"?`,
      body: 'Backup jobs that write to this target will fail on their next run until you reassign them.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.backups.deleteTarget(target.id);
      toast.success('Deleted');
      goto('/backups?tab=targets');
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const usedPct = $derived(target && target.total_bytes > 0 ? (target.used_bytes / target.total_bytes) * 100 : 0);
  const jobsHere = $derived(target ? jobs.filter((j) => j.target_type === target!.type) : []);

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
  const meta = $derived(target ? targetTypeMeta(target.type) : { label: '', glyph: '' });
  const cfgEntries = $derived(
    target?.config && typeof target.config === 'object'
      ? Object.entries(target.config as Record<string, any>)
      : []
  );
</script>

{#if loading && !target}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=targets" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / targets
      </a>
    </div>
    <div class="dm-card bk-detail-loading"><Skeleton width="60%" height="6rem" /></div>
  </section>
{:else if notFound || !target}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=targets" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / targets
      </a>
    </div>
    <div class="dm-card bk-detail-loading">
      <EmptyState icon={Box} title="Target not found" description="This target has been deleted or you don't have access." />
    </div>
  </section>
{:else}
  <section class="bk-detail-page">
    <div class="bk-detail-back-row">
      <a href="/backups?tab=targets" class="bk-detail-back">
        <ChevronLeft size={14} strokeWidth={1.5} /> backups / targets
      </a>
    </div>

    <header class="bk-detail-header">
      <span class="bk-detail-glyph">{meta.glyph}</span>
      <div class="bk-detail-header-text">
        <h1 class="ed-title bk-detail-title">{target.name}</h1>
        <p class="ed-subtitle bk-detail-subtitle">{meta.label}</p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm bk-detail-danger" onclick={deleteTarget}>
          <Trash2 size={11} strokeWidth={1.5} /> Delete
        </button>
        <a href="/backups?tab=targets&edit={target.id}" class="dm-btn dm-btn-secondary dm-btn-sm">
          <Edit size={11} strokeWidth={1.5} /> Edit
        </a>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={runTest} disabled={testing}>
          <RefreshCw size={11} strokeWidth={1.5} class={testing ? 'ed-spin' : ''} /> {testing ? 'Testing…' : 'Test connection'}
        </button>
      </div>
    </header>

    <!-- Stat row -->
    <div class="bk-detail-stat-row">
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Status</span>
        {#if target.status === 'connected'}
          <span class="dm-pill dm-pill-success bk-detail-pill"><span class="dm-pill-dot"></span> connected</span>
        {:else if target.status === 'error'}
          <span class="dm-pill dm-pill-danger bk-detail-pill"><span class="dm-pill-dot"></span> error</span>
        {:else}
          <span class="dm-pill dm-pill-neutral bk-detail-pill">{target.status || 'unknown'}</span>
        {/if}
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Tested</span>
        <span class="bk-detail-stat-value">{fmtTime(target.last_checked_at)}</span>
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Jobs</span>
        <span class="bk-detail-stat-value">{jobsHere.length}</span>
        <span class="bk-detail-stat-mono">writing here</span>
      </div>
      <div class="bk-detail-stat">
        <span class="bk-detail-stat-label">Type</span>
        <span class="bk-detail-stat-value">{target.type.toUpperCase()}</span>
      </div>
    </div>

    <div class="bk-detail-grid">
      <!-- Left column — storage + config -->
      <div class="bk-detail-col">
        <section class="bk-detail-section dm-card">
          <Eyebrow>Storage</Eyebrow>
          <div class="bk-detail-storage" class:warn={usedPct > 80}>
            <div class="bk-detail-storage-head">
              <span class="bk-detail-storage-label">Used</span>
              {#if target.total_bytes > 0}
                <span class="font-mono bk-detail-storage-num">
                  {fmtBytes(target.used_bytes)} <span class="bk-detail-storage-num-total">/ {fmtBytes(target.total_bytes)}</span>
                </span>
              {:else}
                <span class="bk-detail-storage-num-total">—</span>
              {/if}
            </div>
            <div class="bk-detail-storage-bar">
              <span class="bk-detail-storage-bar-fill" style="width: {usedPct}%"></span>
            </div>
            <div class="bk-detail-storage-meta font-mono">
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

        <section class="bk-detail-section dm-card">
          <Eyebrow>Configuration</Eyebrow>
          {#if cfgEntries.length === 0}
            <p class="bk-detail-empty">No configuration recorded.</p>
          {:else}
            <dl class="bk-detail-kv">
              {#each cfgEntries as [k, v] (k)}
                <dt>{k}</dt>
                <dd class="font-mono">
                  {#if isSecretKey(k)}
                    <span class="bk-detail-secret">••••••••</span>
                  {:else}
                    {String(v ?? '—')}
                  {/if}
                </dd>
              {/each}
            </dl>
          {/if}
        </section>
      </div>

      <!-- Right column — jobs -->
      <div class="bk-detail-col">
        <section class="bk-detail-section dm-card">
          <Eyebrow>Jobs · {jobsHere.length}</Eyebrow>
          {#if jobsHere.length === 0}
            <p class="bk-detail-empty">No backup jobs are configured to write to this target.</p>
          {:else}
            <ul class="bk-detail-list">
              {#each jobsHere as j (j.id)}
                <a href="/backups/jobs/{j.id}" class="bk-detail-list-row bk-detail-list-link">
                  <span class="dm-pill dm-pill-neutral bk-detail-pill">{j.enabled ? 'on' : 'off'}</span>
                  <span class="font-mono bk-detail-list-name">{j.name}</span>
                  <span class="font-mono bk-detail-list-meta">{j.schedule}</span>
                  <ArrowRight size={11} strokeWidth={1.5} class="bk-detail-list-arrow" />
                </a>
              {/each}
            </ul>
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
    gap: 16px;
    flex-wrap: wrap;
    margin-bottom: 22px;
  }
  .bk-detail-glyph {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 44px;
    height: 44px;
    border-radius: 5px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-family: var(--font-mono);
    font-size: 13px;
    font-weight: 600;
    letter-spacing: 0.04em;
    flex-shrink: 0;
  }
  .bk-detail-header-text { flex: 1; min-width: 0; max-width: 70ch; }
  .bk-detail-title {
    font-size: 26px;
    margin-top: 8px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-detail-subtitle {
    margin-top: 4px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
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

  /* Storage block */
  .bk-detail-storage {
    margin-top: 8px;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
  }
  .bk-detail-storage.warn { border-left: 3px solid var(--color-warning-500); }
  .bk-detail-storage-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 8px;
  }
  .bk-detail-storage-label {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .bk-detail-storage-num { font-size: 13px; color: var(--fg); }
  .bk-detail-storage-num-total { color: var(--fg-subtle); font-size: 12px; }
  .bk-detail-storage-bar {
    height: 6px;
    background: var(--bg-elevated);
    border-radius: 3px;
    overflow: hidden;
    position: relative;
  }
  .bk-detail-storage-bar-fill {
    position: absolute;
    inset: 0;
    right: auto;
    background: var(--accent);
    border-radius: 3px;
    transition: width 200ms;
  }
  .bk-detail-storage.warn .bk-detail-storage-bar-fill { background: var(--color-warning-400); }
  .bk-detail-storage-meta {
    margin-top: 6px;
    font-size: 11px;
    color: var(--fg-muted);
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }

  /* Configuration kv */
  .bk-detail-kv {
    display: grid;
    grid-template-columns: 110px 1fr;
    row-gap: 8px;
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
    padding-top: 1px;
  }
  .bk-detail-kv dd {
    margin: 0;
    color: var(--fg);
    word-break: break-all;
  }
  .bk-detail-secret { color: var(--fg-subtle); letter-spacing: 0.2em; }

  /* Jobs list */
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
    text-decoration: none;
    color: var(--fg);
    transition: border-color 120ms, background 120ms;
  }
  .bk-detail-list-link:hover { border-color: var(--border-strong); background: var(--surface-hover); }
  .bk-detail-list-name {
    font-size: 12.5px;
    color: var(--fg);
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-detail-list-meta {
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  :global(.bk-detail-list-arrow) { color: var(--fg-subtle); }

  .muted { color: var(--fg-subtle); }
  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
