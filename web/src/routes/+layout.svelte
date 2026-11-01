<script lang="ts">
  import '../app.css';
  import '$lib/styles/ds.css';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { api } from '$lib/api';
  import { fmtVersion } from '$lib/version';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { Toaster, ConfirmDialog, UpdateBanner } from '$lib/components/ui';
  import Menu, { type MenuItem } from '$lib/components/ui/Menu.svelte';
  import NotificationCenter from '$lib/components/ui/NotificationCenter.svelte';
  import NoticeBanner from '$lib/components/ui/NoticeBanner.svelte';
  import BackupHealthPill from '$lib/components/BackupHealthPill.svelte';
  import MigrationActivePill from '$lib/components/MigrationActivePill.svelte';
  import MigrationDrawer from '$lib/components/MigrationDrawer.svelte';
  import OperationsIndicator from '$lib/components/operations/OperationsIndicator.svelte';
  import OperationDrawer from '$lib/components/operations/OperationDrawer.svelte';
  import { operations } from '$lib/stores/operations.svelte';
  import Sidebar, { type Badge } from '$lib/components/shell/Sidebar.svelte';
  import CommandPalette from '$lib/components/shell/CommandPalette.svelte';
  import SettingsNav from '$lib/components/shell/SettingsNav.svelte';
  import { activeItem, SETTINGS_PATHS, matches } from '$lib/components/shell/nav';
  import { Sun, Moon, ChevronDown, Server, Menu as MenuIcon } from 'lucide-svelte';

  let { children } = $props();

  // Theme: an explicit choice (stored) wins, otherwise follow the OS.
  let theme = $state<'light' | 'dark'>('dark');
  $effect(() => {
    const stored = localStorage.getItem('dockmesh.theme');
    if (stored === 'light' || stored === 'dark') {
      theme = stored;
      return;
    }
    const mq = window.matchMedia('(prefers-color-scheme: light)');
    theme = mq.matches ? 'light' : 'dark';
    const onChange = (e: MediaQueryListEvent) => {
      if (!localStorage.getItem('dockmesh.theme')) theme = e.matches ? 'light' : 'dark';
    };
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  });
  $effect(() => {
    document.documentElement.dataset.theme = theme;
  });
  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem('dockmesh.theme', theme);
  }

  $effect(() => {
    if (auth.isAuthenticated) operations.start();
    else operations.stop();
  });

  let mobileOpen = $state(false);
  let paletteOpen = $state(false);
  let migDrawerOpen = $state(false);

  let sidebarCollapsed = $state<boolean>(
    typeof localStorage !== 'undefined' && localStorage.getItem('dm_sidebar_collapsed') === '1'
  );
  $effect(() => {
    localStorage.setItem('dm_sidebar_collapsed', sidebarCollapsed ? '1' : '0');
  });

  function onGlobalKey(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      if (!auth.isAuthenticated) return;
      e.preventDefault();
      paletteOpen = !paletteOpen;
    }
  }

  // Host list + per-host meta for the lens (agent version and uptime,
  // server version and uptime), refreshed every 10 s while signed in.
  let hostPollTimer: ReturnType<typeof setInterval> | null = null;
  let agentsByID = $state<Record<string, { version?: string; online_since?: string; status: string }>>({});
  let serverInfo = $state<{ version: string; uptime_seconds: number } | null>(null);
  async function refreshHosts() {
    if (!auth.isAuthenticated) return;
    try {
      hosts.setAvailable(await api.hosts.list());
    } catch {
      /* lens falls back to the last known list */
    }
    try {
      const ags = await api.agents.list();
      const map: Record<string, { version?: string; online_since?: string; status: string }> = {};
      for (const a of ags) map[a.id] = { version: a.version, online_since: a.online_since, status: a.status };
      agentsByID = map;
    } catch {
      /* meta shows the status text instead */
    }
    try {
      const info = await api.system.info();
      serverInfo = { version: info.version, uptime_seconds: info.uptime_seconds };
    } catch {
      /* meta shows "local" instead */
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

  // Sidebar counts: stacks, containers (warn when some are unhealthy) and
  // hosts (bad when one is offline). Follows the lens.
  let counts = $state<{ stacks?: number; containers?: number; unhealthy?: number }>({});
  async function refreshCounts() {
    if (!auth.isAuthenticated) return;
    const host = hosts.id;
    const [stacks, summary] = await Promise.all([
      allowed('stacks.view') ? api.stacks.list().catch(() => null) : Promise.resolve(null),
      allowed('containers.view') && hosts.usable(allowed('hosts.view')) ? api.containers.summary(host).catch(() => null) : Promise.resolve(null)
    ]);
    counts = {
      stacks: stacks?.length,
      containers: summary?.total,
      unhealthy: summary?.unhealthy
    };
  }
  $effect(() => {
    if (!auth.isAuthenticated) return;
    void hosts.id;
    void hosts.available.length;
    void page.url.pathname;
    refreshCounts();
    const t = setInterval(refreshCounts, 30_000);
    return () => clearInterval(t);
  });
  const badges = $derived.by<Record<string, Badge>>(() => {
    const b: Record<string, Badge> = {};
    if (counts.stacks != null) b.stacks = { text: String(counts.stacks) };
    if (counts.containers != null) b.containers = { text: String(counts.containers), tone: counts.unhealthy ? 'warn' : '' };
    if (hosts.available.length) {
      const off = hosts.available.filter((h) => h.status !== 'online').length;
      b.hosts = { text: String(hosts.available.length), tone: off ? 'bad' : '' };
    }
    return b;
  });

  function fmtUptimeShort(seconds: number): string {
    if (!seconds || seconds < 60) return `${Math.round(seconds)}s`;
    const m = Math.floor(seconds / 60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h`;
    return `${Math.floor(h / 24)}d`;
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
      return `${fmtVersion(serverInfo.version)} · ${fmtUptimeShort(serverInfo.uptime_seconds)}`;
    }
    const a = agentsByID[h.id];
    if (!a) return h.status;
    if (a.status !== 'online') return a.status;
    const up = fmtUptimeFromISO(a.online_since);
    if (!a.version) return up ?? 'online';
    return up ? `${fmtVersion(a.version)} · ${up}` : fmtVersion(a.version);
  }

  const lensItems = $derived.by<MenuItem[]>(() => {
    const out: MenuItem[] = [{ heading: 'Lens · filter every page by host' }];
    hosts.withAll.forEach((h, i) => {
      out.push({
        label: h.name,
        kbd: metaFor(h),
        checked: h.id === hosts.id,
        disabled: h.status !== 'online',
        hint: h.status !== 'online' ? `${h.name} is ${h.status}` : undefined,
        onclick: () => hosts.set(h.id)
      });
      if (i === 0 && h.kind === 'all') out.push({ separator: true });
    });
    out.push({ separator: true }, { label: 'Manage hosts', href: '/hosts', icon: Server });
    return out;
  });

  // Setup-mode probe: a fresh install sends everyone to /setup.
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

  $effect(() => {
    const path = page.url.pathname;
    if (path === '/setup' || path.startsWith('/setup/')) return;
    // Invite links work without a session.
    if (path.startsWith('/invite/')) return;
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
      /* the session is dropped locally either way */
    }
    toast.info('Signed out');
    goto('/login');
  }

  const SPECIAL: Record<string, { section: string | null; leaf: string; href: string }> = {
    '/account': { section: null, leaf: 'My account', href: '/account' },
    '/tokens': { section: null, leaf: 'API tokens', href: '/tokens' },
    '/settings/updates': { section: 'Settings', leaf: 'Updates', href: '/settings/updates' },
    '/settings/encryption': { section: 'Settings', leaf: 'Encryption key', href: '/settings/encryption' },
    '/settings': { section: 'Settings', leaf: 'General', href: '/settings' },
    '/authentication': { section: 'Settings', leaf: 'Authentication', href: '/authentication' },
    '/registries': { section: 'Settings', leaf: 'Registries', href: '/registries' },
    '/environment': { section: 'Settings', leaf: 'Global environment', href: '/environment' },
    '/images': { section: 'Resources', leaf: 'Images', href: '/resources?tab=images' },
    '/volumes': { section: 'Resources', leaf: 'Volumes', href: '/resources?tab=volumes' },
    '/networks': { section: 'Resources', leaf: 'Networks', href: '/resources?tab=networks' },
    '/agents': { section: 'Hosts', leaf: 'Agents', href: '/agents' },
    '/migrations': { section: 'Backups', leaf: 'Migrations', href: '/migrations' }
  };

  interface Crumb {
    label: string;
    href?: string;
  }
  const crumbs = $derived.by<Crumb[]>(() => {
    const p = page.url.pathname;
    const out: Crumb[] = [];
    const special = Object.entries(SPECIAL).find(([k]) => matches(p, k))?.[1];
    if (special) {
      if (special.section) out.push({ label: special.section, href: special.section === 'Settings' ? '/settings' : undefined });
      out.push({ label: special.leaf, href: special.href });
    } else {
      const a = activeItem(p);
      if (a) {
        if (a.group.title) out.push({ label: a.group.title });
        out.push({ label: a.item.label, href: a.item.href });
      } else {
        const seg = p.split('/').filter(Boolean)[0] ?? '';
        out.push({ label: seg ? seg.replace(/-/g, ' ') : 'Overview', href: `/${seg}` });
      }
    }
    if (pageContext.entityName) {
      if (pageContext.trail.length) {
        // A trail replaces the list crumb (a container shown inside its stack).
        out.splice(out.length - 1, 1, ...pageContext.trail);
      }
      out.push({ label: pageContext.entityName });
    }
    return out;
  });

  const inSettings = $derived(SETTINGS_PATHS.some((s) => matches(page.url.pathname, s)));
  const lensHost = $derived(hosts.selected ?? hosts.available[0]);
</script>

<svelte:window onkeydown={onGlobalKey} />

<Toaster />
<ConfirmDialog />
<MigrationDrawer bind:open={migDrawerOpen} />
{#if auth.isAuthenticated}<OperationDrawer />{/if}

{#if page.url.pathname === '/setup' || page.url.pathname.startsWith('/setup/')}
  {@render children()}
{:else if !setupProbed || setupActive}
  <!-- nothing until we know where the operator should land -->
{:else if page.url.pathname === '/login' || page.url.pathname.startsWith('/invite/')}
  {@render children()}
{:else if auth.isAuthenticated}
  <a class="skip" href="#main">Skip to content</a>
  <div class="app" class:mobile-open={mobileOpen}>
    <Sidebar
      bind:collapsed={sidebarCollapsed}
      {badges}
      {theme}
      ontheme={toggleTheme}
      onpalette={() => (paletteOpen = true)}
      onsignout={doLogout}
      onnavigate={() => (mobileOpen = false)}
    />
    {#if mobileOpen}
      <button type="button" class="scrim" aria-label="Close navigation" onclick={() => (mobileOpen = false)}></button>
    {/if}

    <main class="main" id="main">
      <header class="topbar">
        <button type="button" class="tb-btn tb-icon mob" onclick={() => (mobileOpen = !mobileOpen)} aria-label="Toggle navigation">
          <MenuIcon size={16} />
        </button>
        <nav class="crumbs" aria-label="Breadcrumb">
          {#each crumbs as c, i (i)}
            {#if i === crumbs.length - 1}
              <b aria-current="page">{c.label}</b>
            {:else}
              {#if c.href}<a href={c.href}>{c.label}</a>{:else}<span>{c.label}</span>{/if}
              <span class="sep" aria-hidden="true">/</span>
            {/if}
          {/each}
        </nav>

        <div class="pills" aria-live="polite">
          <OperationsIndicator />
          <MigrationActivePill onOpen={() => (migDrawerOpen = true)} />
          <BackupHealthPill />
        </div>

        {#if hosts.available.length > 0 && lensHost}
          {@const warn = lensHost.kind === 'agent' && lensHost.status !== 'online'}
          <Menu label="Lens: filter pages by host" items={lensItems} width={280} triggerClass="tb-btn lens">
            {#snippet trigger()}
              <span class="lensdot" class:all={lensHost.kind === 'all'} class:warn></span>
              <span class="lens-name">{lensHost.name}</span>
              <ChevronDown size={13} />
            {/snippet}
          </Menu>
        {/if}

        <span class="tb-bell"><NotificationCenter /></span>

        <button
          type="button"
          class="tb-btn tb-icon"
          onclick={toggleTheme}
          title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
          aria-label="Toggle theme"
        >
          {#if theme === 'dark'}<Sun size={15} />{:else}<Moon size={15} />{/if}
        </button>
      </header>

      <NoticeBanner />
      <UpdateBanner />

      {#if inSettings}
        <div class="settings-wrap">
          <SettingsNav />
          <div class="settings-body">{@render children()}</div>
        </div>
      {:else}
        <div class="page">{@render children()}</div>
      {/if}
    </main>
  </div>
  <CommandPalette bind:open={paletteOpen} ontheme={toggleTheme} />
{/if}

<style>
  .app { display: flex; min-height: 100vh; }
  .main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .skip {
    position: absolute;
    left: -999px;
    top: 8px;
    z-index: 300;
    background: var(--menu);
    color: var(--fg);
    padding: 6px 10px;
    border-radius: var(--r);
    border: 1px solid var(--accent);
  }
  .skip:focus { left: 8px; }
  .topbar {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 24px;
    height: 48px;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--bg);
    z-index: 20;
  }
  .crumbs {
    flex: 1;
    font-size: var(--fs-sm);
    color: var(--subtle);
    display: flex;
    gap: 6px;
    align-items: center;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
  }
  .crumbs a { color: var(--subtle); text-decoration: none; }
  .crumbs a:hover { color: var(--fg); }
  .crumbs b { color: var(--fg); font-weight: 600; overflow: hidden; text-overflow: ellipsis; }
  .pills { display: flex; align-items: center; gap: 6px; }
  .topbar :global(.tb-btn) {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 32px;
    padding: 0 10px;
    border-radius: var(--r);
    border: 1px solid var(--border);
    background: var(--surface);
    cursor: pointer;
    color: var(--fg);
    position: relative;
    font: inherit;
  }
  .topbar :global(.tb-btn:hover) { border-color: var(--accent); }
  .tb-icon { width: 32px; justify-content: center; padding: 0 !important; }
  .lens-name { max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lensdot { width: 8px; height: 8px; border-radius: 4px; background: var(--ok); flex: none; }
  .lensdot.all { background: var(--accent); }
  .lensdot.warn { background: var(--bad); }
  .tb-bell :global(> button) {
    width: 32px !important;
    height: 32px !important;
    border: 1px solid var(--border) !important;
    border-radius: var(--r) !important;
    background: var(--surface) !important;
    color: var(--fg) !important;
  }
  .tb-bell :global(> button:hover) { border-color: var(--accent) !important; }
  .page { flex: 1; min-width: 0; padding: 22px 32px 48px; }
  .settings-wrap {
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
    gap: 24px;
    padding: 22px 32px 48px;
    align-items: start;
  }
  .settings-body { min-width: 0; }
  .mob { display: none !important; }
  .scrim { display: none; }

  @media (max-width: 900px) {
    .app :global(.app-side) {
      position: fixed;
      left: 0;
      top: 0;
      transform: translateX(-100%);
      transition: transform 0.15s;
      z-index: 60;
    }
    .app.mobile-open :global(.app-side) { transform: none; }
    .scrim { display: block; position: fixed; inset: 0; background: var(--overlay); z-index: 50; border: 0; }
    .mob { display: inline-flex !important; }
    .topbar { padding: 0 12px; }
    .pills { display: none; }
    .settings-wrap { grid-template-columns: minmax(0, 1fr); padding: 16px; }
    .page { padding: 16px 16px 40px; }
  }
</style>
