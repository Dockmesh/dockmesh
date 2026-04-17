<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { api, type BackupStatus } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Toaster } from '$lib/components/ui';
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
    ShieldCheck,
    ShieldAlert,
    ShieldOff,
    Package
  } from 'lucide-svelte';

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

  // Sidebar "last backup" pill: refreshed on login, then every 60s. A
  // stale/failed pill is the single load-bearing signal that
  // automated DR coverage is actually working — worth the tiny
  // polling cost. 60s is well below the 36h staleness threshold.
  let backupStatus = $state<BackupStatus | null>(null);
  let backupPollTimer: ReturnType<typeof setInterval> | null = null;
  async function refreshBackupStatus() {
    if (!auth.isAuthenticated) return;
    try {
      backupStatus = await api.system.backupStatus();
    } catch {
      /* ignore — surfaced on next tick */
    }
  }
  $effect(() => {
    if (auth.isAuthenticated) {
      refreshBackupStatus();
      if (!backupPollTimer) {
        backupPollTimer = setInterval(refreshBackupStatus, 60000);
      }
    } else if (backupPollTimer) {
      clearInterval(backupPollTimer);
      backupPollTimer = null;
      backupStatus = null;
    }
  });

  // Humanised "X ago" for the pill tooltip. Server sends age_seconds
  // so we don't have to fight timezones on the client.
  function fmtAge(secs?: number): string {
    if (secs == null) return '—';
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }

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

