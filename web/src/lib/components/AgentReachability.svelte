<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError, type AgentReachability } from '$lib/api';
  import { Check, X, TriangleAlert, Minus, RefreshCw } from 'lucide-svelte';

  // Runs the server-side agent URL check and lists what an agent would hit.
  interface Props {
    auto?: boolean;
    showSettingsLink?: boolean;
  }
  let { auto = true, showSettingsLink = true }: Props = $props();

  let result = $state<AgentReachability | null>(null);
  let loading = $state(false);
  let error = $state('');

  async function run() {
    loading = true;
    error = '';
    try {
      result = await api.agents.reachability();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Check failed';
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    if (auto) run();
  });

  const problems = $derived((result?.checks ?? []).filter((c) => c.status === 'fail' || c.status === 'warn'));
  const where = $derived(
    result?.agent_source === 'env'
      ? 'DOCKMESH_AGENT_PUBLIC_URL in dockmesh.env sets it (restart after changing).'
      : null
  );
</script>

<div class="ar">
  <div class="ar-head">
    <span class="ar-title">Agent connection</span>
    {#if result}
      <code class="dm-mono ar-url">{result.agent_url}</code>
    {/if}
    <span class="dm-sp"></span>
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={run} disabled={loading}>
      <RefreshCw size={11} strokeWidth={1.5} />
      {loading ? 'Checking…' : result ? 'Check again' : 'Check connection'}
    </button>
  </div>

  {#if error}
    <p class="ar-line fail"><X size={12} strokeWidth={1.75} /> {error}</p>
  {:else if result}
    {#if result.ok && problems.length === 0}
      <p class="ar-line ok">
        <Check size={12} strokeWidth={1.75} />
        Reachable from this server: name, port and the agent listener certificate check out.
      </p>
    {/if}
    {#each problems as c (c.id)}
      <p class="ar-line {c.status}">
        {#if c.status === 'fail'}<X size={12} strokeWidth={1.75} />{:else}<TriangleAlert size={12} strokeWidth={1.75} />{/if}
        <span>{c.message}</span>
      </p>
    {/each}
    <details class="ar-details">
      <summary>All checks</summary>
      <ul>
        {#each result.checks ?? [] as c (c.id)}
          <li class={c.status}>
            {#if c.status === 'ok'}<Check size={11} strokeWidth={1.75} />{:else if c.status === 'fail'}<X size={11} strokeWidth={1.75} />{:else if c.status === 'warn'}<TriangleAlert size={11} strokeWidth={1.75} />{:else}<Minus size={11} strokeWidth={1.75} />{/if}
            <span>{c.message}</span>
          </li>
        {/each}
        {#if result.cert_sans?.length}
          <li class="skip"><Minus size={11} strokeWidth={1.75} /><span>Certificate names: <span class="dm-mono">{result.cert_sans.join(', ')}</span></span></li>
        {/if}
      </ul>
    </details>
    {#if !result.ok || problems.length > 0}
      <p class="ar-hint">
        Checked from this server. Remote hosts must reach the same address on the same port, without a proxy that
        terminates TLS in between.
        {#if where}{where}{:else if showSettingsLink}Change the address under <a href="/settings">Settings → General</a>.{/if}
      </p>
    {/if}
  {/if}
</div>

<style>
  .ar {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .ar-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .ar-title { font-size: 12px; font-weight: 600; color: var(--fg); white-space: nowrap; }
  .ar-url { font-size: 11.5px; color: var(--fg-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ar-line { display: flex; gap: 6px; align-items: flex-start; margin: 0; font-size: 12px; line-height: 1.5; color: var(--fg); }
  .ar-line :global(svg) { flex-shrink: 0; margin-top: 3px; }
  .ar-line.ok :global(svg), .ar-details li.ok :global(svg) { color: var(--color-success-400); }
  .ar-line.fail :global(svg), .ar-details li.fail :global(svg) { color: var(--color-danger-400); }
  .ar-line.warn :global(svg), .ar-details li.warn :global(svg) { color: var(--color-warning-400); }
  .ar-details { font-size: 11.5px; color: var(--fg-muted); }
  .ar-details summary { cursor: pointer; color: var(--fg-subtle); }
  .ar-details ul { list-style: none; margin: 6px 0 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  .ar-details li { display: flex; gap: 6px; align-items: flex-start; line-height: 1.5; }
  .ar-details li :global(svg) { flex-shrink: 0; margin-top: 3px; color: var(--fg-subtle); }
  .ar-hint { margin: 0; font-size: 11.5px; color: var(--fg-subtle); line-height: 1.55; }
  .ar-hint a { color: var(--accent-fg); }
</style>
