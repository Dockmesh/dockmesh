<script lang="ts">
  // Global side-drawer showing live + recent stack migrations.
  // Opens from MigrationActivePill in the topbar, can also be opened
  // programmatically (e.g. from a stacks/hosts page's "View all" link).
  //
  // Two sections: Active (live runs with progress) + Recent (terminal).
  // Polls every 5s while open, stops on close.
  import { api, type Migration } from '$lib/api';
  import { goto } from '$app/navigation';
  import { X, ArrowRightLeft } from 'lucide-svelte';
  import { Eyebrow } from '$lib/components/editorial';

  interface Props {
    open: boolean;
  }
  let { open = $bindable(false) }: Props = $props();

  let active = $state<Migration[]>([]);
  let recent = $state<Migration[]>([]);
  let hostNames = $state<Map<string, string>>(new Map());
  let loading = $state(true);

  async function load() {
    try {
      const [a, all, hosts] = await Promise.all([
        api.migrations.active().catch(() => [] as Migration[]),
        api.migrations.list(50).catch(() => [] as Migration[]),
        api.hosts.list().catch(() => []),
      ]);
      active = a;
      const activeIds = new Set(a.map((m) => m.id));
      recent = all.filter((m) => !activeIds.has(m.id)).slice(0, 30);
      const m = new Map<string, string>();
      for (const h of hosts) m.set(h.id, h.name);
      hostNames = m;
    } catch {
      /* keep prior */
    } finally {
      loading = false;
    }
  }

  let timer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    if (open) {
      load();
      timer = setInterval(load, 5_000);
    } else {
      if (timer) { clearInterval(timer); timer = null; }
    }
    return () => {
      if (timer) { clearInterval(timer); timer = null; }
    };
  });

  function onKeydown(e: KeyboardEvent) {
    if (open && e.key === 'Escape') close();
  }

  function close() { open = false; }

  function openStack(name: string) {
    close();
    goto(`/stacks/${encodeURIComponent(name)}`);
  }

  function hostLabel(id: string): string {
    return hostNames.get(id) ?? id;
  }

  function fmtAgo(iso?: string): string {
    if (!iso) return '—';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return `${Math.round(d)}s ago`;
    if (d < 3600) return `${Math.round(d / 60)}m ago`;
    if (d < 86400) return `${Math.round(d / 3600)}h ago`;
    return `${Math.round(d / 86400)}d ago`;
  }

  function fmtDuration(start?: string, end?: string): string {
    if (!start) return '—';
    const s = new Date(start).getTime();
    const e = end ? new Date(end).getTime() : Date.now();
    const secs = Math.floor((e - s) / 1000);
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
    return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
  }

  function fmtBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }

  function progressPercent(m: Migration): number {
    if (!m.progress) return 0;
    if (m.progress.bytes_total > 0) {
      return Math.min(100, Math.round((m.progress.bytes_done / m.progress.bytes_total) * 100));
    }
    if (m.progress.volumes_total > 0) {
      return Math.min(100, Math.round((m.progress.volume_index / m.progress.volumes_total) * 100));
    }
    return 0;
  }

  function statusTone(s: string): 'ok' | 'warn' | 'fail' | 'neutral' {
    if (s === 'completed') return 'ok';
    if (s === 'failed') return 'fail';
    if (s === 'rolled_back') return 'warn';
    return 'neutral';
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div class="md-scrim" onclick={close} role="presentation"></div>
  <aside class="md-drawer" role="dialog" aria-label="Migrations">
    <header class="md-head">
      <div class="md-head-text">
        <Eyebrow active>Live · migrations</Eyebrow>
        <h2 class="ed-title md-title">Stack <em>migrations</em></h2>
        <p class="ed-subtitle md-blurb">
          Active and recent moves across all hosts. Click a row to jump to the stack.
        </p>
      </div>
      <button type="button" class="md-close" onclick={close} aria-label="Close">
        <X size={14} strokeWidth={1.5} />
      </button>
    </header>

    <div class="md-body">
      {#if loading && active.length === 0 && recent.length === 0}
        <div class="md-empty">
          <Eyebrow>loading</Eyebrow>
          <div class="md-empty-text">Fetching migration state…</div>
        </div>
      {:else if active.length === 0 && recent.length === 0}
        <div class="md-empty">
          <ArrowRightLeft size={20} strokeWidth={1.4} />
          <div class="md-empty-text">No migrations yet.</div>
          <p class="ed-subtitle md-empty-blurb">
            Initiate a migration from a stack's detail page to move it from one host to another.
          </p>
        </div>
      {/if}

      {#if active.length > 0}
        <section class="md-section">
          <div class="md-section-head">
            <Eyebrow active>active · {active.length}</Eyebrow>
          </div>
          <div class="md-section-body">
            {#each active as m (m.id)}
              {@const pct = progressPercent(m)}
              <button
                type="button"
                class="md-card md-card-active"
                onclick={() => openStack(m.stack_name)}
              >
                <header class="md-card-head">
                  <span class="md-card-stack">{m.stack_name}</span>
                  <span class="md-card-status">{m.status}</span>
                </header>
                <div class="md-card-route">
                  <span>{hostLabel(m.source_host_id)}</span>
                  <ArrowRightLeft size={10} strokeWidth={1.5} />
                  <span>{hostLabel(m.target_host_id)}</span>
                </div>
                {#if m.phase}
                  <div class="md-card-phase">{m.phase}</div>
                {/if}
                {#if m.progress && (m.progress.bytes_total > 0 || m.progress.volumes_total > 0)}
                  <div class="md-progress">
                    <div class="md-progress-bar">
                      <div class="md-progress-fill" style="width: {pct}%"></div>
                    </div>
                    <div class="md-progress-meta">
                      {#if m.progress.bytes_total > 0}
                        {fmtBytes(m.progress.bytes_done)} / {fmtBytes(m.progress.bytes_total)} ·
                      {/if}
                      vol {m.progress.volume_index}/{m.progress.volumes_total}
                      {#if m.progress.images_total > 0}
                        · img {m.progress.images_pulled}/{m.progress.images_total}
                      {/if}
                      · {pct}%
                    </div>
                  </div>
                {/if}
                <footer class="md-card-foot">
                  started {fmtAgo(m.started_at)} · by {m.initiated_by}
                </footer>
              </button>
            {/each}
          </div>
        </section>
      {/if}

      {#if recent.length > 0}
        <section class="md-section">
          <div class="md-section-head">
            <Eyebrow>recent · {recent.length}</Eyebrow>
          </div>
          <div class="md-section-body">
            {#each recent as m (m.id)}
              <button
                type="button"
                class="md-row"
                onclick={() => openStack(m.stack_name)}
              >
                <span class="md-row-status" data-tone={statusTone(m.status)} aria-hidden="true"></span>
                <div class="md-row-text">
                  <div class="md-row-stack">{m.stack_name}</div>
                  <div class="md-row-route">
                    <span>{hostLabel(m.source_host_id)}</span>
                    <ArrowRightLeft size={9} strokeWidth={1.5} />
                    <span>{hostLabel(m.target_host_id)}</span>
                  </div>
                  {#if m.error_message}
                    <div class="md-row-error" title={m.error_message}>{m.error_message}</div>
                  {/if}
                </div>
                <div class="md-row-aside">
                  <span class="md-row-status-text" data-tone={statusTone(m.status)}>{m.status}</span>
                  <span class="md-row-duration">{fmtDuration(m.started_at, m.completed_at)}</span>
                </div>
              </button>
            {/each}
          </div>
        </section>
      {/if}
    </div>
  </aside>
{/if}

<style>
  .md-scrim {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.55);
    z-index: 99;
    animation: md-fade 150ms ease-out;
  }
  @keyframes md-fade {
    from { opacity: 0; }
    to { opacity: 1; }
  }

  .md-drawer {
    position: fixed;
    top: 0;
    right: 0;
    height: 100vh;
    width: min(520px, 92vw);
    background: var(--bg);
    border-left: 1px solid var(--border-strong);
    z-index: 100;
    display: flex;
    flex-direction: column;
    animation: md-slide 180ms cubic-bezier(.2,.7,.3,1);
  }
  @keyframes md-slide {
    from { transform: translateX(20px); opacity: 0.5; }
    to { transform: translateX(0); opacity: 1; }
  }

  .md-head {
    padding: 24px 24px 16px;
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .md-head-text { min-width: 0; }
  .md-title {
    font-size: 24px;
    line-height: 1.1;
    margin-top: 8px;
  }
  .md-blurb {
    margin-top: 6px;
    max-width: 52ch;
  }
  .md-close {
    width: 28px;
    height: 28px;
    background: transparent;
    border: 1px solid var(--border);
    color: var(--fg-subtle);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    border-radius: 4px;
    flex-shrink: 0;
  }
  .md-close:hover {
    color: var(--fg);
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }

  .md-body {
    flex: 1;
    overflow-y: auto;
    padding: 18px 24px 24px;
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* Empty */
  .md-empty {
    margin-top: 32px;
    text-align: center;
    color: var(--fg-subtle);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
  }
  .md-empty-text {
    font-size: 14px;
    color: var(--fg);
    margin-top: 6px;
  }
  .md-empty-blurb {
    margin-top: 4px;
    max-width: 38ch;
  }

  /* Sections */
  .md-section {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .md-section-head { /* nothing extra */ }
  .md-section-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  /* Active cards */
  .md-card {
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
    display: flex;
    flex-direction: column;
    gap: 6px;
    text-align: left;
    cursor: pointer;
    font: inherit;
    color: inherit;
    transition: border-color 120ms, background 120ms;
  }
  .md-card:hover {
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }
  .md-card-active {
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 4%, var(--surface));
  }
  .md-card-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
  }
  .md-card-stack {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .md-card-status {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--color-warning-400);
  }
  .md-card-route {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .md-card-phase {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .md-progress {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-top: 4px;
  }
  .md-progress-bar {
    height: 3px;
    background: var(--border-subtle);
    border-radius: 2px;
    overflow: hidden;
  }
  .md-progress-fill {
    height: 100%;
    background: var(--color-warning-500);
    transition: width 800ms cubic-bezier(.4,.7,.3,1);
  }
  .md-progress-meta {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
  }
  .md-card-foot {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }

  /* Recent rows */
  .md-row {
    padding: 10px 12px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg);
    display: grid;
    grid-template-columns: 8px minmax(0, 1fr) auto;
    gap: 10px;
    align-items: center;
    cursor: pointer;
    font: inherit;
    color: inherit;
    text-align: left;
    transition: border-color 120ms, background 120ms;
  }
  .md-row:hover {
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }
  .md-row-status {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--border-strong);
  }
  .md-row-status[data-tone="ok"]   { background: var(--color-success-500); }
  .md-row-status[data-tone="fail"] { background: var(--color-danger-500); }
  .md-row-status[data-tone="warn"] { background: var(--color-warning-500); }
  .md-row-text { min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .md-row-stack {
    font-size: 12.5px;
    font-weight: 500;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .md-row-route {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .md-row-error {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--color-danger-400);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    margin-top: 1px;
  }
  .md-row-aside {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 1px;
    flex-shrink: 0;
  }
  .md-row-status-text {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-muted);
  }
  .md-row-status-text[data-tone="ok"]   { color: var(--color-success-400); }
  .md-row-status-text[data-tone="fail"] { color: var(--color-danger-400); }
  .md-row-status-text[data-tone="warn"] { color: var(--color-warning-400); }
  .md-row-duration {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
  }
</style>