{#if $page.url.pathname === '/login'}
  {@render children()}
{:else if auth.isAuthenticated}
  <div class="flex h-screen overflow-hidden">
    <!-- Sidebar -->
    <aside
      class="fixed md:static inset-y-0 left-0 z-40 {sidebarCollapsed ? 'md:w-16' : 'md:w-64'} w-64 bg-[var(--bg-elevated)] border-r border-[var(--border)] flex flex-col transform {mobileOpen
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
        <a href="/" class="flex items-center min-w-0" aria-label="Dockmesh home">
          {#if sidebarCollapsed}
            <img src="/logo-mark.svg" alt="Dockmesh" class="h-9 w-9" />
          {:else}
            <!-- Inlined wordmark: the static SVG hard-codes the text
                 fill to a dark slate which is unreadable on the dark
                 theme. Inlining lets the text use currentColor and
                 inherit var(--fg), so it reads in both themes. Bumped
                 the intrinsic font-size from 16 → 19 for a chunkier
                 look at the 40px render height. -->
            <svg viewBox="0 0 180 36" class="h-10 text-[var(--fg)]" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
              <g transform="translate(18,18) scale(0.082) translate(-768,-390)">
                <path fill="#2c3f4e" d="M 855.48 288.24 L 767.88 335.08 L 767.88 240.97 L 768.12 239.82 Z"/>
                <path fill="#04d1eb" d="M 952.82 345.30 L 952.16 341.57 L 855.48 288.24 L 767.88 335.08 L 865.50 389.75 Z"/>
                <path fill="#03a0c2" d="M 952.82 345.30 L 865.50 389.75 L 952.87 440.58 Z"/>
                <path fill="#097d9a" d="M 865.50 389.75 L 952.87 440.58 L 858.10 493.81 L 767.88 444.59 Z"/>
                <path fill="#136079" d="M 767.88 444.59 L 858.10 493.81 L 804.62 523.76 L 767.88 544.10 Z"/>
                <path fill="#304a5b" d="M 767.88 444.59 L 767.88 544.10 L 676.54 493.21 Z"/>
                <path fill="#416276" d="M 767.88 444.59 L 669.94 390.15 L 584.24 441.28 L 676.54 493.21 Z"/>
                <path fill="#213342" d="M 767.88 335.08 L 669.94 390.15 L 584.24 441.28 L 583.01 440.24 L 583.01 342.70 L 583.42 341.94 L 763.56 242.12 L 767.88 240.97 Z"/>
              </g>
              <text x="40" y="24" font-family="system-ui,-apple-system,Segoe UI,Roboto,sans-serif" font-size="19" font-weight="600" letter-spacing="0.3" fill="currentColor">dockmesh</text>
            </svg>
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
                class="flex items-center {sidebarCollapsed ? 'justify-center px-2' : 'gap-3 px-3'} py-2 rounded-lg text-sm transition-colors
                       {active
                  ? 'bg-[var(--surface)] text-[var(--fg)] font-medium'
                  : 'text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
              >
                <Icon class="w-4 h-4 shrink-0" />
                {#if !sidebarCollapsed}<span>{item.label}</span>{/if}
              </a>
            {/each}
          </div>
        {/each}
      </nav>

      <!-- Last-backup pill: sits just above Settings so admins see
           DR coverage the moment they land on any page. Clicking
           jumps to the System tab where the toggle + recovery docs
           live. Hidden entirely for users without user.manage since
           non-admins can't act on the status anyway. -->
      {#if allowed('user.manage') && backupStatus}
        {@const st = backupStatus.state}
        {@const cls = st === 'ok'
          ? 'text-[var(--color-success-400)] bg-[color-mix(in_srgb,var(--color-success-500)_10%,transparent)]'
          : st === 'stale'
          ? 'text-[var(--color-warning-400)] bg-[color-mix(in_srgb,var(--color-warning-500)_12%,transparent)]'
          : st === 'failed'
          ? 'text-[var(--color-danger-400)] bg-[color-mix(in_srgb,var(--color-danger-500)_12%,transparent)]'
          : 'text-[var(--fg-muted)] bg-[var(--surface)]'}
        {@const label = st === 'ok'
          ? `Backup ${fmtAge(backupStatus.age_seconds)}`
          : st === 'stale'
          ? `Backup stale (${fmtAge(backupStatus.age_seconds)})`
          : st === 'failed'
          ? 'Backup failed'
          : st === 'disabled'
          ? 'Backups off'
          : 'No backups yet'}
        <div class="border-t border-[var(--border)] {sidebarCollapsed ? 'px-2' : 'px-3'} pt-2 pb-1">
          <a
            href="/settings?tab=system"
            onclick={() => (mobileOpen = false)}
            class="flex items-center {sidebarCollapsed ? 'justify-center px-2 py-2' : 'gap-2 px-2.5 py-1.5'} rounded-md text-[11px] font-medium {cls}"
            title={backupStatus.last_error || label}
          >
            {#if st === 'ok'}
              <ShieldCheck class="w-3.5 h-3.5 shrink-0" />
            {:else if st === 'failed'}
              <ShieldAlert class="w-3.5 h-3.5 shrink-0" />
            {:else}
              <ShieldOff class="w-3.5 h-3.5 shrink-0" />
            {/if}
            {#if !sidebarCollapsed}<span class="truncate">{label}</span>{/if}
          </a>
        </div>
      {/if}

      <!-- Sidebar footer: Settings (tool config) sits directly above the
           user card (identity) — both are "meta" actions, visually anchored
           at the bottom of the sidebar. This pairing is the default
           pattern in every modern SaaS tool (Notion, Slack, Linear, …). -->
      <div class="border-t border-[var(--border)] {sidebarCollapsed ? 'px-2' : 'px-3'} pt-2 pb-2 space-y-0.5">
        <a
          href="/settings"
          onclick={() => (mobileOpen = false)}
          title={sidebarCollapsed ? 'Settings' : undefined}
          class="flex items-center {sidebarCollapsed ? 'justify-center px-2' : 'gap-3 px-3'} py-2 rounded-lg text-sm transition-colors
                 {isActive('/settings')
            ? 'bg-[var(--surface)] text-[var(--fg)] font-medium'
            : 'text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
        >
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
    <div class="flex-1 flex flex-col min-w-0">
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

      <main class="flex-1 overflow-auto px-5 md:px-8 py-6 md:py-10">
        <div class="max-w-7xl mx-auto dm-fade-in">
          {@render children()}
        </div>
      </main>
    </div>
  </div>
{/if}
