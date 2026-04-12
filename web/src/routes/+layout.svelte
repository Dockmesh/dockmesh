<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { api } from '$lib/api';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('dark');

  $effect(() => {
    if (typeof document !== 'undefined') {
      document.documentElement.dataset.theme = theme;
    }
  });

  // Route guard: redirect to /login if not authed (except for /login itself).
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
    } catch { /* ignore */ }
    goto('/login');
  }

  const nav = [
    { href: '/', label: 'Dashboard' },
    { href: '/stacks', label: 'Stacks' },
    { href: '/containers', label: 'Containers' },
    { href: '/images', label: 'Images' },
    { href: '/settings', label: 'Settings' }
  ];
</script>

{#if $page.url.pathname === '/login'}
  {@render children()}
{:else if auth.isAuthenticated}
  <div class="flex h-full min-h-screen">
    <aside class="w-56 border-r border-[var(--border)] bg-[var(--panel)] p-4 hidden md:flex md:flex-col">
      <div class="text-xl font-bold mb-6">Dockmesh</div>
      <nav class="space-y-1 flex-1">
        {#each nav as item}
          <a
            href={item.href}
            class="block px-3 py-2 rounded hover:bg-[var(--bg)] {$page.url.pathname === item.href
              ? 'bg-[var(--bg)] font-semibold'
              : ''}"
          >
            {item.label}
          </a>
        {/each}
      </nav>
      <div class="mt-4 pt-4 border-t border-[var(--border)] text-sm text-[var(--muted)]">
        <div class="mb-2">{auth.user?.username} ({auth.user?.role})</div>
        <button class="text-left hover:text-[var(--fg)]" onclick={doLogout}>Logout</button>
      </div>
    </aside>

    <main class="flex-1 p-6 overflow-auto">
      <header class="flex justify-between items-center mb-6">
        <h1 class="text-2xl font-semibold">Dockmesh</h1>
        <button
          class="px-3 py-1 border border-[var(--border)] rounded text-sm"
          onclick={() => (theme = theme === 'dark' ? 'light' : 'dark')}
        >
          {theme === 'dark' ? 'Light' : 'Dark'}
        </button>
      </header>
      {@render children()}
    </main>
  </div>
{/if}
