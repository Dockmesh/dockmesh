<script lang="ts" module>
  export interface Badge {
    text: string;
    tone?: '' | 'warn' | 'bad';
  }
</script>

<script lang="ts">
  import { page } from '$app/state';
  import { auth } from '$lib/stores/auth.svelte';
  import { NAV, activeItem } from './nav';
  import Menu from '../ui/Menu.svelte';
  import HealthDot from '../ui/HealthDot.svelte';
  import { ChevronLeft, ChevronRight, Search, KeyRound, LogOut, Sun, Moon, CircleUser } from 'lucide-svelte';

  let {
    collapsed = $bindable(false),
    badges = {},
    theme,
    ontheme,
    onpalette,
    onsignout,
    onnavigate
  }: {
    collapsed?: boolean;
    badges?: Record<string, Badge>;
    theme: 'light' | 'dark';
    ontheme: () => void;
    onpalette: () => void;
    onsignout: () => void;
    onnavigate?: () => void;
  } = $props();

  const groups = $derived(
    NAV.map((g) => ({ ...g, items: g.items.filter((i) => i.show()) })).filter((g) => g.items.length > 0)
  );
  const current = $derived(activeItem(page.url.pathname)?.item.key ?? null);
  const initial = $derived(auth.user?.username?.[0]?.toUpperCase() ?? '?');
  const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);
</script>

