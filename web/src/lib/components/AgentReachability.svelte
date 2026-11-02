<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
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
      error = err instanceof ApiError ? err.message : m.ui_agentcheck_failed();
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
      ? m.ui_agentcheck_env_source()
      : null
  );
</script>

<div class="ar">
  <div class="ar-head">
    <span class="ar-title">{m.ui_agentcheck_title()}</span>
    {#if result}
      <code class="dm-mono ar-url">{result.agent_url}</code>
    {/if}
    <span class="dm-sp"></span>
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={run} disabled={loading}>
      <RefreshCw size={11} strokeWidth={1.5} />
      {loading ? m.ui_agentcheck_checking() : result ? m.ui_agentcheck_again() : m.ui_agentcheck_run()}
    </button>
  </div>

  {#if error}
    <p class="ar-line fail"><X size={12} strokeWidth={1.75} /> {error}</p>
  {:else if result}
    {#if result.ok && problems.length === 0}
      <p class="ar-line ok">
        <Check size={12} strokeWidth={1.75} />
        {m.ui_agentcheck_ok()}
      </p>
    {/if}
    {#each problems as c (c.id)}
      <p class="ar-line {c.status}">
        {#if c.status === 'fail'}<X size={12} strokeWidth={1.75} />{:else}<TriangleAlert size={12} strokeWidth={1.75} />{/if}
        <span>{c.message}</span>
      </p>
    {/each}
    <details class="ar-details">
      <summary>{m.ui_agentcheck_all()}</summary>
      <ul>
        {#each result.checks ?? [] as c (c.id)}
          <li class={c.status}>
            {#if c.status === 'ok'}<Check size={11} strokeWidth={1.75} />{:else if c.status === 'fail'}<X size={11} strokeWidth={1.75} />{:else if c.status === 'warn'}<TriangleAlert size={11} strokeWidth={1.75} />{:else}<Minus size={11} strokeWidth={1.75} />{/if}
            <span>{c.message}</span>
          </li>
        {/each}
        {#if result.cert_sans?.length}
          <li class="skip"><Minus size={11} strokeWidth={1.75} /><span>{m.ui_agentcheck_cert_names()} <span class="dm-mono">{result.cert_sans.join(', ')}</span></span></li>
        {/if}
      </ul>
    </details>
    {#if !result.ok || problems.length > 0}
      <p class="ar-hint">
        {m.ui_agentcheck_hint()}
        {#if where}{where}{:else if showSettingsLink}{m.ui_agentcheck_change_before()}<a href="/settings">{m.ui_agentcheck_change_link()}</a>{m.ui_agentcheck_change_after()}{/if}
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
