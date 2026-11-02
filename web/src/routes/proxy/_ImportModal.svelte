<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, ApiError, type ProxyImportResult } from '$lib/api';
  import { Modal, StatusChip } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';

  // Paste a Caddyfile, see what Dockmesh can take over, then import.
  // Imported sites start disabled so they can be checked first.
  let { open = $bindable(false), onimported }: { open: boolean; onimported?: () => void } = $props();

  let text = $state('');
  let result = $state<ProxyImportResult | null>(null);
  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    if (open) {
      result = null;
      error = '';
    }
  });

  const importable = $derived(result?.sites.filter((s) => s.status !== 'skipped').length ?? 0);

  async function run(dryRun: boolean) {
    busy = true;
    error = '';
    try {
      const res = await api.proxy.importCaddyfile(text, dryRun);
      if (dryRun) {
        result = res;
        return;
      }
      const created = res.sites.filter((s) => s.route_id).length;
      toast.success(m.proxy_import_done({ count: created }), m.proxy_import_done_hint());
      open = false;
      onimported?.();
    } catch (e) {
      error = e instanceof ApiError ? e.message : m.proxy_import_failed();
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={m.proxy_import_title()} maxWidth="max-w-2xl">
  {#if !result}
    <div class="dm-field">
      <label for="imp-text">{m.proxy_import_paste()}</label>
      <textarea id="imp-text" class="dm-input dm-mono imp-text" bind:value={text} spellcheck="false"
        placeholder={'jellyfin.example.org {\n\treverse_proxy 192.168.10.40:8096\n}'}></textarea>
      <span class="hint">{m.proxy_import_paste_hint()}</span>
    </div>
  {:else}
    <p class="dm-sub">{m.proxy_import_found({ count: result.sites.length })} · {m.proxy_import_importable({ n: importable })}</p>
    <div class="dm-tbl imp-list" role="table" aria-label={m.proxy_import_table()}>
      {#each result.sites as s (s.host)}
        <div class="dm-tr" role="row" style="grid-template-columns: 90px minmax(160px, 1fr) minmax(0, 1.4fr)">
          <span role="cell"><StatusChip tone={s.status === 'ready' ? 'ok' : s.status === 'partial' ? 'warn' : 'bad'} label={s.status === 'skipped' ? m.proxy_import_st_skipped() : s.status === 'partial' ? m.proxy_import_st_partial() : s.status === 'ready' ? m.proxy_import_st_ready() : s.status} /></span>
          <span role="cell" class="dm-mono" title={s.host}>{s.host}</span>
          <span role="cell" class="dm-dim" title={s.error ?? (s.unsupported?.length ? m.proxy_import_not_imported_title({ list: s.unsupported.join(', ') }) : s.upstream)}>
            {#if s.error}<span class="bad">{s.error}</span>
            {:else if s.unsupported?.length}→ {s.upstream} · {m.proxy_import_not_imported({ list: s.unsupported.join(', ') })}
            {:else}→ {s.upstream}{/if}
          </span>
        </div>
      {/each}
    </div>
    {#if result.global_unsupported?.length}
      <p class="dm-sub">{m.proxy_import_global_not_imported({ list: result.global_unsupported.join(', ') })}</p>
    {/if}
    {#each result.warnings ?? [] as w}<p class="dm-sub">{w}</p>{/each}
    <p class="dm-sub">{m.proxy_import_disabled_note()}</p>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}

  {#snippet footer()}
    {#if result}
      <button type="button" class="dm-btn" onclick={() => (result = null)}>{m.common_back()}</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={busy || importable === 0} onclick={() => run(false)}>
        {m.proxy_import_button({ count: importable })}
      </button>
    {:else}
      <button type="button" class="dm-btn" onclick={() => (open = false)}>{m.common_cancel()}</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={busy || !text.trim()} onclick={() => run(true)}>{m.proxy_import_analyze()}</button>
    {/if}
  {/snippet}
</Modal>

<style>
  .imp-text { min-height: 240px; font-size: 12px; resize: vertical; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); }
  .imp-list { margin: 8px 0; }
  .bad { color: var(--bad); }
  .err { margin: 8px 0 0; color: var(--bad); font-size: var(--fs-sm); }
</style>
