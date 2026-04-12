<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { auth } from '$lib/stores/auth.svelte';

  type Tab = 'account' | 'users' | 'audit';
  let tab = $state<Tab>('account');

  // ---------- Account ----------
  let me = $state<any>(null);
  let newPassword = $state('');
  let pwMsg = $state('');
  let pwErr = $state('');

  async function loadMe() {
    try {
      me = await api.users.me();
    } catch (err) {
      pwErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  async function changeOwnPassword(e: Event) {
    e.preventDefault();
    pwMsg = '';
    pwErr = '';
    if (newPassword.length < 8) {
      pwErr = 'min 8 chars';
      return;
    }
    try {
      await api.users.changePassword(me.id, newPassword);
      pwMsg = 'Password updated.';
      newPassword = '';
    } catch (err) {
      pwErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  // ---------- Users ----------
  let users = $state<Array<{ id: string; username: string; email?: string; role: string }>>([]);
  let usersErr = $state('');
  let showCreate = $state(false);
  let cUsername = $state('');
  let cPassword = $state('');
  let cRole = $state('viewer');
  let cEmail = $state('');

  async function loadUsers() {
    usersErr = '';
    try {
      users = await api.users.list();
    } catch (err) {
      usersErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  async function createUser(e: Event) {
    e.preventDefault();
    try {
      await api.users.create(cUsername, cPassword, cRole, cEmail || undefined);
      cUsername = '';
      cPassword = '';
      cEmail = '';
      cRole = 'viewer';
      showCreate = false;
      await loadUsers();
    } catch (err) {
      usersErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  async function deleteUser(id: string, username: string) {
    if (!confirm(`Delete user "${username}"?`)) return;
    try {
      await api.users.delete(id);
      await loadUsers();
    } catch (err) {
      usersErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  async function changeRole(id: string, email: string, role: string) {
    try {
      await api.users.update(id, email ?? '', role);
      await loadUsers();
    } catch (err) {
      usersErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  // ---------- Audit ----------
  let auditEntries = $state<Array<any>>([]);
  let auditErr = $state('');
  let auditLimit = $state(100);

  async function loadAudit() {
    auditErr = '';
    try {
      auditEntries = await api.audit.list(auditLimit);
    } catch (err) {
      auditErr = err instanceof ApiError ? err.message : 'failed';
    }
  }

  function actionColor(action: string): string {
    if (action.includes('delete') || action.includes('remove') || action.includes('failed')) return 'text-red-500';
    if (action.includes('create') || action.includes('deploy')) return 'text-green-500';
    if (action.includes('update') || action.includes('start') || action.includes('restart')) return 'text-blue-400';
    return 'text-[var(--muted)]';
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
</script>

<section class="space-y-4">
  <h2 class="text-xl font-semibold">Settings</h2>

  <div class="border-b border-[var(--border)] flex gap-0 flex-wrap">
    <button
      class="px-4 py-2 text-sm border-b-2 {tab === 'account' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
      onclick={() => (tab = 'account')}
    >
      Account
    </button>
    {#if isAdmin}
      <button
        class="px-4 py-2 text-sm border-b-2 {tab === 'users' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
        onclick={() => (tab = 'users')}
      >
        Users
      </button>
    {/if}
    <button
      class="px-4 py-2 text-sm border-b-2 {tab === 'audit' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
      onclick={() => (tab = 'audit')}
    >
      Audit Log
    </button>
  </div>

  {#if tab === 'account'}
    {#if me}
      <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)] space-y-2 max-w-md">
        <div class="text-xs text-[var(--muted)]">Username</div>
        <div class="font-mono">{me.username}</div>
        <div class="text-xs text-[var(--muted)] mt-3">Role</div>
        <div>
          <span class="text-xs px-2 py-0.5 rounded bg-[var(--bg)] font-mono">{me.role}</span>
        </div>
      </div>

      <form onsubmit={changeOwnPassword} class="p-4 rounded border border-[var(--border)] bg-[var(--panel)] space-y-3 max-w-md">
        <h3 class="font-semibold">Change password</h3>
        <input
          type="password"
          class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]"
          placeholder="New password (min 8 chars)"
          bind:value={newPassword}
          autocomplete="new-password"
        />
        {#if pwErr}<div class="text-sm text-red-500">{pwErr}</div>{/if}
        {#if pwMsg}<div class="text-sm text-green-500">{pwMsg}</div>{/if}
        <button type="submit" class="px-4 py-2 rounded bg-brand-500 text-white font-semibold disabled:opacity-50" disabled={newPassword.length < 8}>
          Update
        </button>
      </form>
    {/if}
  {:else if tab === 'users'}
    {#if usersErr}
      <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{usersErr}</div>
    {/if}
    <div class="flex justify-between items-center">
      <div class="text-sm text-[var(--muted)]">{users.length} users</div>
      <button class="px-3 py-1 rounded bg-brand-500 text-white text-sm" onclick={() => (showCreate = true)}>+ New User</button>
    </div>
    <div class="space-y-2">
      {#each users as u}
        <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)] flex items-center gap-3">
          <div class="flex-1 min-w-0">
            <div class="font-mono text-sm">{u.username}</div>
            <div class="text-xs text-[var(--muted)] truncate">{u.email ?? '—'}</div>
          </div>
          <select
            class="px-2 py-1 text-xs rounded border border-[var(--border)] bg-[var(--bg)]"
            value={u.role}
            onchange={(e) => changeRole(u.id, u.email ?? '', (e.target as HTMLSelectElement).value)}
            disabled={u.id === me?.id}
          >
            <option value="admin">admin</option>
            <option value="operator">operator</option>
            <option value="viewer">viewer</option>
          </select>
          <button
            class="px-2 py-1 text-xs border border-red-500/50 text-red-500 rounded disabled:opacity-30"
            onclick={() => deleteUser(u.id, u.username)}
            disabled={u.id === me?.id}
          >
            Delete
          </button>
        </div>
      {/each}
    </div>

    {#if showCreate}
      <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-10">
        <form
          onsubmit={createUser}
          class="w-full max-w-md p-6 rounded border border-[var(--border)] bg-[var(--panel)] space-y-3"
        >
          <h3 class="text-lg font-semibold">Create User</h3>
          <input class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]" placeholder="Username" bind:value={cUsername} />
          <input class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]" type="email" placeholder="Email (optional)" bind:value={cEmail} />
          <input class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]" type="password" placeholder="Password (min 8 chars)" bind:value={cPassword} autocomplete="new-password" />
          <select class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]" bind:value={cRole}>
            <option value="viewer">viewer</option>
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </select>
          <div class="flex justify-end gap-2">
            <button type="button" class="px-4 py-2 rounded border border-[var(--border)]" onclick={() => (showCreate = false)}>Cancel</button>
            <button type="submit" class="px-4 py-2 rounded bg-brand-500 text-white font-semibold" disabled={!cUsername || cPassword.length < 8}>
              Create
            </button>
          </div>
        </form>
      </div>
    {/if}
  {:else if tab === 'audit'}
    {#if auditErr}
      <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{auditErr}</div>
    {/if}
    <div class="flex items-center gap-3">
      <label class="text-sm">Limit:
        <select class="px-2 py-1 text-sm rounded border border-[var(--border)] bg-[var(--bg)] ml-1" bind:value={auditLimit} onchange={loadAudit}>
          <option value={50}>50</option>
          <option value={100}>100</option>
          <option value={500}>500</option>
          <option value={1000}>1000</option>
        </select>
      </label>
      <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={loadAudit}>Refresh</button>
      <span class="text-xs text-[var(--muted)] ml-auto">{auditEntries.length} entries</span>
    </div>
    <div class="rounded border border-[var(--border)] bg-[var(--panel)] overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-[var(--bg)] text-left text-xs text-[var(--muted)]">
          <tr>
            <th class="px-3 py-2">Timestamp</th>
            <th class="px-3 py-2">Action</th>
            <th class="px-3 py-2">Target</th>
            <th class="px-3 py-2">User</th>
            <th class="px-3 py-2">Details</th>
          </tr>
        </thead>
        <tbody>
          {#each auditEntries as e}
            <tr class="border-t border-[var(--border)]">
              <td class="px-3 py-2 font-mono text-xs whitespace-nowrap">{fmtTs(e.ts)}</td>
              <td class="px-3 py-2 font-mono text-xs {actionColor(e.action)}">{e.action}</td>
              <td class="px-3 py-2 font-mono text-xs truncate max-w-[200px]">{e.target ?? '—'}</td>
              <td class="px-3 py-2 font-mono text-xs truncate max-w-[150px]">{e.user_id?.slice(0, 8) ?? '—'}</td>
              <td class="px-3 py-2 font-mono text-xs text-[var(--muted)] truncate max-w-[300px]">{e.details ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>
