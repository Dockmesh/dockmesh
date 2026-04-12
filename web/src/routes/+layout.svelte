<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { api } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Toaster } from '$lib/components/ui';
  import { allowed } from '$lib/rbac';
  import {
    LayoutDashboard,
    Layers,
    Box,
    Image as ImageIcon,
    Globe,
    Settings as SettingsIcon,
    Moon,
    Sun,
    LogOut,
    Menu,
    X
  } from 'lucide-svelte';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('dark');
  let mobileOpen = $state(false);

  $effect(() => {
    if (typeof document !== 'undefined') {
      document.documentElement.dataset.theme = theme;
    }
  });

  // Route guard.
  $effect(() => {
    const path = $page.url.pathname;
    if (!auth.isAuthenticated && path !== '/login') {
      goto('/login');
    } else if (auth.isAuthenticated && path === '/login') {
      goto('/');
    }
  });

  async function doLogout() {
    try {
      await api.auth.logout();
    } catch {
      /* ignore */
    }
    toast.info('Signed out');
    goto('/login');
  }

  // Nav entries — every authenticated user can see these; RBAC enforcement
  // is on the actions within each page, not on navigation itself. The
  // Images page is hidden for viewer since there's nothing they can do there.
  const nav = $derived(
    [
      { href: '/', label: 'Dashboard', icon: LayoutDashboard, show: true },
      { href: '/stacks', label: 'Stacks', icon: Layers, show: true },
      { href: '/containers', label: 'Containers', icon: Box, show: true },
      { href: '/images', label: 'Images', icon: ImageIcon, show: allowed('image.write') || allowed('read') },
      { href: '/proxy', label: 'Proxy', icon: Globe, show: allowed('user.manage') },
      { href: '/settings', label: 'Settings', icon: SettingsIcon, show: true }
    ].filter((n) => n.show)
  );

  function isActive(href: string): boolean {
    const p = $page.url.pathname;
    if (href === '/') return p === '/';
    return p === href || p.startsWith(href + '/');
  }

  function pageTitle(): string {
    const path = $page.url.pathname;
    const match = nav.find((n) => n.href !== '/' && (path === n.href || path.startsWith(n.href + '/')));
    if (match) return match.label;
    return 'Dashboard';
  }
</script>

<Toaster />

{#if $page.url.pathname === '/login'}
  {@render children()}
{:else if auth.isAuthenticated}
  <div class="flex h-screen overflow-hidden">
    <!-- Sidebar -->
    <aside
      class="fixed md:static inset-y-0 left-0 z-40 w-60 bg-[var(--bg-elevated)] border-r border-[var(--border)] flex flex-col transform {mobileOpen
        ? 'translate-x-0'
        : '-translate-x-full'} md:translate-x-0 transition-transform duration-200"
    >
      <div class="px-5 h-16 flex items-center gap-2 border-b border-[var(--border)]">
        <div class="w-8 h-8 rounded-lg bg-gradient-to-br from-brand-400 to-brand-600 flex items-center justify-center font-bold text-white text-sm">
          D
        </div>
        <div class="font-semibold text-[var(--fg)] tracking-tight">Dockmesh</div>
      </div>

      <nav class="flex-1 px-3 py-4 space-y-0.5 overflow-y-auto">
        {#each nav as item}
          {@const Icon = item.icon}
          {@const active = isActive(item.href)}
          <a
            href={item.href}
            onclick={() => (mobileOpen = false)}
            class="flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors
                   {active
              ? 'bg-[var(--surface)] text-[var(--fg)] font-medium'
              : 'text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
          >
            <Icon class="w-4 h-4 shrink-0" />
            <span>{item.label}</span>
          </a>
        {/each}
      </nav>

      <div class="px-3 py-3 border-t border-[var(--border)]">
        <div class="flex items-center gap-2 px-2 py-2 rounded-lg">
          <div class="w-8 h-8 rounded-full bg-gradient-to-br from-brand-500 to-brand-700 flex items-center justify-center text-white text-xs font-semibold">
            {auth.user?.username?.[0]?.toUpperCase() ?? '?'}
          </div>
          <div class="flex-1 min-w-0">
            <div class="text-sm font-medium text-[var(--fg)] truncate">{auth.user?.username}</div>
            <div class="text-xs text-[var(--fg-muted)] truncate">{auth.user?.role}</div>
          </div>
          <button
            onclick={doLogout}
            class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]"
            title="Sign out"
            aria-label="Sign out"
          >
            <LogOut class="w-4 h-4" />
          </button>
        </div>
      </div>
    </aside>

    <!-- Mobile overlay -->
    {#if mobileOpen}
      <button
        class="fixed inset-0 bg-black/50 z-30 md:hidden"
        aria-label="Close sidebar"
        onclick={() => (mobileOpen = false)}
      ></button>
    {/if}

    <!-- Main -->
    <div class="flex-1 flex flex-col min-w-0">
      <header class="h-16 shrink-0 border-b border-[var(--border)] bg-[var(--bg)] flex items-center gap-3 px-5 md:px-8">
        <button
          class="md:hidden p-2 -ml-2 rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-hover)]"
          onclick={() => (mobileOpen = !mobileOpen)}
          aria-label="Toggle sidebar"
        >
          {#if mobileOpen}<X class="w-5 h-5" />{:else}<Menu class="w-5 h-5" />{/if}
        </button>
        <h1 class="text-base font-semibold text-[var(--fg)]">{pageTitle()}</h1>
        <div class="flex-1"></div>
        <button
          class="p-2 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]"
          onclick={() => (theme = theme === 'dark' ? 'light' : 'dark')}
          title="Toggle theme"
          aria-label="Toggle theme"
        >
          {#if theme === 'dark'}<Sun class="w-4 h-4" />{:else}<Moon class="w-4 h-4" />{/if}
        </button>
      </header>

      <main class="flex-1 overflow-auto px-5 md:px-8 py-6">
        <div class="max-w-7xl mx-auto dm-fade-in">
          {@render children()}
        </div>
      </main>
    </div>
  </div>
{/if}
