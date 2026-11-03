<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import {
    api, ApiError,
    type CustomRole, type RoleCatalog, type UserRow, type InviteRow, type Scope, type HostInfo
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo } from '$lib/format';
  import { Skeleton, EmptyState, StatusChip, Tabs, Modal, Menu } from '$lib/components/ui';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { assignmentLabel, roleOneLine, scopeLabel, type HostNames } from '$lib/access';
  import ScopePicker from '$lib/components/access/ScopePicker.svelte';
  import AssignmentDialog from '$lib/components/access/AssignmentDialog.svelte';
  import { Plus, Users as UsersIcon, Send } from 'lucide-svelte';
  import { fmtDateTime } from '$lib/i18n';
  import { m } from '$lib/paraglide/messages.js';

  type TabId = 'users' | 'roles' | 'invites';
  const openTab = urlTab<TabId>(['users', 'roles', 'invites'], 'users');
  const tab = $derived(openTab.current);

  let users = $state<UserRow[]>([]);
  let roles = $state<CustomRole[]>([]);
  let catalog = $state<RoleCatalog | null>(null);
  let invites = $state<InviteRow[]>([]);
  let hostNames = $state<HostNames>({});
  let loading = $state(true);
  let search = $state('');

  async function loadAll() {
    try {
      const [u, r, c, h, inv] = await Promise.all([
        api.users.list(),
        api.roles.list().catch(() => [] as CustomRole[]),
        api.roles.catalog().catch(() => null),
        api.hosts.list().catch(() => [] as HostInfo[]),
        allowed('users.create') ? api.users.listInvites().catch(() => [] as InviteRow[]) : Promise.resolve([] as InviteRow[])
      ]);
      users = u;
      roles = r;
      catalog = c;
      hostNames = Object.fromEntries(h.map((x) => [x.id, x.name]));
      invites = inv ?? [];
    } catch (err) {
      toast.error(m.users_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  onMount(loadAll);

  const filtered = $derived(
    users.filter((u) => {
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return u.username.toLowerCase().includes(q) || (u.email ?? '').toLowerCase().includes(q) ||
        u.assignments.some((a) => assignmentLabel(a, roles, hostNames).toLowerCase().includes(q));
    })
  );
  const pendingInvites = $derived(invites.filter((i) => !i.used_at && Date.parse(i.expires_at) > Date.now()));

  function locked(u: UserRow) {
    return !!u.locked_until && Date.parse(u.locked_until) > Date.now();
  }

  async function unlock(u: UserRow) {
    try {
      await api.auth.unlockUser(u.id);
      toast.success(m.users_unlocked(), u.username);
      await loadAll();
    } catch (err) {
      toast.error(m.users_unlock_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function remove(u: UserRow) {
    if (!(await confirm.ask({ title: m.users_delete_title({ name: u.username }), message: m.users_delete_message(), confirmLabel: m.common_delete(), danger: true }))) return;
    try {
      await api.users.delete(u.id);
      toast.success(m.users_deleted(), u.username);
      await loadAll();
    } catch (err) {
      toast.error(m.users_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  let assignFor = $state('');
  let assignOpen = $state(false);

  function rowMenu(u: UserRow): MenuItem[] {
    const items: MenuItem[] = [{ label: m.common_open(), href: `/users/${u.id}` }];
    if (allowed('users.update')) {
      items.push({ label: m.users_add_assignment(), onclick: () => { assignFor = u.id; assignOpen = true; } });
      if (locked(u)) items.push({ label: m.users_unlock(), onclick: () => unlock(u) });
    }
    if (allowed('users.delete')) items.push({ separator: true }, { label: m.common_delete_ellipsis(), danger: true, onclick: () => remove(u) });
    return items;
  }

  // Create user
  let createOpen = $state(false);
  let cUser = $state('');
  let cEmail = $state('');
  let cPass = $state('');
  let cRole = $state('viewer');
  let cScope = $state<Scope>({ type: 'global' });
  let cBusy = $state(false);

  function genPassword(): string {
    const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789';
    const buf = new Uint32Array(18);
    crypto.getRandomValues(buf);
    return Array.from(buf, (n) => chars[n % chars.length]).join('');
  }

  function openCreate() {
    cUser = ''; cEmail = ''; cPass = genPassword(); cRole = 'viewer'; cScope = { type: 'global' };
    createOpen = true;
  }

  async function submitCreate() {
    cBusy = true;
    try {
      const u = await api.users.create({ username: cUser.trim(), email: cEmail.trim() || undefined, password: cPass, role: cRole || undefined, scope: cScope });
      toast.success(m.users_created(), u.username);
      createOpen = false;
      goto(`/users/${u.id}`);
    } catch (err) {
      toast.error(m.users_create_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      cBusy = false;
    }
  }

  // Invites
  let inviteOpen = $state(false);
  let iEmails = $state('');
  let iRole = $state('viewer');
  let iScope = $state<Scope>({ type: 'global' });
  let iTTL = $state(72);
  let iBusy = $state(false);
  let iLinks = $state<Array<{ email: string; url: string }>>([]);

  function openInvite() {
    iEmails = ''; iRole = 'viewer'; iScope = { type: 'global' }; iTTL = 72; iLinks = [];
    inviteOpen = true;
  }

  async function sendInvites() {
    const emails = iEmails.split(/[\s,;]+/).map((e) => e.trim()).filter(Boolean);
    const list = emails.length ? emails : [''];
    iBusy = true;
    const links: Array<{ email: string; url: string }> = [];
    for (const email of list) {
      try {
        const res = await api.users.createInvite({ role: iRole, scopes: [iScope], email_hint: email || undefined, ttl_hours: iTTL });
        links.push({ email: email || m.users_anyone_with_link(), url: res.accept_url });
      } catch (err) {
        toast.error(email ? m.users_invite_for_failed({ email }) : m.users_invite_failed(), err instanceof ApiError ? err.message : undefined);
      }
    }
    iBusy = false;
    if (links.length) {
      iLinks = links;
      await loadAll();
    }
  }

  async function revokeInvite(i: InviteRow) {
    try {
      await api.users.revokeInvite(i.id);
      toast.success(m.users_invite_revoked());
      await loadAll();
    } catch (err) {
      toast.error(m.users_revoke_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  const tabs = $derived([
    { key: 'users', label: m.common_users(), count: users.length },
    { key: 'roles', label: m.common_roles(), count: roles.length },
    ...(allowed('users.create') ? [{ key: 'invites', label: m.users_tab_invites(), count: pendingInvites.length }] : [])
  ]);

  const U_COLS = 'minmax(180px, 1.2fr) minmax(240px, 2.4fr) 110px 70px 110px 120px 36px';
  const R_COLS = 'minmax(280px, 2fr) 90px 100px minmax(260px, 3fr)';
  const I_COLS = 'minmax(180px, 1.2fr) minmax(200px, 1.6fr) 140px 120px 90px';
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>{m.users_title()}</h1>
    <span class="dm-sub">{m.users_subtitle()}</span>
    <span class="dm-sp"></span>
    {#if tab === 'roles' && allowed('roles.create')}
      <a class="dm-btn dm-btn-primary" href="/users/roles/new"><Plus size={14} strokeWidth={2} /> {m.users_new_role()}</a>
    {:else if allowed('users.create')}
      <button type="button" class="dm-btn" onclick={openCreate}><Plus size={14} strokeWidth={2} /> {m.users_add_user()}</button>
      <button type="button" class="dm-btn dm-btn-primary" onclick={openInvite}><Send size={13} strokeWidth={1.8} /> {m.users_invite()}</button>
    {/if}
  </div>

  <Tabs {tabs} active={tab} onchange={(k) => openTab.set(k)} label={m.users_tabs_label()} />

  {#if loading}
    <div class="dm-tbl">
      {#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}
    </div>
  {:else if tab === 'users'}
    <div class="dm-toolbar">
      <span class="dm-sp"></span>
      <input type="search" class="dm-filter" placeholder={m.users_filter_ph()} aria-label={m.users_filter_aria()} bind:value={search} style="width: 280px" />
    </div>
    {#if filtered.length === 0}
      <EmptyState icon={UsersIcon} title={m.users_no_match()} description={m.users_change_filter()} />
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.common_users()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {U_COLS}">
          <span role="columnheader">{m.common_user()}</span><span role="columnheader">{m.users_col_access()}</span><span role="columnheader">{m.users_col_signin()}</span>
          <span role="columnheader">2FA</span><span role="columnheader">{m.common_status()}</span><span role="columnheader">{m.users_col_last_signin()}</span><span></span>
        </div>
        {#each filtered as u (u.id)}
          <div class="dm-tr dm-click" role="row" style="grid-template-columns: {U_COLS}">
            <a class="dm-nm lnk" href="/users/{u.id}">
              <span class="dm-t">{u.display_name || u.username}</span>
              <span class="dm-sub dm-trunc">{[u.display_name ? u.username : '', u.email ?? ''].filter(Boolean).join(' · ') || m.users_no_email()}</span>
            </a>
            <span class="chips">
              {#if u.assignments.length === 0}<span class="dm-dim">{m.users_no_access()}</span>{/if}
              {#each u.assignments.slice(0, 3) as a (a.id)}
                <span class="chip" title={a.via ? m.users_via({ via: a.via }) : undefined}>{assignmentLabel(a, roles, hostNames)}{a.source === 'idp' ? ' ·  IdP' : ''}</span>
              {/each}
              {#if u.assignments.length > 3}<span class="dm-dim">+{u.assignments.length - 3}</span>{/if}
            </span>
            <span class="dm-dim">{u.source === 'sso' ? m.users_source_idp() : m.common_local()}</span>
            <span class={u.mfa_enabled ? '' : 'dm-dim'}>{u.mfa_enabled ? m.common_on() : m.common_off()}</span>
            <span>{#if locked(u)}<StatusChip tone="bad" label={m.users_locked()} />{:else}<StatusChip tone="ok" label={m.users_active()} />{/if}</span>
            <span class="dm-num dm-dim">{fmtAgo(u.last_login_at, m.common_never())}</span>
            <span><Menu label={m.users_actions_for({ name: u.username })} items={rowMenu(u)} /></span>
          </div>
        {/each}
      </div>
    {/if}
  {:else if tab === 'roles'}
    <div class="dm-tbl" role="table" aria-label={m.common_roles()}>
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {R_COLS}">
        <span role="columnheader">{m.common_role()}</span><span role="columnheader">{m.common_type()}</span><span role="columnheader">{m.users_col_assignments()}</span><span role="columnheader">{m.users_col_allows()}</span>
      </div>
      {#each roles as r (r.name)}
        <a class="dm-tr dm-click lnk" role="row" style="grid-template-columns: {R_COLS}" href="/users/roles/{encodeURIComponent(r.name)}">
          <span class="dm-nm"><span class="dm-t">{r.display}</span><span class="dm-sub dm-trunc" title={r.description || undefined}>{r.description || m.users_no_description()}</span></span>
          <span class="dm-dim">{r.builtin ? m.users_builtin() : m.common_custom()}</span>
          <span class="dm-num">{r.assignments ?? 0}</span>
          <span class="dm-trunc dm-dim" title={roleOneLine(catalog, r)}>{roleOneLine(catalog, r)}</span>
        </a>
      {/each}
    </div>
  {:else if tab === 'invites'}
    {#if pendingInvites.length === 0}
      <EmptyState icon={Send} title={m.users_no_invites()} description={m.users_no_invites_desc()}>
        {#snippet action()}<button type="button" class="dm-btn dm-btn-primary" onclick={openInvite}>{m.users_invite()}</button>{/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.users_open_invites()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {I_COLS}">
          <span role="columnheader">{m.users_col_for()}</span><span role="columnheader">{m.users_col_access()}</span><span role="columnheader">{m.users_col_expires()}</span><span role="columnheader">{m.common_created()}</span><span></span>
        </div>
        {#each pendingInvites as i (i.id)}
          <div class="dm-tr" role="row" style="grid-template-columns: {I_COLS}">
            <span class="dm-trunc">{i.email_hint || m.users_anyone_with_link()}</span>
            <span class="dm-trunc">{(i.scopes ?? []).map((s) => m.users_role_on_scope({ role: roles.find((r) => r.name === i.role)?.display ?? i.role, scope: scopeLabel(s, hostNames) })).join(', ')}</span>
            <span class="dm-num dm-dim">{fmtDateTime(i.expires_at)}</span>
            <span class="dm-num dm-dim">{fmtAgo(i.created_at)}</span>
            <span><button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => revokeInvite(i)}>{m.users_revoke()}</button></span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<AssignmentDialog bind:open={assignOpen} {users} {roles} {catalog} userId={assignFor} onsaved={loadAll} />

<Modal bind:open={createOpen} title={m.users_add_user()} maxWidth="max-w-xl">
  <div class="form">
    <div class="dm-field"><label for="cu-name">{m.users_username()}</label><input id="cu-name" class="dm-input" bind:value={cUser} autocomplete="off" /></div>
    <div class="dm-field"><label for="cu-mail">{m.users_email_optional()}</label><input id="cu-mail" class="dm-input" type="email" bind:value={cEmail} /></div>
    <div class="dm-field">
      <label for="cu-pw">{m.users_initial_password()}</label>
      <div class="row">
        <input id="cu-pw" class="dm-input dm-mono" bind:value={cPass} />
        <button type="button" class="dm-btn dm-btn-sm" onclick={() => copyWithToast(cPass, m.users_password_copied())}>{m.common_copy()}</button>
      </div>
    </div>
    <div class="dm-field">
      <label for="cu-role">{m.common_role()}</label>
      <select id="cu-role" class="dm-input" bind:value={cRole}>
        <option value="">{m.users_no_access_yet()}</option>
        {#each roles as r (r.name)}<option value={r.name}>{r.display}</option>{/each}
      </select>
    </div>
    {#if cRole}<ScopePicker bind:value={cScope} label={m.users_scope_on()} />{/if}
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (createOpen = false)}>{m.common_cancel()}</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={cBusy || !cUser.trim() || cPass.length < 8} onclick={submitCreate}>{cBusy ? m.users_creating() : m.users_create_user()}</button>
  {/snippet}
</Modal>

<Modal bind:open={inviteOpen} title={m.users_invite_users()} maxWidth="max-w-xl">
  {#if iLinks.length}
    <div class="form">
      <p class="dm-sub">{m.users_share_links()}</p>
      {#each iLinks as l (l.url)}
        <div class="dm-field">
          <span class="lbl">{l.email}</span>
          <div class="row"><input class="dm-input dm-mono" readonly value={l.url} /><button type="button" class="dm-btn dm-btn-sm" onclick={() => copyWithToast(l.url, m.users_link_copied())}>{m.common_copy()}</button></div>
        </div>
      {/each}
    </div>
  {:else}
    <div class="form">
      <div class="dm-field"><label for="iv-mail">{m.users_emails_optional()}</label><textarea id="iv-mail" class="dm-input" rows="3" bind:value={iEmails} placeholder="anna@example.org, bob@example.org"></textarea></div>
      <div class="dm-field">
        <label for="iv-role">{m.common_role()}</label>
        <select id="iv-role" class="dm-input" bind:value={iRole}>{#each roles as r (r.name)}<option value={r.name}>{r.display}</option>{/each}</select>
      </div>
      <ScopePicker bind:value={iScope} label={m.users_scope_on()} />
      <div class="dm-field">
        <label for="iv-ttl">{m.users_link_expires_after()}</label>
        <select id="iv-ttl" class="dm-input" bind:value={iTTL}><option value={24}>{m.users_days({ count: 1 })}</option><option value={72}>{m.users_days({ count: 3 })}</option><option value={168}>{m.users_days({ count: 7 })}</option></select>
      </div>
    </div>
  {/if}
  {#snippet footer()}
    {#if iLinks.length}
      <button type="button" class="dm-btn dm-btn-primary" onclick={() => (inviteOpen = false)}>{m.common_done()}</button>
    {:else}
      <button type="button" class="dm-btn" onclick={() => (inviteOpen = false)}>{m.common_cancel()}</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={iBusy || !iRole} onclick={sendInvites}>{iBusy ? m.users_creating() : m.users_create_invite_links()}</button>
    {/if}
  {/snippet}
</Modal>

<style>
  .lnk { color: var(--fg); text-decoration: none; }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; min-width: 0; }
  .chip { font-size: var(--fs-sm); padding: 1px 8px; border-radius: 999px; background: color-mix(in srgb, var(--fg) 8%, transparent); white-space: nowrap; }
  .form { display: flex; flex-direction: column; gap: 14px; }
  .row { display: flex; gap: 8px; align-items: center; }
  .row .dm-input { flex: 1; }
  .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
</style>
