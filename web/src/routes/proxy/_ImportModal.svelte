<script lang="ts">
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
      toast.success(`${created} site${created === 1 ? '' : 's'} imported`, 'They are disabled until you turn them on.');
      open = false;
      onimported?.();
    } catch (e) {
      error = e instanceof ApiError ? e.message : 'Import failed';
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title="Import Caddyfile" maxWidth="max-w-2xl">
  {#if !result}
    <div class="dm-field">
      <label for="imp-text">Paste your Caddyfile</label>
      <textarea id="imp-text" class="dm-input dm-mono imp-text" bind:value={text} spellcheck="false"
        placeholder={'jellyfin.example.org {\n\treverse_proxy 192.168.10.40:8096\n}'}></textarea>
      <span class="hint">Nothing changes until you import; file imports and environment placeholders are refused.</span>
    </div>
  {:else}
    <p class="dm-sub">{result.sites.length} site{result.sites.length === 1 ? '' : 's'} found · {importable} can be imported</p>
    <div class="dm-tbl imp-list" role="table" aria-label="Sites in the Caddyfile">
      {#each result.sites as s (s.host)}
        <div class="dm-tr" role="row" style="grid-template-columns: 90px minmax(160px, 1fr) minmax(0, 1.4fr)">
          <span role="cell"><StatusChip tone={s.status === 'ready' ? 'ok' : s.status === 'partial' ? 'warn' : 'bad'} label={s.status === 'skipped' ? 'skipped' : s.status} /></span>
          <span role="cell" class="dm-mono" title={s.host}>{s.host}</span>
          <span role="cell" class="dm-dim" title={s.error ?? (s.unsupported?.length ? `Not imported: ${s.unsupported.join(', ')}` : s.upstream)}>
            {#if s.error}<span class="bad">{s.error}</span>
            {:else if s.unsupported?.length}→ {s.upstream} · not imported: {s.unsupported.join(', ')}
            {:else}→ {s.upstream}{/if}
          </span>
        </div>
      {/each}
    </div>
    {#if result.global_unsupported?.length}
      <p class="dm-sub">Global options not imported: {result.global_unsupported.join(', ')}</p>
    {/if}
    {#each result.warnings ?? [] as w}<p class="dm-sub">{w}</p>{/each}
    <p class="dm-sub">Imported sites start disabled so you can check them before they go live.</p>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}

  {#snippet footer()}
    {#if result}
      <button type="button" class="dm-btn" onclick={() => (result = null)}>Back</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={busy || importable === 0} onclick={() => run(false)}>
        Import {importable} site{importable === 1 ? '' : 's'}
      </button>
    {:else}
      <button type="button" class="dm-btn" onclick={() => (open = false)}>Cancel</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={busy || !text.trim()} onclick={() => run(true)}>Analyze</button>
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
