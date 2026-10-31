<script lang="ts" module>
  export interface TabItem {
    key: string;
    label: string;
    count?: number | string | null;
    href?: string;
    dot?: boolean;
  }
</script>

<script lang="ts">
  // Underlined tabs for 2-6 fixed views of one object. With `href` the tabs
  // are links (deep-linkable); otherwise buttons that call onchange.
  let {
    tabs,
    active,
    onchange,
    label = 'Sections'
  }: { tabs: TabItem[]; active: string; onchange?: (key: string) => void; label?: string } = $props();

  let root: HTMLElement;
  function onKey(e: KeyboardEvent) {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const items = [...root.querySelectorAll<HTMLElement>('[role="tab"]')];
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (i < 0) return;
    e.preventDefault();
    items[(i + (e.key === 'ArrowRight' ? 1 : items.length - 1)) % items.length].focus();
  }
</script>

<div class="tabs" role="tablist" aria-label={label} bind:this={root} onkeydown={onKey} tabindex="-1">
  {#each tabs as t (t.key)}
    {#if t.href}
      <a
        href={t.href}
        role="tab"
        aria-selected={active === t.key}
        tabindex={active === t.key ? 0 : -1}
        class:on={active === t.key}
        onclick={() => onchange?.(t.key)}
        data-sveltekit-noscroll
        >{t.label}{#if t.count != null && t.count !== ''}<span class="c">{t.count}</span>{/if}{#if t.dot}<span
            class="dot"
            title="Needs attention"
          ></span>{/if}</a
      >
    {:else}
      <button
        type="button"
        role="tab"
        aria-selected={active === t.key}
        tabindex={active === t.key ? 0 : -1}
        class:on={active === t.key}
        onclick={() => onchange?.(t.key)}
        >{t.label}{#if t.count != null && t.count !== ''}<span class="c">{t.count}</span>{/if}{#if t.dot}<span
            class="dot"
            title="Needs attention"
          ></span>{/if}</button
      >
    {/if}
  {/each}
</div>

<style>
  .tabs {
    display: flex;
    gap: 2px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
    outline: none;
    flex: none;
  }
  a,
  button {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 8px 12px;
    color: var(--muted);
    border: 0;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    background: none;
    font: inherit;
    text-decoration: none;
    white-space: nowrap;
    cursor: pointer;
  }
  a:hover,
  button:hover { color: var(--fg); }
  .on { color: var(--fg); border-bottom-color: var(--accent); font-weight: 600; }
  .c { color: var(--subtle); font-size: var(--fs-xs); font-weight: 400; margin-left: 2px; }
  .dot { width: 6px; height: 6px; border-radius: 3px; background: var(--warn); margin-left: 3px; }
</style>
