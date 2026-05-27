<script lang="ts">
  import '../app.css';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { api } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Toaster, ConfirmDialog, UpdateBanner, HealthDot } from '$lib/components/ui';
  import NotificationCenter from '$lib/components/ui/NotificationCenter.svelte';
  import BackupHealthPill from '$lib/components/BackupHealthPill.svelte';
  import MigrationActivePill from '$lib/components/MigrationActivePill.svelte';
  import MigrationDrawer from '$lib/components/MigrationDrawer.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import {
    LayoutDashboard,
    Layers,
    Box,
    Boxes,
    Globe,
    GitBranch,
    Bell,
    Archive,
    Server,
    Settings as SettingsIcon,
    Moon,
    Sun,
    LogOut,
    Menu,
    X,
    ChevronsLeft,
    ChevronsRight,
    ArrowRightLeft,
    Package,
    Activity,
    Users as UsersIcon,
    ShieldCheck as ShieldCheckIcon,
    KeyRound,
    UserCircle,
  } from 'lucide-svelte';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('dark');
  let mobileOpen = $state(false);
  let hostMenuOpen = $state(false);
  let userMenuOpen = $state(false);
  let hostMenuRef = $state<HTMLDivElement | null>(null);
  // Global migrations drawer — opens from the topbar pill, can also be
  // triggered from stack/host pages later.
  let migDrawerOpen = $state(false);

  // Click-outside handler for the topbar host picker. We can't use the
  // .app-overlay trick the sidebar uses, because the topbar has
  // `backdrop-filter: blur(...)` which turns it into the containing
  // block for `position: fixed` descendants — the overlay then only
  // covers the topbar height, not the viewport. Instead, listen on the
  // window while the menu is open and close it for any mousedown
  // outside the wrapper.
  $effect(() => {
    if (!hostMenuOpen) return;
    function onDocDown(e: MouseEvent) {
      if (!hostMenuRef) return;
      if (e.target instanceof Node && hostMenuRef.contains(e.target)) return;
      hostMenuOpen = false;
    }
    window.addEventListener('mousedown', onDocDown);
    return () => window.removeEventListener('mousedown', onDocDown);
  });

  // Sidebar collapse — persisted across reloads.
  let sidebarCollapsed = $state<boolean>(
    typeof localStorage !== 'undefined' && localStorage.getItem('dm_sidebar_collapsed') === '1'
  );
  $effect(() => {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('dm_sidebar_collapsed', sidebarCollapsed ? '1' : '0');
    }
    if (sidebarCollapsed) hostMenuOpen = false;
  });

  // Theme persistence — also seed from the value the login page may have
  // stored before auth, so the editorial dark/light choice carries over.
  $effect(() => {
    if (typeof window === 'undefined') return;
    const stored = localStorage.getItem('dockmesh.theme');
    if (stored === 'light' || stored === 'dark') theme = stored;
  });
  $effect(() => {
    if (typeof document === 'undefined') return;
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('dockmesh.theme', theme);
  });

  // Host poll: refresh available hosts every 10s while authenticated.
  // Also refreshes the per-host meta the topbar host-picker shows
  // (`v0.4.0 · 12d`-style strings) by pulling /agents (version + uptime
  // from online_since) and /system/info (server version + uptime). Both
  // polled together so the picker stays in sync with the visible host
  // list. Failures are silent — the menu falls back to status text.
  let hostPollTimer: ReturnType<typeof setInterval> | null = null;
  let agentsByID = $state<Record<string, { version?: string; online_since?: string; status: string }>>({});
  let serverInfo = $state<{ version: string; uptime_seconds: number } | null>(null);
  async function refreshHosts() {
    if (!auth.isAuthenticated) return;
    try {
      const list = await api.hosts.list();
      hosts.setAvailable(list);
    } catch {
      /* ignore */
    }
    try {
      const ags = await api.agents.list();
      const map: Record<string, { version?: string; online_since?: string; status: string }> = {};
      for (const a of ags) map[a.id] = { version: a.version, online_since: a.online_since, status: a.status };
      agentsByID = map;
    } catch {
      /* ignore */
    }
    try {
      const info = await api.system.info();
      serverInfo = { version: info.version, uptime_seconds: info.uptime_seconds };
    } catch {
      /* ignore */
    }
  }
  $effect(() => {
    if (auth.isAuthenticated) {
      refreshHosts();
      if (!hostPollTimer) hostPollTimer = setInterval(refreshHosts, 10000);
    } else if (hostPollTimer) {
      clearInterval(hostPollTimer);
      hostPollTimer = null;
    }
  });

  // Format helpers used by the topbar host-picker meta column.
  function fmtUptimeShort(seconds: number): string {
    if (!seconds || seconds < 60) return `${Math.round(seconds)}s`;
    const m = Math.floor(seconds / 60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h`;
    const d = Math.floor(h / 24);
    return `${d}d`;
  }
  function fmtUptimeFromISO(iso?: string): string | null {
    if (!iso) return null;
    const t = Date.parse(iso);
    if (!t || isNaN(t)) return null;
    return fmtUptimeShort((Date.now() - t) / 1000);
  }
  function metaFor(h: { id: string; kind: string; status: string }): string {
    if (h.kind === 'all') return 'fleet-wide';
    if (h.id === 'local') {
      if (!serverInfo) return 'local';
      const up = fmtUptimeShort(serverInfo.uptime_seconds);
      return `v${serverInfo.version} · ${up}`;
    }
    const a = agentsByID[h.id];
    if (!a) return h.status;
    if (a.status !== 'online') return a.status;
    const up = fmtUptimeFromISO(a.online_since);
    if (!a.version && !up) return 'online';
    if (!a.version) return up ?? 'online';
    if (!up) return `v${a.version}`;
    return `v${a.version} · ${up}`;
  }

  // Setup-mode probe (P.14.3).
  let setupProbed = $state(false);
  let setupActive = $state(false);
  $effect(() => {
    if (setupProbed) return;
    fetch('/api/v1/setup/status')
      .then((r) => (r.ok ? r.json() : Promise.reject()))
      .then((d) => {
        setupActive = !!d.active;
      })
      .catch(() => {})
      .finally(() => {
        setupProbed = true;
      });
  });

  // Route guard.
  $effect(() => {
    const path = $page.url.pathname;
    if (path === '/setup' || path.startsWith('/setup/')) return;
    if (!setupProbed) return;
    if (setupActive) {
      goto('/setup');
      return;
    }
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

  type NavItem = { href: string; label: string; icon: any; show: boolean };
  type NavSection = { title: string | null; items: NavItem[] };

  const sections = $derived<NavSection[]>(
    (
      [
        {
          title: null,
          items: [
            { href: '/', label: 'Dashboard', icon: LayoutDashboard, show: true },
            { href: '/stacks', label: 'Stacks', icon: Layers, show: allowed('stacks.view') },
            { href: '/templates', label: 'Templates', icon: Package, show: allowed('templates.view') },
            { href: '/containers', label: 'Containers', icon: Box, show: allowed('containers.view') },
            { href: '/resources', label: 'Resources', icon: Boxes, show: allowed('images.view') || allowed('volumes.view') || allowed('networks.view') },
            { href: '/hosts', label: 'Hosts', icon: Server, show: allowed('hosts.view') },
          ],
        },
        {
          title: 'Network',
          items: [
            { href: '/topology', label: 'Topology', icon: GitBranch, show: allowed('containers.view') },
            { href: '/proxy', label: 'Proxy', icon: Globe, show: allowed('proxy.view') },
          ],
        },
        {
          title: 'Automation',
          items: [
            { href: '/environment', label: 'Environment', icon: Box, show: allowed('system.update') },
            { href: '/alerts', label: 'Alerts', icon: Bell, show: allowed('alerts.view') },
            { href: '/backups', label: 'Backups', icon: Archive, show: allowed('backups.view') },
          ],
        },
        {
          title: 'Platform',
          items: [
            { href: '/users', label: 'Users & Roles', icon: UsersIcon, show: allowed('users.view') },
            { href: '/authentication', label: 'Authentication', icon: ShieldCheckIcon, show: allowed('system.update') },
            { href: '/registries', label: 'Registries', icon: KeyRound, show: allowed('registries.view') },
            { href: '/audit', label: 'Audit Log', icon: Activity, show: allowed('audit.view') },
          ],
        },
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

  // Crumb derived from the current pathname. The first segment becomes the
  // section ("fleet" for daily-use, otherwise the segment itself); the
  // rendered label is the readable label of the matching nav item.
  const crumb = $derived.by<{ section: string; leaf: string; leafHref: string }>(() => {
    const p = $page.url.pathname;
    if (p === '/') return { section: 'fleet', leaf: 'dashboard', leafHref: '/' };
    if (p === '/settings' || p.startsWith('/settings/'))
      return { section: 'platform', leaf: 'settings', leafHref: '/settings' };
    if (p === '/account' || p.startsWith('/account/'))
      return { section: 'profile', leaf: 'account', leafHref: '/account' };
    if (p === '/tokens' || p.startsWith('/tokens/'))
      return { section: 'profile', leaf: 'api tokens', leafHref: '/tokens' };
    // Resource detail pages (volumes/networks/images) live under the
    // Resources tab strip — the breadcrumb should send users back there
    // rather than to a bare /volumes route that doesn't exist.
    if (p.startsWith('/volumes/'))
      return { section: 'fleet', leaf: 'resources', leafHref: '/resources?tab=volumes' };
    if (p.startsWith('/networks/'))
      return { section: 'fleet', leaf: 'resources', leafHref: '/resources?tab=networks' };
    if (p.startsWith('/images/'))
      return { section: 'fleet', leaf: 'resources', leafHref: '/resources?tab=images' };
    for (const sec of sections) {
      for (const it of sec.items) {
        if (p === it.href || p.startsWith(it.href + '/')) {
          return {
            section: sec.title ? sec.title.toLowerCase() : 'fleet',
            leaf: it.label.toLowerCase(),
            leafHref: it.href,
          };
        }
      }
    }
    // Fallback: derive from the first path segment.
    const seg = p.split('/').filter(Boolean)[0] ?? 'fleet';
    return { section: 'fleet', leaf: seg.replace(/-/g, ' '), leafHref: `/${seg}` };
  });

  const userInitial = $derived(auth.user?.username?.[0]?.toUpperCase() ?? '?');
</script>

<Toaster />
<ConfirmDialog />
<MigrationDrawer bind:open={migDrawerOpen} />

{#if $page.url.pathname === '/setup' || $page.url.pathname.startsWith('/setup/')}
  {@render children()}
{:else if !setupProbed || setupActive}
  <!-- Render nothing while we figure out where the operator should land. -->
{:else if $page.url.pathname === '/login'}
  {@render children()}
{:else if auth.isAuthenticated}
  <div class="ed-stage app-stage-root">
    <div
      class="app-shell"
      class:app-shell-collapsed={sidebarCollapsed}
      class:mobile-open={mobileOpen}
    >
      <!-- ─────────────────────────────────────────── Sidebar -->
      <aside class="app-nav">
        <div class="app-nav-brand">
          <a href="/" aria-label="dockmesh home">
            <span class="brand-mark">
              <img src="/logo-mark.svg" alt="" aria-hidden="true" width="22" height="22" />
            </span>
            {#if !sidebarCollapsed}
              <span>dock<span class="accent">mesh</span></span>
            {/if}
          </a>
          <button
            type="button"
            class="app-nav-collapse"
            onclick={() => (sidebarCollapsed = !sidebarCollapsed)}
            title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          >
            {#if sidebarCollapsed}
              <ChevronsRight size={12} strokeWidth={1.5} />
            {:else}
              <ChevronsLeft size={12} strokeWidth={1.5} />
            {/if}
          </button>
        </div>

        <nav class="app-nav-scroll">
          {#each sections as section (section.title ?? '_top')}
            <div class="app-nav-section">
              {#if section.title && !sidebarCollapsed}
                <span class="app-nav-section-label">{section.title}</span>
              {/if}
              {#each section.items as item (item.href)}
                {@const Icon = item.icon}
                <a
                  href={item.href}
                  class="app-nav-item"
                  class:active={isActive(item.href)}
                  title={sidebarCollapsed ? item.label : undefined}
                  onclick={() => (mobileOpen = false)}
                >
                  <Icon size={15} strokeWidth={1.5} />
                  <span class="app-nav-item-label">{item.label}</span>
                </a>
              {/each}
            </div>
          {/each}

          <!-- Settings sits in its own section above the user footer. -->
          <div class="app-nav-section">
            <a
              href="/settings"
              class="app-nav-item"
              class:active={isActive('/settings')}
              title={sidebarCollapsed ? 'Settings' : undefined}
              onclick={() => (mobileOpen = false)}
            >
              <SettingsIcon size={15} strokeWidth={1.5} />
              <span class="app-nav-item-label">Settings</span>
            </a>
          </div>
        </nav>

        <div class="app-nav-footer" style="position: relative;">
          <button
            type="button"
            class="app-nav-avatar"
            onclick={() => (userMenuOpen = !userMenuOpen)}
            title={sidebarCollapsed ? `${auth.user?.username} (${auth.user?.role})` : 'Open user menu'}
            aria-haspopup="menu"
            aria-expanded={userMenuOpen}
          >
            {userInitial}
          </button>
          {#if !sidebarCollapsed}
            <div class="app-nav-userblock">
              <span class="name">{auth.user?.username ?? '—'}</span>
              <span class="role">{auth.user?.role ?? ''}</span>
            </div>
          {/if}
          {#if auth.isAuthenticated}
            <HealthDot />
          {/if}

          {#if userMenuOpen}
            <button
              type="button"
              class="app-overlay"
              aria-label="Close menu"
              onclick={() => (userMenuOpen = false)}
            ></button>
            <div class="app-user-menu" role="menu">
              <div class="app-user-menu-head">
                <div class="name">{auth.user?.username}</div>
                <div class="role">{auth.user?.role}</div>
              </div>
              <a
                href="/account"
                class="app-user-menu-item"
                role="menuitem"
                onclick={() => (userMenuOpen = false)}
              >
                <UserCircle size={13} strokeWidth={1.5} />
                Profile &amp; security
              </a>
              <a
                href="/tokens"
                class="app-user-menu-item"
                role="menuitem"
                onclick={() => (userMenuOpen = false)}
              >
                <KeyRound size={13} strokeWidth={1.5} />
                API tokens
              </a>
              <div class="app-user-menu-sep"></div>
              <button
                type="button"
                class="app-user-menu-item danger"
                role="menuitem"
                onclick={() => {
                  userMenuOpen = false;
                  doLogout();
                }}
              >
                <LogOut size={13} strokeWidth={1.5} />
                Sign out
              </button>
            </div>
          {/if}
        </div>
      </aside>

      <!-- ─────────────────────────────────────────── Main column -->
      <main class="app-main">
        <div class="app-mobile-bar">
          <button
            type="button"
            class="app-mobile-button"
            onclick={() => (mobileOpen = !mobileOpen)}
            aria-label="Toggle sidebar"
          >
            {#if mobileOpen}
              <X size={16} strokeWidth={1.5} />
            {:else}
              <Menu size={16} strokeWidth={1.5} />
            {/if}
          </button>
          <span class="app-mobile-title">{crumb.leaf}</span>
        </div>

        <div class="app-topbar">
          <div class="app-topbar-crumb">
            <span>{crumb.section}</span>
            <span class="sep">/</span>
            {#if pageContext.entityName}
              {#if pageContext.trail.length > 0}
                {#each pageContext.trail as t}
                  {#if t.href}
                    <a href={t.href} class="leaf-link">{t.label}</a>
                  {:else}
                    <span>{t.label}</span>
                  {/if}
                  <span class="sep">/</span>
                {/each}
              {:else}
                <a href={crumb.leafHref} class="leaf-link">{crumb.leaf}</a>
                <span class="sep">/</span>
              {/if}
              <span class="leaf">{pageContext.entityName}</span>
            {:else}
              <span class="leaf">{crumb.leaf}</span>
            {/if}
          </div>

          <!-- Alerts slot — surfaces backup health + live migration state.
               Lives left of the host-picker so it sits in the natural
               left-to-right reading order: page · alerts · host · theme. -->
          <div class="app-topbar-alerts" aria-live="polite">
            <MigrationActivePill onOpen={() => (migDrawerOpen = true)} />
            <BackupHealthPill />
          </div>

          <!-- Host picker (lens) — replaces the old sidebar host-block.
               Mockup pattern: dot · label · meta · ▾ . The popover is
               labeled "Lens — filter pages by host" so it's clear the
               selection narrows the current view rather than navigating. -->
          {#if hosts.available.length > 0}
            {@const sel = hosts.selected ?? hosts.available[0]}
            {@const selWarn = sel?.kind === 'agent' && sel?.status !== 'online'}
            <div class="app-topbar-host-wrap" bind:this={hostMenuRef} style="position: relative;">
              <button
                type="button"
                class="app-topbar-host-btn"
                onclick={() => (hostMenuOpen = !hostMenuOpen)}
                aria-haspopup="listbox"
                aria-expanded={hostMenuOpen}
              >
                <span class={sel?.kind === 'all' ? 'dot-brand' : selWarn ? 'dot-warn' : 'dot-ok'}></span>
                <span class="label">{sel?.name ?? 'local'}</span>
                <span class="sep">·</span>
                <span class="meta">{sel ? metaFor(sel) : ''}</span>
                <span class="caret">▾</span>
              </button>
              {#if hostMenuOpen}
                <div class="app-topbar-host-menu" role="listbox">
                  <div class="app-topbar-host-menu-label">Lens — filter pages by host</div>
                  {#each hosts.withAll as h, idx (h.id)}
                    {@const online = h.status === 'online'}
                    {@const itemWarn = h.kind === 'agent' && h.status !== 'online'}
                    <button
                      type="button"
                      class="app-topbar-host-menu-item"
                      class:active={h.id === hosts.id}
                      onclick={() => {
                        hosts.set(h.id);
                        hostMenuOpen = false;
                      }}
                      disabled={!online}
                      role="option"
                      aria-selected={h.id === hosts.id}
                    >
                      <span class={h.kind === 'all' ? 'dot-brand' : itemWarn ? 'dot-warn' : 'dot-ok'}></span>
                      <span class="label">{h.name}</span>
                      <span class="meta">{metaFor(h)}</span>
                      {#if h.id === hosts.id}<span class="check">✓</span>{/if}
                    </button>
                    {#if idx === 0 && h.kind === 'all'}
                      <div class="app-topbar-host-menu-divider"></div>
                    {/if}
                  {/each}
                  <div class="app-topbar-host-menu-divider"></div>
                  <a href="/hosts" class="app-topbar-host-menu-item manage" onclick={() => (hostMenuOpen = false)}>
                    <Server size={11} strokeWidth={1.5} />
                    <span class="label">Manage hosts</span>
                  </a>
                </div>
              {/if}
            </div>
          {/if}

          <NotificationCenter />

          <button
            type="button"
            class="app-topbar-theme-btn"
            onclick={() => (theme = theme === 'dark' ? 'light' : 'dark')}
            title={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
            aria-label="Toggle theme"
          >
            {#if theme === 'dark'}
              <Sun size={13} strokeWidth={1.5} />
            {:else}
              <Moon size={13} strokeWidth={1.5} />
            {/if}
          </button>
        </div>

        <UpdateBanner />

        <div class="app-page dm-fade-in">
          {@render children()}
        </div>
      </main>
    </div>
  </div>
{/if}

<style>
  .app-stage-root { min-height: 100vh; }
  .app-main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 100vh;
  }
  .app-overlay {
    position: fixed;
    inset: 0;
    z-index: 20;
    background: transparent;
    border: 0;
    cursor: default;
  }
  .app-mobile-title {
    margin-left: 8px;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg);
    letter-spacing: 0.04em;
    text-transform: lowercase;
  }
  :global(.text-brand-accent) { color: var(--color-brand-400); flex-shrink: 0; }
</style>
