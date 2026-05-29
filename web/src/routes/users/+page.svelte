<script lang="ts">
  // Users & Roles — editorial rebuild based on `Dockmesh Wizard (5)/
  // settings-users.jsx` + `roles.jsx`. Two tabs (Users · Roles) on one
  // route. RBAC v2 stage R-4: matrix with fixed verb columns + sensitive
  // section + scope tree picker in the Edit-Role drawer + ? tooltips.
  // See project_users_roles_open.md + project_rbac_v2_spec.md memories.
  import { api, ApiError, type CustomRole, type PermissionInfo, type RoleScope, type HostInfo, type StackListEntry } from '$lib/api';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Users as UsersIcon, Plus, Trash2, Search, MoreVertical,
    Lock, Unlock, ArrowRight, Send, Check, X, Copy, Edit3,
    Mail, Shield, Server, Container as ContainerIcon, HardDrive,
    Network, Image as ImageIcon, Layers, Eye, EyeOff
  } from 'lucide-svelte';

  type Sub = 'users' | 'roles';
  let sub = $state<Sub>((new URLSearchParams($page.url.search).get('sub') as Sub) || 'users');

  // ─── Users state ──────────────────────────────────────────────────────
  interface UserRow {
    id: string;
    username: string;
    email?: string;
    role: string;
    scope_tags?: string[];
  }
  let users = $state<UserRow[]>([]);
  let usersLoading = $state(true);
  let me = $state<{ id: string } | null>(null);
  let userSearch = $state('');
  let openMenu = $state<string | null>(null);

  const filteredUsers = $derived(
    users.filter((u) => {
      const q = userSearch.trim().toLowerCase();
      if (!q) return true;
      return u.username.toLowerCase().includes(q)
        || (u.email ?? '').toLowerCase().includes(q)
        || u.role.toLowerCase().includes(q);
    })
  );

  // ─── Roles state ──────────────────────────────────────────────────────
  let roles = $state<CustomRole[]>([]);
  let allPerms = $state<PermissionInfo[]>([]);
  let rolesLoading = $state(true);
  let selectedRole = $state<string>('admin');
  // Drawer state — null = closed; 'new' = create-mode; <name> = edit existing
  let drawerMode = $state<null | 'new' | string>(null);

  // ─── Edit User Modal state ────────────────────────────────────────────
  let editUser = $state<UserRow | null>(null);
  let editRole = $state('');
  let editEmail = $state('');
  let editScope = $state<Set<string>>(new Set());
  let editScopeInput = $state('');
  let editBusy = $state(false);

  function openEditUser(u: UserRow) {
    editUser = u;
    editRole = u.role;
    editEmail = u.email ?? '';
    editScope = new Set(u.scope_tags ?? []);
    editScopeInput = '';
    openMenu = null;
  }

  function toggleEditScope(tag: string) {
    const next = new Set(editScope);
    if (next.has(tag)) next.delete(tag); else next.add(tag);
    editScope = next;
  }

  function addEditScopeChip() {
    const t = editScopeInput.trim().toLowerCase();
    if (!t) return;
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(t)) {
      toast.error('Invalid tag', 'Use lowercase letters, digits, hyphens. 1-32 chars.');
      return;
    }
    if (editScope.has(t)) { editScopeInput = ''; return; }
    const next = new Set(editScope);
    next.add(t);
    editScope = next;
    editScopeInput = '';
  }

  async function saveEditUser() {
    if (!editUser) return;
    editBusy = true;
    try {
      await api.users.update(editUser.id, editEmail, editRole, [...editScope]);
      toast.success('User updated', editUser.username);
      editUser = null;
      await loadAll();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      editBusy = false;
    }
  }

  // ─── Reset Password Modal state ───────────────────────────────────────
  let resetUser = $state<UserRow | null>(null);
  let resetPw1 = $state('');
  let resetPw2 = $state('');
  let resetShow = $state(false);
  let resetBusy = $state(false);

  function openResetPassword(u: UserRow) {
    resetUser = u;
    resetPw1 = '';
    resetPw2 = '';
    resetShow = false;
    openMenu = null;
  }

  async function submitResetPassword() {
    if (!resetUser) return;
    if (resetPw1.length < 8) {
      toast.error('Too short', 'Password must be at least 8 characters.');
      return;
    }
    if (resetPw1 !== resetPw2) {
      toast.error('Passwords don’t match');
      return;
    }
    resetBusy = true;
    try {
      await api.users.changePassword(resetUser.id, resetPw1);
      toast.success('Password reset', resetUser.username);
      resetUser = null;
    } catch (err) {
      toast.error('Reset failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      resetBusy = false;
    }
  }

  // ─── Add User Modal state ─────────────────────────────────────────────
  let showInvite = $state(false);
  let inviteEmails = $state('');
  let inviteRole = $state('viewer');
  let inviteScope = $state<Set<string>>(new Set(['__all__']));
  let inviteAuth = $state<'local' | 'oidc'>('local');
  let inviteExpiry = $state<'1d' | '3d' | '7d' | '14d' | '30d'>('7d');
  let inviteEnforce2fa = $state(true);
  let invitePassword = $state('');
  let inviteBusy = $state(false);
  // After sendInvitations() runs, this holds the generated links so
  // the admin can copy them out and share manually (no SMTP). Empty
  // means "back on the input form".
  let inviteLinks = $state<Array<{ email: string; url: string }>>([]);
  let allHostTags = $state<string[]>([]);

  const inviteEmailList = $derived(
    inviteEmails.split(/[,\n]/).map((s) => s.trim()).filter(Boolean)
  );

  // ─── Loaders ─────────────────────────────────────────────────────────
  // Stable display order for built-in roles: descending privilege tier
  // (admin → host-admin → deployer → operator → viewer). Custom roles
  // come after, in API order (which is alphabetical from listFromDB
  // today; switching to created_at ordering is a backend follow-up).
  const BUILTIN_ORDER = ['admin', 'host-admin', 'deployer', 'operator', 'viewer'];
  function sortRoles(rs: CustomRole[]): CustomRole[] {
    const idx = (r: CustomRole) => {
      if (!r.builtin) return 1000; // built-ins first, then customs
      const i = BUILTIN_ORDER.indexOf(r.name);
      return i < 0 ? 999 : i;
    };
    return [...rs].sort((a, b) => {
      const da = idx(a), db = idx(b);
      if (da !== db) return da - db;
      return a.name.localeCompare(b.name);
    });
  }

  async function loadAll() {
    if (!allowed('users.update')) return;
    usersLoading = true;
    rolesLoading = true;
    try {
      const [u, r, p, m, t, h, s] = await Promise.all([
        api.users.list(),
        api.roles.list(),
        api.roles.permissions(),
        api.users.me().catch(() => null),
        api.hosts.allTags().catch(() => []),
        api.hosts.list().catch(() => []),
        api.stacks.list().catch(() => [])
      ]);
      users = u;
      roles = sortRoles(r);
      allPerms = p;
      me = m as { id: string } | null;
      allHostTags = t;
      allHostsList = h;
      allStacksList = s;
      // First-time selection only — don't override an explicit pick the
      // user already made (otherwise switching tabs would reset it).
      if (!selectedRole || !roles.find((x) => x.name === selectedRole)) {
        selectedRole = roles[0]?.name ?? 'admin';
      }
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      usersLoading = false;
      rolesLoading = false;
    }
  }

  // Mount-only load — `$effect(() => loadAll())` self-triggered because
  // loadAll both reads and writes the `roles` state, so Svelte 5's auto-
  // tracking reran the effect on every assignment. onMount runs once.
  onMount(() => { loadAll(); });

  // ─── User actions ─────────────────────────────────────────────────────
  async function deleteUser(u: UserRow) {
    if (!(await confirm.ask({
      title: `Remove ${u.username}`,
      message: `Remove user "${u.username}"?`,
      body: 'All their API tokens are revoked. Active sessions log out on their next refresh.',
      confirmLabel: 'Remove', danger: true
    }))) return;
    try {
      await api.users.delete(u.id);
      toast.success('Removed', u.username);
      await loadAll();
    } catch (err) {
      toast.error('Remove failed', err instanceof ApiError ? err.message : undefined);
    }
    openMenu = null;
  }

  // ─── Add user / Invite ────────────────────────────────────────────────
  function genTempPassword(): string {
    const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789';
    let pw = '';
    for (let i = 0; i < 16; i++) {
      pw += chars[Math.floor(Math.random() * chars.length)];
    }
    return pw;
  }

  async function sendInvitations() {
    if (inviteEmailList.length === 0) return;
    inviteBusy = true;
    const links: Array<{ email: string; url: string }> = [];
    let failCount = 0;
    const scopeTags = inviteScope.has('__all__') ? [] : [...inviteScope];
    for (const email of inviteEmailList) {
      try {
        const res = await api.users.createInvite({
          role: inviteRole,
          scope_tags: scopeTags,
          email_hint: email,
        });
        links.push({ email, url: res.accept_url });
      } catch (err) {
        failCount++;
        toast.error(`Invite for ${email} failed`, err instanceof ApiError ? err.message : undefined);
      }
    }
    inviteBusy = false;
    if (links.length > 0) {
      // Switch the modal into "show me the links" mode. Admin
      // copies + shares them manually; no SMTP involvement.
      inviteLinks = links;
      toast.success(
        `${links.length} invite link${links.length === 1 ? '' : 's'} generated`,
        'Copy each link and share it with the recipient',
      );
      inviteEmails = '';
      invitePassword = '';
      await loadAll();
    }
  }

  async function copyInviteLink(url: string) {
    await copyWithToast(url, 'Link copied');
  }

  function closeInviteModal() {
    showInvite = false;
    inviteLinks = [];
    inviteEmails = '';
    invitePassword = '';
  }

  function toggleInviteScope(id: string) {
    const next = new Set(inviteScope);
    if (id === '__all__') {
      next.clear();
      next.add('__all__');
    } else {
      next.delete('__all__');
      if (next.has(id)) next.delete(id); else next.add(id);
      if (next.size === 0) next.add('__all__');
    }
    inviteScope = next;
  }

  // ─── Permissions matrix helpers ───────────────────────────────────────
  // RBAC v2 matrix shape: 4 fixed standard verb columns + a per-category
  // "Sensitive operations" sub-section for perms flagged sensitive=true
  // by the catalog (exec, browse, migrate, etc.). Categories that don't
  // populate every standard column render `—` cells (visual N/A).
  const STANDARD_VERBS = ['view', 'deploy', 'update', 'delete'] as const;

  const permsMatrix = $derived.by(() => {
    const groups = new Map<string, { standard: Map<string, PermissionInfo>; sensitive: PermissionInfo[] }>();
    for (const p of allPerms) {
      const c = p.category || 'Other';
      if (!groups.has(c)) groups.set(c, { standard: new Map(), sensitive: [] });
      const g = groups.get(c)!;
      if (p.sensitive) {
        g.sensitive.push(p);
      } else if ((STANDARD_VERBS as readonly string[]).includes(p.verb)) {
        g.standard.set(p.verb, p);
      } else {
        // Non-standard non-sensitive verb (e.g., "scan", "tag") — surface
        // in the sensitive section so it doesn't get lost.
        g.sensitive.push(p);
      }
    }
    return [...groups.entries()].map(([category, g]) => ({ category, standard: g.standard, sensitive: g.sensitive }));
  });

  function categoryIcon(cat: string) {
    switch (cat) {
      case 'Containers': return ContainerIcon;
      case 'Stacks': return Layers;
      case 'Images': return ImageIcon;
      case 'Volumes': return HardDrive;
      case 'Networks': return Network;
      case 'Hosts': return Server;
      case 'Users': return UsersIcon;
      case 'Roles': return Shield;
      case 'Audit': return Eye;
      case 'System': return Shield;
      default: return Shield;
    }
  }

  // ─── Edit Role Drawer state ───────────────────────────────────────────
  let drawerName = $state('');
  let drawerDisplay = $state('');
  let drawerDescription = $state('');
  let drawerPerms = $state<Set<string>>(new Set());
  // Scope state — Set of encoded entries: "host:<id>" / "stack:<name>" /
  // "host_tag:<tag>". Empty = unscoped (= access to everything the perms
  // allow). RoleScope[] gets serialised on save.
  let drawerScopes = $state<Set<string>>(new Set());
  let drawerBusy = $state(false);

  // Lists driving the scope picker tree.
  let allHostsList = $state<HostInfo[]>([]);
  let allStacksList = $state<StackListEntry[]>([]);

  function encodeScope(s: RoleScope): string {
    return `${s.scope_type}:${s.scope_value}`;
  }
  function decodeScope(enc: string): RoleScope | null {
    const idx = enc.indexOf(':');
    if (idx < 0) return null;
    const type = enc.slice(0, idx);
    const value = enc.slice(idx + 1);
    if (type !== 'host' && type !== 'stack' && type !== 'host_tag') return null;
    return { scope_type: type, scope_value: value };
  }
  function toggleDrawerScope(enc: string) {
    const next = new Set(drawerScopes);
    if (next.has(enc)) next.delete(enc); else next.add(enc);
    drawerScopes = next;
  }

  // Per-section search state for the scope picker. With 50+ hosts/stacks
  // a flat chip-grid becomes unusable; the search input filters the
  // visible chips while keeping any already-selected items pinned at
  // the top of each section so they're never hidden by the filter.
  let scopeHostQuery = $state('');
  let scopeTagQuery = $state('');
  let scopeStackQuery = $state('');

  // SCOPE_SHOW_LIMIT is the threshold past which a section auto-hides
  // unselected items unless a search query is entered. Keeps the
  // drawer compact for fleets with hundreds of stacks.
  const SCOPE_SHOW_LIMIT = 12;

  function filterScopeItems<T>(items: T[], q: string, label: (x: T) => string, isSelected: (x: T) => boolean): { visible: T[]; hidden: number } {
    const trimmed = q.trim().toLowerCase();
    const matches = trimmed
      ? items.filter((x) => label(x).toLowerCase().includes(trimmed))
      : items;
    if (trimmed) return { visible: matches, hidden: 0 };
    if (matches.length <= SCOPE_SHOW_LIMIT) return { visible: matches, hidden: 0 };
    // Without a query: show only selected + first SCOPE_SHOW_LIMIT items
    // and report the remainder as hidden (UI shows "+ N more" hint).
    const selected = matches.filter(isSelected);
    const rest = matches.filter((x) => !isSelected(x));
    const visible = [...selected, ...rest.slice(0, Math.max(0, SCOPE_SHOW_LIMIT - selected.length))];
    return { visible, hidden: matches.length - visible.length };
  }

  const visibleHosts = $derived(filterScopeItems(allHostsList, scopeHostQuery, (h) => h.name, (h) => drawerScopes.has(`host:${h.id}`)));
  const visibleHostTags = $derived(filterScopeItems(allHostTags, scopeTagQuery, (t) => t, (t) => drawerScopes.has(`host_tag:${t}`)));
  const visibleStacks = $derived(filterScopeItems(allStacksList, scopeStackQuery, (s) => s.name, (s) => drawerScopes.has(`stack:${s.name}`)));

  function openDrawer(name: string | 'new') {
    drawerMode = name;
    if (name === 'new') {
      drawerName = '';
      drawerDisplay = '';
      drawerDescription = '';
      drawerPerms = new Set();
      drawerScopes = new Set();
    } else {
      const r = roles.find((x) => x.name === name);
      if (!r) return;
      drawerName = r.name;
      drawerDisplay = r.display;
      drawerDescription = r.description ?? '';
      drawerPerms = new Set(r.permissions);
      drawerScopes = new Set((r.scopes ?? []).map(encodeScope));
    }
  }

  function togglePerm(perm: string) {
    const next = new Set(drawerPerms);
    if (next.has(perm)) next.delete(perm); else next.add(perm);
    drawerPerms = next;
  }

  async function saveRole() {
    drawerBusy = true;
    try {
      const scopes: RoleScope[] = [];
      for (const enc of drawerScopes) {
        const s = decodeScope(enc);
        if (s) scopes.push(s);
      }
      const payload = {
        display: drawerDisplay || drawerName,
        description: drawerDescription,
        permissions: [...drawerPerms],
        scopes
      };
      if (drawerMode === 'new') {
        await api.roles.create({ name: drawerName, ...payload });
        toast.success('Role created', drawerName);
        selectedRole = drawerName;
      } else {
        await api.roles.update(drawerName, payload);
        toast.success('Role updated', drawerName);
      }
      drawerMode = null;
      await loadAll();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      drawerBusy = false;
    }
  }

  async function duplicateRole(r: CustomRole) {
    const newName = window.prompt(`Duplicate "${r.name}" — new role name:`, `${r.name}-copy`);
    if (!newName) return;
    try {
      await api.roles.create({
        name: newName,
        display: `Copy of ${r.display}`,
        description: r.description,
        permissions: r.permissions
      });
      toast.success('Duplicated', newName);
      selectedRole = newName;
      await loadAll();
    } catch (err) {
      toast.error('Duplicate failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteRole(r: CustomRole) {
    if (r.builtin) return;
    if (!(await confirm.ask({
      title: `Delete role ${r.name}`,
      message: `Delete role "${r.name}"?`,
      body: 'Users currently assigned to this role lose its permissions immediately on next request. Reassign them first if you don’t want that.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.roles.delete(r.name);
      toast.success('Deleted', r.name);
      if (selectedRole === r.name) selectedRole = roles.find((x) => x.builtin)?.name ?? 'admin';
      await loadAll();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // ─── Helpers ──────────────────────────────────────────────────────────
  function initialsOf(name: string): string {
    return name.split(/[\s._-]+/).slice(0, 2).map((p) => p[0] || '').join('').toUpperCase();
  }

  function rolePillClass(role: string): string {
    if (role === 'admin') return 'dm-pill dm-pill-warning';
    if (role === 'operator') return 'dm-pill dm-pill-success';
    if (role === 'viewer') return 'dm-pill dm-pill-neutral';
    return 'dm-pill dm-pill-custom';
  }

  function membersOf(role: string): UserRow[] {
    return users.filter((u) => u.role === role);
  }

  const selectedRoleObj = $derived(roles.find((r) => r.name === selectedRole) ?? null);
  const selectedRolePerms = $derived(new Set(selectedRoleObj?.permissions ?? []));
  const totalPerms = $derived(allPerms.length);
  const grantedCount = $derived(
    allPerms.filter((p) => selectedRolePerms.has(p.name)).length
  );
</script>

<section class="ed-users">
  {#if !allowed('users.update')}
    <div class="dm-card" style="padding: 32px;">
      <EmptyState icon={UsersIcon} title="Admin-only" description="User and role management requires user-manage permission." />
    </div>
  {:else}
    <header class="ed-users-header">
      <div class="ed-users-header-text">
        <h1 class="ed-title ed-users-title">Users &amp; Roles</h1>
        <p class="ed-subtitle ed-users-subtitle">
          {users.length} user{users.length === 1 ? '' : 's'} · {roles.length} role{roles.length === 1 ? '' : 's'}
        </p>
      </div>
    </header>

    <!-- Tab strip -->
    <div class="ed-tabs ed-users-tabs">
      <button
        type="button"
        class="ed-tab"
        class:active={sub === 'users'}
        onclick={() => (sub = 'users')}
      >
        <UsersIcon size={13} strokeWidth={1.5} />
        Users <span class="ed-count">{users.length}</span>
      </button>
      <button
        type="button"
        class="ed-tab"
        class:active={sub === 'roles'}
        onclick={() => (sub = 'roles')}
      >
        <Shield size={13} strokeWidth={1.5} />
        Roles <span class="ed-count">{roles.length}</span>
      </button>
    </div>

    {#if sub === 'users'}
      <!-- ─── Users tab ─── -->
      <div class="ed-users-toolbar">
        <div class="ed-users-search">
          <Search size={13} strokeWidth={1.5} class="ed-users-search-icon" />
          <input
            type="text"
            class="ed-underline-input"
            placeholder="filter by name, email, or role…"
            bind:value={userSearch}
          />
        </div>
        <span class="ed-users-search-count">
          {filteredUsers.length} of {users.length}
        </span>
        <div class="ed-users-toolbar-actions">
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            onclick={() => (showInvite = true)}
          >
            <Plus size={13} strokeWidth={1.5} /> Invite user
          </button>
        </div>
      </div>

      {#if usersLoading && users.length === 0}
        <div class="dm-card" style="padding: 22px;">
          <Skeleton width="80%" height="6rem" />
        </div>
      {:else if filteredUsers.length === 0}
        <div class="dm-card" style="padding: 32px;">
          <EmptyState icon={UsersIcon} title="No users match" description={userSearch ? 'Adjust your search.' : 'Invite the first user.'} />
        </div>
      {:else}
        <div class="dm-card ed-users-table-wrap">
          <table class="ed-table ed-users-table">
            <thead>
              <tr>
                <th>User</th>
                <th>Email</th>
                <th>Role</th>
                <th class="center">2FA</th>
                <th>Scope</th>
                <th>Last seen</th>
                <th class="actions"></th>
              </tr>
            </thead>
            <tbody>
              {#each filteredUsers as u (u.id)}
                <tr class:self={me?.id === u.id}>
                  <td>
                    <div class="ed-user-cell">
                      <span class="ed-user-avatar">{initialsOf(u.username)}</span>
                      <div class="ed-user-name-block">
                        <span class="ed-user-name">
                          {u.username}
                          {#if me?.id === u.id}
                            <span class="ed-user-self">— this is you</span>
                          {/if}
                        </span>
                      </div>
                    </div>
                  </td>
                  <td class="font-mono ed-user-email">
                    {#if u.email}{u.email}{:else}<span class="ed-user-email-empty">none</span>{/if}
                  </td>
                  <td>
                    <span class={rolePillClass(u.role)}>
                      <span class="dm-pill-dot"></span>{u.role}
                    </span>
                  </td>
                  <td class="center">
                    <!-- 2FA column — placeholder until MFA is wired in
                         backend (see project_users_roles_open.md). All
                         users render as "no 2FA" today; the column slot
                         is here so we don't reshuffle the table later. -->
                    <Unlock size={13} strokeWidth={1.5} class="ed-user-2fa-off" />
                  </td>
                  <td class="font-mono ed-user-scope">
                    {#if !u.scope_tags || u.scope_tags.length === 0}
                      <span class="ed-user-scope-all">all hosts</span>
                    {:else}
                      {u.scope_tags.join(', ')}
                    {/if}
                  </td>
                  <td class="font-mono ed-user-seen">
                    <!-- Last seen — see project_users_roles_open.md.
                         Backend doesn't track yet; renders "—". -->
                    <span class="ed-user-seen-empty">—</span>
                  </td>
                  <td class="actions">
                    <div class="ed-user-actions">
                      <button
                        type="button"
                        class="dm-btn dm-btn-ghost dm-btn-xs"
                        onclick={(e) => { e.stopPropagation(); openMenu = openMenu === u.id ? null : u.id; }}
                        aria-label="User actions"
                      >
                        <MoreVertical size={13} strokeWidth={1.5} />
                      </button>
                      {#if openMenu === u.id}
                        <div class="ed-user-menu" role="menu">
                          <button
                            type="button"
                            class="ed-user-menu-item"
                            onclick={() => openEditUser(u)}
                          >
                            Edit role &amp; scope
                          </button>
                          <button
                            type="button"
                            class="ed-user-menu-item"
                            onclick={() => openResetPassword(u)}
                          >
                            Reset password
                          </button>
                          <button
                            type="button"
                            class="ed-user-menu-item"
                            disabled
                            title="Suspend will land with the user-disable backend slice (see memory)."
                          >
                            Suspend
                          </button>
                          <button
                            type="button"
                            class="ed-user-menu-item ed-user-menu-item-danger"
                            disabled={me?.id === u.id}
                            onclick={() => { if (me?.id !== u.id) deleteUser(u); }}
                            title={me?.id === u.id ? 'You can’t remove yourself.' : undefined}
                          >
                            Remove
                          </button>
                        </div>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

      {/if}
    {:else}
      <!-- ─── Roles tab ─── -->
      {#if rolesLoading && roles.length === 0}
        <div class="dm-card" style="padding: 22px; margin-top: 24px;">
          <Skeleton width="80%" height="6rem" />
        </div>
      {:else}
        <div class="ed-roles-layout">
          <!-- Left rail -->
          <aside class="ed-roles-rail">
            <div class="ed-roles-rail-head">
              <Eyebrow>{roles.length} roles</Eyebrow>
              <button
                type="button"
                class="dm-btn dm-btn-ghost dm-btn-xs"
                onclick={() => openDrawer('new')}
              >
                <Plus size={11} strokeWidth={1.5} /> New
              </button>
            </div>

            {#each roles as r (r.name)}
              {@const granted = r.permissions.length}
              <button
                type="button"
                class="ed-role-rail-item"
                class:active={selectedRole === r.name}
                onclick={() => (selectedRole = r.name)}
              >
                <div class="ed-role-rail-head">
                  <span class="ed-role-rail-dot ed-role-rail-dot--{r.name}"></span>
                  <span class="ed-role-rail-name" class:custom={!r.builtin}>
                    {r.display || r.name}
                  </span>
                  {#if r.builtin}
                    <Lock size={10} strokeWidth={1.5} class="ed-role-rail-lock" />
                  {:else}
                    <span class="ed-role-rail-custom-tag">custom</span>
                  {/if}
                </div>
                <div class="ed-role-rail-blurb">
                  {r.description || (r.builtin ? 'Built-in role.' : 'Custom role.')}
                </div>
                <div class="ed-role-rail-foot">
                  <span class="ed-role-rail-count">
                    {granted} / {totalPerms} perms
                  </span>
                  <span class="ed-role-rail-members">
                    · {membersOf(r.name).length} member{membersOf(r.name).length === 1 ? '' : 's'}
                  </span>
                </div>
              </button>
            {/each}
          </aside>

          <!-- Right detail -->
          <div class="ed-roles-detail">
            {#if selectedRoleObj}
              <div class="ed-roles-detail-head">
                <div class="ed-roles-detail-head-text">
                  <Eyebrow>Permissions matrix</Eyebrow>
                  <h2 class="ed-roles-detail-name">
                    <span class="ed-roles-detail-name-text">{selectedRoleObj.display || selectedRoleObj.name}</span>
                    {#if selectedRoleObj.builtin}
                      <span class="ed-roles-readonly">read-only</span>
                    {/if}
                  </h2>
                  <p class="ed-roles-detail-meta">
                    <span>{grantedCount} of {totalPerms} permissions granted.</span>
                    {#if !selectedRoleObj.builtin && selectedRoleObj.scopes && selectedRoleObj.scopes.length > 0}
                      <span class="ed-roles-detail-scope">· Scope: {selectedRoleObj.scopes.map((s) => s.scope_value).join(', ')}</span>
                    {/if}
                  </p>
                </div>
                <div class="ed-actions ed-roles-detail-actions">
                  <button
                    type="button"
                    class="dm-btn dm-btn-ghost dm-btn-sm"
                    onclick={() => duplicateRole(selectedRoleObj)}
                  >
                    <Copy size={12} strokeWidth={1.5} />
                    {selectedRoleObj.builtin ? 'Duplicate as custom' : 'Duplicate'}
                  </button>
                  {#if !selectedRoleObj.builtin}
                    <button
                      type="button"
                      class="dm-btn dm-btn-secondary dm-btn-sm"
                      onclick={() => openDrawer(selectedRoleObj.name)}
                    >
                      <Edit3 size={12} strokeWidth={1.5} /> Edit role
                    </button>
                    <button
                      type="button"
                      class="dm-btn dm-btn-ghost dm-btn-sm dm-btn-danger"
                      onclick={() => deleteRole(selectedRoleObj)}
                    >
                      <Trash2 size={12} strokeWidth={1.5} />
                    </button>
                  {/if}
                </div>
              </div>

              <!-- Matrix card — fixed verb columns (view/deploy/update/
                   delete) plus a per-category "Sensitive" sub-row for
                   exec / browse / migrate / etc. Read-only for built-in
                   roles, click-to-toggle would be wired via the Edit
                   drawer for custom roles. -->
              <div class="dm-card ed-perm-matrix">
                <div class="ed-perm-matrix-head">
                  <span class="ed-perm-matrix-resource">Resource</span>
                  {#each STANDARD_VERBS as v (v)}
                    <span class="ed-perm-matrix-verb">{v}</span>
                  {/each}
                </div>
                {#each permsMatrix as group (group.category)}
                  {@const Icon = categoryIcon(group.category)}
                  <div class="ed-perm-matrix-row">
                    <span class="ed-perm-matrix-resource">
                      <Icon size={13} strokeWidth={1.5} />
                      <span>{group.category}</span>
                    </span>
                    {#each STANDARD_VERBS as v (v)}
                      {@const p = group.standard.get(v)}
                      {#if p}
                        {@const on = selectedRolePerms.has(p.name)}
                        <button
                          type="button"
                          class="ed-perm-cell"
                          class:on
                          class:locked={selectedRoleObj.builtin}
                          class:danger={p.danger_level === 'high' && on}
                          title="{p.display_name} — {p.description}"
                          tabindex={selectedRoleObj.builtin ? -1 : 0}
                          aria-label="{p.display_name}"
                          disabled
                        >
                          {#if on}
                            <Check size={11} strokeWidth={1.5} />
                          {:else if selectedRoleObj.builtin}
                            ·
                          {:else}
                            ☐
                          {/if}
                        </button>
                      {:else}
                        <span class="ed-perm-cell ed-perm-cell-na" title="not applicable">—</span>
                      {/if}
                    {/each}
                  </div>
                  {#if group.sensitive.length > 0}
                    <div class="ed-perm-sensitive">
                      <span class="ed-perm-sensitive-label">sensitive ops →</span>
                      <div class="ed-perm-sensitive-cells">
                        {#each group.sensitive as p (p.name)}
                          {@const on = selectedRolePerms.has(p.name)}
                          <button
                            type="button"
                            class="ed-perm-sensitive-pill"
                            class:on
                            class:danger={p.danger_level === 'high'}
                            title="{p.display_name} — {p.description}"
                            disabled
                          >
                            {#if on}<Check size={10} strokeWidth={1.5} />{:else}<span class="ed-perm-sensitive-off"></span>{/if}
                            {p.verb}
                          </button>
                        {/each}
                      </div>
                    </div>
                  {/if}
                {/each}
              </div>

              <!-- Members list -->
              {#if membersOf(selectedRoleObj.name).length > 0}
                <section class="ed-roles-members">
                  <Eyebrow>Members · {membersOf(selectedRoleObj.name).length}</Eyebrow>
                  <div class="ed-roles-members-list">
                    {#each membersOf(selectedRoleObj.name) as u (u.id)}
                      <div class="ed-roles-member-row">
                        <span class="ed-user-avatar">{initialsOf(u.username)}</span>
                        <span class="ed-roles-member-name">{u.username}</span>
                        {#if u.email}
                          <span class="ed-roles-member-email">{u.email}</span>
                        {/if}
                        {#if u.scope_tags && u.scope_tags.length > 0}
                          <span class="ed-roles-member-scope">scope: {u.scope_tags.join(', ')}</span>
                        {/if}
                      </div>
                    {/each}
                  </div>
                </section>
              {:else}
                <p class="ed-roles-members-empty">No users assigned to this role yet.</p>
              {/if}
            {/if}
          </div>
        </div>
      {/if}
    {/if}
  {/if}
</section>

<!-- ─── Invite User Modal ─── -->
{#if showInvite}
  <div class="ed-modal-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget) closeInviteModal(); }}>
    <div class="ed-modal ed-invite-modal" role="dialog" aria-modal="true">
      <header class="ed-modal-head">
        <div>
          <Eyebrow>Invite users</Eyebrow>
          <h2 class="ed-modal-title">
            {inviteLinks.length > 0 ? 'Share these links' : 'Generate invite links'}
          </h2>
        </div>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={closeInviteModal} aria-label="Close">
          <X size={13} strokeWidth={1.5} />
        </button>
      </header>

      {#if inviteLinks.length > 0}
        <div class="ed-modal-body">
          <p class="ed-modal-hint">
            Copy each link and share it with the recipient via your tool of choice
            (Slack, signal, whatever). Links expire in 24 hours and are single-use.
          </p>
          <div class="ed-invite-links">
            {#each inviteLinks as link (link.email)}
              <div class="ed-invite-link-row">
                <div class="ed-invite-link-email">{link.email}</div>
                <input class="dm-input ed-invite-link-url" value={link.url} readonly />
                <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm"
                        onclick={() => copyInviteLink(link.url)}>
                  Copy
                </button>
              </div>
            {/each}
          </div>
        </div>
        <footer class="ed-modal-foot">
          <span class="ed-modal-foot-status">
            {inviteLinks.length} link{inviteLinks.length === 1 ? '' : 's'} generated
          </span>
          <div class="ed-actions">
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={closeInviteModal}>Done</button>
          </div>
        </footer>
      {:else}

      <div class="ed-modal-body">
        <p class="ed-modal-hint">
          One-time invite links — no SMTP involved. You generate the links here and
          share them with the recipients yourself (Slack, signal, etc.). Each link
          provisions a new user with the role + scope you pick below.
        </p>

        <div class="ed-field">
          <label class="ed-field-label">
            Emails · {inviteEmailList.length}
            <span class="ed-field-hint">comma-separated or one per line</span>
          </label>
          <textarea
            class="dm-input ed-invite-emails"
            rows="3"
            placeholder="alex@haus.lan, mira@haus.lan&#10;or one per line"
            bind:value={inviteEmails}
          ></textarea>
        </div>

        <div class="ed-invite-grid-2">
          <div class="ed-field">
            <label class="ed-field-label">Role</label>
            <select class="dm-input" bind:value={inviteRole}>
              {#each roles as r (r.name)}
                <option value={r.name}>{r.display || r.name}</option>
              {/each}
            </select>
          </div>
          <div class="ed-field">
            <label class="ed-field-label">Auth method</label>
            <div class="ed-seg-radio">
              <button
                type="button"
                class="ed-seg-radio-item"
                class:active={inviteAuth === 'local'}
                onclick={() => (inviteAuth = 'local')}
              >Local password</button>
              <button
                type="button"
                class="ed-seg-radio-item ed-seg-radio-item-disabled"
                disabled
                title="OIDC ships with the SSO slice — see memory."
              >OIDC</button>
            </div>
          </div>
        </div>

        <div class="ed-field">
          <label class="ed-field-label">
            Scope · which hosts
            <span class="ed-field-hint">
              {inviteScope.has('__all__') ? 'all hosts' : `${inviteScope.size} tag${inviteScope.size === 1 ? '' : 's'}`}
            </span>
          </label>
          <div class="ed-host-chip-grid">
            <button
              type="button"
              class="ed-host-chip"
              class:on={inviteScope.has('__all__')}
              onclick={() => toggleInviteScope('__all__')}
            >
              <Server size={11} strokeWidth={1.5} />
              <span>all hosts</span>
              {#if inviteScope.has('__all__')}
                <Check size={11} strokeWidth={1.5} class="ed-host-chip-check" />
              {/if}
            </button>
            {#each allHostTags as tag (tag)}
              <button
                type="button"
                class="ed-host-chip"
                class:on={inviteScope.has(tag)}
                onclick={() => toggleInviteScope(tag)}
              >
                <span class="ed-host-chip-tag-mark">#</span>
                <span>{tag}</span>
                {#if inviteScope.has(tag)}
                  <Check size={11} strokeWidth={1.5} class="ed-host-chip-check" />
                {/if}
              </button>
            {/each}
          </div>
          {#if allHostTags.length === 0}
            <p class="ed-field-empty">No host tags defined yet — scope falls back to all hosts. Add tags from a host's detail page.</p>
          {/if}
        </div>

        <div class="ed-invite-grid-2">
          <div class="ed-field">
            <label class="ed-field-label">Initial password</label>
            <input
              type="text"
              class="dm-input"
              placeholder="Leave empty for random"
              bind:value={invitePassword}
            />
            <p class="ed-field-hint" style="margin-top: 4px;">
              {invitePassword ? 'Same password for every user above.' : 'A random 16-char password is generated per user.'}
            </p>
          </div>
          <div class="ed-field">
            <label class="ed-field-label">
              Link expiry
              <span class="ed-field-hint">used by email-link slice</span>
            </label>
            <select class="dm-input" bind:value={inviteExpiry} disabled>
              <option value="1d">1 day</option>
              <option value="3d">3 days</option>
              <option value="7d">7 days</option>
              <option value="14d">14 days</option>
              <option value="30d">30 days</option>
            </select>
          </div>
        </div>

        {#if inviteEmailList.length > 0}
          <div class="ed-callout">
            <div>
              <strong>{inviteEmailList.length}</strong>
              {inviteEmailList.length === 1 ? 'user gets' : 'users get'} a
              <em class="ed-accent">{inviteRole}</em> account
              scoped to <em class="ed-accent">{inviteScope.has('__all__') ? 'all hosts' : `${inviteScope.size} tag${inviteScope.size === 1 ? '' : 's'}`}</em>.
              {invitePassword ? 'Shared initial password.' : 'Random initial password per user.'}
            </div>
          </div>
        {/if}
      </div>

      <footer class="ed-modal-foot">
        <span class="ed-modal-foot-status">
          {inviteEmailList.length === 0 ? 'No emails yet' : `${inviteEmailList.length} ready`}
        </span>
        <div class="ed-actions">
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={closeInviteModal}>Cancel</button>
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            disabled={inviteEmailList.length === 0 || inviteBusy}
            onclick={sendInvitations}
          >
            <Send size={12} strokeWidth={1.5} />
            {inviteBusy ? 'Generating…' : `Generate ${inviteEmailList.length || ''} link${inviteEmailList.length === 1 ? '' : 's'}`}
          </button>
        </div>
      </footer>
      {/if}
    </div>
  </div>
{/if}

<!-- ─── Edit User Modal ─── -->
{#if editUser}
  <div class="ed-modal-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget) editUser = null; }}>
    <div class="ed-modal" role="dialog" aria-modal="true" style="width: min(520px, 92vw);">
      <header class="ed-modal-head">
        <div>
          <Eyebrow>Edit · user</Eyebrow>
          <h2 class="ed-modal-title">{editUser.username}</h2>
        </div>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (editUser = null)} aria-label="Close">
          <X size={13} strokeWidth={1.5} />
        </button>
      </header>

      <div class="ed-modal-body">
        <div class="ed-field">
          <label class="ed-field-label">Email</label>
          <input
            type="email"
            class="ed-soft-input"
            placeholder="user@example.com"
            bind:value={editEmail}
          />
        </div>

        <div class="ed-field">
          <label class="ed-field-label">Role</label>
          <select class="dm-input" bind:value={editRole}>
            {#each roles as r (r.name)}
              <option value={r.name}>{r.display || r.name}</option>
            {/each}
          </select>
        </div>

        <!-- Advanced disclosure: per-user scope_tags. The role's own
             scope already restricts what this user can do; user-level
             scope_tags only NARROW further (intersection). Hidden by
             default since most admins don't need it. -->
        <details class="ed-advanced">
          <summary class="ed-advanced-summary">
            Advanced — per-user scope override
            <span class="ed-field-hint">
              {editScope.size === 0 ? 'inherits role scope' : `narrows to ${editScope.size} tag${editScope.size === 1 ? '' : 's'}`}
            </span>
          </summary>
          <p class="ed-advanced-hint">
            Optional — restricts this single user to a subset of the hosts their role allows. Effective scope = role.scope ∩ user.scope_tags. Empty = no narrowing.
          </p>
          <div class="ed-host-chip-grid">
            {#each allHostTags as tag (tag)}
              <button
                type="button"
                class="ed-host-chip"
                class:on={editScope.has(tag)}
                onclick={() => toggleEditScope(tag)}
              >
                <span class="ed-host-chip-tag-mark">#</span>
                <span>{tag}</span>
                {#if editScope.has(tag)}
                  <Check size={11} strokeWidth={1.5} class="ed-host-chip-check" />
                {/if}
              </button>
            {/each}
          </div>
          <div class="ed-edit-scope-add">
            <input
              type="text"
              class="ed-underline-input"
              placeholder="add custom tag…"
              bind:value={editScopeInput}
              onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addEditScopeChip(); } }}
            />
            <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={addEditScopeChip} disabled={!editScopeInput.trim()}>
              Add
            </button>
          </div>
          {#if [...editScope].some((t) => !allHostTags.includes(t))}
            <div class="ed-edit-scope-extra">
              <span class="ed-field-hint">Extra tags (not yet on any host):</span>
              {#each [...editScope].filter((t) => !allHostTags.includes(t)) as tag (tag)}
                <span class="ed-host-chip on">
                  <span class="ed-host-chip-tag-mark">#</span>
                  <span>{tag}</span>
                  <button type="button" class="ed-host-chip-remove" onclick={() => toggleEditScope(tag)} aria-label="Remove">
                    <X size={10} strokeWidth={1.5} />
                  </button>
                </span>
              {/each}
            </div>
          {/if}
          <p class="ed-field-hint" style="margin-top: 6px; text-transform: none; letter-spacing: 0.02em;">
            Tags here filter this user's view across host-tagged resources, never broaden the role's scope.
          </p>
        </details>
      </div>

      <footer class="ed-modal-foot">
        <span class="ed-modal-foot-status">
          {editScope.size === 0 ? 'inherits role scope' : `${editScope.size} extra narrowing tag${editScope.size === 1 ? '' : 's'}`}
        </span>
        <div class="ed-actions">
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (editUser = null)}>Cancel</button>
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={editBusy} onclick={saveEditUser}>
            {editBusy ? 'Saving…' : 'Save changes'}
          </button>
        </div>
      </footer>
    </div>
  </div>
{/if}

<!-- ─── Reset Password Modal ─── -->
{#if resetUser}
  <div class="ed-modal-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget) resetUser = null; }}>
    <div class="ed-modal" role="dialog" aria-modal="true" style="width: min(440px, 92vw);">
      <header class="ed-modal-head">
        <div>
          <Eyebrow>Reset · password</Eyebrow>
          <h2 class="ed-modal-title">{resetUser.username}</h2>
        </div>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (resetUser = null)} aria-label="Close">
          <X size={13} strokeWidth={1.5} />
        </button>
      </header>

      <div class="ed-modal-body">
        <p class="ed-modal-hint">
          The user is signed out on next refresh and will need to log in with the new password. Any active sessions revoke automatically.
        </p>

        <div class="ed-field">
          <label class="ed-field-label">New password</label>
          <div class="ed-pw-row">
            <input
              type={resetShow ? 'text' : 'password'}
              class="dm-input font-mono"
              placeholder="at least 8 characters"
              bind:value={resetPw1}
            />
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs ed-pw-eye" onclick={() => (resetShow = !resetShow)} aria-label={resetShow ? 'Hide' : 'Show'}>
              {#if resetShow}<EyeOff size={13} strokeWidth={1.5} />{:else}<Eye size={13} strokeWidth={1.5} />{/if}
            </button>
          </div>
        </div>

        <div class="ed-field">
          <label class="ed-field-label">Confirm</label>
          <input
            type={resetShow ? 'text' : 'password'}
            class="dm-input font-mono"
            placeholder="repeat"
            bind:value={resetPw2}
          />
          {#if resetPw1 && resetPw2 && resetPw1 !== resetPw2}
            <p class="ed-field-hint" style="color: var(--color-danger-400); text-transform: none; letter-spacing: 0.02em;">
              Passwords don’t match.
            </p>
          {/if}
        </div>
      </div>

      <footer class="ed-modal-foot">
        <span class="ed-modal-foot-status">
          {resetPw1.length === 0 ? 'No password yet' : resetPw1.length < 8 ? `${resetPw1.length} / 8 chars` : `${resetPw1.length} chars · ok`}
        </span>
        <div class="ed-actions">
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (resetUser = null)}>Cancel</button>
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            disabled={resetBusy || resetPw1.length < 8 || resetPw1 !== resetPw2}
            onclick={submitResetPassword}
          >
            {resetBusy ? 'Resetting…' : 'Reset password'}
          </button>
        </div>
      </footer>
    </div>
  </div>
{/if}

<!-- ─── Edit Role Drawer ─── -->
{#if drawerMode !== null}
  <div class="ed-drawer-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget) drawerMode = null; }}>
    <aside class="ed-drawer ed-role-drawer" role="dialog" aria-modal="true">
      <header class="ed-drawer-head">
        <div>
          <Eyebrow>{drawerMode === 'new' ? 'New custom role' : 'Edit · custom role'}</Eyebrow>
          <h2 class="ed-drawer-title">{drawerMode === 'new' ? (drawerName || 'unnamed') : drawerName}</h2>
        </div>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (drawerMode = null)} aria-label="Close">
          <X size={13} strokeWidth={1.5} />
        </button>
      </header>

      <div class="ed-drawer-body">
        {#if drawerMode === 'new'}
          <div class="ed-field">
            <label class="ed-field-label">Role name</label>
            <input
              type="text"
              class="ed-soft-input font-mono"
              placeholder="e.g. backup-operator"
              bind:value={drawerName}
            />
            <p class="ed-field-hint" style="margin-top: 4px;">
              Lowercase, hyphens, no spaces. Used as identifier — can't be changed later.
            </p>
          </div>
        {/if}

        <div class="ed-field">
          <label class="ed-field-label">Display name</label>
          <input
            type="text"
            class="ed-soft-input"
            placeholder="Backup Operator"
            bind:value={drawerDisplay}
          />
        </div>

        <div class="ed-field">
          <label class="ed-field-label">Description</label>
          <textarea
            class="ed-soft-input"
            rows="2"
            placeholder="One line that explains what this role can do."
            bind:value={drawerDescription}
          ></textarea>
        </div>

        <!-- ─── Scope tree picker ─── -->
        <div class="ed-drawer-scope">
          <Eyebrow>
            Scope · {drawerScopes.size === 0 ? 'all hosts' : `${drawerScopes.size} entr${drawerScopes.size === 1 ? 'y' : 'ies'}`}
          </Eyebrow>
          <p class="ed-field-hint" style="text-transform: none; letter-spacing: 0.02em; margin: 6px 0 10px;">
            Empty scope = role can act on every host and every stack its permissions allow. Add entries to narrow it. Scope rows combine with OR — a role scoped to host A AND stack monitoring matches either.
          </p>

          <div class="ed-scope-tree">
            {#if allHostsList.length > 0}
              <fieldset class="ed-scope-section">
                <legend>
                  <span>Hosts</span>
                  <span class="ed-scope-section-count">{allHostsList.length}</span>
                </legend>
                {#if allHostsList.length > SCOPE_SHOW_LIMIT}
                  <input type="text" class="ed-soft-input ed-scope-search" placeholder="filter hosts…" bind:value={scopeHostQuery} />
                {/if}
                <div class="ed-scope-chips">
                  {#each visibleHosts.visible as h (h.id)}
                    {@const enc = `host:${h.id}`}
                    {@const on = drawerScopes.has(enc)}
                    <button type="button" class="ed-scope-chip" class:on onclick={() => toggleDrawerScope(enc)}>
                      <Server size={11} strokeWidth={1.5} />
                      <span>{h.name}</span>
                      {#if h.kind === 'local'}<span class="ed-scope-chip-meta">· this server</span>{/if}
                      {#if on}<Check size={11} strokeWidth={1.5} />{/if}
                    </button>
                  {/each}
                </div>
                {#if visibleHosts.hidden > 0}
                  <span class="ed-scope-more">+ {visibleHosts.hidden} more — type to filter</span>
                {/if}
              </fieldset>
            {/if}

            {#if allHostTags.length > 0}
              <fieldset class="ed-scope-section">
                <legend>
                  <span>Host tags</span>
                  <span class="ed-scope-section-count">{allHostTags.length}</span>
                </legend>
                {#if allHostTags.length > SCOPE_SHOW_LIMIT}
                  <input type="text" class="ed-soft-input ed-scope-search" placeholder="filter tags…" bind:value={scopeTagQuery} />
                {/if}
                <div class="ed-scope-chips">
                  {#each visibleHostTags.visible as tag (tag)}
                    {@const enc = `host_tag:${tag}`}
                    {@const on = drawerScopes.has(enc)}
                    <button type="button" class="ed-scope-chip" class:on onclick={() => toggleDrawerScope(enc)} title="All hosts carrying the tag #{tag}">
                      <span class="ed-scope-chip-tag-mark">#</span>
                      <span>{tag}</span>
                      {#if on}<Check size={11} strokeWidth={1.5} />{/if}
                    </button>
                  {/each}
                </div>
                {#if visibleHostTags.hidden > 0}
                  <span class="ed-scope-more">+ {visibleHostTags.hidden} more — type to filter</span>
                {/if}
              </fieldset>
            {/if}

            {#if allStacksList.length > 0}
              <fieldset class="ed-scope-section">
                <legend>
                  <span>Stacks</span>
                  <span class="ed-scope-section-count">{allStacksList.length}</span>
                </legend>
                {#if allStacksList.length > SCOPE_SHOW_LIMIT}
                  <input type="text" class="ed-soft-input ed-scope-search" placeholder="filter stacks…" bind:value={scopeStackQuery} />
                {/if}
                <div class="ed-scope-chips">
                  {#each visibleStacks.visible as s (s.name)}
                    {@const enc = `stack:${s.name}`}
                    {@const on = drawerScopes.has(enc)}
                    <button type="button" class="ed-scope-chip" class:on onclick={() => toggleDrawerScope(enc)}>
                      <Layers size={11} strokeWidth={1.5} />
                      <span>{s.name}</span>
                      {#if on}<Check size={11} strokeWidth={1.5} />{/if}
                    </button>
                  {/each}
                </div>
                {#if visibleStacks.hidden > 0}
                  <span class="ed-scope-more">+ {visibleStacks.hidden} more — type to filter</span>
                {/if}
              </fieldset>
            {/if}

            {#if allHostsList.length === 0 && allHostTags.length === 0 && allStacksList.length === 0}
              <p class="ed-field-empty">No hosts, tags, or stacks defined yet — scope can't be narrowed.</p>
            {/if}
          </div>
        </div>

        <!-- ─── Permissions matrix in drawer (fixed verbs + sensitive sub-section) ─── -->
        <div class="ed-drawer-perms">
          <Eyebrow>Permissions · {drawerPerms.size} of {totalPerms}</Eyebrow>

          {#each permsMatrix as group (group.category)}
            {@const Icon = categoryIcon(group.category)}
            <fieldset class="ed-drawer-perm-group">
              <legend>
                <Icon size={13} strokeWidth={1.5} />
                <span>{group.category}</span>
              </legend>
              <div class="ed-drawer-perm-row-grid">
                {#each STANDARD_VERBS as v (v)}
                  {@const p = group.standard.get(v)}
                  {#if p}
                    {@const on = drawerPerms.has(p.name)}
                    <button
                      type="button"
                      class="ed-drawer-verb-toggle"
                      class:on
                      class:danger={p.danger_level === 'high'}
                      onclick={() => togglePerm(p.name)}
                      title="{p.display_name} — {p.description}"
                    >
                      <span class="ed-drawer-verb-label">{v}</span>
                      {#if on}<Check size={10} strokeWidth={1.5} />{/if}
                    </button>
                  {:else}
                    <span class="ed-drawer-verb-na">—</span>
                  {/if}
                {/each}
              </div>
              {#if group.sensitive.length > 0}
                <div class="ed-drawer-sensitive">
                  <span class="ed-drawer-sensitive-label">sensitive:</span>
                  <div class="ed-drawer-sensitive-list">
                    {#each group.sensitive as p (p.name)}
                      {@const on = drawerPerms.has(p.name)}
                      <button
                        type="button"
                        class="ed-drawer-sensitive-pill"
                        class:on
                        class:danger={p.danger_level === 'high'}
                        onclick={() => togglePerm(p.name)}
                        title="{p.display_name} — {p.description}"
                      >
                        {#if on}<Check size={10} strokeWidth={1.5} />{/if}
                        {p.verb}
                        {#if p.danger_level === 'high'}<span class="ed-drawer-perm-danger">high</span>{/if}
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}
            </fieldset>
          {/each}
        </div>
      </div>

      <footer class="ed-drawer-foot">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (drawerMode = null)}>Cancel</button>
        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          disabled={drawerBusy || (drawerMode === 'new' && !drawerName.trim())}
          onclick={saveRole}
        >
          {drawerBusy ? 'Saving…' : drawerMode === 'new' ? 'Create role' : 'Save changes'}
        </button>
      </footer>
    </aside>
  </div>
{/if}

<style>
  /* ─── Page-level layout ──────────────────────────────────────────── */
  .ed-users { display: flex; flex-direction: column; gap: 22px; }
  .ed-users-header { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; flex-wrap: wrap; }
  .ed-users-header-text { min-width: 0; flex: 1; }
  .ed-users-title { font-size: 26px; max-width: 32ch; }

  .ed-users-tabs { margin-top: 6px; }
  /* Tab-count badges use global .ed-count (square corners). Tabs add a
     little spacing so the badge sits next to the label. */
  .ed-users-tabs :global(.ed-count) { margin-left: 6px; }

  /* ─── Users tab ─── */
  .ed-users-toolbar {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    margin-top: 8px;
  }
  .ed-users-search {
    position: relative;
    flex: 1 1 280px;
    max-width: 360px;
  }
  .ed-users-search :global(.ed-users-search-icon) {
    position: absolute;
    left: 8px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
  }
  .ed-underline-input {
    width: 100%;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    padding: 6px 0 6px 22px;
    font-size: 13px;
    color: var(--fg);
    font-family: var(--font-mono);
    outline: none;
    transition: border-color 0.1s;
  }
  .ed-underline-input::placeholder { color: var(--fg-subtle); }
  .ed-underline-input:focus { border-bottom-color: var(--accent); }

  /* Underline-only input style for drawer + modal text fields. Replaces
     the boxed dm-input look with a calmer underline that matches the
     editorial mockup. Mono variant via font-mono. */
  .ed-soft-input {
    width: 100%;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    padding: 8px 2px;
    font-size: 13px;
    color: var(--fg);
    outline: none;
    transition: border-color 0.1s;
    border-radius: 0;
    resize: vertical;
  }
  .ed-soft-input::placeholder { color: var(--fg-subtle); }
  .ed-soft-input:focus { border-bottom-color: var(--accent); }
  .ed-soft-input.font-mono { font-family: var(--font-mono); letter-spacing: 0.02em; }
  .ed-users-search-count {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .ed-users-toolbar-actions {
    margin-left: auto;
    display: inline-flex;
    gap: 8px;
  }

  /* No overflow:hidden — the more-menu Dropdown is positioned absolutely
     inside its <td> and would otherwise be clipped. */
  .ed-users-table-wrap { overflow: visible; padding: 0; }
  .ed-users-table { width: 100%; border-collapse: collapse; }
  .ed-users-table thead {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
  }
  .ed-users-table th {
    text-align: left;
    padding: 10px 14px;
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 1px solid var(--border);
    font-weight: 500;
  }
  .ed-users-table th.center { text-align: center; }
  .ed-users-table th.actions { width: 36px; }
  .ed-users-table tbody tr {
    border-top: 1px solid var(--border);
  }
  .ed-users-table tbody tr.self {
    background: color-mix(in srgb, var(--color-brand-500) 5%, transparent);
  }
  .ed-users-table td {
    padding: 11px 14px;
    vertical-align: middle;
  }
  .ed-users-table td.center { text-align: center; }
  .ed-users-table td.actions { position: relative; text-align: right; }
  .ed-user-cell { display: flex; align-items: center; gap: 10px; }
  .ed-user-avatar {
    width: 28px;
    height: 28px;
    border-radius: 999px;
    background: var(--surface-hover);
    border: 1px solid var(--border-strong);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg);
    flex-shrink: 0;
  }
  .ed-user-name {
    color: var(--fg);
    font-size: 13.5px;
    font-weight: 500;
    display: inline-flex;
    align-items: baseline;
    gap: 7px;
  }
  .ed-user-self {
    font-size: 11px;
    font-weight: 400;
    color: var(--accent-fg);
    font-style: normal;
    font-family: var(--font-mono);
    letter-spacing: 0.02em;
  }
  .ed-user-email { font-size: 12.5px; color: var(--fg-muted); }
  .ed-user-email-empty { color: var(--fg-subtle); }
  .ed-user-scope { font-size: 12px; color: var(--fg-muted); max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ed-user-scope-all { color: var(--fg-subtle); }
  .ed-user-seen { font-size: 12px; color: var(--fg-muted); }
  .ed-user-seen-empty { color: var(--fg-subtle); }
  :global(.ed-user-2fa-off) { color: var(--color-warning-400); }

  .ed-user-actions { position: relative; display: inline-flex; }
  .ed-user-menu {
    position: absolute;
    right: 8px;
    top: 32px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    padding: 4px;
    z-index: 5;
    min-width: 180px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .ed-user-menu-item {
    text-align: left;
    background: transparent;
    border: 0;
    padding: 7px 10px;
    font-size: 12px;
    color: var(--fg);
    cursor: pointer;
    border-radius: 4px;
  }
  .ed-user-menu-item:hover:not(:disabled) { background: var(--surface-hover); }
  .ed-user-menu-item:disabled { color: var(--fg-subtle); cursor: not-allowed; }
  .ed-user-menu-item-danger { color: var(--color-danger-400); }
  .ed-user-menu-item-danger:disabled { color: color-mix(in srgb, var(--color-danger-400) 40%, var(--fg-subtle)); }

  /* Custom-role pill: cyan accent border */
  :global(.dm-pill-custom) {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--color-brand-500) 40%, var(--border));
  }

  /* ─── Advanced disclosure (per-user scope_tags) ─── */
  .ed-advanced {
    border-top: 1px dashed var(--border);
    padding-top: 12px;
    margin-top: 4px;
  }
  .ed-advanced-summary {
    display: flex;
    align-items: baseline;
    gap: 10px;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    list-style: none;
  }
  .ed-advanced-summary::-webkit-details-marker { display: none; }
  .ed-advanced-summary::before {
    content: '▸';
    color: var(--fg-subtle);
    transition: transform 0.1s;
    display: inline-block;
  }
  details[open] > .ed-advanced-summary::before { transform: rotate(90deg); }
  .ed-advanced-summary:hover { color: var(--fg); }
  .ed-advanced-hint {
    margin: 10px 0;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }

  /* ─── Edit user modal scope-add row + extra-tag chip remove ─── */
  .ed-edit-scope-add {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
  }
  .ed-edit-scope-add .ed-underline-input { padding-left: 0; flex: 1; }
  .ed-edit-scope-extra {
    margin-top: 8px;
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .ed-host-chip-remove {
    background: transparent;
    border: 0;
    cursor: pointer;
    padding: 0;
    display: inline-flex;
    color: var(--fg-subtle);
    margin-left: 4px;
  }
  .ed-host-chip-remove:hover { color: var(--color-danger-400); }

  /* ─── Reset-password modal ─── */
  .ed-pw-row { display: flex; align-items: center; gap: 6px; }
  .ed-pw-row .dm-input { flex: 1; }
  .ed-pw-eye { padding: 6px; }

  /* ─── Roles tab layout ─── */
  .ed-roles-layout {
    display: grid;
    grid-template-columns: 280px minmax(0, 1fr);
    gap: 28px;
    margin-top: 16px;
    align-items: start;
  }
  @media (max-width: 880px) {
    .ed-roles-layout { grid-template-columns: 1fr; }
  }

  .ed-roles-rail {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ed-roles-rail-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 4px;
  }
  .ed-role-rail-item {
    text-align: left;
    background: transparent;
    border: 0;
    border-radius: 5px;
    padding: 10px 12px;
    cursor: pointer;
    transition: background 0.1s;
    display: flex;
    flex-direction: column;
    gap: 4px;
    border-left: 2px solid transparent;
  }
  .ed-role-rail-item:hover { background: var(--surface-hover); }
  .ed-role-rail-item.active {
    background: var(--surface-hover);
    border-left-color: var(--accent);
  }
  .ed-role-rail-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ed-role-rail-dot {
    width: 8px;
    height: 8px;
    border-radius: 999px;
    background: var(--fg-subtle);
    flex-shrink: 0;
  }
  .ed-role-rail-dot--admin { background: var(--color-warning-400); }
  .ed-role-rail-dot--operator { background: var(--color-success-400); }
  .ed-role-rail-dot--viewer { background: var(--fg-subtle); }
  .ed-role-rail-name {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    letter-spacing: 0.02em;
    font-weight: 500;
  }
  .ed-role-rail-name.custom { color: var(--accent-fg); }
  :global(.ed-role-rail-lock) { margin-left: auto; color: var(--fg-subtle); }
  .ed-role-rail-custom-tag {
    margin-left: auto;
    font-size: 9.5px;
    font-family: var(--font-mono);
    letter-spacing: 0.06em;
    color: var(--accent-fg);
    text-transform: uppercase;
  }
  .ed-role-rail-blurb {
    font-size: 11.5px;
    color: var(--fg-muted);
    line-height: 1.45;
    padding-left: 16px;
  }
  .ed-role-rail-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 4px;
    padding-left: 16px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
  }

  /* ─── Roles detail ─── */
  .ed-roles-detail-head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 16px;
    margin-bottom: 18px;
  }
  .ed-roles-detail-head-text { display: flex; flex-direction: column; gap: 8px; }
  .ed-roles-detail-name {
    font-family: var(--font-sans);
    font-size: 22px;
    font-weight: 600;
    margin: 0;
    color: var(--fg);
    letter-spacing: -0.01em;
    display: inline-flex;
    align-items: baseline;
    gap: 12px;
    font-style: normal;
  }
  .ed-roles-detail-name-text { color: var(--accent-fg); }
  .ed-roles-readonly {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    font-weight: 400;
    letter-spacing: 0.04em;
  }
  .ed-roles-detail-meta {
    font-size: 12.5px;
    color: var(--fg-muted);
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .ed-roles-detail-scope {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  /* ─── Permissions matrix ─── */
  .ed-perm-matrix {
    padding: 0;
    overflow: hidden;
  }
  .ed-perm-matrix-head, .ed-perm-matrix-row {
    display: grid;
    grid-template-columns: minmax(0, 1.4fr) repeat(4, minmax(60px, 1fr));
    align-items: center;
    padding: 0 14px;
  }
  .ed-perm-matrix-head {
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
    padding-top: 10px;
    padding-bottom: 10px;
  }
  .ed-perm-matrix-row {
    border-bottom: 1px solid var(--border);
    padding-top: 10px;
    padding-bottom: 10px;
  }
  .ed-perm-matrix-row:last-child { border-bottom: 0; }
  .ed-perm-matrix-resource {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--fg);
    font-size: 13px;
    font-weight: 500;
  }
  .ed-perm-matrix-verb {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-transform: lowercase;
    letter-spacing: 0.04em;
    text-align: center;
  }
  .ed-perm-cell {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: 4px;
    background: transparent;
    border: 1px solid var(--border);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    margin: 0 auto;
  }
  .ed-perm-cell.on {
    background: color-mix(in srgb, var(--color-brand-500) 18%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 50%, var(--border));
    color: var(--accent-fg);
  }
  .ed-perm-cell.danger {
    background: color-mix(in srgb, var(--color-warning-500) 14%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 45%, var(--border));
    color: var(--color-warning-400);
  }
  .ed-perm-cell.locked { opacity: 0.7; }
  .ed-perm-cell-na { border: 0; color: var(--fg-subtle); background: transparent; }
  .ed-perm-cell:disabled { cursor: default; }

  /* ─── Sensitive-ops sub-row inside matrix ─── */
  .ed-perm-sensitive {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 14px;
    background: color-mix(in srgb, var(--color-warning-500) 4%, transparent);
    border-bottom: 1px solid var(--border);
  }
  .ed-perm-sensitive-label {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    flex-shrink: 0;
  }
  .ed-perm-sensitive-cells { display: inline-flex; gap: 6px; flex-wrap: wrap; }
  .ed-perm-sensitive-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 3px 8px;
    border-radius: 999px;
    background: transparent;
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    cursor: default;
  }
  .ed-perm-sensitive-pill.on {
    background: color-mix(in srgb, var(--color-brand-500) 16%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 50%, var(--border));
    color: var(--accent-fg);
  }
  .ed-perm-sensitive-pill.on.danger {
    background: color-mix(in srgb, var(--color-warning-500) 16%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 50%, var(--border));
    color: var(--color-warning-400);
  }
  .ed-perm-sensitive-off {
    width: 8px;
    height: 8px;
    border: 1px solid var(--border);
    border-radius: 2px;
    display: inline-block;
  }

  /* ─── Members list ─── */
  .ed-roles-members { margin-top: 24px; }
  .ed-roles-members-list {
    margin-top: 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .ed-roles-member-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    background: var(--surface);
  }
  .ed-roles-member-row + .ed-roles-member-row {
    border-top: 1px solid var(--border);
  }
  .ed-roles-member-name { font-size: 13px; color: var(--fg); }
  .ed-roles-member-email {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .ed-roles-member-scope {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    margin-left: auto;
  }
  .ed-roles-members-empty {
    margin: 18px 0 0;
    font-style: normal;
    font-size: 12.5px;
    color: var(--fg-subtle);
  }

  /* ─── Modal + Drawer chrome ─── */
  /* Backdrop matches the editorial mockup (shell.css mig-drawer-backdrop
     pattern): semi-transparent page bg with a soft blur, NOT pure black.
     Reads as "the page is veiled" rather than "the lights went out". */
  .ed-modal-backdrop, .ed-drawer-backdrop {
    position: fixed;
    inset: 0;
    background: color-mix(in srgb, var(--bg) 70%, transparent);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .ed-drawer-backdrop { justify-content: flex-end; }

  /* Modal surface uses --bg-elevated so it sits ABOVE the page tone,
     same pattern as the mockup's add-host-modal / verify-modal rules. */
  .ed-modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    width: min(620px, 92vw);
    max-height: 92vh;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .ed-modal-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    padding: 18px 22px;
    border-bottom: 1px solid var(--border);
  }
  .ed-modal-title {
    margin: 4px 0 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--fg);
    font-family: var(--font-sans);
  }
  .ed-modal-body {
    padding: 18px 22px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .ed-modal-hint {
    margin: 0 0 4px;
    font-size: 13px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .ed-modal-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 22px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
  }
  .ed-modal-foot-status {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }

  /* Drawer surface uses --bg-elevated like the mockup's mig-drawer rule
     — page bg shows through the (blurred) backdrop, drawer sits on the
     elevated surface tone above it. */
  .ed-drawer {
    width: min(560px, 96vw);
    height: 100vh;
    background: var(--bg-elevated);
    border-left: 1px solid var(--border);
    display: flex;
    flex-direction: column;
  }
  .ed-drawer-body { padding: 22px 26px; gap: 22px; }
  .ed-drawer-head { padding: 22px 26px 18px; }
  .ed-drawer-foot { padding: 16px 26px; }
  .ed-drawer-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    padding: 18px 22px;
    border-bottom: 1px solid var(--border);
  }
  .ed-drawer-title {
    margin: 4px 0 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--fg);
    font-family: var(--font-sans);
  }
  .ed-drawer-body {
    padding: 18px 22px;
    overflow-y: auto;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .ed-drawer-foot {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    padding: 14px 22px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
  }

  /* ─── Field primitive ─── */
  .ed-field { display: flex; flex-direction: column; gap: 6px; }
  .ed-field-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    display: flex;
    align-items: baseline;
    justify-content: space-between;
  }
  .ed-field-hint {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    text-transform: none;
    letter-spacing: 0.02em;
  }
  .ed-field-empty {
    margin: 6px 0 0;
    font-size: 11.5px;
    color: var(--fg-subtle);
    font-style: normal;
  }

  /* ─── Invite modal-specific ─── */
  .ed-invite-links {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 12px;
  }
  .ed-invite-link-row {
    display: grid;
    grid-template-columns: 160px 1fr auto;
    gap: 10px;
    align-items: center;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--surface-hover);
  }
  .ed-invite-link-email {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ed-invite-link-url {
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .ed-invite-emails {
    font-family: var(--font-mono);
    font-size: 12.5px;
    resize: vertical;
    min-height: 70px;
  }
  .ed-invite-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
  }
  @media (max-width: 540px) {
    .ed-invite-grid-2 { grid-template-columns: 1fr; }
  }
  .ed-seg-radio {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 5px;
    padding: 2px;
  }
  .ed-seg-radio-item {
    background: transparent;
    border: 0;
    padding: 6px 10px;
    font-size: 12px;
    font-family: var(--font-mono);
    color: var(--fg-muted);
    cursor: pointer;
    border-radius: 3px;
  }
  .ed-seg-radio-item.active {
    background: var(--bg-elevated);
    color: var(--fg);
  }
  .ed-seg-radio-item-disabled { opacity: 0.5; cursor: not-allowed; }

  .ed-host-chip-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 6px;
  }
  .ed-host-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 7px 10px;
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 5px;
    font-size: 11.5px;
    color: var(--fg-muted);
    cursor: pointer;
    text-align: left;
  }
  .ed-host-chip:hover { background: var(--surface-hover); }
  .ed-host-chip.on {
    background: color-mix(in srgb, var(--color-brand-500) 12%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 45%, var(--border));
    color: var(--accent-fg);
  }
  .ed-host-chip-tag-mark {
    font-family: var(--font-mono);
    color: var(--fg-subtle);
  }
  :global(.ed-host-chip-check) { margin-left: auto; }

  .ed-callout {
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    padding: 10px 14px;
    border-radius: 5px;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }

  /* ─── Drawer perm groups ─── */
  .ed-drawer-perms { display: flex; flex-direction: column; gap: 14px; }
  .ed-drawer-perm-group {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ed-drawer-perm-group legend {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    margin-bottom: 4px;
  }
  .ed-drawer-perm-list { display: flex; flex-direction: column; gap: 4px; }
  .ed-drawer-perm-toggle {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 9px 11px;
    border: 1px solid var(--border);
    border-radius: 5px;
    cursor: pointer;
  }
  .ed-drawer-perm-toggle:hover { background: var(--surface-hover); }
  .ed-drawer-perm-toggle.on {
    background: color-mix(in srgb, var(--color-brand-500) 8%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 40%, var(--border));
  }
  .ed-drawer-perm-toggle.danger.on {
    background: color-mix(in srgb, var(--color-warning-500) 10%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
  }
  .ed-drawer-perm-toggle input { margin-top: 3px; }
  .ed-drawer-perm-row {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1;
  }
  .ed-drawer-perm-name {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg);
    letter-spacing: 0.02em;
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .ed-drawer-perm-verb { color: var(--accent-fg); font-weight: 500; }
  .ed-drawer-perm-danger {
    font-size: 9px;
    color: var(--color-warning-400);
    background: color-mix(in srgb, var(--color-warning-500) 12%, transparent);
    padding: 1px 5px;
    border-radius: 3px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .ed-drawer-perm-desc {
    font-size: 12px;
    color: var(--fg-muted);
    line-height: 1.5;
  }
  :global(.ed-drawer-perm-check) { color: var(--accent-fg); margin-top: 3px; }

  /* ─── Drawer: scope tree picker ─── */
  .ed-drawer-scope { display: flex; flex-direction: column; gap: 8px; }
  .ed-scope-tree { display: flex; flex-direction: column; gap: 12px; margin-top: 4px; }
  .ed-scope-section {
    border: 0;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .ed-scope-section legend {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    margin-bottom: 4px;
    display: inline-flex;
    align-items: baseline;
    gap: 8px;
  }
  .ed-scope-section-count {
    font-size: 10px;
    color: var(--fg-subtle);
    background: var(--surface-hover);
    padding: 1px 6px;
    border-radius: 999px;
    letter-spacing: 0.04em;
    font-style: normal;
  }
  .ed-scope-search {
    margin-bottom: 6px;
    font-size: 12px;
  }
  .ed-scope-more {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
    margin-top: 2px;
  }
  .ed-scope-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .ed-scope-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 5px;
    font-size: 11.5px;
    color: var(--fg-muted);
    cursor: pointer;
    text-align: left;
  }
  .ed-scope-chip:hover { background: var(--surface-hover); }
  .ed-scope-chip.on {
    background: color-mix(in srgb, var(--color-brand-500) 12%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 45%, var(--border));
    color: var(--accent-fg);
  }
  .ed-scope-chip-meta { font-size: 10px; color: var(--fg-subtle); font-family: var(--font-mono); }
  .ed-scope-chip-tag-mark { font-family: var(--font-mono); color: var(--fg-subtle); }

  /* ─── Drawer: per-category perm grid (4 fixed verbs + sensitive ops) ─── */
  .ed-drawer-perm-group legend {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    margin-bottom: 4px;
  }
  .ed-drawer-perm-row-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 6px;
  }
  .ed-drawer-verb-toggle {
    display: inline-flex;
    align-items: center;
    justify-content: space-between;
    gap: 6px;
    padding: 7px 10px;
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    cursor: pointer;
    text-align: left;
  }
  .ed-drawer-verb-toggle:hover { background: var(--surface-hover); }
  .ed-drawer-verb-toggle.on {
    background: color-mix(in srgb, var(--color-brand-500) 12%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 45%, var(--border));
    color: var(--accent-fg);
  }
  .ed-drawer-verb-toggle.danger.on {
    background: color-mix(in srgb, var(--color-warning-500) 12%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 45%, var(--border));
    color: var(--color-warning-400);
  }
  .ed-drawer-verb-label { letter-spacing: 0.02em; }
  .ed-drawer-verb-na {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 7px 10px;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 11.5px;
    border: 1px dashed color-mix(in srgb, var(--border) 60%, transparent);
    border-radius: 4px;
    background: transparent;
  }
  .ed-drawer-sensitive {
    margin-top: 8px;
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 6px 0 0;
    border-top: 1px dashed var(--border);
  }
  .ed-drawer-sensitive-label {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    margin-top: 6px;
    flex-shrink: 0;
  }
  .ed-drawer-sensitive-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    flex: 1;
  }
  .ed-drawer-sensitive-pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 5px 10px;
    border-radius: 999px;
    background: transparent;
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
    cursor: pointer;
  }
  .ed-drawer-sensitive-pill:hover { background: var(--surface-hover); }
  .ed-drawer-sensitive-pill.on {
    background: color-mix(in srgb, var(--color-brand-500) 14%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-500) 45%, var(--border));
    color: var(--accent-fg);
  }
  .ed-drawer-sensitive-pill.danger.on {
    background: color-mix(in srgb, var(--color-warning-500) 14%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 45%, var(--border));
    color: var(--color-warning-400);
  }
</style>
