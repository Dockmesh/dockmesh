<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { Plus, X } from 'lucide-svelte';
  import type { HeaderRow } from './_options';

  // Set-header rows. `chip` names rows to mark, e.g. headers that
  // replace one of the site's.
  let {
    list = $bindable(),
    label,
    secrets = false,
    readonly = false,
    chip
  }: {
    list: HeaderRow[];
    label: string;
    secrets?: boolean;
    readonly?: boolean;
    chip?: (name: string) => string;
  } = $props();

  // Keeps password managers from storing these as a Dockmesh login.
  const NO_MANAGER = { 'data-1p-ignore': '', 'data-lpignore': 'true', 'data-bwignore': '' };
</script>

<div class="rows">
  {#each list as h, i (i)}
    <div class="hrow" class:nosecret={!secrets}>
      <input class="dm-input dm-mono" bind:value={h.name} placeholder={m.proxy_oe_header_name_ph()} aria-label={m.proxy_oe_row_name({ label })} disabled={readonly} autocomplete="off" />
      {#if h.secret}
        <input class="dm-input dm-mono" type="password" bind:value={h.value} aria-label={m.proxy_oe_row_value({ label })}
          placeholder={h.valueSet ? m.proxy_oe_secret_hint() : m.proxy_oe_value_ph()} disabled={readonly} autocomplete="new-password" {...NO_MANAGER} />
      {:else}
        <input class="dm-input dm-mono" bind:value={h.value} placeholder={m.proxy_oe_value_ph()} aria-label={m.proxy_oe_row_value({ label })} disabled={readonly} autocomplete="off" />
      {/if}
      {#if secrets}<label class="chk" title={h.secret && h.valueSet && !h.value ? m.proxy_oe_secret_reenter() : m.proxy_oe_secret_writeonly()}>
        <input type="checkbox" bind:checked={h.secret} disabled={readonly || (h.secret && h.valueSet && !h.value)} /> {m.proxy_oe_secret()}
      </label>{/if}
      {#if !readonly}
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-label={m.proxy_oe_remove_header()} onclick={() => list.splice(i, 1)}><X size={13} /></button>
      {:else}<span></span>{/if}
      {#if chip?.(h.name)}<span class="dm-badge dm-badge-info tag">{chip(h.name)}</span>{/if}
    </div>
  {/each}
  {#if !readonly}
    <button type="button" class="dm-btn dm-btn-sm add" onclick={() => list.push({ name: '', value: '', secret: false, valueSet: false })}><Plus size={13} /> {m.proxy_oe_add_header()}</button>
  {:else if !list.length}<span class="dm-dim">{m.common_none()}</span>{/if}
</div>

<style>
  .rows { display: flex; flex-direction: column; gap: 6px; }
  .hrow { display: grid; grid-template-columns: minmax(0, 220px) minmax(0, 1fr) auto auto; gap: 6px; align-items: center; }
  .hrow.nosecret { grid-template-columns: minmax(0, 220px) minmax(0, 1fr) auto; }
  .tag { grid-column: 1 / -1; justify-self: start; }
  .chk { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-xs); color: var(--muted); white-space: nowrap; }
  .add { align-self: flex-start; }
</style>
