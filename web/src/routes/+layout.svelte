<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { api } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Toaster, ConfirmDialog, UpdateBanner } from '$lib/components/ui';
  import { allowed } from '$lib/rbac';
  import {
    LayoutDashboard,
    Layers,
    Box,
    Image as ImageIcon,
    Globe,
    Bell,
    Archive,
    Network as NetworkIcon,
    Server,
    Settings as SettingsIcon,
    Moon,
    Sun,
    LogOut,
    Menu,
    X,
    HardDrive,
    ChevronDown,
    ChevronsLeft,
    ChevronsRight,
    ArrowRightLeft,
    Package,
    Activity,
    Users as UsersIcon,
    ShieldCheck as ShieldCheckIcon,
    KeyRound
  } from 'lucide-svelte';
  import { HealthDot } from '$lib/components/ui';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('dark');
  let mobileOpen = $state(false);
  let hostMenuOpen = $state(false);

  // Desktop sidebar collapse — persisted in localStorage so the choice
  // survives reloads. Mobile always uses the off-canvas full-width
  // panel regardless of this flag.
  let sidebarCollapsed = $state<boolean>(
    typeof localStorage !== 'undefined' && localStorage.getItem('dm_sidebar_collapsed') === '1'
  );
  $effect(() => {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('dm_sidebar_collapsed', sidebarCollapsed ? '1' : '0');
    }
    // A collapsed sidebar can't host the host-switcher dropdown — force
    // it closed so we don't leak an orphaned popover.
    if (sidebarCollapsed) hostMenuOpen = false;
  });

  // Refresh the available host list whenever auth flips on, and poll
  // every 10s so newly-connected agents show up in the switcher without
  // a page reload.
  let hostPollTimer: ReturnType<typeof setInterval> | null = null;
  async function refreshHosts() {
    if (!auth.isAuthenticated) return;
    try {
      const list = await api.hosts.list();
      hosts.setAvailable(list);
    } catch {
      /* ignore — we'll retry on the next tick */
    }
  }
  $effect(() => {
    if (auth.isAuthenticated) {
      refreshHosts();
      if (!hostPollTimer) {
        hostPollTimer = setInterval(refreshHosts, 10000);
      }
    } else if (hostPollTimer) {
      clearInterval(hostPollTimer);
      hostPollTimer = null;
    }
  });

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

  // Nav structure:
  //  - Untitled top group = daily-use items (Dashboard, Stacks, Containers,
  //    Images, Agents). This is the "most-clicked" cluster; keeping it
  //    ungrouped matches modern patterns (Supabase, Vercel, Notion).
  //  - NETWORK = the networking layer (Networks, Proxy).
  //  - AUTOMATION = things that run on a schedule / react to events.
  //  - Settings is rendered separately above the user card at the bottom,
  //    not inside `sections`, so it sits apart from day-to-day nav — the
  //    standard "tool config lives next to user identity" pattern used by
  //    Notion, Slack, Figma, Linear, GitLab.
  type NavItem = { href: string; label: string; icon: any; show: boolean };
  type NavSection = { title: string | null; items: NavItem[] };

  const sections = $derived<NavSection[]>(
    (
      [
        {
          title: null,
          items: [
            { href: '/', label: 'Dashboard', icon: LayoutDashboard, show: true },
            { href: '/stacks', label: 'Stacks', icon: Layers, show: true },
            { href: '/templates', label: 'Templates', icon: Package, show: true },
            { href: '/containers', label: 'Containers', icon: Box, show: true },
            { href: '/images', label: 'Images', icon: ImageIcon, show: allowed('image.write') || allowed('read') },
            { href: '/volumes', label: 'Volumes', icon: HardDrive, show: allowed('read') },
            { href: '/agents', label: 'Agents', icon: Server, show: allowed('user.manage') },
            { href: '/migrations', label: 'Migrations', icon: ArrowRightLeft, show: allowed('stack.deploy') }
          ]
        },
        {
          title: 'Network',
          items: [
            { href: '/networks', label: 'Networks', icon: NetworkIcon, show: allowed('read') },
            { href: '/proxy', label: 'Proxy', icon: Globe, show: allowed('user.manage') }
          ]
        },
        {
          title: 'Automation',
          items: [
            { href: '/environment', label: 'Environment', icon: Box, show: allowed('user.manage') },
            { href: '/alerts', label: 'Alerts', icon: Bell, show: allowed('user.manage') },
            { href: '/backups', label: 'Backups', icon: Archive, show: allowed('user.manage') }
          ]
        },
        // Platform-admin group — promoted out of Settings tabs to
        // first-class sidebar entries. The old 8-tab Settings page
        // buried compliance + user-management below fold; modern peers
        // (Portainer Business, Rancher, Coolify) all expose these as
        // top-level nav. RBAC-gated: non-admins don't see this group
        // at all, so operator sidebars stay lean.
        {
          title: 'Platform',
          items: [
            { href: '/users', label: 'Users & Roles', icon: UsersIcon, show: allowed('user.manage') },
            { href: '/authentication', label: 'Authentication', icon: ShieldCheckIcon, show: allowed('user.manage') },
            { href: '/registries', label: 'Registries', icon: KeyRound, show: allowed('user.manage') },
            { href: '/audit', label: 'Audit Log', icon: Activity, show: allowed('audit.read') || allowed('user.manage') }
          ]
        }
      ] as NavSection[]
    )
      .map((s) => ({ ...s, items: s.items.filter((i) => i.show) }))
      .filter((s) => s.items.length > 0)
  );

  function isActive(href: string): boolean {
    const p = $page.url.pathname;
    if (href === '/') return p === '/';
    return p === href || p.startsWith(href + '/');
  }
