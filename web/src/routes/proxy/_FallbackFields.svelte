<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { TriangleAlert } from 'lucide-svelte';
  import type { FallbackForm } from './_options';

  // What a site does with requests no path route takes. Edited on the
  // Options tab and on the Paths tab's fallback row.
  let {
    fb = $bindable(),
    idp,
    errors = {},
    warning = '',
    readonly = false
  }: {
    fb: FallbackForm;
    idp: string;
    errors?: Record<string, string>;
    warning?: string;
    readonly?: boolean;
  } = $props();

  const F = 'routing.fallback';
</script>

{#snippet err(f: string)}
  {#if errors[`${F}.${f}`]}<span class="ferr" role="alert">{errors[`${F}.${f}`]}</span>{/if}
{/snippet}

<fieldset class="radios">
  <legend>{m.proxy_fb_label()}</legend>
  <label><input type="radio" bind:group={fb.mode} value="proxy" disabled={readonly} /> {m.proxy_fb_proxy()}</label>
  <label><input type="radio" bind:group={fb.mode} value="redirect" disabled={readonly} /> {m.proxy_fb_redirect()}</label>
  <label><input type="radio" bind:group={fb.mode} value="404" disabled={readonly} /> {m.proxy_fb_404()}</label>
  {@render err('mode')}
</fieldset>
{#if fb.mode === 'redirect'}
  <div class="redir">
    <div class="dm-field">
      <label for="{idp}-to">{m.proxy_redir_to()}</label>
      <input id="{idp}-to" class="dm-input dm-mono" bind:value={fb.to} disabled={readonly} placeholder={m.proxy_redir_to_ph()} autocomplete="off" />
      {@render err('to')}
    </div>
    <div class="dm-field">
      <label for="{idp}-code">{m.proxy_redir_code()}</label>
      <select id="{idp}-code" class="dm-input" bind:value={fb.code} disabled={readonly}>
        <option value={301}>{m.proxy_redir_301()}</option>
        <option value={302}>{m.proxy_redir_302()}</option>
        <option value={307}>{m.proxy_redir_307()}</option>
        <option value={308}>{m.proxy_redir_308()}</option>
      </select>
      {@render err('code')}
    </div>
  </div>
  {#if warning}<span class="risk"><TriangleAlert size={12} strokeWidth={1.8} /> {warning}</span>{/if}
{/if}
<span class="hint">{m.proxy_fb_hint()}</span>

<style>
  .radios { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 4px; }
  .radios legend { font-size: 12px; color: var(--muted); font-weight: 500; margin-bottom: 4px; }
  .radios label { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); }
  .redir { display: grid; grid-template-columns: minmax(0, 1fr) 220px; gap: 10px; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); }
  .risk { display: inline-flex; align-items: flex-start; gap: 4px; font-size: var(--fs-xs); color: var(--warn); }
  .risk :global(svg) { flex: none; margin-top: 2px; }
  .ferr { color: var(--bad); font-size: var(--fs-xs); }
</style>
