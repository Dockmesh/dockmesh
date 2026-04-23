<script lang="ts">
  // Users & Roles — extracted from two separate Settings tabs. Combined
  // into one page because the two concepts are inseparable in practice:
  // to create a new user you pick a role; to understand what a role
  // means you look at who's assigned to it. Sub-tabs keep the views
  // visually distinct while sharing state + modals.
  import { api, ApiError, type CustomRole, type PermissionInfo } from '$lib/api';
  import { page } from '$app/stores';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Users as UsersIcon, Plus, Trash2, UserCog, ShieldCheck, KeyRound, Shield, X } from 'lucide-svelte';

  type Sub = 'users' | 'roles';
  let sub = $state<Sub>((new URLSearchParams($page.url.search).get('sub') as Sub) || 'users');

  // ===== Users state ========================================================
  let users = $state<Array<{ id: string; username: string; email?: string; role: string; scope_tags?: string[] }>>([]);
  let usersLoading = $state(false);
  let me = $state<{ id: string } | null>(null);

  let showCreate = $state(false);
  let cUsername = $state('');
  let cPassword = $state('');
  let cRole = $state('viewer');
  let cEmail = $state('');

  // Scope editor state (P.11.3)
  let showScopeFor = $state<string | null>(null);
  let scopeDraft = $state<string[]>([]);
  let scopeInput = $state('');
  let scopeSuggestions = $state<string[]>([]);
  let scopeBusy = $state(false);
  let scopeUserRole = $state('');
  let scopeUserEmail = $state('');

  async function loadUsers() {
    usersLoading = true;
    try {
      users = await api.users.list();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      usersLoading = false;
    }
  }

  async function loadMe() {
    try { me = await api.users.me() as any; } catch { /* ignore */ }
  }

  async function createUser(e: Event) {
    e.preventDefault();
    try {
      await api.users.create(cUsername, cPassword, cRole, cEmail || undefined);
      toast.success('User created', cUsername);
      cUsername = ''; cPassword = ''; cEmail = ''; cRole = 'viewer';
      showCreate = false;
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteUser(id: string, username: string) {
    if (!(await confirm.ask({ title: 'Delete user', message: `Delete user "${username}"?`, body: 'All their API tokens are revoked. Active sessions log out on their next refresh.', confirmLabel: 'Delete', danger: true }))) return;
    try {
      await api.users.delete(id);
      toast.success('Deleted', username);
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function changeRole(id: string, email: string | undefined, role: string, scopeTags: string[] | undefined) {
    try {
      await api.users.update(id, email ?? '', role, scopeTags ?? []);
      toast.success('Role updated', role);
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function resetUserMFA(userId: string, username: string) {
    if (!(await confirm.ask({ title: 'Reset 2FA', message: `Reset 2FA for "${username}"?`, body: 'The user will need to re-enroll from the Account tab on next login.', confirmLabel: 'Reset', danger: true }))) return;
    try {
      await api.mfa.reset(userId);
      toast.success('2FA reset', username);
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function openScope(user: { id: string; email?: string; role: string; scope_tags?: string[] }) {
    showScopeFor = user.id;
    scopeDraft = [...(user.scope_tags ?? [])];
    scopeInput = '';
    scopeUserRole = user.role;
    scopeUserEmail = user.email ?? '';
    try { scopeSuggestions = await api.hosts.allTags(); } catch { scopeSuggestions = []; }
  }

  function addScopeDraft(tag: string) {
    const t = tag.trim().toLowerCase();
    if (!t || scopeDraft.includes(t)) { scopeInput = ''; return; }
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(t)) {
      toast.error('Invalid tag', 'Use lowercase letters, digits, hyphens. 1-32 chars.');
      return;
    }
    scopeDraft = [...scopeDraft, t];
    scopeInput = '';
  }

  function removeScopeDraft(t: string) {
    scopeDraft = scopeDraft.filter((x) => x !== t);
  }

  async function saveScope() {
    if (!showScopeFor) return;
    scopeBusy = true;
    try {
      await api.users.update(showScopeFor, scopeUserEmail, scopeUserRole, scopeDraft);
      toast.success(scopeDraft.length === 0 ? 'Scope cleared (all hosts)' : 'Scope updated');
      showScopeFor = null;
      await loadUsers();
    } catch (err) {
      toast.error('Failed to save scope', err instanceof ApiError ? err.message : undefined);
    } finally {
      scopeBusy = false;
    }
  }

  // ===== Roles state =========================================================
  let roles = $state<CustomRole[]>([]);
  let allPerms = $state<PermissionInfo[]>([]);
  let rolesLoading = $state(false);
  let showRole = $state(false);
  let editingRole = $state<CustomRole | null>(null);
  let roleForm = $state({ name: '', display: '', permissions: [] as string[] });
  let viewingRole = $state<CustomRole | null>(null);
  let showView = $state(false);

  function openViewRole(r: CustomRole) {
    viewingRole = r;
    showView = true;
  }

  async function loadRoles() {
    rolesLoading = true;
    try {
      [roles, allPerms] = await Promise.all([api.roles.list(), api.roles.permissions()]);
    } catch (err) {
      toast.error('Failed to load roles', err instanceof ApiError ? err.message : undefined);
    } finally {
      rolesLoading = false;
    }
  }

  function openNewRole() {
    editingRole = null;
    roleForm = { name: '', display: '', permissions: [] };
    showRole = true;
  }

  function openEditRole(r: CustomRole) {
    editingRole = r;
    roleForm = { name: r.name, display: r.display, permissions: [...r.permissions] };
    showRole = true;
  }

  async function saveRole(e: Event) {
    e.preventDefault();
    try {
      if (editingRole) {
        await api.roles.update(editingRole.name, { display: roleForm.display, permissions: roleForm.permissions });
      } else {
        await api.roles.create(roleForm);
      }
      showRole = false;
      toast.success(editingRole ? 'Role updated' : 'Role created');
      await loadRoles();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteRole(name: string) {
    if (!(await confirm.ask({ title: 'Delete role', message: `Delete role "${name}"?`, body: 'Users currently assigned to this role lose its permissions immediately on next request.', confirmLabel: 'Delete', danger: true }))) return;
    try {
      await api.roles.delete(name);
      toast.success('Role deleted');
      await loadRoles();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function togglePerm(perm: string) {
    if (roleForm.permissions.includes(perm)) {
      roleForm.permissions = roleForm.permissions.filter(p => p !== perm);
    } else {
      roleForm.permissions = [...roleForm.permissions, perm];
    }
  }

  $effect(() => {
    if (!allowed('user.manage')) return;
    loadUsers();
    loadRoles();
    loadMe();
  });
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-2xl font-semibold tracking-tight">Users &amp; Roles</h2>
    <p class="text-sm text-[var(--fg-muted)] mt-0.5">
      User accounts and their access scopes, plus the role definitions that
      grant permissions. Users pick from these roles; roles map to permissions.
    </p>
  </div>

  {#if !allowed('user.manage')}
    <Card>
      <EmptyState icon={UsersIcon} title="Admin-only" description="User and role management requires user-manage permission." />
    </Card>
  {:else}
    <!-- Sub-tabs: Users | Roles. Flat pair — no third tab expected. -->
    <div class="flex gap-1 border-b border-[var(--border)]">
      <button
        type="button"
        class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {sub === 'users' ? 'border-[var(--accent)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (sub = 'users')}
      >
        <UsersIcon class="w-3.5 h-3.5 inline -mt-0.5" />
        Users
        <span class="text-xs text-[var(--fg-muted)] ml-1">({users.length})</span>
      </button>
      <button
        type="button"
        class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {sub === 'roles' ? 'border-[var(--accent)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (sub = 'roles')}
      >
        <Shield class="w-3.5 h-3.5 inline -mt-0.5" />
        Roles
        <span class="text-xs text-[var(--fg-muted)] ml-1">({roles.length})</span>
      </button>
    </div>

    {#if sub === 'users'}
      <section class="space-y-4">
        <div class="flex items-center justify-between gap-3 flex-wrap">
          <span class="text-sm text-[var(--fg-muted)]">{users.length} user{users.length === 1 ? '' : 's'}</span>
          <Button variant="primary" onclick={() => (showCreate = true)}>
            <Plus class="w-4 h-4" /> New user
          </Button>
        </div>

        {#if usersLoading && users.length === 0}
          <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
        {:else if users.length === 0}
          <Card><EmptyState icon={UsersIcon} title="No users" description="Create the first user account." /></Card>
        {:else}
          <Card>
            <div class="overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                    <th class="text-left px-5 py-3">User</th>
                    <th class="text-left px-3 py-3">Email</th>
                    <th class="text-left px-3 py-3">Role</th>
                    <th class="text-left px-3 py-3">Scope</th>
                    <th class="text-center px-3 py-3">2FA</th>
                    <th class="text-right px-3 py-3 w-28">Actions</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-[var(--border)]">
                  {#each users as u}
                    <tr class="hover:bg-[var(--surface-hover)]">
                      <td class="px-5 py-3">
                        <div class="flex items-center gap-2.5">
                          <div class="w-8 h-8 rounded-full bg-gradient-to-br from-brand-500 to-brand-700 flex items-center justify-center text-white text-xs font-semibold shrink-0">
                            {u.username[0]?.toUpperCase()}
                          </div>
                          <span class="font-medium text-sm">{u.username}</span>
                        </div>
                      </td>
                      <td class="px-3 py-3 text-xs text-[var(--fg-muted)]">{u.email ?? '—'}</td>
                      <td class="px-3 py-3">
                        <select
                          class="dm-input !py-1 !px-2 !w-auto text-xs font-mono"
                          value={u.role}
                          onchange={(e) => changeRole(u.id, u.email, (e.target as HTMLSelectElement).value, u.scope_tags)}
                          disabled={u.id === me?.id}
                        >
                          {#each roles as r}
                            <option value={r.name}>{r.display || r.name}</option>
                          {/each}
                        </select>
                      </td>
                      <td class="px-3 py-3">
                        <button
                          class="flex items-center gap-1 hover:underline text-left"
                          onclick={() => openScope(u)}
                          aria-label="Edit scope for {u.username}"
                        >
                          {#if !u.scope_tags || u.scope_tags.length === 0}
                            <span class="text-xs text-[var(--fg-muted)] italic">all hosts</span>
                          {:else}
                            <div class="flex flex-wrap gap-1">
                              {#each u.scope_tags.slice(0, 3) as t}
                                <span class="inline-flex items-center h-5 px-1.5 rounded text-[10px] font-mono bg-[var(--surface-hover)] text-[var(--fg-muted)] border border-[var(--border)]">
                                  {t}
                                </span>
                              {/each}
                              {#if u.scope_tags.length > 3}
                                <span class="text-[10px] text-[var(--fg-muted)]">+{u.scope_tags.length - 3}</span>
                              {/if}
                            </div>
                          {/if}
                        </button>
                      </td>
                      <td class="px-3 py-3 text-center">
                        {#if (u as any).mfa_enabled}
                          <ShieldCheck class="w-4 h-4 text-[var(--color-success-400)] inline" />
                        {:else}
                          <span class="text-xs text-[var(--fg-subtle)]">—</span>
                        {/if}
                      </td>
                      <td class="px-3 py-3">
                        <div class="flex gap-0.5 justify-end">
                          {#if (u as any).mfa_enabled}
                            <button class="p-1.5 rounded-md text-[var(--color-warning-400)] hover:bg-[var(--surface-hover)]" title="Reset 2FA" onclick={() => resetUserMFA(u.id, u.username)}>
                              <KeyRound class="w-3.5 h-3.5" />
                            </button>
                          {/if}
                          <button
                            class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]"
                            title="Delete user"
                            onclick={() => deleteUser(u.id, u.username)}
                            disabled={u.id === me?.id}
                          >
                            <Trash2 class="w-3.5 h-3.5" />
                          </button>
                        </div>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </Card>
        {/if}
      </section>
    {:else}
      <section class="space-y-4">
        <div class="flex justify-between items-center">
          <span class="text-sm text-[var(--fg-muted)]">{roles.length} role{roles.length === 1 ? '' : 's'}</span>
          <Button variant="primary" onclick={openNewRole}>
            <Plus class="w-4 h-4" /> New role
          </Button>
        </div>

        {#if rolesLoading}
          <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
        {:else}
          <Card>
            <div class="overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                    <th class="text-left px-5 py-3">Role</th>
                    <th class="text-left px-3 py-3">Identifier</th>
                    <th class="text-left px-3 py-3">Type</th>
                    <th class="text-right px-3 py-3">Permissions</th>
                    <th class="text-right px-3 py-3 w-24">Actions</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-[var(--border)]">
                  {#each roles as role}
                    <tr class="hover:bg-[var(--surface-hover)]">
                      <td class="px-5 py-3 font-medium">{role.display}</td>
                      <td class="px-3 py-3 font-mono text-xs text-[var(--fg-muted)]">{role.name}</td>
                      <td class="px-3 py-3">
                        {#if role.builtin}<Badge variant="default">built-in</Badge>{:else}<Badge variant="info">custom</Badge>{/if}
                      </td>
                      <td class="px-3 py-3 text-right tabular-nums">
                        <button
                          type="button"
                          class="text-[var(--accent)] hover:underline tabular-nums"
                          title="View permissions"
                          onclick={() => openViewRole(role)}
                        >
                          {role.permissions.length}
                        </button>
                      </td>
                      <td class="px-3 py-3">
                        <div class="flex gap-0.5 justify-end">
                          {#if !role.builtin}
                            <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Edit" onclick={() => openEditRole(role)}>
                              <UserCog class="w-3.5 h-3.5" />
                            </button>
                            <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteRole(role.name)}>
                              <Trash2 class="w-3.5 h-3.5" />
                            </button>
                          {/if}
                        </div>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </Card>
        {/if}
      </section>
    {/if}
  {/if}
</section>

<!-- Create user modal -->
<Modal bind:open={showCreate} title="Create user" maxWidth="max-w-md">
  <form onsubmit={createUser} class="space-y-4" id="create-user-form">
    <Input label="Username" bind:value={cUsername} />
    <Input label="Email (optional)" type="email" bind:value={cEmail} />
    <Input label="Password" type="password" bind:value={cPassword} hint="minimum 8 characters" autocomplete="new-password" />
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Role</span>
      <select class="dm-input" bind:value={cRole}>
        {#each roles as r}
          <option value={r.name}>{r.display || r.name}</option>
        {/each}
      </select>
    </div>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="create-user-form" disabled={!cUsername || cPassword.length < 8}>
      Create user
    </Button>
  {/snippet}
</Modal>

<!-- Scope-edit modal -->
<Modal
  open={showScopeFor !== null}
  onclose={() => (showScopeFor = null)}
  title="Edit user scope"
  maxWidth="max-w-md"
>
  <div class="space-y-4">
    <p class="text-sm text-[var(--fg-muted)]">
      Limit this user's role to hosts with matching tags. Leave empty to grant
      access across all hosts — the default for new users. Tags use
      <span class="font-medium text-[var(--fg)]">OR semantics</span>: a user with
      scope <code class="font-mono text-xs">[prod, staging]</code> sees any host
      tagged prod <em>or</em> staging.
    </p>

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-2">Allowed host tags</span>
      {#if scopeDraft.length === 0}
        <div class="px-3 py-2 rounded border border-dashed border-[var(--border)] text-sm text-[var(--fg-muted)] italic">
          No scope — user has access to all hosts.
        </div>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each scopeDraft as t}
            <span class="inline-flex items-center gap-1 h-6 px-2 rounded text-xs font-mono bg-[var(--surface-hover)] border border-[var(--border)]">
              {t}
              <button
                class="ml-0.5 text-[var(--fg-muted)] hover:text-[var(--color-danger-400)]"
                onclick={() => removeScopeDraft(t)}
                aria-label="Remove {t}"
                type="button"
              >
                <X class="w-3 h-3" />
              </button>
            </span>
          {/each}
        </div>
      {/if}
    </div>

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Add tag</span>
      <div class="flex gap-2">
        <input
          class="dm-input flex-1"
          placeholder="prod, team-backend..."
          bind:value={scopeInput}
          list="scope-suggestions"
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addScopeDraft(scopeInput); } }}
        />
        <datalist id="scope-suggestions">
          {#each scopeSuggestions.filter((s) => !scopeDraft.includes(s)) as s}
            <option value={s}></option>
          {/each}
        </datalist>
        <Button variant="secondary" onclick={() => addScopeDraft(scopeInput)}>Add</Button>
      </div>
      {#if scopeSuggestions.length > 0}
        <p class="text-xs text-[var(--fg-muted)] mt-1">Tags from your fleet autocomplete.</p>
      {/if}
    </div>
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showScopeFor = null)}>Cancel</Button>
    <Button variant="primary" loading={scopeBusy} onclick={saveScope}>Save scope</Button>
  {/snippet}
</Modal>

<!-- Role create/edit modal with grouped permissions -->
<Modal bind:open={showRole} title={editingRole ? `Edit role: ${editingRole.display}` : 'Create role'} maxWidth="max-w-lg">
  <form onsubmit={saveRole} id="role-form" class="space-y-4">
    {#if !editingRole}
      <Input label="Name" placeholder="devops" hint="Lowercase, used as identifier" bind:value={roleForm.name} />
    {/if}
    <Input label="Display name" placeholder="DevOps Engineer" bind:value={roleForm.display} />
    <div>
      <div class="text-xs font-medium text-[var(--fg-muted)] mb-2">Permissions</div>
      {#if allPerms.length > 0}
      {@const groups = Object.entries(
        allPerms.reduce((acc, p) => {
          const cat = p.name.includes('.') ? p.name.split('.')[0] : 'general';
          if (!acc[cat]) acc[cat] = [];
          acc[cat].push(p);
          return acc;
        }, {} as Record<string, typeof allPerms>)
      ).sort(([a], [b]) => a.localeCompare(b))}
      <div class="space-y-3 max-h-72 overflow-auto">
        {#each groups as [group, perms]}
          <div class="border border-[var(--border)] rounded-lg">
            <div class="px-3 py-2 bg-[var(--surface)] text-xs font-medium uppercase tracking-wider text-[var(--fg-muted)] flex items-center justify-between">
              <span>{group}</span>
              <label class="flex items-center gap-1 cursor-pointer text-[10px] font-normal normal-case">
                <input
                  type="checkbox"
                  checked={perms.every(p => roleForm.permissions.includes(p.name))}
                  onchange={() => {
                    const allIn = perms.every(p => roleForm.permissions.includes(p.name));
                    if (allIn) {
                      roleForm.permissions = roleForm.permissions.filter(p => !perms.some(pp => pp.name === p));
                    } else {
                      const toAdd = perms.map(p => p.name).filter(n => !roleForm.permissions.includes(n));
                      roleForm.permissions = [...roleForm.permissions, ...toAdd];
                    }
                  }}
                  class="accent-[var(--color-brand-500)]"
                />
                all
              </label>
            </div>
            <div class="divide-y divide-[var(--border)]">
              {#each perms as perm}
                <label class="flex items-center gap-2 px-3 py-1.5 cursor-pointer hover:bg-[var(--surface-hover)] text-xs">
                  <input
                    type="checkbox"
                    checked={roleForm.permissions.includes(perm.name)}
                    onchange={() => togglePerm(perm.name)}
                    class="accent-[var(--color-brand-500)]"
                  />
                  <code class="font-mono">{perm.name}</code>
                  <span class="text-[var(--fg-muted)] ml-auto">{perm.description}</span>
                </label>
              {/each}
            </div>
          </div>
        {/each}
      </div>
      {/if}
    </div>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showRole = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="role-form" disabled={!roleForm.display || (!editingRole && !roleForm.name)}>
      {editingRole ? 'Update' : 'Create'}
    </Button>
  {/snippet}
</Modal>

<!-- Read-only permission view for any role (built-in or custom). Opened by
     clicking the permission count in the roles table. -->
<Modal
  bind:open={showView}
  onclose={() => (viewingRole = null)}
  title={viewingRole ? `Permissions: ${viewingRole.display}` : ''}
  maxWidth="max-w-lg"
>
  {#if viewingRole}
    <div class="space-y-3">
      <div class="flex items-center gap-2 text-xs text-[var(--fg-muted)]">
        <code class="font-mono">{viewingRole.name}</code>
        {#if viewingRole.builtin}<Badge variant="default">built-in</Badge>{:else}<Badge variant="info">custom</Badge>{/if}
        <span class="ml-auto tabular-nums">{viewingRole.permissions.length} permission{viewingRole.permissions.length === 1 ? '' : 's'}</span>
      </div>
      {#if viewingRole.permissions.length === 0}
        <p class="text-sm text-[var(--fg-muted)] px-1 py-4">This role has no permissions assigned.</p>
      {:else if allPerms.length > 0}
        {@const assigned = new Set(viewingRole.permissions)}
        {@const groups = Object.entries(
          allPerms.filter(p => assigned.has(p.name)).reduce((acc, p) => {
            const cat = p.name.includes('.') ? p.name.split('.')[0] : 'general';
            if (!acc[cat]) acc[cat] = [];
            acc[cat].push(p);
            return acc;
          }, {} as Record<string, typeof allPerms>)
        ).sort(([a], [b]) => a.localeCompare(b))}
        <div class="space-y-3 max-h-80 overflow-auto">
          {#each groups as [group, perms]}
            <div class="border border-[var(--border)] rounded-lg">
              <div class="px-3 py-2 bg-[var(--surface)] text-xs font-medium uppercase tracking-wider text-[var(--fg-muted)]">
                {group}
              </div>
              <div class="divide-y divide-[var(--border)]">
                {#each perms as perm}
                  <div class="flex items-center gap-2 px-3 py-1.5 text-xs">
                    <code class="font-mono">{perm.name}</code>
                    <span class="text-[var(--fg-muted)] ml-auto">{perm.description}</span>
                  </div>
                {/each}
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <ul class="space-y-1 max-h-80 overflow-auto text-xs">
          {#each viewingRole.permissions as p}
            <li><code class="font-mono">{p}</code></li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showView = false; viewingRole = null; }}>Close</Button>
  {/snippet}
</Modal>
