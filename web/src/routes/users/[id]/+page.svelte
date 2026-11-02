<script lang="ts">
  import { getLocale } from '$lib/paraglide/runtime.js';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    api, ApiError,
    type ApiToken, type Assignment, type CustomRole, type HostInfo, type RoleCatalog, type StackListEntry,
    type UserRow, type AccessCheckResult, type Session
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { auth } from '$lib/stores/auth.svelte';
  import { urlTab } from '$lib/urlTab.svelte';
  import { fmtAgo } from '$lib/format';
  import { Skeleton, EmptyState, StatusChip, Tabs, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { assignmentLabel, isGlobal, levelIndex, roleDisplay, scopeLabel, type HostNames } from '$lib/access';
  import AssignmentDialog from '$lib/components/access/AssignmentDialog.svelte';
  import { Plus, Trash2, Check, X } from 'lucide-svelte';
  import { fmtDate, fmtDateTime } from '$lib/i18n';
  import { m } from '$lib/paraglide/messages.js';

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
      loadError = err instanceof ApiError ? err.message : m.users_user_load_failed();
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
      toast.success(m.common_saved(), user.username);
    } catch (err) {
      toast.error(m.users_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { saveBusy = false; }
  }
  async function setSource(source: 'manual' | 'sso') {
    if (!user) return;
    try {
      user = await api.users.update(user.id, { source });
      toast.success(source === 'sso' ? m.users_source_sso_on() : m.users_source_sso_off());
    } catch (err) {
      toast.error(m.users_save_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  // Access
  let assignOpen = $state(false);
  async function removeAssignment(a: Assignment) {
    const label = assignmentLabel(a, roles, hostNames);
    if (!(await confirm.ask({ title: m.users_remove_assignment_title(), message: m.users_user_loses({ user: user?.username ?? '', label }), body: a.source === 'idp' ? m.users_idp_assignment_body() : undefined, confirmLabel: m.common_remove(), danger: true }))) return;
    try {
      await api.assignments.delete(a.id);
      toast.success(m.users_assignment_removed(), label);
      await loadAll();
    } catch (err) {
      toast.error(m.users_remove_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  function levelIdx(area: string, level: string | undefined): number {
    return catalog ? Math.max(0, levelIndex(catalog, area, level)) : 0;
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
        else if (area.scoped) limited.push(m.users_role_on_scope({ role: area.levels[i].label, scope: scopeLabel(a.scope, hostNames) }));
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
    for (const a of catalog?.areas ?? []) for (const l of a.levels.slice(1)) out.push({ key: `${a.key}:${l.key}`, label: `${a.label}: ${(getLocale() === "en" ? l.items[0]?.toLowerCase() : l.items[0]) ?? l.label}`, area: a.key, level: l.key });
    for (const e of catalog?.extras ?? []) out.push({ key: `extra:${e.key}`, label: e.label, extra: e.key });
    return out;
  });
  let chkAction = $state('stacks:deploy');
  // A host id, or "tag:<tag>" for any host with that tag.
  let chkHost = $state('');
  let chkStack = $state('');
  const chkTag = $derived(chkHost.startsWith('tag:') ? chkHost.slice(4) : '');
  const hostTags = $derived([...new Set(hosts.flatMap((h) => h.tags ?? []))].sort());
  let chkBusy = $state(false);
  let chkResult = $state<AccessCheckResult | null>(null);
  async function runCheck() {
    const act = actions.find((a) => a.key === chkAction);
    if (!act || !user) return;
    chkBusy = true;
    try {
      chkResult = await api.access.check({
        user_id: user.id, area: act.area, level: act.level, extra: act.extra,
        host: (!chkTag && chkHost) || undefined, host_tag: chkTag || undefined, stack: chkStack || undefined
      });
    } catch (err) {
      toast.error(m.users_check_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { chkBusy = false; }
  }
  const chkTarget = $derived(
    chkStack ? m.users_target_stack({ name: chkStack }) : chkTag ? m.users_target_tagged({ tag: chkTag }) : chkHost ? m.users_target_host({ name: hostNames[chkHost] ?? chkHost }) : m.users_target_everything()
  );

  const everything = scopeLabel({ type: 'global' });

  // Security
  let pwOpen = $state(false);
  let pw1 = $state('');
  let pwBusy = $state(false);
  async function resetPassword() {
    if (!user) return;
    pwBusy = true;
    try {
      await api.users.changePassword(user.id, pw1);
      toast.success(m.users_password_reset(), m.users_password_reset_desc());
      pwOpen = false;
    } catch (err) {
      toast.error(m.users_reset_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { pwBusy = false; }
  }
  async function resetMFA() {
    if (!user) return;
    if (!(await confirm.ask({ title: m.users_mfa_off_title({ name: user.username }), message: m.users_mfa_off_message(), confirmLabel: m.users_mfa_off_confirm(), danger: true }))) return;
    try {
      await api.mfa.reset(user.id);
      toast.success(m.users_mfa_turned_off(), user.username);
      await loadAll();
    } catch (err) {
      toast.error(m.users_mfa_reset_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function unlock() {
    if (!user) return;
    try {
      await api.auth.unlockUser(user.id);
      toast.success(m.users_unlocked(), user.username);
      await loadAll();
    } catch (err) {
      toast.error(m.users_unlock_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  // Another user's sessions, for whoever may manage the account.
  const canManageSessions = $derived(allowed('users.update') && !isSelf);
  let sessions = $state<Session[]>([]);
  let sessionsLoaded = $state(false);
  async function loadSessions() {
    if (!user) return;
    try {
      sessions = await api.users.sessions(user.id);
    } catch (err) {
      sessions = [];
      toast.error(m.users_sessions_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally { sessionsLoaded = true; }
  }
  $effect(() => {
    if (tab === 'security' && canManageSessions && user) untrack(() => loadSessions());
  });
  async function endSession(s: Session) {
    if (!user) return;
    try {
      await api.users.revokeSession(user.id, s.family_id);
      toast.success(m.users_session_ended(), s.user_agent || s.ip || s.family_id.slice(0, 8));
      await loadSessions();
    } catch (err) {
      toast.error(m.users_session_end_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function endAllSessions() {
    if (!user) return;
    if (!(await confirm.ask({ title: m.users_signout_all_title({ name: user.username }), message: m.users_signout_all_message(), confirmLabel: m.users_signout_all(), danger: true }))) return;
    try {
      await api.users.revokeAllSessions(user.id);
      toast.success(m.users_signed_out_all(), user.username);
      await loadSessions();
    } catch (err) {
      toast.error(m.users_sessions_end_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function removeUser() {
    if (!user) return;
    if (!(await confirm.ask({ title: m.users_delete_title({ name: user.username }), message: m.users_delete_message(), confirmLabel: m.common_delete(), danger: true, typeToConfirm: user.username }))) return;
    try {
      await api.users.delete(user.id);
      toast.success(m.users_deleted(), user.username);
      goto('/users');
    } catch (err) {
      toast.error(m.users_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function revokeToken(t: ApiToken) {
    if (!(await confirm.ask({ title: m.users_revoke_token_title({ name: t.name }), message: m.users_revoke_token_message(), confirmLabel: m.users_revoke(), danger: true }))) return;
    try {
      await api.apiTokens.revoke(t.id);
      toast.success(m.users_token_revoked(), t.name);
      await loadAll();
    } catch (err) {
      toast.error(m.users_revoke_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  const tabs = $derived([
    { key: 'overview', label: m.common_overview() },
    { key: 'access', label: m.users_col_access(), count: user?.assignments.length ?? 0 },
    { key: 'security', label: m.users_tab_sessions_2fa() },
    { key: 'tokens', label: m.users_tab_api_tokens(), count: tokens.length },
    ...(allowed('audit.view') ? [{ key: 'activity', label: m.users_tab_activity() }] : [])
  ]);

  const A_COLS = 'minmax(150px, 1fr) minmax(200px, 1.6fr) minmax(160px, 1.2fr) 110px 40px';
  const E_COLS = 'minmax(180px, 1fr) 140px minmax(240px, 2fr)';
  const T_COLS = 'minmax(160px, 1fr) minmax(180px, 1.4fr) 120px 120px 80px';
  const S_COLS = 'minmax(160px, 1.6fr) 130px 110px 110px 70px';
  const L_COLS = '150px minmax(180px, 1fr) minmax(200px, 2fr)';
</script>

<section class="dm-page">
  {#if loading && !user}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="4rem" />
  {:else if loadError || !user}
    <EmptyState tone="error" title={m.users_user_load_failed_title()} description={loadError ?? ''}>
      {#snippet action()}<a class="dm-btn" href="/users">{m.users_back_to_users()}</a>{/snippet}
    </EmptyState>
  {:else}
    <div class="dm-ph">
      <h1>{user.display_name || user.username}</h1>
      {#if locked}<StatusChip tone="bad" label={m.users_locked()} />{:else}<StatusChip tone="ok" label={m.users_active()} />{/if}
      <span class="dm-sub dm-trunc">{user.username}{user.email ? ` · ${user.email}` : ''} · {user.source === 'sso' ? m.users_access_follows_idp() : m.users_local_access()}</span>
      <span class="dm-sp"></span>
      {#if allowed('users.update')}
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => (assignOpen = true)}><Plus size={14} strokeWidth={2} /> {m.users_add_assignment_btn()}</button>
      {/if}
    </div>

    <Tabs {tabs} active={tab} onchange={(k) => openTab.set(k)} label={m.users_sections_label()} />

    {#if tab === 'overview'}
      <div class="grid2">
        <div class="col">
          <div>
            <h2 class="dm-h2">{m.users_col_access()}</h2>
            {#if user.assignments.length === 0}
              <p class="dm-sub">{m.users_no_assignments_sees_nothing({ name: user.username })}</p>
            {:else}
              <div class="dm-tbl">
                {#each user.assignments as a (a.id)}
                  <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 140px">
                    <span class="dm-trunc">{assignmentLabel(a, roles, hostNames)}</span>
                    <span class="dm-dim dm-trunc">{a.source === 'idp' ? m.users_via({ via: a.via || m.users_identity_provider_lc() }) : m.users_manual()}</span>
                  </div>
                {/each}
              </div>
            {/if}
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm more" onclick={() => openTab.set('access')}>{m.users_effective_link()}</button>
          </div>
        </div>
        <div class="col">
          <div>
            <h2 class="dm-h2">{m.common_details()}</h2>
            <dl class="dm-kv">
              <dt>{m.users_username()}</dt><dd class="dm-mono">{user.username}</dd>
              <dt>{m.users_col_last_signin()}</dt><dd>{fmtAgo(user.last_login_at, m.common_never())}</dd>
              <dt>{m.users_password_changed()}</dt><dd>{fmtAgo(user.password_changed_at, m.common_never())}</dd>
              <dt>2FA</dt><dd>{user.mfa_enabled ? m.common_on() : m.common_off()}</dd>
            </dl>
          </div>
          {#if allowed('users.update')}
            <div class="dm-form-sec">
              <h2 class="dm-h2">{m.users_profile()}</h2>
              <div class="dm-field"><label for="ud-name">{m.users_display_name()}</label><input id="ud-name" class="dm-input" bind:value={draftName} /></div>
              <div class="dm-field"><label for="ud-mail">{m.users_email()}</label><input id="ud-mail" class="dm-input" type="email" bind:value={draftEmail} /></div>
              <div><button type="button" class="dm-btn" disabled={saveBusy} onclick={saveProfile}>{saveBusy ? m.common_saving() : m.common_save()}</button></div>
            </div>
            <div class="dm-form-sec">
              <h2 class="dm-h2">{m.users_idp_groups()}</h2>
              <p class="dm-sub">
                {user.source === 'sso'
                  ? m.users_idp_groups_on()
                  : m.users_idp_groups_off()}
              </p>
              <div>
                {#if user.source === 'sso'}
                  <button type="button" class="dm-btn" onclick={() => setSource('manual')}>{m.users_stop_updating_groups()}</button>
                {:else}
                  <button type="button" class="dm-btn" onclick={() => setSource('sso')}>{m.users_update_from_groups()}</button>
                {/if}
              </div>
            </div>
          {/if}
        </div>
      </div>

    {:else if tab === 'access'}
      <div>
        <h2 class="dm-h2">{m.users_col_assignments()}</h2>
        {#if user.assignments.length === 0}
          <div class="dm-tbl"><div class="dm-empty">{m.users_no_assignments_yet()}</div></div>
        {:else}
          <div class="dm-tbl" role="table" aria-label={m.users_col_assignments()}>
            <div class="dm-tr dm-th" role="row" style="grid-template-columns: {A_COLS}">
              <span role="columnheader">{m.common_role()}</span><span role="columnheader">{m.users_col_on()}</span><span role="columnheader">{m.common_source()}</span><span role="columnheader">{m.users_col_added()}</span><span></span>
            </div>
            {#each user.assignments as a (a.id)}
              <div class="dm-tr" role="row" style="grid-template-columns: {A_COLS}">
                <a class="lnk dm-trunc" href="/users/roles/{encodeURIComponent(a.role)}">{roleDisplay(roles, a.role)}</a>
                <span class="dm-trunc">{scopeLabel(a.scope, hostNames)}</span>
                <span class="dm-dim dm-trunc">{a.source === 'idp' ? m.users_via({ via: a.via || m.users_identity_provider_lc() }) : m.users_manual()}</span>
                <span class="dm-num dm-dim">{fmtAgo(a.created_at)}</span>
                <span>
                  {#if allowed('users.update')}
                    <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" aria-label={m.users_remove_assignment()} title={m.common_remove()} onclick={() => removeAssignment(a)}><Trash2 size={13} /></button>
                  {/if}
                </span>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <div>
        <h2 class="dm-h2">{m.users_effective_permissions()}</h2>
        <p class="dm-sub sub">{m.users_effective_sub()}</p>
        <div class="dm-tbl" role="table" aria-label={m.users_effective_permissions()}>
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {E_COLS}">
            <span role="columnheader">{m.users_col_area()}</span><span role="columnheader">{m.users_col_on_everything()}</span><span role="columnheader">{m.users_col_on_narrower()}</span>
          </div>
          {#each effective as e (e.area.key)}
            <div class="dm-tr" role="row" style="grid-template-columns: {E_COLS}">
              <span>{e.area.label}</span>
              <span class={e.globalNone ? 'dm-dim' : ''}>{e.global}</span>
              <span class="dm-trunc dm-dim" title={e.limited.join(' · ')}>{e.limited.length ? e.limited.join(' · ') : e.area.scoped ? '—' : m.users_only_on_everything()}</span>
            </div>
          {/each}
          {#each effectiveExtras as x (x.extra.key)}
            <div class="dm-tr" role="row" style="grid-template-columns: {E_COLS}">
              <span title={x.extra.warning}><span class="warn">⚠</span> {x.extra.label}</span>
              <span class={x.where.includes(everything) ? '' : 'dm-dim'}>{x.where.includes(everything) ? m.common_yes() : m.common_no()}</span>
              <span class="dm-trunc dm-dim">{x.where.filter((w) => w !== everything).join(' · ') || '—'}</span>
            </div>
          {/each}
        </div>
      </div>

      <div class="dm-form-sec">
        <h2 class="dm-h2">{m.users_access_check()}</h2>
        <div class="check">
          <span>{m.users_check_can()} <b>{user.username}</b></span>
          <select class="dm-input" aria-label={m.users_action()} bind:value={chkAction}>
            {#each actions as a (a.key)}<option value={a.key}>{a.label}</option>{/each}
          </select>
          <span>{m.users_check_on()}</span>
          <select class="dm-input" aria-label={m.users_host_or_tag()} bind:value={chkHost} onchange={() => (chkStack = '')}>
            <option value="">{m.users_any_host()}</option>
            <optgroup label={m.common_hosts()}>
              {#each hosts as h (h.id)}<option value={h.id}>{h.name}</option>{/each}
            </optgroup>
            {#if hostTags.length}
              <optgroup label={m.users_host_tags()}>
                {#each hostTags as t (t)}<option value={'tag:' + t}>#{t}</option>{/each}
              </optgroup>
            {/if}
          </select>
          <select class="dm-input" aria-label={m.common_stack()} bind:value={chkStack} disabled={!!chkTag}>
            <option value="">{m.users_no_stack()}</option>
            {#each stacks.filter((s) => !chkHost || (s.deployment?.host_id ?? s.target_host) === chkHost) as s (s.name)}<option value={s.name}>{s.name}</option>{/each}
          </select>
          <button type="button" class="dm-btn" disabled={chkBusy} onclick={runCheck}>{m.users_check()}</button>
        </div>
        {#if chkResult}
          <p class="result" role="status">
            {#if chkResult.allowed}
              <span class="ok"><Check size={14} /> {m.common_yes()}</span> {m.users_check_yes_before()} <b>{chkResult.granted_by ? assignmentLabel(chkResult.granted_by, roles, hostNames) : m.users_check_base_rights()}</b>{chkResult.granted_by?.via ? m.users_check_via({ via: chkResult.granted_by.via }) : ''}.
            {:else}
              <span class="bad"><X size={14} /> {m.common_no()}</span> —
              {chkResult.closest.length
                ? m.users_check_no_closest({ target: chkTarget, list: chkResult.closest.map((a) => assignmentLabel(a, roles, hostNames)).join(', ') })
                : m.users_check_no_none({ user: user.username, target: chkTarget })}. {m.users_check_needed({ needed: chkResult.needed })}
            {/if}
          </p>
        {/if}
      </div>

    {:else if tab === 'security'}
      <div class="grid2">
        <div class="col">
          <div class="dm-form-sec">
            <h2 class="dm-h2">{m.users_two_factor()}</h2>
            <p class="dm-sub">{user.mfa_enabled ? m.users_mfa_is_on() : m.users_mfa_is_off()}</p>
            {#if user.mfa_enabled && allowed('users.delete') && !isSelf}
              <div><button type="button" class="dm-btn dm-btn-danger" onclick={resetMFA}>{m.users_mfa_turn_off_ellipsis()}</button></div>
            {/if}
          </div>
          <div class="dm-form-sec">
            <h2 class="dm-h2">{m.users_password_lockout()}</h2>
            <p class="dm-sub">{locked ? m.users_locked_until({ date: fmtDateTime(user.locked_until!) }) : m.users_not_locked()} {m.users_password_changed_ago({ ago: fmtAgo(user.password_changed_at, m.common_never()) })}</p>
            {#if allowed('users.update') && !isSelf}
              <div class="row">
                {#if locked}<button type="button" class="dm-btn" onclick={unlock}>{m.users_unlock()}</button>{/if}
                <button type="button" class="dm-btn" onclick={() => { pw1 = ''; pwOpen = true; }}>{m.users_reset_password_ellipsis()}</button>
              </div>
            {/if}
          </div>
        </div>
        <div class="col">
          <div class="dm-form-sec">
            <h2 class="dm-h2">{m.users_sessions()}</h2>
            {#if isSelf}
              <p class="dm-sub">{m.users_own_sessions_before()} <a href="/account">{m.users_own_sessions_link()}</a>{m.users_own_sessions_after()}</p>
            {:else if !canManageSessions}
              <p class="dm-sub">{m.users_reset_signs_out()}</p>
            {:else if !sessionsLoaded}
              <Skeleton class="h-16" />
            {:else if sessions.length === 0}
              <p class="dm-sub">{m.users_no_active_sessions()}</p>
            {:else}
              <div class="dm-tbl" role="table" aria-label={m.users_sessions()}>
                <div class="dm-tr dm-th" role="row" style="grid-template-columns: {S_COLS}">
                  <span role="columnheader">{m.users_col_client()}</span><span role="columnheader">IP</span><span role="columnheader">{m.users_col_signed_in()}</span><span role="columnheader">{m.users_col_last_seen()}</span><span></span>
                </div>
                {#each sessions as s (s.family_id)}
                  <div class="dm-tr" role="row" style="grid-template-columns: {S_COLS}">
                    <span class="dm-trunc" title={s.user_agent}>{s.user_agent || m.users_unknown_client()}</span>
                    <span class="dm-mono dm-dim dm-trunc">{s.ip || '—'}</span>
                    <span class="dm-num dm-dim">{fmtAgo(s.created_at, '—')}</span>
                    <span class="dm-num dm-dim">{fmtAgo(s.last_seen_at, '—')}</span>
                    <span><button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => endSession(s)}>{m.users_end()}</button></span>
                  </div>
                {/each}
              </div>
              <div><button type="button" class="dm-btn" onclick={endAllSessions}>{m.users_signout_all_ellipsis()}</button></div>
            {/if}
          </div>
          {#if allowed('users.delete') && !isSelf}
            <div class="dm-form-sec">
              <h2 class="dm-h2">{m.users_delete_account()}</h2>
              <p class="dm-sub">{m.users_delete_account_desc()}</p>
              <div><button type="button" class="dm-btn dm-btn-danger" onclick={removeUser}>{m.users_delete_name_ellipsis({ name: user.username })}</button></div>
            </div>
          {/if}
        </div>
      </div>

    {:else if tab === 'tokens'}
      {#if tokens.length === 0}
        <EmptyState title={m.users_no_tokens()} description={m.users_no_tokens_desc({ name: user.username })} />
      {:else}
        <div class="dm-tbl" role="table" aria-label={m.users_tab_api_tokens()}>
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {T_COLS}">
            <span role="columnheader">{m.common_name()}</span><span role="columnheader">{m.users_col_restricted_to()}</span><span role="columnheader">{m.users_col_last_used()}</span><span role="columnheader">{m.users_col_expires()}</span><span></span>
          </div>
          {#each tokens as t (t.id)}
            <div class="dm-tr" role="row" style="grid-template-columns: {T_COLS}">
              <span class="dm-nm"><span class="dm-t">{t.name}</span><span class="dm-sub dm-mono">{t.prefix}…</span></span>
              <span class="dm-trunc">{t.role ? m.users_role_on_scope({ role: roleDisplay(roles, t.role), scope: scopeLabel(t.scope, hostNames) }) : m.users_owner_full_access()}</span>
              <span class="dm-num dm-dim">{fmtAgo(t.last_used_at, m.common_never())}</span>
              <span class="dm-num dm-dim">{t.revoked_at ? m.users_revoked() : t.expires_at ? fmtDate(t.expires_at) : m.common_never()}</span>
              <span>{#if !t.revoked_at}<button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => revokeToken(t)}>{m.users_revoke()}</button>{/if}</span>
            </div>
          {/each}
        </div>
      {/if}

    {:else if tab === 'activity'}
      {#if activity.length === 0}
        <EmptyState title={m.users_no_activity()} description={m.users_no_activity_desc({ name: user.username })} />
      {:else}
        <div class="dm-tbl" role="table" aria-label={m.users_tab_activity()}>
          <div class="dm-tr dm-th" role="row" style="grid-template-columns: {L_COLS}">
            <span role="columnheader">{m.users_col_when()}</span><span role="columnheader">{m.users_action()}</span><span role="columnheader">{m.common_target()}</span>
          </div>
          {#each activity as e (e.id)}
            <div class="dm-tr" role="row" style="grid-template-columns: {L_COLS}">
              <span class="dm-num dm-dim" title={fmtDateTime(e.ts)}>{fmtAgo(e.ts)}</span>
              <span class="dm-mono dm-trunc">{e.action}</span>
              <span class="dm-trunc dm-dim">{e.target ?? ''}</span>
            </div>
          {/each}
        </div>
        <a class="dm-btn dm-btn-ghost dm-btn-sm more" href="/audit?user_id={encodeURIComponent(user.id)}">{m.users_open_audit()}</a>
      {/if}
    {/if}
  {/if}
</section>

<AssignmentDialog bind:open={assignOpen} {users} {roles} {catalog} userId={id} onsaved={loadAll} />

<Modal bind:open={pwOpen} title={m.users_reset_pw_title({ name: user?.username ?? '' })} maxWidth="max-w-md">
  <div class="dm-field">
    <label for="ud-pw">{m.users_new_password()}</label>
    <input id="ud-pw" class="dm-input" type="password" autocomplete="new-password" bind:value={pw1} />
    <span class="dm-sub">{m.users_new_password_hint()}</span>
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (pwOpen = false)}>{m.common_cancel()}</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={pwBusy || pw1.length < 8} onclick={resetPassword}>{pwBusy ? m.users_resetting() : m.users_reset_password()}</button>
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
