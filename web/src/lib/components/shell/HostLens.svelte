<script lang="ts">
  import { tick } from 'svelte';
  import type { HostInfo } from '$lib/api';
  import { hosts } from '$lib/stores/host.svelte';
  import { filterHosts } from '$lib/lens';
  import { Check, ChevronDown, Search, Server } from 'lucide-svelte';

  // Top-bar host filter: every page shows the selected host, or all of
  // them. Searchable by name and tag, driven by the keyboard.
  let { meta }: { meta: (h: HostInfo) => string } = $props();

  let open = $state(false);
  let query = $state('');
  let active = $state(0);
  let root = $state<HTMLElement | null>(null);
  let trigger = $state<HTMLButtonElement | null>(null);
  let input = $state<HTMLInputElement | null>(null);
  const listId = `lens-${Math.random().toString(36).slice(2, 8)}`;

  const current = $derived(hosts.selected ?? hosts.available[0]);
  const all = $derived(hosts.withAll.find((h) => h.kind === 'all'));
  const real = $derived(hosts.withAll.filter((h) => h.kind !== 'all'));
  const shown = $derived(filterHosts(real, query));
  const rows = $derived<HostInfo[]>(all && !query.trim() ? [all, ...shown] : shown);
  const warn = $derived(current?.kind === 'agent' && current.status !== 'online');
  const showSearch = $derived(real.length > 3 || query !== '');

  async function show() {
    open = true;
    query = '';
    active = Math.max(0, rows.findIndex((h) => h.id === hosts.id));
    await tick();
    if (input) input.focus();
    else root?.querySelector<HTMLElement>('[role="option"][aria-selected="true"]')?.focus();
  }
  function hide(refocus = false) {
    open = false;
    if (refocus) trigger?.focus();
  }
  function pick(h: HostInfo) {
    if (h.status !== 'online' && h.kind !== 'all' && h.id !== 'local') return;
    hosts.set(h.id);
    hide(true);
  }

  $effect(() => {
    void query;
    active = 0;
  });

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      hide(true);
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!rows.length) return;
      active = (active + (e.key === 'ArrowDown' ? 1 : rows.length - 1)) % rows.length;
      if (!input) root?.querySelectorAll<HTMLElement>('[role="option"]')[active]?.focus();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const h = rows[active];
      if (h) pick(h);
    }
  }

  $effect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (root && !root.contains(e.target as Node)) hide();
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  });

  const usable = (h: HostInfo) => h.kind === 'all' || h.id === 'local' || h.status === 'online';
  const tone = (h: HostInfo) => (h.kind === 'all' ? 'acc' : h.status === 'online' ? 'ok' : h.status === 'pending' ? 'warn' : 'bad');
</script>

{#if current}
  <div class="lens" bind:this={root}>
    <button
      type="button"
      class="tb-btn"
      bind:this={trigger}
      onclick={() => (open ? hide() : show())}
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-label="Host filter: {current.name}"
      title="Filter every page by host"
    >
      <span class="dm-dot {current.kind === 'all' ? 'acc' : warn ? 'bad' : 'ok'}"></span>
      <span class="name">{current.name}</span>
      <ChevronDown size={13} />
    </button>

    {#if open}
      <div class="pop" role="presentation" onkeydown={onKey}>
        <div class="h">Show pages for</div>
        {#if showSearch}
          <div class="search">
            <Search size={13} />
            <input
              bind:this={input}
              bind:value={query}
              placeholder="Search hosts or #tag"
              aria-label="Search hosts"
              aria-controls={listId}
              aria-activedescendant={rows[active] ? `${listId}-${rows[active].id}` : undefined}
            />
          </div>
        {/if}
        <ul id={listId} role="listbox" aria-label="Hosts">
          {#each rows as h, i (h.id)}
            {@const tags = h.tags ?? []}
            <li
              id="{listId}-{h.id}"
              role="option"
              aria-selected={h.id === hosts.id}
              aria-disabled={!usable(h)}
              tabindex={input ? -1 : i === active ? 0 : -1}
              class:act={i === active}
              class:off={!usable(h)}
              class:sep={h.kind === 'all'}
              title={usable(h) ? '' : `${h.name} is ${h.status}`}
              onclick={() => pick(h)}
              onkeydown={() => {}}
              onmouseenter={() => (active = i)}
            >
              <span class="ck">{#if h.id === hosts.id}<Check size={13} />{/if}</span>
              <span class="dm-dot {tone(h)}"></span>
              <span class="nm">{h.name}</span>
              {#if tags.length}
                <span class="tags" title={tags.join(', ')}>
                  {#each tags.slice(0, 2) as t (t)}<button type="button" class="tag" tabindex="-1" onclick={(e) => { e.stopPropagation(); query = `#${t}`; input?.focus(); }}>{t}</button>{/each}
                  {#if tags.length > 2}<span class="more">+{tags.length - 2}</span>{/if}
                </span>
              {/if}
              <span class="meta" title={meta(h)}>{meta(h)}</span>
            </li>
          {:else}
            <li class="none" role="presentation">No host matches “{query}”.</li>
          {/each}
        </ul>
        <a class="foot" href="/hosts" onclick={() => hide()}><Server size={13} /> Manage hosts</a>
      </div>
    {/if}
  </div>
{/if}

<style>
  .lens { position: relative; }
  .name { max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pop {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    width: 380px;
    background: var(--menu);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    box-shadow: var(--pop);
    z-index: 60;
    padding: 4px;
  }
  .h { padding: 8px 10px 6px; font-size: var(--fs-xs); color: var(--subtle); }
  .search { display: flex; align-items: center; gap: 8px; margin: 0 4px 4px; padding: 0 8px; border: 1px solid var(--border); border-radius: var(--r-sm); background: var(--surface); color: var(--subtle); }
  .search:focus-within { border-color: var(--accent); }
  .search input { flex: 1; border: 0; background: none; color: var(--fg); font: inherit; font-size: var(--fs-sm); height: 30px; outline: none; }
  ul { list-style: none; margin: 0; padding: 0; max-height: min(420px, 60vh); overflow: auto; }
  li[role='option'] {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 32px;
    padding: 0 8px;
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: var(--fs-sm);
    outline: none;
  }
  li.act { background: var(--field); }
  li[role='option']:focus-visible { box-shadow: inset 0 0 0 2px var(--accent); }
  li.off { color: var(--subtle); cursor: not-allowed; }
  li.sep { margin-bottom: 4px; border-bottom: 1px solid var(--border); border-radius: var(--r-sm) var(--r-sm) 0 0; }
  .ck { width: 14px; display: inline-flex; color: var(--accent); flex: none; }
  .nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
  .tags { display: inline-flex; gap: 4px; flex: none; }
  .tag { border: 0; border-radius: 9px; padding: 0 7px; line-height: 18px; font: inherit; font-size: 11.5px; background: color-mix(in srgb, var(--fg) 9%, transparent); color: var(--muted); cursor: pointer; }
  .tag:hover { color: var(--fg); }
  .more { font-size: 11.5px; color: var(--subtle); }
  .meta { margin-left: auto; padding-left: 8px; color: var(--subtle); font-size: var(--fs-xs); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 150px; flex: none; font-variant-numeric: tabular-nums; }
  .none { padding: 14px 10px; color: var(--subtle); font-size: var(--fs-sm); }
  .foot { display: flex; align-items: center; gap: 8px; margin-top: 4px; padding: 8px 10px; border-top: 1px solid var(--border); color: var(--muted); font-size: var(--fs-sm); text-decoration: none; }
  .foot:hover { color: var(--fg); }
</style>
