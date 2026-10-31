<script lang="ts" module>
  export interface MenuItem {
    label?: string;
    href?: string;
    onclick?: () => void;
    danger?: boolean;
    disabled?: boolean;
    kbd?: string;
    hint?: string;
    separator?: boolean;
    heading?: string;
    checked?: boolean;
    icon?: any;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import { tick } from 'svelte';
  import { portal } from '$lib/a11y';
  import { Check, Ellipsis } from 'lucide-svelte';

  // Popover menu. The panel is portalled to <body> so table rows that clip
  // overflow cannot cut it off; it opens below the trigger and flips up
  // when there is no room.
  let {
    items,
    label,
    align = 'end',
    placement = 'below',
    trigger,
    triggerClass = 'dm-menu-btn',
    width = 200,
    children
  }: {
    items?: MenuItem[];
    label: string;
    align?: 'start' | 'end';
    placement?: 'below' | 'above';
    trigger?: Snippet;
    triggerClass?: string;
    width?: number;
    children?: Snippet<[() => void]>;
  } = $props();

  let open = $state(false);
  let btn: HTMLButtonElement;
  let panel = $state<HTMLDivElement | null>(null);
  let pos = $state({ top: 0, left: 0 });
  const menuId = `menu-${Math.random().toString(36).slice(2, 9)}`;

  async function show() {
    open = true;
    await tick();
    place();
    panel?.querySelector<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"]), [role="menuitemcheckbox"]')?.focus();
  }
  function hide(refocus = true) {
    open = false;
    if (refocus) btn?.focus();
  }
  function place() {
    if (!btn || !panel) return;
    const r = btn.getBoundingClientRect();
    const pw = panel.offsetWidth;
    const ph = panel.offsetHeight;
    let left = align === 'end' ? r.right - pw : r.left;
    left = Math.max(8, Math.min(left, window.innerWidth - pw - 8));
    let top = placement === 'above' ? r.top - ph - 6 : r.bottom + 6;
    if (placement === 'below' && top + ph > window.innerHeight - 8) top = r.top - ph - 6;
    if (top < 8) top = Math.min(r.bottom + 6, window.innerHeight - ph - 8);
    pos = { top, left };
  }
  function onKey(e: KeyboardEvent) {
    if (!panel) return;
    const list = [...panel.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([aria-disabled="true"])')];
    const i = list.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      hide();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      list[(i + 1) % list.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      list[(i - 1 + list.length) % list.length]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      list[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      list[list.length - 1]?.focus();
    } else if (e.key === 'Tab') {
      hide(false);
    }
  }
  function pick(it: MenuItem) {
    if (it.disabled) return;
    hide();
    it.onclick?.();
  }
  $effect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (panel?.contains(t) || btn?.contains(t)) return;
      hide(false);
    };
    const onScroll = () => place();
    window.addEventListener('mousedown', onDown);
    window.addEventListener('resize', onScroll);
    window.addEventListener('scroll', onScroll, true);
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('resize', onScroll);
      window.removeEventListener('scroll', onScroll, true);
    };
  });
</script>

<button
  bind:this={btn}
  type="button"
  class={triggerClass}
  aria-label={trigger ? undefined : label}
  title={label}
  aria-haspopup="menu"
  aria-expanded={open}
  aria-controls={open ? menuId : undefined}
  onclick={(e) => {
    e.stopPropagation();
    if (open) hide();
    else show();
  }}
  onkeydown={(e) => {
    if ((e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') && !open) {
      e.preventDefault();
      show();
    }
  }}
>
  {#if trigger}{@render trigger()}{:else}<Ellipsis size={16} strokeWidth={2} />{/if}
</button>

{#if open}
  <div
    use:portal
    bind:this={panel}
    id={menuId}
    class="pop"
    role="menu"
    aria-label={label}
    tabindex="-1"
    style="top: {pos.top}px; left: {pos.left}px; min-width: {width}px"
    onkeydown={onKey}
  >
    {#if children}
      {@render children(() => hide())}
    {/if}
    {#each items ?? [] as it, i (i)}
      {#if it.separator}
        <hr />
      {:else if it.heading}
        <div class="ph">{it.heading}</div>
      {:else if it.href && !it.disabled}
        <a href={it.href} role="menuitem" class:danger={it.danger} onclick={() => hide(false)} tabindex="-1">
          {#if it.icon}{@const I = it.icon}<I size={14} strokeWidth={1.8} />{/if}
          <span class="l">{it.label}</span>{#if it.kbd}<span class="k">{it.kbd}</span>{/if}
        </a>
      {:else}
        <button
          type="button"
          role={it.checked !== undefined ? 'menuitemcheckbox' : 'menuitem'}
          aria-checked={it.checked}
          aria-disabled={it.disabled ? 'true' : undefined}
          class:danger={it.danger}
          title={it.hint}
          tabindex="-1"
          onclick={() => pick(it)}
        >
          {#if it.checked !== undefined}
            <span class="ck">{#if it.checked}<Check size={14} strokeWidth={2} />{/if}</span>
          {:else if it.icon}{@const I = it.icon}<I size={14} strokeWidth={1.8} />{/if}
          <span class="l">{it.label}</span>{#if it.kbd}<span class="k">{it.kbd}</span>{/if}
        </button>
      {/if}
    {/each}
  </div>
{/if}

<style>
  .pop {
    position: fixed;
    z-index: 150;
    background: var(--menu);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    box-shadow: var(--pop);
    padding: 5px;
    max-height: calc(100vh - 16px);
    overflow: auto;
    outline: none;
  }
  .pop :global(button[role^='menuitem']),
  .pop :global(a[role='menuitem']) {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    border: 0;
    background: transparent;
    font: inherit;
    font-size: 13px;
    text-align: left;
    padding: 0 10px;
    min-height: 32px;
    border-radius: var(--r-sm);
    cursor: pointer;
    color: var(--fg);
    text-decoration: none;
    white-space: nowrap;
  }
  .pop :global(button[role^='menuitem']:hover),
  .pop :global(a[role='menuitem']:hover),
  .pop :global(button[role^='menuitem']:focus-visible),
  .pop :global(a[role='menuitem']:focus-visible) {
    background: var(--field);
    outline: none;
  }
  .pop :global(button[aria-disabled='true']) { opacity: 0.45; cursor: not-allowed; }
  .pop :global(.danger) { color: var(--bad); }
  .l { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .k { margin-left: auto; font-family: var(--font-mono); font-size: 10.5px; color: var(--subtle); padding-left: 12px; }
  .ck { width: 14px; display: inline-flex; }
  hr { border: 0; border-top: 1px solid var(--border); margin: 4px 0; }
  .ph { font-size: var(--fs-xs); color: var(--subtle); padding: 6px 10px 2px; }
</style>
