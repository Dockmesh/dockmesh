<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    api, ApiError,
    type ApiToken, type Assignment, type CustomRole, type HostInfo, type RoleCatalog, type StackListEntry,
    type UserRow, type AccessCheckResult
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { auth } from '$lib/stores/auth.svelte';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo } from '$lib/format';
  import { Skeleton, EmptyState, StatusChip, Tabs, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { assignmentLabel, isGlobal, roleDisplay, scopeLabel, type HostNames } from '$lib/access';
  import AssignmentDialog from '$lib/components/access/AssignmentDialog.svelte';
  import { Plus, Trash2, Check, X } from 'lucide-svelte';

  const id = $derived(page.params.id ?? '');
  type TabId = 'overview' | 'access' | 'security' | 'tokens' | 'activity';
  const openTab = urlTab<TabId>(['overview', 'access', 'security', 'tokens', 'activity'], 'overview');
  const tab = $derived(openTab.current);

  let user = $state<UserRow | null>(null);
  let users = $state<UserRow[]>([]);
  let roles = $state<CustomRole[]>([]);
  let catalog = $state<RoleCatalog | null>(null);
  let hosts = $state<HostInfo[]>([]);
  let stacks = $state<StackListEntry[]>([]);
  let tokens = $state<ApiToken[]>([]);
  let activity = $state<Array<{ id: number; ts: string; action: string; target?: string }>>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  const hostNames = $derived<HostNames>(Object.fromEntries(hosts.map((h) => [h.id, h.name])));
  const isSelf = $derived(auth.user?.id === id);

  async function loadAll() {
    loadError = null;
    try {
      const [u, all, r, c, h, s] = await Promise.all([
        api.users.get(id),
        api.users.list().catch(() => [] as UserRow[]),
        api.roles.list().catch(() => [] as CustomRole[]),
        api.roles.catalog().catch(() => null),
        api.hosts.list().catch(() => [] as HostInfo[]),
        api.stacks.list().catch(() => [] as StackListEntry[])
      ]);
      user = u;
      users = all;
      roles = r;
      catalog = c;
      hosts = h.filter((x) => x.kind !== 'all' && x.status !== 'revoked');
      stacks = s;
      draftEmail = u.email ?? '';
      draftName = u.display_name ?? '';
      if (allowed('tokens.manage_others') || isSelf) {
        tokens = (await api.apiTokens.list().catch(() => [] as ApiToken[])).filter((t) => t.created_by === id);
      }
      if (allowed('audit.view')) activity = await api.audit.list(100, '', id).catch(() => []);
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : 'Could not load the user';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void id;
    untrack(() => { loading = true; loadAll(); });
  });
  $effect(() => {
    if (user) pageContext.set(user.username);
    return () => pageContext.clear();
  });

  const locked = $derived(!!user?.locked_until && Date.parse(user.locked_until) > Date.now());

  // Overview edits
  let draftEmail = $state('');
  let draftName = $state('');
  let saveBusy = $state(false);
  async function saveProfile() {
    if (!user) return;
    saveBusy = true;
    try {
      user = await api.users.update(user.id, { email: draftEmail.trim(), display_name: draftName.trim() });
      toast.success('Saved', user.username);
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally { saveBusy = false; }
  }
  async function setSource(source: 'manual' | 'sso') {
    if (!user) return;
    try {
      user = await api.users.update(user.id, { source });
      toast.success(source === 'sso' ? 'Sign-in now updates this account from its groups' : 'Sign-in no longer changes this account’s access');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // Access
  let assignOpen = $state(false);
  async function removeAssignment(a: Assignment) {
    const label = assignmentLabel(a, roles, hostNames);
    if (!(await confirm.ask({ title: 'Remove assignment?', message: `${user?.username} loses ${label}.`, body: a.source === 'idp' ? 'It comes from an identity provider group and returns at the next sign-in unless the group mapping changes.' : undefined, confirmLabel: 'Remove', danger: true }))) return;
    try {
      await api.assignments.delete(a.id);
      toast.success('Assignment removed', label);
      await loadAll();
    } catch (err) {
      toast.error('Could not remove it', err instanceof ApiError ? err.message : undefined);
    }
  }

  function levelIdx(area: string, level: string | undefined): number {
    const a = catalog?.areas.find((x) => x.key === area);
    if (!a) return 0;
    return Math.max(0, a.levels.findIndex((l) => l.key === (level || 'none')));
  }
  function roleOf(name: string) { return roles.find((r) => r.name === name); }

  // Effective view: per area, the level held on everything and the
  // levels held on narrower scopes.
  const effective = $derived.by(() => {
    if (!catalog || !user) return [];
    const as = user.assignments;
    return catalog.areas.map((area) => {
      let globalIdx = 0;
      const limited: string[] = [];
      for (const a of as) {
        const r = roleOf(a.role);
        if (!r) continue;
        const i = levelIdx(area.key, r.levels[area.key]);
        if (i === 0) continue;
        if (isGlobal(a.scope)) globalIdx = Math.max(globalIdx, i);
        else if (area.scoped) limited.push(`${area.levels[i].label} on ${scopeLabel(a.scope, hostNames)}`);
      }
      return { area, global: area.levels[globalIdx].label, globalNone: globalIdx === 0, limited };
    });
  });
  const effectiveExtras = $derived.by(() => {
    if (!catalog || !user) return [];
    return catalog.extras.map((e) => {
      const where = user!.assignments.filter((a) => roleOf(a.role)?.extras.includes(e.key)).map((a) => scopeLabel(a.scope, hostNames));
      return { extra: e, where };
    });
  });

  // Access check
  const actions = $derived.by(() => {
    const out: Array<{ key: string; label: string; area?: string; level?: string; extra?: string }> = [];
    for (const a of catalog?.areas ?? []) for (const l of a.levels.slice(1)) out.push({ key: `${a.key}:${l.key}`, label: `${a.label}: ${l.items[0]?.toLowerCase() ?? l.label}`, area: a.key, level: l.key });
    for (const e of catalog?.extras ?? []) out.push({ key: `extra:${e.key}`, label: e.label, extra: e.key });
    return out;
  });
  let chkAction = $state('stacks:deploy');
  let chkHost = $state('');
  let chkStack = $state('');
  let chkBusy = $state(false);
  let chkResult = $state<AccessCheckResult | null>(null);
  async function runCheck() {
    const act = actions.find((a) => a.key === chkAction);
    if (!act || !user) return;
    chkBusy = true;
    try {
      chkResult = await api.access.check({ user_id: user.id, area: act.area, level: act.level, extra: act.extra, host: chkHost || undefined, stack: chkStack || undefined });
    } catch (err) {
      toast.error('Check failed', err instanceof ApiError ? err.message : undefined);
    } finally { chkBusy = false; }
  }
  const chkTarget = $derived(chkStack ? `stack ${chkStack}` : chkHost ? `host ${hostNames[chkHost] ?? chkHost}` : 'everything');

  // Security
  let pwOpen = $state(false);
  let pw1 = $state('');
  let pwBusy = $state(false);
  async function resetPassword() {
    if (!user) return;
    pwBusy = true;
    try {
      await api.users.changePassword(user.id, pw1);
      toast.success('Password reset', 'All sessions of the account were signed out.');
      pwOpen = false;
    } catch (err) {
      toast.error('Reset failed', err instanceof ApiError ? err.message : undefined);
    } finally { pwBusy = false; }
  }
  async function resetMFA() {
    if (!user) return;
    if (!(await confirm.ask({ title: `Turn off 2FA for ${user.username}?`, message: 'They sign in with the password alone until they enroll again.', confirmLabel: 'Turn off 2FA', danger: true }))) return;
    try {
      await api.mfa.reset(user.id);
      toast.success('2FA turned off', user.username);
      await loadAll();
    } catch (err) {
      toast.error('Could not reset 2FA', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function unlock() {
    if (!user) return;
    try {
      await api.auth.unlockUser(user.id);
      toast.success('Unlocked', user.username);
      await loadAll();
    } catch (err) {
      toast.error('Unlock failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function removeUser() {
    if (!user) return;
    if (!(await confirm.ask({ title: `Delete ${user.username}?`, message: 'The account, its sessions and API tokens are removed.', confirmLabel: 'Delete', danger: true, typeToConfirm: user.username }))) return;
    try {
      await api.users.delete(user.id);
      toast.success('User deleted', user.username);
      goto('/users');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function revokeToken(t: ApiToken) {
    if (!(await confirm.ask({ title: `Revoke ${t.name}?`, message: 'Clients using this token stop working immediately.', confirmLabel: 'Revoke', danger: true }))) return;
    try {
      await api.apiTokens.revoke(t.id);
      toast.success('Token revoked', t.name);
      await loadAll();
    } catch (err) {
      toast.error('Revoke failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const tabs = $derived([
    { key: 'overview', label: 'Overview' },
    { key: 'access', label: 'Access', count: user?.assignments.length ?? 0 },
    { key: 'security', label: 'Sessions & 2FA' },
    { key: 'tokens', label: 'API tokens', count: tokens.length },
    ...(allowed('audit.view') ? [{ key: 'activity', label: 'Activity' }] : [])
  ]);

  const A_COLS = 'minmax(150px, 1fr) minmax(200px, 1.6fr) minmax(160px, 1.2fr) 110px 40px';
  const E_COLS = 'minmax(180px, 1fr) 140px minmax(240px, 2fr)';
  const T_COLS = 'minmax(160px, 1fr) minmax(180px, 1.4fr) 120px 120px 80px';
  const L_COLS = '150px minmax(180px, 1fr) minmax(200px, 2fr)';
</script>

<section class="dm-page">
  {#if loading && !user}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="4rem" />
  {:else if loadError || !user}
    <EmptyState tone="error" title="Could not load this user" description={loadError ?? ''}>
      {#snippet action()}<a class="dm-btn" href="/users">Back to users</a>{/snippet}
    </EmptyState>
  {:else}
    <div class="dm-ph">
      <h1>{user.display_name || user.username}</h1>
      {#if locked}<StatusChip tone="bad" label="Locked" />{:else}<StatusChip tone="ok" label="Active" />{/if}
      <span class="dm-sub dm-trunc">{user.username}{user.email ? ` · ${user.email}` : ''} · {user.source === 'sso' ? 'access follows identity provider groups' : 'local access'}</span>
      <span class="dm-sp"></span>
      {#if allowed('users.update')}
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => (assignOpen = true)}><Plus size={14} strokeWidth={2} /> Add assignment</button>
      {/if}
    </div>

    <Tabs {tabs} active={tab} onchange={(k) => openTab.set(k)} label="User sections" />

    {#if tab === 'overview'}
      <div class="grid2">
        <div class="col">
          <div>
            <h2 class="dm-h2">Access</h2>
            {#if user.assignments.length === 0}
              <p class="dm-sub">No assignments: {user.username} can sign in but sees nothing.</p>
            {:else}
              <div class="dm-tbl">
                {#each user.assignments as a (a.id)}
                  <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 140px">
                    <span class="dm-trunc">{assignmentLabel(a, roles, hostNames)}</span>
                    <span class="dm-dim dm-trunc">{a.source === 'idp' ? `via ${a.via || 'identity provider'}` : 'manual'}</span>
                  </div>
                {/each}
              </div>
            {/if}
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm more" onclick={() => openTab.set('access')}>Effective permissions and access check →</button>
          </div>
        </div>
        <div class="col">
          <div>
            <h2 class="dm-h2">Details</h2>
            <dl class="dm-kv">
              <dt>Username</dt><dd class="dm-mono">{user.username}</dd>
              <dt>Last sign-in</dt><dd>{fmtAgo(user.last_login_at, 'never')}</dd>
              <dt>Password changed</dt><dd>{fmtAgo(user.password_changed_at, 'never')}</dd>
              <dt>2FA</dt><dd>{user.mfa_enabled ? 'On' : 'Off'}</dd>
            </dl>
          </div>
          {#if allowed('users.update')}
            <div class="dm-form-sec">
              <h2 class="dm-h2">Profile</h2>
              <div class="dm-field"><label for="ud-name">Display name</label><input id="ud-name" class="dm-input" bind:value={draftName} /></div>
              <div class="dm-field"><label for="ud-mail">Email</label><input id="ud-mail" class="dm-input" type="email" bind:value={draftEmail} /></div>
              <div><button type="button" class="dm-btn" disabled={saveBusy} onclick={saveProfile}>{saveBusy ? 'Saving…' : 'Save'}</button></div>
            </div>
            <div class="dm-form-sec">
              <h2 class="dm-h2">Identity provider groups</h2>
              <p class="dm-sub">
                {user.source === 'sso'
                  ? 'Each sign-in through an identity provider replaces this account’s IdP assignments with those of its group mappings. Manual assignments stay.'
                  : 'Sign-in leaves this account’s access alone; group mappings are not applied.'}
              </p>
              <div>
                {#if user.source === 'sso'}
                  <button type="button" class="dm-btn" onclick={() => setSource('manual')}>Stop updating from groups</button>
                {:else}
                  <button type="button" class="dm-btn" onclick={() => setSource('sso')}>Update from groups at sign-in</button>
                {/if}
              </div>
            </div>
          {/if}
        </div>
      </div>

    {:else if tab === 'access'}
      <div>
        <h2 class="dm-h2">Assignments</h2>
        {#if user.assignments.length === 0}
          <div class="dm-tbl"><div class="dm-empty">No assignments yet.</div></div>
        {:else}
          <div class="dm-tbl" role="table" aria-label="Assignments">
            <div class="dm-tr dm-th" role="row" style="grid-template-columns: {A_COLS}">
              <span role="columnheader">Role</span><span role="columnheader">On</span><span role="columnheader">Source</span><span role="columnheader">Added</span><span></span>
            </div>
            {#each user.assignments as a (a.id)}
              <div class="dm-tr" role="row" style="grid-template-columns: {A_COLS}">
                <a class="lnk dm-trunc" href="/users/roles/{encodeURIComponent(a.role)}">{roleDisplay(roles, a.role)}</a>
                <span class="dm-trunc">{scopeLabel(a.scope, hostNames)}</span>
                <span class="dm-dim dm-trunc">{a.source === 'idp' ? `via ${a.via || 'identity provider'}` : 'manual'}</span>
                <span class="dm-num dm-dim">{fmtAgo(a.created_at)}</span>
                <span>
                  {#if allowed('users.update')}
                    <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" aria-label="Remove assignment" title="Remove" onclick={() => removeAssignment(a)}><Trash2 size={13} /></button>
                  {/if}
                </span>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <div>
        <h2 class="dm-h2">Effective permissions</h2>
        <p class="dm-sub sub">The union of all assignments. Proxy, alert rules, users & roles and settings & audit only come from an assignment on everything.</p>
        <div class="dm-tbl" role="table" aria-label="Effective permissions">
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {E_COLS}">
            <span role="columnheader">Area</span><span role="columnheader">On everything</span><span role="columnheader">On narrower scopes</span>
          </div>
          {#each effective as e (e.area.key)}
            <div class="dm-tr" role="row" style="grid-template-columns: {E_COLS}">
              <span>{e.area.label}</span>
              <span class={e.globalNone ? 'dm-dim' : ''}>{e.global}</span>
              <span class="dm-trunc dm-dim" title={e.limited.join(' · ')}>{e.limited.length ? e.limited.join(' · ') : e.area.scoped ? '—' : 'only on everything'}</span>
            </div>
          {/each}
          {#each effectiveExtras as x (x.extra.key)}
            <div class="dm-tr" role="row" style="grid-template-columns: {E_COLS}">
              <span title={x.extra.warning}><span class="warn">⚠</span> {x.extra.label}</span>
              <span class={x.where.includes('everything') ? '' : 'dm-dim'}>{x.where.includes('everything') ? 'Yes' : 'No'}</span>
              <span class="dm-trunc dm-dim">{x.where.filter((w) => w !== 'everything').join(' · ') || '—'}</span>
            </div>
          {/each}
        </div>
      </div>

      <div class="dm-form-sec">
        <h2 class="dm-h2">Access check</h2>
        <div class="check">
          <span>Can <b>{user.username}</b></span>
          <select class="dm-input" aria-label="Action" bind:value={chkAction}>
            {#each actions as a (a.key)}<option value={a.key}>{a.label}</option>{/each}
          </select>
          <span>on</span>
          <select class="dm-input" aria-label="Host" bind:value={chkHost} onchange={() => (chkStack = '')}>
            <option value="">any host</option>
            {#each hosts as h (h.id)}<option value={h.id}>{h.name}</option>{/each}
          </select>
          <select class="dm-input" aria-label="Stack" bind:value={chkStack}>
            <option value="">no stack</option>
            {#each stacks.filter((s) => !chkHost || s.deployment?.host_id === chkHost) as s (s.name)}<option value={s.name}>{s.name}</option>{/each}
          </select>
          <button type="button" class="dm-btn" disabled={chkBusy} onclick={runCheck}>Check</button>
        </div>
        {#if chkResult}
          <p class="result" role="status">
            {#if chkResult.allowed}
              <span class="ok"><Check size={14} /> Yes</span> — through the assignment <b>{chkResult.granted_by ? assignmentLabel(chkResult.granted_by, roles, hostNames) : 'base rights of every signed-in user'}</b>{chkResult.granted_by?.via ? ` (via ${chkResult.granted_by.via})` : ''}.
            {:else}
              <span class="bad"><X size={14} /> No</span> —
              {chkResult.closest.length
                ? `the assignments that reach ${chkTarget} (${chkResult.closest.map((a) => assignmentLabel(a, roles, hostNames)).join(', ')}) do not include it`
                : `no assignment of ${user.username} reaches ${chkTarget}`}. Needed: {chkResult.needed}.
            {/if}
          </p>
        {/if}
      </div>

    {:else if tab === 'security'}
      <div class="grid2">
        <div class="col">
          <div class="dm-form-sec">
            <h2 class="dm-h2">Two-factor authentication</h2>
            <p class="dm-sub">{user.mfa_enabled ? '2FA is on for this account.' : '2FA is off for this account.'}</p>
            {#if user.mfa_enabled && allowed('users.delete') && !isSelf}
              <div><button type="button" class="dm-btn dm-btn-danger" onclick={resetMFA}>Turn off 2FA…</button></div>
            {/if}
          </div>
          <div class="dm-form-sec">
            <h2 class="dm-h2">Password and lockout</h2>
            <p class="dm-sub">{locked ? `Locked until ${new Date(user.locked_until!).toLocaleString()}.` : 'Not locked.'} Password changed {fmtAgo(user.password_changed_at, 'never')}.</p>
            {#if allowed('users.update') && !isSelf}
              <div class="row">
                {#if locked}<button type="button" class="dm-btn" onclick={unlock}>Unlock</button>{/if}
                <button type="button" class="dm-btn" onclick={() => { pw1 = ''; pwOpen = true; }}>Reset password…</button>
              </div>
            {/if}
          </div>
        </div>
        <div class="col">
          <div class="dm-form-sec">
            <h2 class="dm-h2">Sessions</h2>
            {#if isSelf}
              <p class="dm-sub">Your own sessions are listed on <a href="/account">your account page</a>.</p>
            {:else}
              <p class="dm-sub">Resetting the password signs this account out everywhere.</p>
            {/if}
          </div>
          {#if allowed('users.delete') && !isSelf}
            <div class="dm-form-sec">
              <h2 class="dm-h2">Delete account</h2>
              <p class="dm-sub">Removes the account with its assignments, sessions and API tokens.</p>
              <div><button type="button" class="dm-btn dm-btn-danger" onclick={removeUser}>Delete {user.username}…</button></div>
            </div>
          {/if}
        </div>
      </div>

    {:else if tab === 'tokens'}
      {#if tokens.length === 0}
        <EmptyState title="No API tokens" description="{user.username} has not created any API tokens." />
      {:else}
        <div class="dm-tbl" role="table" aria-label="API tokens">
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {T_COLS}">
            <span role="columnheader">Name</span><span role="columnheader">Restricted to</span><span role="columnheader">Last used</span><span role="columnheader">Expires</span><span></span>
          </div>
          {#each tokens as t (t.id)}
            <div class="dm-tr" role="row" style="grid-template-columns: {T_COLS}">
              <span class="dm-nm"><span class="dm-t">{t.name}</span><span class="dm-sub dm-mono">{t.prefix}…</span></span>
              <span class="dm-trunc">{t.role ? `${roleDisplay(roles, t.role)} on ${scopeLabel(t.scope, hostNames)}` : 'Owner’s full access'}</span>
              <span class="dm-num dm-dim">{fmtAgo(t.last_used_at, 'never')}</span>
              <span class="dm-num dm-dim">{t.revoked_at ? 'revoked' : t.expires_at ? new Date(t.expires_at).toLocaleDateString() : 'never'}</span>
              <span>{#if !t.revoked_at}<button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => revokeToken(t)}>Revoke</button>{/if}</span>
            </div>
          {/each}
        </div>
      {/if}

    {:else if tab === 'activity'}
      {#if activity.length === 0}
        <EmptyState title="No activity" description="Nothing in the audit log for {user.username}." />
      {:else}
        <div class="dm-tbl" role="table" aria-label="Activity">
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {L_COLS}">
            <span role="columnheader">When</span><span role="columnheader">Action</span><span role="columnheader">Target</span>
          </div>
          {#each activity as e (e.id)}
            <div class="dm-tr" role="row" style="grid-template-columns: {L_COLS}">
              <span class="dm-num dm-dim" title={new Date(e.ts).toLocaleString()}>{fmtAgo(e.ts)}</span>
              <span class="dm-mono dm-trunc">{e.action}</span>
              <span class="dm-trunc dm-dim">{e.target ?? ''}</span>
            </div>
          {/each}
        </div>
        <a class="dm-btn dm-btn-ghost dm-btn-sm more" href="/audit?user_id={encodeURIComponent(user.id)}">Open in the audit log →</a>
      {/if}
    {/if}
  {/if}
</section>

<AssignmentDialog bind:open={assignOpen} {users} {roles} {catalog} userId={id} onsaved={loadAll} />

<Modal bind:open={pwOpen} title="Reset password for {user?.username ?? ''}" maxWidth="max-w-md">
  <div class="dm-field">
    <label for="ud-pw">New password</label>
    <input id="ud-pw" class="dm-input" type="password" autocomplete="new-password" bind:value={pw1} />
    <span class="dm-sub">At least 8 characters. Every session of the account is signed out.</span>
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (pwOpen = false)}>Cancel</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={pwBusy || pw1.length < 8} onclick={resetPassword}>{pwBusy ? 'Resetting…' : 'Reset password'}</button>
  {/snippet}
</Modal>

<style>
  .grid2 { display: grid; grid-template-columns: minmax(0, 3fr) minmax(300px, 2fr); gap: 28px; align-items: start; }
  .col { display: flex; flex-direction: column; gap: 22px; min-width: 0; }
  @media (max-width: 1100px) { .grid2 { grid-template-columns: minmax(0, 1fr); } }
  .lnk { color: var(--fg); text-decoration: none; }
  .lnk:hover { text-decoration: underline; }
  .more { margin-top: 6px; }
  .sub { margin: -4px 0 8px; }
  .warn { color: var(--warn); }
  .ok { color: var(--ok); display: inline-flex; align-items: center; gap: 2px; font-weight: 600; }
  .bad { color: var(--bad); display: inline-flex; align-items: center; gap: 2px; font-weight: 600; }
  .check { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
  .check select { width: auto; max-width: 320px; }
  .result { margin: 10px 0 0; font-size: var(--fs-sm); line-height: 1.6; }
  .row { display: flex; gap: 8px; }
</style>
