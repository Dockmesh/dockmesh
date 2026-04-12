<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { auth } from '$lib/stores/auth.svelte';
  import { Card, Button, Input, Modal, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { User, Users, Activity, Plus, Trash2, UserCog } from 'lucide-svelte';

  type Tab = 'account' | 'users' | 'audit';
  let tab = $state<Tab>('account');

  // Account
  let me = $state<any>(null);
  let newPassword = $state('');

  async function loadMe() {
    try {
      me = await api.users.me();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function changeOwnPassword(e: Event) {
    e.preventDefault();
    if (newPassword.length < 8) {
      toast.error('Password too short', 'min 8 characters');
      return;
    }
    try {
      await api.users.changePassword(me.id, newPassword);
      toast.success('Password updated');
      newPassword = '';
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // Users
  let users = $state<Array<{ id: string; username: string; email?: string; role: string }>>([]);
  let usersLoading = $state(false);
  let showCreate = $state(false);
  let cUsername = $state('');
  let cPassword = $state('');
  let cRole = $state('viewer');
  let cEmail = $state('');

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

  async function createUser(e: Event) {
    e.preventDefault();
    try {
      await api.users.create(cUsername, cPassword, cRole, cEmail || undefined);
      toast.success('User created', cUsername);
      cUsername = '';
      cPassword = '';
      cEmail = '';
      cRole = 'viewer';
      showCreate = false;
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteUser(id: string, username: string) {
    if (!confirm(`Delete user "${username}"?`)) return;
    try {
      await api.users.delete(id);
      toast.success('Deleted', username);
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function changeRole(id: string, email: string | undefined, role: string) {
    try {
      await api.users.update(id, email ?? '', role);
      toast.success('Role updated', role);
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // Audit
  let auditEntries = $state<Array<any>>([]);
  let auditLoading = $state(false);
  let auditLimit = $state(100);

  async function loadAudit() {
    auditLoading = true;
    try {
      auditEntries = await api.audit.list(auditLimit);
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      auditLoading = false;
    }
  }

  function actionVariant(action: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (action.includes('delete') || action.includes('remove') || action.includes('failed')) return 'danger';
    if (action.includes('create') || action.includes('deploy')) return 'success';
    if (action.includes('update') || action.includes('start') || action.includes('restart')) return 'info';
    return 'default';
  }

  function fmtTs(ts: string): string {
    return ts.slice(0, 19).replace('T', ' ');
  }

  $effect(() => {
    if (tab === 'account') loadMe();
    else if (tab === 'users') loadUsers();
    else if (tab === 'audit') loadAudit();
  });

  const isAdmin = $derived(auth.user?.role === 'admin');

  const tabs: Array<{ id: Tab; label: string; icon: any; show: boolean }> = $derived([
    { id: 'account', label: 'Account', icon: User, show: true },
    { id: 'users', label: 'Users', icon: Users, show: isAdmin },
    { id: 'audit', label: 'Audit Log', icon: Activity, show: true }
  ]);
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-2xl font-semibold tracking-tight">Settings</h2>
    <p class="text-sm text-[var(--fg-muted)] mt-0.5">Manage your account, users and audit trail.</p>
  </div>

  <div class="border-b border-[var(--border)] flex gap-1">
    {#each tabs.filter((t) => t.show) as t}
      {@const Icon = t.icon}
      <button
        class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2
               {tab === t.id
          ? 'border-[var(--color-brand-500)] text-[var(--fg)]'
          : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (tab = t.id)}
      >
        <Icon class="w-3.5 h-3.5" />
        {t.label}
      </button>
    {/each}
  </div>

  {#if tab === 'account'}
    {#if me}
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4 max-w-3xl">
        <Card class="p-5">
          <div class="flex items-center gap-3 mb-4">
            <div class="w-12 h-12 rounded-full bg-gradient-to-br from-brand-400 to-brand-700 flex items-center justify-center text-white font-semibold text-lg">
              {me.username[0]?.toUpperCase()}
            </div>
            <div>
              <div class="font-semibold">{me.username}</div>
              <Badge variant="info">{me.role}</Badge>
            </div>
          </div>
          {#if me.email}
            <div class="text-xs text-[var(--fg-muted)]">Email</div>
            <div class="text-sm font-mono">{me.email}</div>
          {/if}
        </Card>

        <Card class="p-5">
          <h3 class="font-semibold mb-3 text-sm">Change password</h3>
          <form onsubmit={changeOwnPassword} class="space-y-3">
            <Input
              type="password"
              placeholder="New password"
              bind:value={newPassword}
              hint="minimum 8 characters"
              autocomplete="new-password"
            />
            <Button variant="primary" type="submit" disabled={newPassword.length < 8}>
              Update password
            </Button>
          </form>
        </Card>
      </div>
    {:else}
      <Skeleton width="100%" height="12rem" />
    {/if}
  {:else if tab === 'users'}
    <div class="flex justify-between items-center">
      <div class="text-sm text-[var(--fg-muted)]">
        {users.length} {users.length === 1 ? 'user' : 'users'}
      </div>
      <Button variant="primary" onclick={() => (showCreate = true)}>
        <Plus class="w-4 h-4" /> New user
      </Button>
    </div>

    {#if usersLoading && users.length === 0}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each Array(3) as _}
            <div class="px-5 py-4 flex items-center gap-3">
              <Skeleton width="2.5rem" height="2.5rem" class="!rounded-full" />
              <Skeleton width="30%" height="1rem" />
            </div>
          {/each}
        </div>
      </Card>
    {:else}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each users as u}
            <div class="flex items-center gap-3 px-5 py-3">
              <div class="w-10 h-10 rounded-full bg-gradient-to-br from-brand-500 to-brand-700 flex items-center justify-center text-white text-sm font-semibold shrink-0">
                {u.username[0]?.toUpperCase()}
              </div>
              <div class="flex-1 min-w-0">
                <div class="font-medium text-sm truncate">{u.username}</div>
                <div class="text-xs text-[var(--fg-muted)] truncate">{u.email ?? '—'}</div>
              </div>
              <select
                class="dm-input !py-1 !px-2 !w-auto text-xs font-mono"
                value={u.role}
                onchange={(e) => changeRole(u.id, u.email, (e.target as HTMLSelectElement).value)}
                disabled={u.id === me?.id}
              >
                <option value="admin">admin</option>
                <option value="operator">operator</option>
                <option value="viewer">viewer</option>
              </select>
              <Button
                size="xs"
                variant="ghost"
                onclick={() => deleteUser(u.id, u.username)}
                disabled={u.id === me?.id}
                aria-label="Delete"
              >
                <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
              </Button>
            </div>
          {/each}
        </div>
      </Card>
    {/if}
  {:else if tab === 'audit'}
    <div class="flex items-center gap-3">
      <label class="text-sm flex items-center gap-2">
        <span class="text-[var(--fg-muted)]">Limit</span>
        <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={auditLimit} onchange={loadAudit}>
          <option value={50}>50</option>
          <option value={100}>100</option>
          <option value={500}>500</option>
          <option value={1000}>1000</option>
        </select>
      </label>
      <Button size="sm" variant="secondary" onclick={loadAudit}>Refresh</Button>
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">{auditEntries.length} entries</span>
    </div>

    {#if auditLoading && auditEntries.length === 0}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each Array(6) as _}
            <div class="px-5 py-3 flex gap-3">
              <Skeleton width="10rem" height="0.85rem" />
              <Skeleton width="8rem" height="0.85rem" />
            </div>
          {/each}
        </div>
      </Card>
    {:else if auditEntries.length === 0}
      <Card>
        <EmptyState icon={Activity} title="No audit entries yet" description="Actions like login, deploy and delete will appear here." />
      </Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-left text-xs text-[var(--fg-muted)] uppercase tracking-wider bg-[var(--bg-elevated)]">
              <tr>
                <th class="px-5 py-3 font-medium">Timestamp</th>
                <th class="px-5 py-3 font-medium">Action</th>
                <th class="px-5 py-3 font-medium">Target</th>
                <th class="px-5 py-3 font-medium">User</th>
                <th class="px-5 py-3 font-medium">Details</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each auditEntries as e}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-5 py-3 font-mono text-xs whitespace-nowrap text-[var(--fg-muted)]">{fmtTs(e.ts)}</td>
                  <td class="px-5 py-3">
                    <Badge variant={actionVariant(e.action)}>{e.action}</Badge>
                  </td>
                  <td class="px-5 py-3 font-mono text-xs truncate max-w-[200px]">{e.target ?? '—'}</td>
                  <td class="px-5 py-3 font-mono text-xs text-[var(--fg-muted)]">{e.user_id?.slice(0, 8) ?? '—'}</td>
                  <td class="px-5 py-3 font-mono text-xs text-[var(--fg-subtle)] truncate max-w-[300px]">{e.details ?? ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}
  {/if}
</section>

<Modal bind:open={showCreate} title="Create user" maxWidth="max-w-md">
  <form onsubmit={createUser} class="space-y-4" id="create-user-form">
    <Input label="Username" bind:value={cUsername} />
    <Input label="Email (optional)" type="email" bind:value={cEmail} />
    <Input label="Password" type="password" bind:value={cPassword} hint="minimum 8 characters" autocomplete="new-password" />
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Role</span>
      <select class="dm-input" bind:value={cRole}>
        <option value="viewer">viewer — read-only</option>
        <option value="operator">operator — start/stop/deploy</option>
        <option value="admin">admin — full access</option>
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