<nav class="app-side" class:collapsed aria-label="Main navigation">
  <div class="brand">
    <a href="/" class="logo" aria-label="Dockmesh overview" onclick={onnavigate}>
      <img src="/logo-mark.svg" alt="" width="24" height="24" />
      {#if !collapsed}<b>dock<span>mesh</span></b>{/if}
    </a>
    <button
      type="button"
      class="collapse-btn"
      onclick={() => (collapsed = !collapsed)}
      title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      aria-expanded={!collapsed}
    >
      {#if collapsed}<ChevronRight size={14} />{:else}<ChevronLeft size={14} />{/if}
    </button>
  </div>

  <button type="button" class="search" onclick={onpalette} title="Search ({isMac ? '⌘' : 'Ctrl'}+K)" aria-label="Search everything">
    <Search size={14} />
    {#if !collapsed}<span>Search everything…</span><kbd>{isMac ? '⌘K' : 'Ctrl K'}</kbd>{/if}
  </button>

  <div class="scroll">
    {#each groups as g (g.title ?? '_')}
      {#if g.title}
        <div class="sec">{collapsed ? '' : g.title}</div>
      {/if}
      {#each g.items as it (it.key)}
        {@const Icon = it.icon}
        {@const b = badges[it.key]}
        <a
          href={it.href}
          class="item"
          class:on={current === it.key}
          aria-current={current === it.key ? 'page' : undefined}
          title={collapsed ? (b ? `${it.label} · ${b.text}` : it.label) : undefined}
          onclick={onnavigate}
        >
          <Icon size={16} strokeWidth={1.8} />
          {#if !collapsed}
            <span class="lbl">{it.label}</span>
            {#if b}<span class="badge {b.tone ?? ''}">{b.text}</span>{/if}
          {:else if b?.tone}
            <span class="dotbadge {b.tone}"></span>
          {/if}
        </a>
      {/each}
    {/each}
  </div>

  <div class="user">
    <Menu
      label="User menu"
      align="start"
      placement="above"
      width={220}
      triggerClass="user-btn"
      items={[
        { heading: `${auth.user?.username ?? ''} · ${auth.user?.role ?? ''}` },
        { label: 'My account', href: '/account', icon: CircleUser },
        { label: 'API tokens', href: '/tokens', icon: KeyRound },
        { label: theme === 'dark' ? 'Light mode' : 'Dark mode', icon: theme === 'dark' ? Sun : Moon, onclick: ontheme },
        { separator: true },
        { label: 'Sign out', icon: LogOut, onclick: onsignout }
      ]}
    >
      {#snippet trigger()}
        <span class="avatar">{initial}</span>
        {#if !collapsed}
          <span class="who">
            <span class="n">{auth.user?.username ?? '—'}</span>
            <span class="r">{auth.user?.role ?? ''}</span>
          </span>
        {/if}
      {/snippet}
    </Menu>
    {#if !collapsed}<HealthDot />{/if}
  </div>
</nav>

<style>
  .app-side {
    flex: 0 0 232px;
    width: 232px;
    background: var(--side);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    padding: 12px 10px 10px;
    position: sticky;
    top: 0;
    height: 100vh;
    transition: width 0.15s, flex-basis 0.15s;
    z-index: 30;
  }
  .app-side.collapsed { flex-basis: 60px; width: 60px; padding: 12px 8px 10px; }
  .brand { display: flex; align-items: center; gap: 8px; padding: 2px 6px 12px; }
  .collapsed .brand { flex-direction: column; padding: 2px 0 10px; }
  .logo { display: flex; align-items: center; gap: 10px; color: var(--fg); text-decoration: none; min-width: 0; }
  .logo b { font-size: 15px; letter-spacing: -0.01em; }
  .logo b span { color: var(--accent); }
  .collapse-btn {
    margin-left: auto;
    width: 26px;
    height: 26px;
    border-radius: var(--r-sm);
    border: 1px solid transparent;
    background: none;
    color: var(--subtle);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .collapsed .collapse-btn { margin: 6px 0 0; }
  .collapse-btn:hover { border-color: var(--border); color: var(--fg); }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 6px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--subtle);
    padding: 0 10px;
    min-height: var(--ctl-h);
    border-radius: var(--r);
    cursor: pointer;
    text-align: left;
    width: 100%;
    font: inherit;
  }
  .search:hover { border-color: var(--accent); }
  .search span { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .collapsed .search { justify-content: center; padding: 0; }
  .scroll { flex: 1; overflow-y: auto; overflow-x: hidden; margin: 0 -4px; padding: 0 4px; }
  .sec { font-size: 11px; font-weight: 600; color: var(--subtle); padding: 12px 10px 4px; white-space: nowrap; }
  .collapsed .sec { padding: 0; height: 1px; background: var(--border); margin: 8px 4px; }
  .item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 10px;
    min-height: 32px;
    border-radius: var(--r);
    color: var(--muted);
    text-decoration: none;
    white-space: nowrap;
    position: relative;
  }
  .item:hover { background: var(--nav-active); color: var(--fg); }
  .item.on { background: var(--nav-active); color: var(--fg); font-weight: 600; }
  .collapsed .item { justify-content: center; padding: 0; }
  .lbl { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .badge { font-size: var(--fs-xs); color: var(--subtle); font-variant-numeric: tabular-nums; }
  .badge.warn { color: var(--warn); }
  .badge.bad { color: var(--bad); }
  .dotbadge { position: absolute; top: 5px; right: 8px; width: 7px; height: 7px; border-radius: 4px; background: var(--warn); }
  .dotbadge.bad { background: var(--bad); }
  .user {
    display: flex;
    align-items: center;
    gap: 6px;
    padding-top: 10px;
    border-top: 1px solid var(--border);
    min-width: 0;
  }
  .collapsed .user { justify-content: center; }
  .user :global(.user-btn) {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 6px;
    border: 0;
    border-radius: var(--r);
    background: none;
    color: var(--fg);
    cursor: pointer;
    font: inherit;
    text-align: left;
  }
  .collapsed .user :global(.user-btn) { flex: none; justify-content: center; padding: 4px; }
  .user :global(.user-btn:hover) { background: var(--nav-active); }
  .avatar {
    width: 26px;
    height: 26px;
    border-radius: 13px;
    background: var(--field);
    border: 1px solid var(--border);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    font-weight: 600;
    flex: 0 0 26px;
  }
  .who { display: flex; flex-direction: column; min-width: 0; line-height: 1.25; }
  .who .n { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .who .r { font-size: 11px; color: var(--subtle); }
</style>
