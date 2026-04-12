<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('dark');

  $effect(() => {
    if (typeof document !== 'undefined') {
      document.documentElement.dataset.theme = theme;
    }
  });

  const nav = [
    { href: '/', label: 'Dashboard' },
    { href: '/stacks', label: 'Stacks' },
    { href: '/containers', label: 'Containers' },
    { href: '/images', label: 'Images' },
    { href: '/settings', label: 'Settings' }
  ];
</script>

<div class="flex h-full min-h-screen">
  <aside class="w-56 border-r border-[var(--border)] bg-[var(--panel)] p-4 hidden md:block">
    <div class="text-xl font-bold mb-6">Dockmesh</div>
    <nav class="space-y-1">
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
  </aside>

  <main class="flex-1 p-6">
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