</script>

<Toaster />
<ConfirmDialog />

{#if $page.url.pathname === '/login'}
  {@render children()}
{:else if auth.isAuthenticated}
  <div class="flex h-screen overflow-hidden">
    <!-- Sidebar -->
    <aside
      class="fixed md:static inset-y-0 left-0 z-40 {sidebarCollapsed ? 'md:w-16' : 'md:w-64'} w-64 bg-[var(--bg)] border-r border-[var(--border)] flex flex-col transform {mobileOpen
        ? 'translate-x-0'
        : '-translate-x-full'} md:translate-x-0 transition-[width,transform] duration-200 relative"
    >
      <!-- Collapse toggle: anchored to the aside's right edge so it
           stays at the same absolute position regardless of the
           collapsed/expanded state. Previously it jumped between the
           header (expanded) and a centred button below (collapsed),
           which read as a visual glitch. -->
      <button
        onclick={() => (sidebarCollapsed = !sidebarCollapsed)}
        class="hidden md:flex absolute top-[22px] -right-3 z-10 w-6 h-6 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--bg-elevated)] text-[var(--fg-muted)] hover:text-[var(--fg)] hover:border-[var(--color-brand-500)] shadow-sm transition-colors"
        title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      >
        {#if sidebarCollapsed}
          <ChevronsRight class="w-3.5 h-3.5" />
        {:else}
          <ChevronsLeft class="w-3.5 h-3.5" />
        {/if}
      </button>

      <div class="h-16 flex items-center border-b border-[var(--border)] {sidebarCollapsed ? 'justify-center px-2' : 'px-4'}">
        <a href="/" class="flex items-center gap-2.5 min-w-0" aria-label="dockmesh home">
          <!-- Real brand mark from /static/logo-mark.svg (same artwork
               the marketing site + favicon use). Previously the sidebar
               inlined a simplified polygon approximation that drifted
               from the actual brand — using the real SVG keeps product
               UI + marketing + docs visually in sync. -->
          <img src="/logo-mark.svg" alt="" aria-hidden="true" class="h-9 w-9 shrink-0" />
          {#if !sidebarCollapsed}
            <!-- Wordmark as HTML so the text inherits var(--fg) and
                 stays readable under both light and dark themes. -->
            <!-- Lowercase wordmark with cyan "mesh" accent — matches the
                 brand lockup on the marketing site + docs. -->
            <span class="text-[1.15rem] font-semibold tracking-tight text-[var(--fg)] select-none">dock<span class="text-[var(--color-brand-400)]">mesh</span></span>
          {/if}
        </a>
      </div>

      <!-- Host switcher — structurally the parent of every action below
           it. Placed here (not in the header) so users never lose sight
           of which host they're operating on. When more than one host
           is registered, a virtual "All hosts" entry sits at the top
           of the dropdown and fans out list pages across every online
           host simultaneously. -->
      <!-- Collapsed host indicator: icon + colored dot so admin
           keeps multi-host awareness even with the sidebar folded. -->
      {#if hosts.available.length > 0 && sidebarCollapsed}
        <div class="hidden md:flex flex-col items-center py-2 border-b border-[var(--border)]">
          <button
            class="p-1.5 rounded-md hover:bg-[var(--surface-hover)] relative"
            title="{hosts.selected?.name ?? 'Local'} ({hosts.selected?.kind ?? 'local'})"
            onclick={() => (sidebarCollapsed = false)}
          >
            {#if hosts.selected?.kind === 'all'}
              <Layers class="w-4 h-4 text-[var(--color-brand-400)]" />
            {:else if hosts.selected?.kind === 'agent'}
              <Server class="w-4 h-4 text-[var(--color-brand-400)]" />
            {:else}
              <HardDrive class="w-4 h-4 text-[var(--fg-muted)]" />
            {/if}
            <span class="absolute -bottom-0.5 -right-0.5 w-2 h-2 rounded-full {hosts.isAll ? 'bg-[var(--color-brand-500)]' : hosts.selected?.status === 'online' ? 'bg-[var(--color-success-500)]' : 'bg-[var(--fg-subtle)]'}"></span>
          </button>
        </div>
      {/if}
      {#if hosts.available.length > 0 && !sidebarCollapsed}
        <div class="px-3 pt-3 pb-2 border-b border-[var(--border)]">
          <div class="text-[10px] uppercase tracking-wider text-[var(--fg-subtle)] font-medium px-2 pb-1.5">
            Host
          </div>
          <div class="relative">
            <button
              class="w-full flex items-center gap-2 px-3 py-2 text-sm rounded-lg border border-[var(--border)] bg-[var(--surface)] hover:border-[var(--color-brand-500)] hover:bg-[var(--surface-hover)] transition-colors"
              onclick={() => (hostMenuOpen = !hostMenuOpen)}
              aria-haspopup="listbox"
              aria-expanded={hostMenuOpen}
            >
              {#if hosts.selected?.kind === 'all'}
                <Layers class="w-4 h-4 text-[var(--color-brand-400)] shrink-0" />
              {:else if hosts.selected?.kind === 'local'}
                <HardDrive class="w-4 h-4 text-[var(--color-brand-400)] shrink-0" />
              {:else}
                <Server class="w-4 h-4 text-[var(--color-brand-400)] shrink-0" />
              {/if}
              <span class="font-mono text-xs text-[var(--fg)] flex-1 text-left truncate">{hosts.selected?.name ?? 'Local'}</span>
              {#if hosts.selected?.kind === 'agent' && hosts.selected.status !== 'online'}
                <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-warning-500)] shrink-0"></span>
              {:else}
                <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)] shrink-0"></span>
              {/if}
              <ChevronDown class="w-3.5 h-3.5 text-[var(--fg-muted)] shrink-0" />
            </button>
            {#if hostMenuOpen}
              <button
                class="fixed inset-0 z-30 cursor-default"
                aria-label="Close host menu"
                onclick={() => (hostMenuOpen = false)}
              ></button>
              <div
                class="absolute left-0 right-0 top-full mt-1 z-40 bg-[var(--bg-elevated)] border border-[var(--border-strong)] rounded-lg shadow-2xl py-1"
                role="listbox"
              >
                {#each hosts.withAll as h, idx}
                  {@const online = h.status === 'online'}
                  <button
                    class="w-full text-left px-3 py-2 text-sm hover:bg-[var(--surface-hover)] flex items-center gap-2 disabled:opacity-50"
                    onclick={() => {
                      hosts.set(h.id);
                      hostMenuOpen = false;
                    }}
                    disabled={!online}
                    role="option"
                    aria-selected={h.id === hosts.id}
                  >
                    {#if h.kind === 'all'}
                      <Layers class="w-3.5 h-3.5 text-[var(--color-brand-400)] shrink-0" />
                    {:else if h.kind === 'local'}
                      <HardDrive class="w-3.5 h-3.5 text-[var(--color-brand-400)] shrink-0" />
                    {:else}
                      <Server class="w-3.5 h-3.5 text-[var(--color-brand-400)] shrink-0" />
                    {/if}
                    <span class="font-mono text-xs flex-1 truncate">{h.name}</span>
                    {#if online}
                      <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)]"></span>
                    {:else}
                      <span class="text-[10px] text-[var(--fg-subtle)]">{h.status}</span>
                    {/if}
                    {#if h.id === hosts.id}
                      <span class="text-[var(--color-brand-400)] text-xs">●</span>
                    {/if}
                  </button>
                  <!-- Separator between the virtual "All hosts" entry
                       and the real host list. Keeps the two semantically
                       distinct so users don't confuse a fan-out with a
                       specific host selection. -->
                  {#if idx === 0 && h.kind === 'all'}
                    <div class="my-1 border-t border-[var(--border)]"></div>
                  {/if}
                {/each}
              </div>
            {/if}
          </div>
        </div>
      {/if}

      <nav class="flex-1 {sidebarCollapsed ? 'px-2' : 'px-3'} py-3 overflow-y-auto">
        {#each sections as section, idx}
          {#if section.title && !sidebarCollapsed}
            <div class="px-3 {idx === 0 ? 'pt-1' : 'pt-4'} pb-1.5 text-[10px] uppercase tracking-wider text-[var(--fg-subtle)] font-medium">
              {section.title}
            </div>
          {:else if idx > 0}
            <div class="my-2 border-t border-[var(--border)]"></div>
          {/if}
          <div class="space-y-0.5">
            {#each section.items as item}
              {@const Icon = item.icon}
              {@const active = isActive(item.href)}
              <a
                href={item.href}
                onclick={() => (mobileOpen = false)}
                title={sidebarCollapsed ? item.label : undefined}
                class="relative flex items-center {sidebarCollapsed ? 'justify-center px-2' : 'gap-3 px-3'} py-2 rounded-lg text-sm transition-colors
                       {active
                  ? 'bg-[var(--accent-bg)] text-[var(--accent-fg)] font-medium'
                  : 'text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
              >
                {#if active && !sidebarCollapsed}
                  <span class="absolute left-0 top-1/2 -translate-y-1/2 w-0.5 h-5 rounded-r-full bg-[var(--accent)]"></span>
                {/if}
                <Icon class="w-4 h-4 shrink-0" />
                {#if !sidebarCollapsed}<span>{item.label}</span>{/if}
              </a>
            {/each}
          </div>
        {/each}
      </nav>

      <!-- Sidebar footer: Settings (tool config) sits directly above the
           user card (identity) — both are "meta" actions, visually anchored
           at the bottom of the sidebar. This pairing is the default
           pattern in every modern SaaS tool (Notion, Slack, Linear, …). -->
      <div class="border-t border-[var(--border)] {sidebarCollapsed ? 'px-2' : 'px-3'} pt-2 pb-2 space-y-0.5">
        <a
          href="/settings"
          onclick={() => (mobileOpen = false)}
          title={sidebarCollapsed ? 'Settings' : undefined}
          class="relative flex items-center {sidebarCollapsed ? 'justify-center px-2' : 'gap-3 px-3'} py-2 rounded-lg text-sm transition-colors
                 {isActive('/settings')
            ? 'bg-[var(--accent-bg)] text-[var(--accent-fg)] font-medium'
            : 'text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
        >
          {#if isActive('/settings') && !sidebarCollapsed}
            <span class="absolute left-0 top-1/2 -translate-y-1/2 w-0.5 h-5 rounded-r-full bg-[var(--accent)]"></span>
          {/if}
          <SettingsIcon class="w-4 h-4 shrink-0" />
          {#if !sidebarCollapsed}<span>Settings</span>{/if}
        </a>
      </div>
      <div class="{sidebarCollapsed ? 'px-2' : 'px-3'} py-2 border-t border-[var(--border)]">
        <div class="flex items-center {sidebarCollapsed ? 'flex-col gap-1' : 'gap-2 px-2'} py-1.5 rounded-lg">
          <div class="w-8 h-8 rounded-full bg-gradient-to-br from-brand-500 to-brand-700 flex items-center justify-center text-white text-xs font-semibold shrink-0"
               title={sidebarCollapsed ? `${auth.user?.username} (${auth.user?.role})` : undefined}>
            {auth.user?.username?.[0]?.toUpperCase() ?? '?'}
          </div>
          {#if !sidebarCollapsed}
            <div class="flex-1 min-w-0">
              <div class="text-sm font-medium text-[var(--fg)] truncate">{auth.user?.username}</div>
              <div class="text-[11px] text-[var(--fg-muted)] truncate">{auth.user?.role}</div>
            </div>
          {/if}
          <!-- Passive always-on system health dot. Click for the
               breakdown (backup / proxy / agents / disk / scanner).
               Replaces the old "Backup Nh ago" pill so everything
               stays in one unified control instead of per-feature
               sidebar real estate. -->
          {#if auth.isAuthenticated}
            <HealthDot />
          {/if}
          <button
            onclick={() => (theme = theme === 'dark' ? 'light' : 'dark')}
            class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]"
            title="Toggle theme"
            aria-label="Toggle theme"
          >
            {#if theme === 'dark'}<Sun class="w-4 h-4" />{:else}<Moon class="w-4 h-4" />{/if}
          </button>
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
    <div class="flex-1 flex flex-col min-w-0 bg-[var(--bg-elevated)]">
      <!-- Mobile-only top bar: hamburger to toggle the off-canvas sidebar.
           On desktop the sidebar is always visible so we drop the whole
           header — each page provides its own H1 + sub-title, and the
           sidebar border is the only chrome. -->
      <header class="md:hidden h-12 shrink-0 border-b border-[var(--border)] bg-[var(--bg)] flex items-center px-4">
        <button
          class="p-2 -ml-2 rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-hover)]"
          onclick={() => (mobileOpen = !mobileOpen)}
          aria-label="Toggle sidebar"
        >
          {#if mobileOpen}<X class="w-5 h-5" />{:else}<Menu class="w-5 h-5" />{/if}
        </button>
      </header>

      <UpdateBanner />
      <main class="flex-1 overflow-auto px-5 md:px-8 py-6 md:py-10">
        <div class="max-w-7xl mx-auto dm-fade-in">
          {@render children()}
        </div>
      </main>
    </div>
  </div>
{/if}
