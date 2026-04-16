<script lang="ts">
  import { api, ApiError, type BackupStatus, type CustomRole, type PermissionInfo } from '$lib/api';
  import { page } from '$app/stores';
  import { auth } from '$lib/stores/auth.svelte';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { User, Users, Activity, Plus, Trash2, UserCog, ShieldCheck, ShieldOff, Copy, KeyRound, Link2, Globe, ExternalLink, HardDrive, ShieldAlert, AlertCircle, Shield } from 'lucide-svelte';
  import type { OIDCProvider, OIDCProviderInput } from '$lib/api';

  type Tab = 'account' | 'users' | 'audit' | 'sso' | 'system' | 'roles';
  // Initial tab honours ?tab=<id> so the sidebar last-backup pill can
  // deep-link straight into the System tab. Snapping back to the first
  // visible tab still happens below for invalid/unauthorised IDs.
  let tab = $state<Tab>((new URLSearchParams($page.url.search).get('tab') as Tab) || 'account');

  // --- System tab (P.6.5) ---
  let backupStatus = $state<BackupStatus | null>(null);
  let backupLoading = $state(false);
  let backupBusy = $state(false);
  async function loadBackup() {
    backupLoading = true;
    try {
      backupStatus = await api.system.backupStatus();
    } catch (err) {
      toast.error('Failed to load backup status', err instanceof ApiError ? err.message : undefined);
    } finally {
      backupLoading = false;
    }
  }
  async function toggleBackup(enabled: boolean) {
    backupBusy = true;
    try {
      backupStatus = await api.system.setBackupEnabled(enabled);
      toast.success(enabled ? 'Automated backups enabled' : 'Automated backups disabled');
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      backupBusy = false;
    }
  }
  function fmtAge(secs?: number): string {
    if (secs == null) return '—';
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }
  function fmtBytes(n?: number): string {
    if (!n) return '—';
    const u = ['B', 'KB', 'MB', 'GB'];
    let v = n;
    let i = 0;
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(1)} ${u[i]}`;
  }

  // --- System tab: instance info ---
  let systemInfo = $state<{ version: string; commit: string; build_date: string; go_version: string; os: string; arch: string; uptime_seconds: number } | null>(null);
  async function loadSystemInfo() {
    try { systemInfo = await api.system.info(); } catch { /* ignore */ }
  }
  function fmtUptime(secs?: number): string {
    if (!secs) return '—';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }

  // --- Roles tab (RBAC v2) ---
  let roles = $state<CustomRole[]>([]);
  let allPerms = $state<PermissionInfo[]>([]);
  let rolesLoading = $state(false);
  let showRole = $state(false);
  let editingRole = $state<CustomRole | null>(null);
  let roleForm = $state({ name: '', display: '', permissions: [] as string[] });

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
    if (!confirm(`Delete role "${name}"?`)) return;
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

  // SSO state
  let oidcProviders = $state<OIDCProvider[]>([]);
  let oidcLoading = $state(false);
  let showOIDC = $state(false);
  let editingOIDC = $state<OIDCProvider | null>(null);
  let oForm = $state<OIDCProviderInput>({
    slug: '',
    display_name: '',
    issuer_url: '',
    client_id: '',
    client_secret: '',
    scopes: 'openid,profile,email',
    group_claim: 'groups',
    admin_group: '',
    operator_group: '',
    default_role: 'viewer',
    enabled: true
  });

  async function loadOIDC() {
    oidcLoading = true;
    try {
      oidcProviders = await api.oidc.listAdmin();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      oidcLoading = false;
    }
  }

  function resetOIDCForm() {
    oForm = {
      slug: '',
      display_name: '',
      issuer_url: '',
      client_id: '',
      client_secret: '',
      scopes: 'openid,profile,email',
      group_claim: 'groups',
      admin_group: '',
      operator_group: '',
      default_role: 'viewer',
      enabled: true
    };
    editingOIDC = null;
  }

  function openNewOIDC() {
    resetOIDCForm();
    showOIDC = true;
  }

  function openEditOIDC(p: OIDCProvider) {
    editingOIDC = p;
    oForm = {
      slug: p.slug,
      display_name: p.display_name,
      issuer_url: p.issuer_url,
      client_id: p.client_id,
      client_secret: '',
      scopes: p.scopes,
      group_claim: p.group_claim ?? '',
      admin_group: p.admin_group ?? '',
      operator_group: p.operator_group ?? '',
      default_role: p.default_role,
      enabled: p.enabled
    };
    showOIDC = true;
  }

  async function saveOIDC(e: Event) {
    e.preventDefault();
    try {
      if (editingOIDC) {
        await api.oidc.update(editingOIDC.id, oForm);
        toast.success('Provider updated', oForm.slug);
      } else {
        await api.oidc.create(oForm);
        toast.success('Provider created', oForm.slug);
      }
      showOIDC = false;
      resetOIDCForm();
      await loadOIDC();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteOIDC(p: OIDCProvider) {
    if (!confirm(`Delete provider "${p.display_name}"?`)) return;
    try {
      await api.oidc.delete(p.id);
      toast.success('Deleted', p.slug);
      await loadOIDC();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // Account
  let me = $state<any>(null);
  let newPassword = $state('');

  // MFA enrollment state
  let mfaOpen = $state(false);
  let mfaStep = $state<'qr' | 'recovery'>('qr');
  let mfaEnroll = $state<{ secret: string; url: string; qr_data_url: string } | null>(null);
  let mfaCode = $state('');
  let mfaRecovery = $state<string[]>([]);
  let mfaBusy = $state(false);

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

  async function startMFAEnroll() {
    mfaBusy = true;
    mfaStep = 'qr';
    mfaCode = '';
    mfaRecovery = [];
    try {
      mfaEnroll = await api.mfa.enrollStart();
      mfaOpen = true;
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      mfaBusy = false;
    }
  }

  async function verifyMFAEnroll(e: Event) {
    e.preventDefault();
    mfaBusy = true;
    try {
      const r = await api.mfa.enrollVerify(mfaCode.trim());
      mfaRecovery = r.recovery_codes;
      mfaStep = 'recovery';
      await loadMe();
    } catch (err) {
      toast.error('Verification failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      mfaBusy = false;
    }
  }

  async function disableMFA() {
    if (!confirm('Disable two-factor authentication?')) return;
    try {
      await api.mfa.disable();
      toast.success('2FA disabled');
      await loadMe();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function resetUserMFA(userId: string, username: string) {
    if (!confirm(`Reset 2FA for "${username}"? The user will need to re-enroll.`)) return;
    try {
      await api.mfa.reset(userId);
      toast.success('2FA reset', username);
      await loadUsers();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function copyText(s: string) {
    if (typeof navigator !== 'undefined' && navigator.clipboard) {
      navigator.clipboard.writeText(s);
      toast.info('Copied');
    }
  }

  function closeMFA() {
    mfaOpen = false;
    mfaEnroll = null;
    mfaCode = '';
    mfaRecovery = [];
    mfaStep = 'qr';
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
  let auditActionFilter = $state('');
  let auditSearch = $state('');
  let verifyResult = $state<null | {
    verified: number;
    broken: number;
    first_break?: number;
    break_reason?: string;
    genesis: string;
    warnings?: string[];
  }>(null);
  let verifying = $state(false);

  async function runVerify() {
    verifying = true;
    try {
      verifyResult = await api.audit.verify();
      if (verifyResult.broken === 0) {
        toast.success('Chain intact', `${verifyResult.verified} entries verified`);
      } else {
        toast.error('Chain broken', verifyResult.break_reason ?? 'see report');
      }
    } catch (err) {
      toast.error('Verify failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      verifying = false;
    }
  }

  async function loadAudit() {
    auditLoading = true;
    try {
      auditEntries = await api.audit.list(auditLimit, auditActionFilter);
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      auditLoading = false;
    }
  }

  // Unique action types for the filter dropdown
  const auditActions = $derived([...new Set(auditEntries.map(e => e.action.split('.')[0]))].sort());

  // Client-side text search over loaded entries
  const filteredAudit = $derived(
    auditEntries.filter(e => {
      if (!auditSearch.trim()) return true;
      const q = auditSearch.toLowerCase();
      return (e.username ?? e.user_id ?? '').toLowerCase().includes(q)
        || e.action.toLowerCase().includes(q)
        || (e.target ?? '').toLowerCase().includes(q);
    })
  );

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
    else if (tab === 'users') { loadUsers(); loadRoles(); }
    else if (tab === 'audit') loadAudit();
    else if (tab === 'sso') { loadOIDC(); loadRoles(); }
    else if (tab === 'system') { loadBackup(); loadSystemInfo(); }
    else if (tab === 'roles') loadRoles();
  });

  const tabs: Array<{ id: Tab; label: string; icon: any; show: boolean }> = $derived([
    { id: 'account', label: 'Account', icon: User, show: true },
    { id: 'users', label: 'Users', icon: Users, show: allowed('user.manage') },
    { id: 'sso', label: 'SSO', icon: Globe, show: allowed('user.manage') },
    { id: 'system', label: 'System', icon: HardDrive, show: allowed('user.manage') },
    { id: 'roles', label: 'Roles', icon: Shield, show: allowed('user.manage') },
    { id: 'audit', label: 'Audit Log', icon: Activity, show: allowed('audit.read') }
  ]);

  // If the user lands on a tab they're not allowed to see (e.g. deep link
  // or role change), snap back to the first visible tab.
  $effect(() => {
    const visible = tabs.filter((t) => t.show).map((t) => t.id);
    if (!visible.includes(tab)) tab = visible[0] ?? 'account';
  });
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
      <div class="max-w-2xl space-y-6">
        <!-- Profile -->
        <Card class="p-5">
          <div class="flex items-center gap-4 mb-5">
            <div class="w-14 h-14 rounded-full bg-gradient-to-br from-brand-400 to-brand-700 flex items-center justify-center text-white font-bold text-xl shrink-0">
              {me.username[0]?.toUpperCase()}
            </div>
            <div class="flex-1 min-w-0">
              <div class="text-lg font-semibold">{me.username}</div>
              <div class="flex items-center gap-2 mt-0.5">
                <Badge variant="info">{me.role}</Badge>
                {#if me.mfa_enabled}<Badge variant="success" dot>2FA</Badge>{/if}
              </div>
            </div>
          </div>
          <div class="grid grid-cols-2 gap-4 text-xs">
            <div>
              <div class="text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-0.5">Email</div>
              <div class="font-mono text-sm">{me.email || '—'}</div>
            </div>
            <div>
              <div class="text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-0.5">Member since</div>
              <div class="font-mono text-sm">{me.created_at ? new Date(me.created_at).toLocaleDateString() : '—'}</div>
            </div>
          </div>
        </Card>

        <!-- Security -->
        <Card class="p-5">
          <h3 class="font-semibold text-sm uppercase tracking-wider text-[var(--fg-muted)] mb-4">Security</h3>
          <div class="space-y-5">
            <!-- Password -->
            <div class="flex items-start justify-between gap-4">
              <div>
                <div class="text-sm font-medium">Password</div>
                <p class="text-xs text-[var(--fg-muted)] mt-0.5">Set a new password (minimum 8 characters).</p>
              </div>
              <form onsubmit={changeOwnPassword} class="flex items-center gap-2">
                <input
                  type="password"
                  placeholder="New password"
                  bind:value={newPassword}
                  autocomplete="new-password"
                  class="dm-input text-sm !py-1.5 !w-48"
                />
                <Button variant="primary" size="sm" type="submit" disabled={newPassword.length < 8}>Update</Button>
              </form>
            </div>
            <div class="border-t border-[var(--border)]"></div>
            <!-- 2FA -->
            <div class="flex items-start justify-between gap-4">
              <div>
                <div class="text-sm font-medium flex items-center gap-2">
                  Two-factor authentication
                  {#if me.mfa_enabled}<Badge variant="success" dot>active</Badge>{/if}
                </div>
                <p class="text-xs text-[var(--fg-muted)] mt-0.5 max-w-sm">
                  TOTP-based second factor. Works with Google Authenticator, Authy, 1Password, Bitwarden.
                </p>
              </div>
              {#if me.mfa_enabled}
                <Button variant="danger" size="sm" onclick={disableMFA}>
                  <ShieldOff class="w-3.5 h-3.5" /> Disable
                </Button>
              {:else}
                <Button variant="primary" size="sm" loading={mfaBusy} onclick={startMFAEnroll}>
                  <ShieldCheck class="w-3.5 h-3.5" /> Enable
                </Button>
              {/if}
            </div>
          </div>
        </Card>
      </div>
    {:else}
      <Skeleton width="100%" height="12rem" />
    {/if}
  {:else if tab === 'users'}
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <span class="text-sm text-[var(--fg-muted)]">{users.length} user{users.length === 1 ? '' : 's'}</span>
      <Button variant="primary" onclick={() => (showCreate = true)}>
        <Plus class="w-4 h-4" /> New user
      </Button>
    </div>

    {#if usersLoading && users.length === 0}
      <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
    {:else if users.length === 0}
      <Card><EmptyState icon={Users} title="No users" description="Create the first user account." /></Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-left px-5 py-3">User</th>
                <th class="text-left px-3 py-3">Email</th>
                <th class="text-left px-3 py-3">Role</th>
                <th class="text-center px-3 py-3">2FA</th>
                <th class="text-right px-3 py-3 w-24">Actions</th>
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
                      onchange={(e) => changeRole(u.id, u.email, (e.target as HTMLSelectElement).value)}
                      disabled={u.id === me?.id}
                    >
                      {#each roles as r}
                        <option value={r.name}>{r.display || r.name}</option>
                      {/each}
                    </select>
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
  {:else if tab === 'audit'}
    <div class="flex items-center gap-3 flex-wrap">
      <div class="relative flex-1 min-w-[180px] max-w-xs">
        <input type="search" placeholder="Search user, action, target…" bind:value={auditSearch} class="dm-input pl-3 pr-3 py-1.5 text-xs w-full" />
      </div>
      <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={auditActionFilter} onchange={loadAudit}>
        <option value="">All actions</option>
        <option value="auth">auth</option>
        <option value="stack">stack</option>
        <option value="container">container</option>
        <option value="image">image</option>
        <option value="user">user</option>
        <option value="oidc">oidc</option>
        <option value="network">network</option>
        <option value="volume">volume</option>
      </select>
      <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={auditLimit} onchange={loadAudit}>
        <option value={50}>50</option>
        <option value={100}>100</option>
        <option value={500}>500</option>
      </select>
      <Button size="sm" variant="secondary" onclick={loadAudit}>Refresh</Button>
      <Button size="sm" variant="secondary" loading={verifying} onclick={runVerify}>
        <Link2 class="w-3.5 h-3.5" />
        Verify chain
      </Button>
      <button
        class="dm-btn dm-btn-secondary dm-btn-sm"
        onclick={() => {
          const csv = ['Timestamp,Action,Target,User,Details']
            .concat(filteredAudit.map(e => `"${e.ts}","${e.action}","${e.target ?? ''}","${e.username ?? e.user_id ?? ''}","${(e.details ?? '').replace(/"/g, '""')}"`))
            .join('\n');
          const blob = new Blob([csv], { type: 'text/csv' });
          const a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = `dockmesh-audit-${new Date().toISOString().slice(0, 10)}.csv`;
          a.click();
        }}
      >Export CSV</button>
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">{filteredAudit.length} / {auditEntries.length}</span>
    </div>

    {#if verifyResult}
      <div class="dm-card p-4 {verifyResult.broken === 0 ? 'border-[color-mix(in_srgb,var(--color-success-500)_40%,transparent)]' : 'border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)]'}">
        <div class="flex items-center gap-2 text-sm font-medium">
          {#if verifyResult.broken === 0}
            <ShieldCheck class="w-4 h-4 text-[var(--color-success-400)]" />
            <span class="text-[var(--color-success-400)]">Chain intact</span>
          {:else}
            <ShieldOff class="w-4 h-4 text-[var(--color-danger-400)]" />
            <span class="text-[var(--color-danger-400)]">Chain broken</span>
          {/if}
        </div>
        <div class="text-xs text-[var(--fg-muted)] mt-2 space-y-1 font-mono">
          <div>verified: <span class="text-[var(--fg)]">{verifyResult.verified}</span></div>
          <div>broken: <span class="text-[var(--fg)]">{verifyResult.broken}</span></div>
          {#if verifyResult.first_break}
            <div>first break: row <span class="text-[var(--color-danger-400)]">{verifyResult.first_break}</span></div>
            <div>reason: <span class="text-[var(--color-danger-400)]">{verifyResult.break_reason}</span></div>
          {/if}
          <div class="pt-1">genesis: <span class="text-[var(--fg-subtle)] break-all">{verifyResult.genesis}</span></div>
          {#if verifyResult.warnings && verifyResult.warnings.length > 0}
            <div class="pt-1 text-[var(--color-warning-400)]">
              {verifyResult.warnings.length} legacy entries without chain
            </div>
          {/if}
        </div>
      </div>
    {/if}

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
              {#each filteredAudit as e}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-5 py-3 font-mono text-xs whitespace-nowrap text-[var(--fg-muted)]">{fmtTs(e.ts)}</td>
                  <td class="px-5 py-3">
                    <Badge variant={actionVariant(e.action)}>{e.action}</Badge>
                  </td>
                  <td class="px-5 py-3 font-mono text-xs truncate max-w-[200px]">{e.target ?? '—'}</td>
                  <td class="px-5 py-3 text-xs">{e.username || e.user_id?.slice(0, 8) || '—'}</td>
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

{#if tab === 'sso' && allowed('user.manage')}
  <section class="space-y-4">
    <div class="flex justify-between items-center">
      <span class="text-sm text-[var(--fg-muted)]">{oidcProviders.length} provider{oidcProviders.length === 1 ? '' : 's'}</span>
      <Button variant="primary" onclick={openNewOIDC}>
        <Plus class="w-4 h-4" /> Add provider
      </Button>
    </div>

    {#if oidcLoading && oidcProviders.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
    {:else if oidcProviders.length === 0}
      <Card>
        <EmptyState
          icon={Globe}
          title="No SSO providers"
          description="Add an OIDC provider (Azure AD, Google, Keycloak, Dex, Auth0, …) to let users sign in via their organisation account."
        />
      </Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-center px-3 py-3 w-10">Status</th>
                <th class="text-left px-3 py-3">Name</th>
                <th class="text-left px-3 py-3">Slug</th>
                <th class="text-left px-3 py-3">Issuer URL</th>
                <th class="text-left px-3 py-3">Default Role</th>
                <th class="text-right px-3 py-3 w-24">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each oidcProviders as p}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-3 py-3 text-center">
                    <span class="w-2 h-2 rounded-full inline-block {p.enabled ? 'bg-[var(--color-success-500)]' : 'bg-[var(--fg-subtle)]'}"></span>
                  </td>
                  <td class="px-3 py-3 font-medium">{p.display_name}</td>
                  <td class="px-3 py-3 font-mono text-xs text-[var(--fg-muted)]">{p.slug}</td>
                  <td class="px-3 py-3 font-mono text-xs text-[var(--fg-muted)] truncate max-w-[250px]" title={p.issuer_url}>{p.issuer_url}</td>
                  <td class="px-3 py-3"><Badge variant="default">{p.default_role}</Badge></td>
                  <td class="px-3 py-3">
                    <div class="flex gap-0.5 justify-end">
                      <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Edit" onclick={() => openEditOIDC(p)}>
                        <UserCog class="w-3.5 h-3.5" />
                      </button>
                      <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteOIDC(p)}>
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

    <Card class="p-4">
      <div class="text-xs text-[var(--fg-muted)] space-y-1">
        <div class="font-medium text-[var(--fg)]">Callback URL</div>
        <code class="font-mono text-[var(--color-brand-400)]">{`${typeof window !== 'undefined' ? window.location.origin : ''}/api/v1/auth/oidc/{slug}/callback`}</code>
        <div>Configure this in your provider's app/client redirect URIs. Replace <code class="font-mono">{'{slug}'}</code> with the provider's slug.</div>
      </div>
    </Card>
  </section>
{/if}

{#if tab === 'system' && allowed('user.manage')}
  <section class="space-y-4 max-w-3xl">
    <!-- Instance info -->
    {#if systemInfo}
      <Card class="p-5">
        <h3 class="font-semibold text-sm uppercase tracking-wider text-[var(--fg-muted)] mb-3">Instance</h3>
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Version</div>
            <div class="font-mono font-medium">{systemInfo.version}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Commit</div>
            <div class="font-mono">{systemInfo.commit.slice(0, 7)}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Built</div>
            <div class="font-mono">{systemInfo.build_date.slice(0, 10)}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Uptime</div>
            <div class="font-mono">{fmtUptime(systemInfo.uptime_seconds)}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Go</div>
            <div class="font-mono">{systemInfo.go_version}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Platform</div>
            <div class="font-mono">{systemInfo.os}/{systemInfo.arch}</div>
          </div>
        </div>
      </Card>
    {/if}

    <!-- Automated backups -->
    <Card class="p-5">
      <div class="flex items-start justify-between gap-4">
        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2">
            <HardDrive class="w-4 h-4 text-[var(--color-brand-400)]" />
            <h3 class="font-semibold">Automated backups</h3>
          </div>
          <p class="text-sm text-[var(--fg-muted)] mt-1">
            Daily snapshot of the Dockmesh database, <code class="font-mono text-xs">/stacks</code>
            directory, and server data dir. Runs at 03:00 server-local time, keeps the last 14
            days. Single point of failure mitigation — restoring this archive is enough to bring
            a destroyed Dockmesh server back up.
          </p>
        </div>
        <label class="relative inline-flex items-center cursor-pointer shrink-0 mt-1">
          <input
            type="checkbox"
            class="sr-only peer"
            checked={!!backupStatus?.enabled}
            disabled={backupBusy || backupLoading}
            onchange={(e) => toggleBackup((e.target as HTMLInputElement).checked)}
          />
          <div class="w-11 h-6 bg-[var(--surface)] border border-[var(--border)] rounded-full peer-checked:bg-[var(--color-brand-500)] peer-checked:border-[var(--color-brand-500)] after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-transform peer-checked:after:translate-x-5"></div>
        </label>
      </div>

      {#if backupLoading && !backupStatus}
        <Skeleton class="mt-4" width="100%" height="3rem" />
      {:else if backupStatus}
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-4 text-xs">
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">State</div>
            <div class="font-medium">
              {#if backupStatus.state === 'ok'}
                <Badge variant="success" dot>healthy</Badge>
              {:else if backupStatus.state === 'stale'}
                <Badge variant="warning" dot>stale</Badge>
              {:else if backupStatus.state === 'failed'}
                <Badge variant="danger" dot>failed</Badge>
              {:else if backupStatus.state === 'disabled'}
                <Badge variant="default" dot>disabled</Badge>
              {:else}
                <Badge variant="default" dot>never run</Badge>
              {/if}
            </div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Last run</div>
            <div class="font-medium">{fmtAge(backupStatus.age_seconds)}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Last size</div>
            <div class="font-medium">{fmtBytes(backupStatus.last_size_bytes)}</div>
          </div>
          <div>
            <div class="text-[var(--fg-muted)] mb-0.5">Storage</div>
            <div class="font-medium font-mono">./data/backups</div>
          </div>
        </div>

        {#if backupStatus.state === 'failed' && backupStatus.last_error}
          <div class="mt-3 p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
            <ShieldAlert class="w-4 h-4 shrink-0 mt-0.5" />
            <div class="font-mono break-all">{backupStatus.last_error}</div>
          </div>
        {/if}

        {#if backupStatus.state === 'stale'}
          <div class="mt-3 p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)] flex items-start gap-2">
            <AlertCircle class="w-4 h-4 shrink-0 mt-0.5" />
            <div>No successful run in the last 36 hours. Check the backup job logs under
              <a class="underline" href="/backups">Backups → Runs</a>.</div>
          </div>
        {/if}

        {#if backupStatus.state === 'never' && backupStatus.enabled}
          <div class="mt-3 text-xs text-[var(--fg-muted)]">
            The first run will happen at the next scheduled time (03:00). You can trigger an
            immediate run from <a class="underline" href="/backups">Backups</a>.
          </div>
        {/if}
      {/if}
    </Card>

    <Card class="p-4">
      <div class="text-xs text-[var(--fg-muted)] space-y-1.5">
        <div class="font-medium text-[var(--fg)] flex items-center gap-1.5">
          <ShieldCheck class="w-3.5 h-3.5" /> Recovery from backup
        </div>
        <ol class="list-decimal list-inside space-y-0.5">
          <li>Stop the dockmesh service on the new host.</li>
          <li>Extract the latest archive from <code class="font-mono">./data/backups/jobs/dockmesh-system/</code>
            (decrypt with <code class="font-mono">age</code> if encrypted).</li>
          <li>Copy <code class="font-mono">dockmesh.db</code> to <code class="font-mono">./data/dockmesh.db</code>
            and restore <code class="font-mono">stacks/</code> + <code class="font-mono">data/</code> in place.</li>
          <li>Start dockmesh; agents will reconnect automatically.</li>
        </ol>
      </div>
    </Card>
  </section>
{/if}

{#if tab === 'roles' && allowed('user.manage')}
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
                  <td class="px-3 py-3 text-right tabular-nums">{role.permissions.length}</td>
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

<!-- Role modal with grouped permissions -->
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

<Modal bind:open={showOIDC} title={editingOIDC ? 'Edit OIDC provider' : 'Add OIDC provider'} maxWidth="max-w-xl" onclose={resetOIDCForm}>
  <form onsubmit={saveOIDC} class="space-y-5" id="oidc-form">
    <!-- Callback URL hint at top (users need this while configuring) -->
    <div class="text-xs p-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-[var(--fg-muted)]">Callback URL:</span>
      <code class="font-mono text-[var(--color-brand-400)] ml-1 break-all">{`${typeof window !== 'undefined' ? window.location.origin : ''}/api/v1/auth/oidc/${oForm.slug || '{slug}'}/callback`}</code>
    </div>

    <!-- Provider basics -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">Provider</legend>
      <div class="grid grid-cols-2 gap-3">
        <Input label="Slug" hint="used in URLs, e.g. azure-ad" bind:value={oForm.slug} disabled={editingOIDC !== null} />
        <Input label="Display name" bind:value={oForm.display_name} />
      </div>
      <label class="flex items-center gap-2 text-sm cursor-pointer">
        <input type="checkbox" bind:checked={oForm.enabled} class="accent-[var(--color-brand-500)]" />
        Enabled
      </label>
    </fieldset>

    <!-- OIDC configuration -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">OIDC Configuration</legend>
      <Input label="Issuer URL" placeholder="https://login.microsoftonline.com/your-tenant/v2.0" bind:value={oForm.issuer_url} hint="OIDC discovery root (.well-known/openid-configuration)" />
      <div class="grid grid-cols-2 gap-3">
        <Input label="Client ID" bind:value={oForm.client_id} />
        <Input label="Client secret" type="password" bind:value={oForm.client_secret}
          hint={editingOIDC ? 'leave blank to keep existing' : undefined} />
      </div>
      <Input label="Scopes" bind:value={oForm.scopes} hint="comma-separated (default: openid,profile,email)" />
    </fieldset>

    <!-- Group mapping -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">Group Mapping</legend>
      <div class="grid grid-cols-3 gap-3">
        <Input label="Group claim" placeholder="groups" bind:value={oForm.group_claim} />
        <Input label="Admin group" bind:value={oForm.admin_group} />
        <Input label="Operator group" bind:value={oForm.operator_group} />
      </div>
      <div>
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Default role (when no group matches)</span>
        <select class="dm-input text-sm" bind:value={oForm.default_role}>
          {#each roles as r}
            <option value={r.name}>{r.display || r.name}</option>
          {/each}
        </select>
      </div>
    </fieldset>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showOIDC = false; resetOIDCForm(); }}>Cancel</Button>
    <Button variant="primary" type="submit" form="oidc-form">
      {editingOIDC ? 'Save' : 'Create'}
    </Button>
  {/snippet}
</Modal>

<Modal bind:open={mfaOpen} title={mfaStep === 'qr' ? 'Enable two-factor authentication' : 'Save your recovery codes'} maxWidth="max-w-md" onclose={closeMFA}>
  {#if mfaStep === 'qr' && mfaEnroll}
    <div class="space-y-4">
      <p class="text-sm text-[var(--fg-muted)]">
        Scan this QR code with your authenticator app, then enter the 6-digit code it shows.
      </p>
      <div class="flex justify-center p-4 bg-white rounded-lg">
        <img src={mfaEnroll.qr_data_url} alt="TOTP QR code" class="w-52 h-52" />
      </div>
      <div>
        <div class="text-xs text-[var(--fg-muted)] mb-1">Or enter manually</div>
        <div class="flex gap-2">
          <code class="flex-1 dm-input font-mono text-xs select-all">{mfaEnroll.secret}</code>
          <Button size="sm" variant="secondary" onclick={() => copyText(mfaEnroll!.secret)}>
            <Copy class="w-3.5 h-3.5" />
          </Button>
        </div>
      </div>
      <form onsubmit={verifyMFAEnroll}>
        <Input
          label="6-digit code"
          bind:value={mfaCode}
          placeholder="000000"
          autocomplete="one-time-code"
          inputmode="numeric"
        />
        <div class="mt-4 flex justify-end gap-2">
          <Button variant="secondary" onclick={closeMFA} type="button">Cancel</Button>
          <Button variant="primary" type="submit" loading={mfaBusy} disabled={mfaCode.length < 6}>
            Verify and enable
          </Button>
        </div>
      </form>
    </div>
  {:else if mfaStep === 'recovery'}
    <div class="space-y-4">
      <div class="flex items-start gap-2 text-xs text-[var(--color-warning-400)] bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_25%,transparent)] rounded-lg px-3 py-2">
        <ShieldCheck class="w-3.5 h-3.5 shrink-0 mt-0.5" />
        <span>
          <strong>Save these recovery codes now.</strong> Each can be used once instead of a TOTP code if
          you lose access to your authenticator. They won't be shown again.
        </span>
      </div>
      <div class="grid grid-cols-2 gap-2 font-mono text-sm">
        {#each mfaRecovery as code}
          <code class="dm-card p-2 text-center select-all">{code}</code>
        {/each}
      </div>
      <div class="flex justify-end gap-2">
        <Button variant="secondary" onclick={() => copyText(mfaRecovery.join('\n'))}>
          <Copy class="w-3.5 h-3.5" /> Copy all
        </Button>
        <Button variant="primary" onclick={closeMFA}>I've saved them</Button>
      </div>
    </div>
  {/if}
</Modal>

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
