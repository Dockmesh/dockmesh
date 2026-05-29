<script lang="ts">
  // Account — editorial rebuild based on `Dockmesh Wizard/account.jsx`.
  //
  // Slice 1: visual rebuild only. Sections that need backend additions are
  // rendered in their final layout but with disabled controls + an inline
  // "available with next update" hint, so the .90 dev deploy shows the
  // complete shape. Slice 2 will wire up:
  //   - display_name + avatar (users.display_name column, avatar storage)
  //   - password_changed_at exposure on /me
  //   - sessions.last_seen_at + geo_country / geo_city / asn
  //   - POST /sessions/revoke-all
  //   - last_login derivation surfaced in the header subtitle
  //
  // Mockup deviations (intentional, see chat 2026-05-09):
  //   - No TabStrip — settings cluster lives as separate top-level routes
  //     (/users, /authentication, /tokens, /audit, /settings) per the
  //     sidebar IA refactor. A second tab strip would be duplicate nav.
  //   - No persistent recovery-codes card. Codes are shown once at
  //     enrollment (existing flow). Recovery on lost authenticator goes
  //     through admin → /users → reset 2FA, which already exists.
  //   - No .txt download for codes; clipboard copy is enough.
  import { api, ApiError, type Session, type PasswordPolicy } from '$lib/api';
  import { Skeleton } from '$lib/components/ui';
  import {
    EditorialPage, Eyebrow, Field, EditorialModal,
  } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    ShieldCheck, ShieldOff, Copy, Trash2, Terminal, User as UserIcon,
    AlertTriangle,
  } from 'lucide-svelte';

  // /me shape — the type in api.ts is incomplete; mfa_enabled and
  // created_at are present in the actual response (handler returns the
  // full user struct via h.Auth.GetUser).
  interface MeShape {
    id: string;
    username: string;
    email?: string;
    role: string;
    mfa_enabled?: boolean;
    created_at?: string;
  }

  let me = $state<MeShape | null>(null);
  let policy = $state<PasswordPolicy | null>(null);
  let sessions = $state<Session[]>([]);
  let sessionsBusy = $state(false);
  // By default we hide expired + revoked sessions — every CLI call without
  // an API token creates a fresh session-family, so the raw list grows
  // unbounded over weeks until the server-side cleanup cron lands. The
  // toggle exposes the full list for audit purposes.
  let showInactiveSessions = $state(false);

  const visibleSessions = $derived.by(() => {
    if (showInactiveSessions) return sessions;
    const now = Date.now();
    return sessions.filter((s) => {
      if (s.revoked_at) return false;
      if (s.expires_at && +new Date(s.expires_at) < now) return false;
      return true;
    });
  });
  const hiddenSessionCount = $derived(sessions.length - visibleSessions.length);

  // Identity-card draft (Slice 2 will add display_name + avatar).
  let draftEmail = $state('');
  let identitySaving = $state(false);
  const identityDirty = $derived(!!me && draftEmail !== (me.email ?? ''));

  // Password
  let currentPassword = $state('');
  let newPassword = $state('');
  let confirmPassword = $state('');
  let passwordBusy = $state(false);
  const minLen = $derived(policy?.min_length ?? 8);
  const passwordStrength = $derived(scorePassword(newPassword));
  const canSubmitPassword = $derived(
    currentPassword.length > 0 &&
    newPassword.length >= minLen &&
    newPassword === confirmPassword,
  );

  // 2FA enrollment modal state
  let mfaOpen = $state(false);
  let mfaStep = $state<'qr' | 'recovery'>('qr');
  let mfaEnroll = $state<{ secret: string; url: string; qr_data_url: string } | null>(null);
  let mfaCode = $state('');
  let mfaRecovery = $state<string[]>([]);
  let mfaBusy = $state(false);

  async function loadMe() {
    try {
      me = (await api.users.me()) as MeShape;
      draftEmail = me.email ?? '';
    } catch (err) {
      toast.error('Failed to load profile', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function loadPolicy() {
    try { policy = await api.auth.getPolicy(); } catch { /* policy is informational */ }
  }

  async function loadSessions() {
    try { sessions = await api.auth.sessions(); } catch { /* keep prior */ }
  }

  async function refreshAll() {
    await Promise.all([loadMe(), loadPolicy(), loadSessions()]);
  }

  // Last-login subtitle: derive from the most recent non-current session.
  // Slice 2 may add a dedicated last_login_at column; until then this is
  // the cheapest accurate value we can show.
  const lastLogin = $derived.by(() => {
    const others = sessions
      .filter((s) => !s.is_current && !s.revoked_at)
      .sort((a, b) => +new Date(b.created_at) - +new Date(a.created_at));
    return others[0] ?? null;
  });

  // ── Identity ─────────────────────────────────────────────────────────
  async function saveIdentity() {
    if (!me) return;
    identitySaving = true;
    try {
      await api.users.update(me.id, draftEmail, me.role);
      toast.success('Profile saved');
      await loadMe();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      identitySaving = false;
    }
  }

  function discardIdentity() {
    draftEmail = me?.email ?? '';
  }

  // ── Password ─────────────────────────────────────────────────────────
  async function changeOwnPassword(e: Event) {
    e.preventDefault();
    if (!me) return;
    if (!canSubmitPassword) return;
    passwordBusy = true;
    try {
      await api.users.changePassword(me.id, newPassword, currentPassword);
      toast.success('Password updated');
      currentPassword = ''; newPassword = ''; confirmPassword = '';
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      passwordBusy = false;
    }
  }

  function scorePassword(pw: string): { score: number; label: string } {
    let s = 0;
    if (pw.length >= 8)  s++;
    if (pw.length >= 12) s++;
    if (/[A-Z]/.test(pw) && /[a-z]/.test(pw)) s++;
    if (/[0-9]/.test(pw) && /[^A-Za-z0-9]/.test(pw)) s++;
    return { score: s, label: ['too short', 'weak', 'fair', 'good', 'strong'][s] };
  }

  // ── 2FA ──────────────────────────────────────────────────────────────
  async function startMFAEnroll() {
    mfaBusy = true;
    mfaStep = 'qr'; mfaCode = ''; mfaRecovery = [];
    try {
      mfaEnroll = await api.mfa.enrollStart();
      mfaOpen = true;
    } catch (err) {
      toast.error('Failed to start enrollment', err instanceof ApiError ? err.message : undefined);
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
    if (!(await confirm.ask({
      title: 'Disable 2FA',
      message: 'Disable two-factor authentication?',
      body: "Your account is only protected by the password again. You can re-enable 2FA any time.",
      confirmLabel: 'Disable', danger: true,
    }))) return;
    try {
      await api.mfa.disable();
      toast.success('2FA disabled');
      await loadMe();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function reEnrollMFA() {
    if (!(await confirm.ask({
      title: 'Re-enroll 2FA device',
      message: 'Replace your current authenticator?',
      body: 'Your existing TOTP and recovery codes will be invalidated. You will be shown a new QR code and fresh recovery codes.',
      confirmLabel: 'Re-enroll', danger: false,
    }))) return;
    try {
      await api.mfa.disable();
      await startMFAEnroll();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function closeMFA() {
    mfaOpen = false; mfaEnroll = null; mfaCode = ''; mfaRecovery = []; mfaStep = 'qr';
  }

  // ── Sessions ─────────────────────────────────────────────────────────
  async function revokeSession(familyID: string, isCurrent: boolean) {
    const ok = await confirm.ask(
      isCurrent
        ? {
            title: 'Revoke current session',
            message: 'Revoking the current session will log you out on the next refresh.',
            body: 'Continue?', confirmLabel: 'Revoke', danger: true,
          }
        : {
            title: 'Revoke session',
            message: 'Revoke this session?',
            body: 'Any client using it (browser, CLI, script) will be logged out on next request.',
            confirmLabel: 'Revoke', danger: true,
          },
    );
    if (!ok) return;
    sessionsBusy = true;
    try {
      await api.auth.revokeSession(familyID);
      toast.success('Session revoked');
      await loadSessions();
    } catch (err) {
      toast.error('Failed to revoke', err instanceof ApiError ? err.message : undefined);
    } finally {
      sessionsBusy = false;
    }
  }

  // ── Helpers ──────────────────────────────────────────────────────────
  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    return new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  }

  function fmtAgo(iso?: string): string {
    if (!iso) return '—';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return 'now';
    if (d < 3600) return `${Math.round(d / 60)}m ago`;
    if (d < 86400) return `${Math.round(d / 3600)}h ago`;
    if (d < 2 * 86400) return 'yesterday';
    return `${Math.round(d / 86400)}d ago`;
  }

  function fmtSession(ua?: string): { device: string; isCli: boolean } {
    if (!ua) return { device: 'unknown', isCli: false };
    const cli = ua.match(/^([a-zA-Z0-9_.-]+)\/([0-9.]+)/);
    if (cli && !ua.includes('Mozilla')) {
      return { device: `${cli[1]} ${cli[2]}`, isCli: true };
    }
    let os = 'unknown';
    if (/Windows NT 11/i.test(ua)) os = 'Windows 11';
    else if (/Windows NT 10\.0/i.test(ua)) os = 'Windows';
    else if (/Mac OS X/i.test(ua)) os = 'macOS';
    else if (/Linux/i.test(ua)) os = 'Linux';
    else if (/Android/i.test(ua)) os = 'Android';
    else if (/iPhone|iPad|iOS/i.test(ua)) os = 'iOS';
    let browser = 'browser';
    const chrome = ua.match(/Chrome\/(\d+)/);
    const firefox = ua.match(/Firefox\/(\d+)/);
    const safari = ua.match(/Version\/(\d+).*Safari/);
    const edge = ua.match(/Edg\/(\d+)/);
    if (edge) browser = `Edge ${edge[1]}`;
    else if (chrome) browser = `Chrome ${chrome[1]}`;
    else if (firefox) browser = `Firefox ${firefox[1]}`;
    else if (safari) browser = `Safari ${safari[1]}`;
    return { device: `${browser} · ${os}`, isCli: false };
  }

  function rolePillClass(role: string): string {
    if (role === 'admin' || role === 'host-admin') return 'dm-pill dm-pill-warning';
    return 'dm-pill dm-pill-neutral';
  }

  function copyText(s: string) {
    copyWithToast(s, 'Copied');
  }

  function initials(name: string): string {
    if (!name) return '·';
    const parts = name.split(/[\s._-]+/).filter(Boolean);
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
    return name.slice(0, 2).toUpperCase();
  }

  $effect(() => { refreshAll(); });
</script>

<EditorialPage>
  <section class="acc">
    {#if !me}
      <div style="padding: 32px 0;">
        <Skeleton width="100%" height="14rem" />
      </div>
    {:else}
      <!-- ───────────────────────── Header ───────────────────────── -->
      <header class="acc-header">
        <div class="acc-header-text">
          <h1 class="ed-title acc-title">Account</h1>
          <p class="ed-subtitle acc-subtitle">
            Signed in as {me.username}{me.created_at ? ` · joined ${fmtDate(me.created_at)}` : ''}{lastLogin ? ` · last login ${fmtAgo(lastLogin.created_at)}${lastLogin.ip ? ` from ${lastLogin.ip}` : ''}` : ''}
          </p>
        </div>
        <div class="acc-header-aside">
          <span class="ed-eyebrow acc-aside-eyebrow">role</span>
          <div class="acc-aside-pills">
            <span class={rolePillClass(me.role)}>
              <span class="dm-pill-dot"></span>{me.role}
            </span>
            {#if me.mfa_enabled}
              <span class="dm-pill dm-pill-success">
                <span class="dm-pill-dot"></span>2FA
              </span>
            {/if}
          </div>
        </div>
      </header>

      <hr class="ed-rule" />

      <!-- ───────────────────── 01 · Identity ─────────────────────── -->
      <section class="acc-section">
        <Eyebrow>01 · Identity</Eyebrow>

        <div class="dm-card acc-identity-card">
          <div class="acc-identity-grid">
            <div class="acc-avatar-col">
              <span class="acc-avatar">{initials(me.username)}</span>
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" disabled>
                change
              </button>
            </div>

            <div class="acc-identity-fields">
              <Field label="Username" hint="Used for sign-in. Cannot be changed.">
                <input class="dm-input acc-input-mono acc-input-locked" value={me.username} disabled />
              </Field>

              <Field label="Display name">
                <input class="dm-input" value="" placeholder="—" disabled />
              </Field>

              <Field label="Email" hint="System notifications + password recovery.">
                <input
                  class="dm-input acc-input-mono"
                  type="email"
                  bind:value={draftEmail}
                  placeholder="—"
                />
              </Field>

              <Field label="Joined">
                <input
                  class="dm-input acc-input-mono acc-input-locked"
                  value={fmtDate(me.created_at)}
                  disabled
                />
              </Field>
            </div>
          </div>

          {#if identityDirty}
            <div class="acc-identity-actions">
              <button
                type="button"
                class="dm-btn dm-btn-ghost dm-btn-sm"
                onclick={discardIdentity}
                disabled={identitySaving}
              >
                Discard
              </button>
              <button
                type="button"
                class="dm-btn dm-btn-primary dm-btn-sm"
                onclick={saveIdentity}
                disabled={identitySaving}
              >
                {identitySaving ? 'Saving…' : 'Save changes'}
              </button>
            </div>
          {/if}
        </div>
      </section>

      <!-- ───────────────────── 02 · Security ─────────────────────── -->
      <section class="acc-section">
        <Eyebrow>02 · Security</Eyebrow>

        <div class="acc-grid-2">
          <!-- Password card -->
          <form class="dm-card acc-pw-card" onsubmit={changeOwnPassword}>
            <div class="acc-card-head">
              <span class="acc-card-title">Change password</span>
              <span class="acc-card-meta">min {minLen} chars</span>
            </div>

            <!-- Hidden username input for password-manager autocomplete. -->
            <input
              type="text"
              value={me.username}
              autocomplete="username"
              readonly
              tabindex="-1"
              aria-hidden="true"
              class="acc-sr-input"
            />

            <div class="acc-pw-fields">
              <Field label="Current password">
                <input
                  class="dm-input acc-input-mono"
                  type="password"
                  bind:value={currentPassword}
                  autocomplete="current-password"
                />
              </Field>

              <Field
                label="New password"
                hint={policy && (policy.require_upper || policy.require_lower || policy.require_digit || policy.require_symbol)
                  ? `Min ${minLen} chars · ${[policy.require_upper && 'upper', policy.require_lower && 'lower', policy.require_digit && 'digit', policy.require_symbol && 'symbol'].filter(Boolean).join(' + ')}`
                  : `Min ${minLen} chars.`}
              >
                <input
                  class="dm-input acc-input-mono"
                  type="password"
                  bind:value={newPassword}
                  autocomplete="new-password"
                />
              </Field>

              {#if newPassword.length > 0}
                <div class="acc-strength">
                  <div class="acc-strength-track">
                    <div
                      class="acc-strength-fill"
                      data-level={passwordStrength.score}
                      style="width: {(passwordStrength.score / 4) * 100}%"
                    ></div>
                  </div>
                  <span class="acc-strength-label">{passwordStrength.label}</span>
                </div>
              {/if}

              <Field label="Confirm new password">
                <input
                  class="dm-input acc-input-mono"
                  type="password"
                  bind:value={confirmPassword}
                  autocomplete="new-password"
                />
                {#if confirmPassword.length > 0 && confirmPassword !== newPassword}
                  <span class="acc-input-err">Passwords don't match.</span>
                {/if}
              </Field>
            </div>

            <div class="acc-card-actions">
              <button
                type="submit"
                class="dm-btn dm-btn-primary dm-btn-sm"
                disabled={!canSubmitPassword || passwordBusy}
              >
                {passwordBusy ? 'Updating…' : 'Update password'}
              </button>
            </div>
          </form>

          <!-- 2FA hero card -->
          <div class="dm-card acc-2fa-card" data-state={me.mfa_enabled ? 'enabled' : 'off'}>
            <div class="acc-2fa-row">
              <span class="acc-2fa-icon" data-state={me.mfa_enabled ? 'enabled' : 'off'}>
                {#if me.mfa_enabled}
                  <ShieldCheck size={18} strokeWidth={1.5} />
                {:else}
                  <AlertTriangle size={18} strokeWidth={1.5} />
                {/if}
              </span>
              <div class="acc-2fa-text">
                <div class="acc-card-title">
                  Two-factor auth —
                  <em class="ed-accent" data-state={me.mfa_enabled ? 'enabled' : 'off'}>
                    {me.mfa_enabled ? 'enabled' : 'off'}
                  </em>
                </div>
                <p class="ed-subtitle acc-2fa-blurb">
                  {#if me.mfa_enabled}
                    TOTP enrolled. Use your authenticator app on next sign-in.
                  {:else}
                    Anyone with your password can sign in. Strongly recommended.
                  {/if}
                </p>
              </div>
            </div>

            <div class="acc-card-actions">
              {#if me.mfa_enabled}
                <button
                  type="button"
                  class="dm-btn dm-btn-secondary dm-btn-sm"
                  onclick={reEnrollMFA}
                  disabled={mfaBusy}
                >
                  Re-enroll device
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-sm acc-btn-danger-ghost"
                  onclick={disableMFA}
                  disabled={mfaBusy}
                >
                  <ShieldOff size={13} strokeWidth={1.5} /> Disable
                </button>
              {:else}
                <button
                  type="button"
                  class="dm-btn dm-btn-primary dm-btn-sm"
                  onclick={startMFAEnroll}
                  disabled={mfaBusy}
                >
                  <ShieldCheck size={13} strokeWidth={1.5} /> Enable 2FA
                </button>
              {/if}
            </div>
          </div>
        </div>
      </section>

      <!-- ───────────────────── 03 · Sessions ─────────────────────── -->
      <section class="acc-section">
        <Eyebrow>03 · Sessions</Eyebrow>

        <div class="dm-card acc-sessions">
          {#if visibleSessions.length === 0}
            <div class="acc-sessions-empty">
              {sessions.length === 0 ? 'No active sessions.' : 'No active sessions. Toggle below to see expired and revoked.'}
            </div>
          {:else}
            {#each visibleSessions as s, i (s.family_id)}
              {@const ua = fmtSession(s.user_agent)}
              <div
                class="acc-session"
                class:is-current={s.is_current}
                class:is-last={i === visibleSessions.length - 1}
              >
                <span class="acc-session-icon">
                  {#if ua.isCli}
                    <Terminal size={13} strokeWidth={1.5} />
                  {:else}
                    <UserIcon size={13} strokeWidth={1.5} />
                  {/if}
                </span>

                <div class="acc-session-main">
                  <div class="acc-session-device">
                    <span class="acc-session-device-text">{ua.device}</span>
                    {#if s.is_current}
                      <em class="ed-accent acc-session-current">— current session</em>
                    {/if}
                    {#if ua.isCli}
                      <span class="dm-pill dm-pill-neutral acc-session-clipill">
                        <span class="dm-pill-dot"></span>cli token
                      </span>
                    {/if}
                    {#if s.revoked_at}
                      <span class="dm-pill dm-pill-neutral acc-session-clipill">
                        <span class="dm-pill-dot"></span>revoked
                      </span>
                    {/if}
                  </div>
                  <span class="acc-session-meta">{s.user_agent ? '' : 'no user-agent'}</span>
                </div>

                <div class="acc-session-ip">
                  <span class="acc-session-ip-text">{s.ip || '—'}</span>
                  <span class="acc-session-geo">—</span>
                </div>

                <span class="acc-session-time">
                  {#if s.revoked_at}revoked {fmtAgo(s.revoked_at)}{:else}created {fmtAgo(s.created_at)}{/if}
                </span>

                <div class="acc-session-revoke">
                  {#if s.is_current && !s.revoked_at}
                    <span class="acc-session-dash">—</span>
                  {:else if !s.revoked_at}
                    <button
                      type="button"
                      class="dm-btn dm-btn-ghost dm-btn-xs acc-btn-danger-ghost"
                      onclick={() => revokeSession(s.family_id, s.is_current)}
                      disabled={sessionsBusy}
                    >
                      <Trash2 size={12} strokeWidth={1.5} /> Revoke
                    </button>
                  {:else}
                    <span class="acc-session-dash">—</span>
                  {/if}
                </div>
              </div>
            {/each}
          {/if}
        </div>

        {#if hiddenSessionCount > 0 || showInactiveSessions}
          <div class="acc-sessions-toggle">
            <button
              type="button"
              class="dm-btn dm-btn-ghost dm-btn-xs"
              onclick={() => (showInactiveSessions = !showInactiveSessions)}
            >
              {#if showInactiveSessions}
                Hide expired and revoked
              {:else}
                Show {hiddenSessionCount} expired or revoked
              {/if}
            </button>
          </div>
        {/if}
      </section>

      <!-- ───────────────────── 04 · Account (Danger) ───────────────── -->
      <section class="acc-section">
        <Eyebrow>04 · Account</Eyebrow>

        <div class="acc-danger-card">
          <div class="acc-danger-text">
            <div class="acc-card-title">Sign out everywhere</div>
            <p class="acc-danger-blurb">
              Revokes every session including this one. You'll need to sign in again.
            </p>
          </div>
          <button
            type="button"
            class="dm-btn dm-btn-secondary dm-btn-sm"
            disabled
          >
            Sign out everywhere
          </button>
        </div>
      </section>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── 2FA enrollment modal ───────────────────────── -->
<EditorialModal
  bind:open={mfaOpen}
  eyebrow={mfaStep === 'qr' ? 'Two-factor · enroll' : 'Recovery codes'}
  title={mfaStep === 'qr' ? 'Scan and verify' : 'Save these now'}
  width={480}
  onclose={closeMFA}
>
  {#if mfaStep === 'qr' && mfaEnroll}
    <div class="acc-mfa-body">
      <p class="ed-subtitle">
        Scan the QR code with your authenticator app, then enter the 6-digit code it shows.
      </p>

      <div class="acc-mfa-qr-frame">
        <img src={mfaEnroll.qr_data_url} alt="TOTP QR code" class="acc-mfa-qr" />
      </div>

      <Field label="Or paste this secret">
        {#snippet right()}
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs"
            onclick={() => copyText(mfaEnroll!.secret)}
          >
            <Copy size={11} strokeWidth={1.5} /> copy
          </button>
        {/snippet}
        <input
          class="dm-input acc-input-mono"
          readonly
          value={mfaEnroll.secret}
        />
      </Field>

      <form onsubmit={verifyMFAEnroll}>
        <Field label="6-digit code">
          <input
            class="dm-input acc-input-mono acc-mfa-code"
            bind:value={mfaCode}
            placeholder="000000"
            autocomplete="one-time-code"
            inputmode="numeric"
            maxlength="6"
          />
        </Field>
        <div class="acc-mfa-actions">
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={closeMFA}>
            Cancel
          </button>
          <button
            type="submit"
            class="dm-btn dm-btn-primary dm-btn-sm"
            disabled={mfaCode.trim().length < 6 || mfaBusy}
          >
            {mfaBusy ? 'Verifying…' : 'Verify and enable'}
          </button>
        </div>
      </form>
    </div>
  {:else if mfaStep === 'recovery'}
    <div class="acc-mfa-body">
      <div class="acc-mfa-warn">
        <ShieldCheck size={14} strokeWidth={1.5} />
        <span>
          <strong>Save these recovery codes now.</strong> Each works once instead of a TOTP code if
          you lose access to your authenticator. They won't be shown again.
        </span>
      </div>

      <div class="acc-mfa-codes">
        {#each mfaRecovery as code}
          <code class="acc-mfa-code-cell">{code}</code>
        {/each}
      </div>

      <div class="acc-mfa-actions">
        <button
          type="button"
          class="dm-btn dm-btn-secondary dm-btn-sm"
          onclick={() => copyText(mfaRecovery.join('\n'))}
        >
          <Copy size={12} strokeWidth={1.5} /> Copy all
        </button>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={closeMFA}>
          I've saved them
        </button>
      </div>
    </div>
  {/if}
</EditorialModal>

<style>
  .acc {
    /* Match the left-aligned layout of routes/hosts/+page.svelte and the
       rest of the editorial pages — the app shell already gives the main
       region its own breathing-room padding, so the page content sits
       flush-left under it. */
    display: flex;
    flex-direction: column;
    gap: 0;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .acc-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
    /* Push the divider further down so role-pill + subtitle aren't kissing
       the rule line. */
    padding-bottom: 22px;
  }
  .acc-header-text { min-width: 0; max-width: 70ch; }
  .acc-title {
    font-size: 38px;
    line-height: 1.05;
    margin-top: 12px;
  }
  .acc-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }
  .acc-header-aside {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 6px;
  }
  .acc-aside-eyebrow { color: var(--fg-subtle); }
  .acc-aside-pills {
    display: inline-flex;
    gap: 6px;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  /* ── Sections ───────────────────────────────────────────────── */
  .acc-section { margin-top: 36px; display: flex; flex-direction: column; gap: 14px; }
  .acc-danger-blurb {
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--fg-muted);
    max-width: 60ch;
  }

  /* ── Identity card ──────────────────────────────────────────── */
  .acc-identity-card { padding: 20px; }
  .acc-identity-grid {
    display: grid;
    grid-template-columns: 120px minmax(0, 1fr);
    gap: 24px;
    align-items: start;
  }
  .acc-avatar-col {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
  }
  .acc-avatar {
    width: 88px;
    height: 88px;
    border-radius: 999px;
    background: var(--surface-hover);
    border: 1px solid var(--border-strong);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-family: var(--font-mono);
    font-size: 26px;
    color: var(--fg);
  }
  .acc-identity-fields {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .acc-input-mono { font-family: var(--font-mono); }
  .acc-input-locked { color: var(--fg-subtle); }
  .acc-identity-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    margin-top: 16px;
    padding-top: 14px;
    border-top: 1px solid var(--border-subtle);
  }

  /* ── Security grid ──────────────────────────────────────────── */
  .acc-grid-2 {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 16px;
  }
  @media (max-width: 720px) {
    .acc-grid-2 { grid-template-columns: 1fr; }
    .acc-identity-fields { grid-template-columns: 1fr; }
    .acc-identity-grid {
      grid-template-columns: 1fr;
      justify-items: center;
    }
  }

  /* ── Password card ──────────────────────────────────────────── */
  .acc-pw-card { padding: 18px; }
  .acc-card-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 14px;
  }
  .acc-card-title {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .acc-card-title em { font-style: normal; }
  .acc-card-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .acc-pw-fields {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .acc-strength {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: -2px;
  }
  .acc-strength-track {
    flex: 1;
    height: 3px;
    background: var(--border-subtle);
    border-radius: 2px;
    overflow: hidden;
  }
  .acc-strength-fill {
    height: 100%;
    transition: width 200ms;
    background: var(--color-danger-500);
  }
  .acc-strength-fill[data-level="2"] { background: var(--color-warning-500); }
  .acc-strength-fill[data-level="3"],
  .acc-strength-fill[data-level="4"] { background: var(--color-success-500); }
  .acc-strength-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    min-width: 60px;
    text-align: right;
  }
  .acc-input-err {
    font-size: 11px;
    color: var(--color-danger-400);
    margin-top: 2px;
  }
  .acc-card-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    margin-top: 14px;
  }

  /* ── 2FA card ───────────────────────────────────────────────── */
  .acc-2fa-card[data-state="enabled"] {
    border-color: color-mix(in srgb, var(--color-success-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 4%, var(--surface));
  }
  .acc-2fa-card[data-state="off"] {
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 5%, var(--surface));
  }
  .acc-2fa-card { padding: 18px; }
  .acc-2fa-row {
    display: flex;
    gap: 14px;
    align-items: center;
  }
  .acc-2fa-icon {
    width: 44px;
    height: 44px;
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 999px;
  }
  .acc-2fa-icon[data-state="enabled"] {
    border: 1px solid var(--color-success-500);
    color: var(--color-success-400);
  }
  .acc-2fa-icon[data-state="off"] {
    border: 1px solid var(--color-warning-500);
    color: var(--color-warning-400);
  }
  .acc-2fa-text { flex: 1; min-width: 0; }
  .acc-2fa-blurb { margin-top: 4px; }
  .ed-accent[data-state="enabled"] { color: var(--color-success-400); }
  .ed-accent[data-state="off"] { color: var(--color-warning-400); }
  .acc-btn-danger-ghost { color: var(--color-danger-400); }

  /* ── Sessions list ──────────────────────────────────────────── */
  .acc-sessions { padding: 0; overflow: hidden; }
  .acc-sessions-empty {
    padding: 16px 18px;
    color: var(--fg-subtle);
    font-size: 12.5px;
  }
  .acc-session {
    display: grid;
    grid-template-columns: 36px minmax(0, 1.4fr) minmax(0, 1.1fr) 110px 110px;
    gap: 14px;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .acc-session.is-current {
    background: color-mix(in srgb, var(--color-brand-500) 5%, transparent);
  }
  .acc-session.is-last { border-bottom: 0; }
  .acc-session-icon {
    width: 28px;
    height: 28px;
    border: 1px solid var(--border-strong);
    border-radius: 999px;
    color: var(--fg-muted);
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .acc-session-main { min-width: 0; }
  .acc-session-device {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex-wrap: wrap;
  }
  .acc-session-device-text {
    font-size: 13px;
    font-weight: 500;
    color: var(--fg);
  }
  .acc-session-current { font-size: 11px; }
  .acc-session-clipill { padding: 0 5px; font-size: 9.5px; }
  .acc-session-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .acc-session-ip {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
  }
  .acc-session-ip-text {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg);
  }
  .acc-session-geo {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .acc-session-time {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
  }
  .acc-session-revoke {
    display: flex;
    justify-content: flex-end;
  }
  .acc-session-dash {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .acc-sessions-toggle {
    margin-top: 10px;
    display: flex;
    justify-content: flex-end;
  }
  @media (max-width: 720px) {
    .acc-session {
      grid-template-columns: 36px minmax(0, 1fr) auto;
      grid-auto-flow: row;
    }
    .acc-session-ip,
    .acc-session-time { grid-column: 2 / -1; }
  }

  /* ── Danger zone ────────────────────────────────────────────── */
  .acc-danger-card {
    padding: 16px 18px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 35%, var(--border));
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-danger-500) 4%, var(--surface));
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .acc-danger-text { min-width: 0; }

  /* ── 2FA modal body ─────────────────────────────────────────── */
  .acc-mfa-body {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .acc-mfa-qr-frame {
    display: flex;
    justify-content: center;
    padding: 16px;
    background: white;
    border-radius: 6px;
    border: 1px solid var(--border-strong);
  }
  .acc-mfa-qr { width: 200px; height: 200px; }
  .acc-mfa-code {
    letter-spacing: 0.3em;
    font-size: 16px;
    text-align: center;
  }
  .acc-mfa-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    margin-top: 6px;
  }
  .acc-mfa-warn {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    padding: 10px 12px;
    background: color-mix(in srgb, var(--color-warning-500) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 30%, transparent);
    border-radius: 6px;
    color: var(--color-warning-400);
    font-size: 12px;
    line-height: 1.5;
  }
  .acc-mfa-warn strong { color: var(--fg); }
  .acc-mfa-codes {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }
  .acc-mfa-code-cell {
    padding: 8px 10px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    text-align: center;
    user-select: all;
    background: var(--bg-elevated);
  }

  /* ── Misc ───────────────────────────────────────────────────── */
  .acc-sr-input {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0,0,0,0);
    white-space: nowrap;
    border: 0;
  }
</style>
