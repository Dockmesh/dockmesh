<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { ArrowLeft } from 'lucide-svelte';
  import AuthShell from '$lib/components/auth/AuthShell.svelte';
  import { auth, consumeSignOutReason } from '$lib/stores/auth.svelte';
  import { api, ApiError } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { m } from '$lib/paraglide/messages.js';

  // Matches the lifetime of the server's pending-MFA token.
  const MFA_WINDOW_MS = 5 * 60_000;

  let screen = $state<'signin' | 'mfa' | 'sso'>('signin');
  let username = $state('');
  let password = $state('');
  let loading = $state(false);
  let error = $state('');
  let errorDetail = $state('');
  let notice = $state('');
  let forgotOpen = $state(false);

  let mfaToken = $state<string | null>(null);
  let mfaStartedAt = 0;
  let mfaCode = $state('');
  let useRecovery = $state(false);

  let providers = $state<Array<{ slug: string; display_name: string }>>([]);

  function focusOnMount(node: HTMLElement) {
    node.focus();
  }

  function setError(msg: string, detail = '') {
    error = msg;
    errorDetail = detail;
  }

  onMount(() => {
    // Tokens are never accepted from the URL; drop a stale-style fragment.
    if (window.location.hash) {
      history.replaceState(null, '', window.location.pathname + window.location.search);
    }
    const params = new URLSearchParams(window.location.search);
    const ssoCode = params.get('sso_code');
    const ssoError = params.get('sso_error');
    const expired = params.has('expired');
    if (ssoCode || ssoError !== null || expired) {
      params.delete('sso_code');
      params.delete('sso_error');
      params.delete('expired');
      const qs = params.toString();
      history.replaceState(null, '', window.location.pathname + (qs ? '?' + qs : ''));
    }

    const reason = consumeSignOutReason();
    if (reason === 'session_outdated') {
      notice = m.login_notice_outdated();
    } else if (expired || reason === 'expired') {
      notice = m.login_notice_expired();
    }
    if (ssoError !== null) {
      setError(m.login_sso_failed(), ssoError);
    }
    if (ssoCode) void exchangeSSOCode(ssoCode);
    void loadProviders();
  });

  async function loadProviders() {
    try {
      providers = await api.oidc.listPublic();
    } catch {
      /* no provider buttons; password sign-in still works */
    }
  }

  async function exchangeSSOCode(code: string) {
    screen = 'sso';
    try {
      const res = await api.auth.ssoExchange(code);
      auth.setSession(res.user as any, res.access_token, res.refresh_token);
      toast.success(m.login_sso_signed_in(), res.user.username);
      goto('/');
    } catch {
      auth.clear();
      screen = 'signin';
      setError(m.login_sso_unfinished());
    }
  }

  // 423 and 429 messages end in "try again in <n> minutes".
  function waitHint(message: string): string {
    const wait = /try again in (.+)$/i.exec(message);
    return wait ? m.login_try_again_in({ time: wait[1] }) : m.login_try_again_later();
  }

  function describe(err: unknown, wrong: string): string {
    if (!(err instanceof ApiError) || err.status === 0) {
      return m.login_unreachable();
    }
    if (err.status === 401) return wrong;
    if (err.status === 423) return `${m.login_locked()} ${waitHint(err.message)}`;
    if (err.status === 429) return `${m.login_too_many()} ${waitHint(err.message)}`;
    return m.login_failed();
  }

  async function submit(e: Event) {
    e.preventDefault();
    if (loading) return;
    if (!username.trim() || !password) {
      setError(m.login_enter_credentials());
      return;
    }
    setError('');
    loading = true;
    try {
      const res = await api.auth.login(username.trim(), password);
      if (res.mfa_required && res.mfa_token) {
        mfaToken = res.mfa_token;
        mfaStartedAt = Date.now();
        mfaCode = '';
        useRecovery = false;
        notice = '';
        screen = 'mfa';
        return;
      }
      if (res.access_token && res.refresh_token && res.user) {
        auth.setSession(res.user, res.access_token, res.refresh_token);
        goto('/');
      }
    } catch (err) {
      setError(describe(err, m.login_wrong_credentials()));
    } finally {
      loading = false;
    }
  }

  // Recovery codes are stored as xxxx-xxxx-xxxx in lowercase hex.
  function normalizedCode(): string {
    const raw = mfaCode.replace(/\s+/g, '');
    if (!useRecovery) return raw;
    const hex = raw.toLowerCase().replace(/-/g, '');
    return /^[0-9a-f]{12}$/.test(hex) ? `${hex.slice(0, 4)}-${hex.slice(4, 8)}-${hex.slice(8)}` : raw.toLowerCase();
  }

  async function submitMFA(e: Event) {
    e.preventDefault();
    if (loading || !mfaToken) return;
    const code = normalizedCode();
    if (!code) {
      setError(useRecovery ? m.login_enter_recovery() : m.login_enter_code());
      return;
    }
    setError('');
    loading = true;
    try {
      const res = await api.auth.verifyMFA(mfaToken, code);
      auth.setSession(res.user, res.access_token, res.refresh_token);
      goto('/');
    } catch (err) {
      const timedOut = Date.now() - mfaStartedAt > MFA_WINDOW_MS;
      const wrong = timedOut
        ? m.login_mfa_timed_out()
        : useRecovery
          ? m.login_recovery_invalid()
          : m.login_code_invalid();
      setError(describe(err, wrong));
    } finally {
      loading = false;
    }
  }

  function toggleRecovery() {
    useRecovery = !useRecovery;
    mfaCode = '';
    setError('');
  }

  function backToSignIn() {
    mfaToken = null;
    mfaCode = '';
    password = '';
    useRecovery = false;
    setError('');
    screen = 'signin';
  }

  function ssoLogin(slug: string) {
    window.location.href = `/api/v1/auth/oidc/${slug}/login`;
  }

  const resetCmd = $derived(`sudo dockmesh admin reset-password --user ${username.trim() || '<username>'}`);
</script>

<svelte:head>
  <title>{m.login_page_title()}</title>
</svelte:head>

{#snippet errorBox()}
  {#if error}
    <p class="auth-error" role="alert">
      {error}
      {#if errorDetail}<span class="auth-error-detail">{errorDetail}</span>{/if}
    </p>
  {/if}
{/snippet}

<AuthShell>
  {#if screen === 'sso'}
    <div class="auth-form" aria-busy="true">
      <h1 class="auth-title">{m.login_sso_signing_in()}</h1>
      <p class="auth-sub">{m.login_sso_finishing()}</p>
    </div>
  {:else if screen === 'signin'}
    <form class="auth-form" onsubmit={submit} novalidate>
      <h1 class="auth-title">{m.login_sign_in()}</h1>

      {#if notice}
        <p class="auth-notice" role="status">{notice}</p>
      {/if}

      <div class="dm-field">
        <label for="login-username">{m.login_username()}</label>
        <input
          id="login-username"
          class="dm-input"
          bind:value={username}
          disabled={loading}
          autocomplete="username"
          autocapitalize="off"
          spellcheck="false"
          use:focusOnMount
        />
      </div>

      <div class="dm-field">
        <div class="auth-label-row">
          <label for="login-password">{m.login_password()}</label>
          <button
            type="button"
            class="auth-link"
            aria-expanded={forgotOpen}
            aria-controls="login-forgot"
            onclick={() => (forgotOpen = !forgotOpen)}
          >{m.login_forgot()}</button>
        </div>
        <input
          id="login-password"
          class="dm-input"
          type="password"
          bind:value={password}
          disabled={loading}
          autocomplete="current-password"
        />
        {#if forgotOpen}
          <p id="login-forgot" class="auth-hint">
            {m.login_forgot_hint()}
            <code class="auth-cmd">{resetCmd}</code>
          </p>
        {/if}
      </div>

      {@render errorBox()}

      <button type="submit" class="dm-btn dm-btn-primary auth-block" disabled={loading}>
        {loading ? m.login_signing_in() : m.login_sign_in()}
      </button>

      {#if providers.length > 0}
        <div class="auth-or">{m.login_or()}</div>
        {#each providers as p (p.slug)}
          <button
            type="button"
            class="dm-btn dm-btn-secondary auth-block"
            disabled={loading}
            onclick={() => ssoLogin(p.slug)}
          >{m.login_continue_with({ provider: p.display_name })}</button>
        {/each}
      {/if}
    </form>
  {:else}
    <form class="auth-form" onsubmit={submitMFA} novalidate>
      <button type="button" class="auth-link back" onclick={backToSignIn}>
        <ArrowLeft size={13} strokeWidth={2} /> {m.common_back()}
      </button>

      <h1 class="auth-title">{m.login_mfa_title()}</h1>
      <p class="auth-sub">
        {useRecovery ? m.login_mfa_sub_recovery() : m.login_mfa_sub_code()}
      </p>

      {#key useRecovery}
        <div class="dm-field">
          {#if useRecovery}
            <label for="login-recovery">{m.login_recovery_code()}</label>
            <input
              id="login-recovery"
              class="dm-input dm-mono"
              bind:value={mfaCode}
              disabled={loading}
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              placeholder="xxxx-xxxx-xxxx"
              maxlength={20}
              use:focusOnMount
            />
          {:else}
            <label for="login-code">{m.login_auth_code()}</label>
            <input
              id="login-code"
              class="dm-input dm-mono code"
              bind:value={mfaCode}
              disabled={loading}
              autocomplete="one-time-code"
              inputmode="numeric"
              maxlength={8}
              use:focusOnMount
            />
          {/if}
        </div>
      {/key}

      {@render errorBox()}

      <button type="submit" class="dm-btn dm-btn-primary auth-block" disabled={loading}>
        {loading ? m.login_verifying() : m.login_verify()}
      </button>

      <button type="button" class="auth-link alt" onclick={toggleRecovery}>
        {useRecovery ? m.login_use_app() : m.login_use_recovery()}
      </button>
    </form>
  {/if}
</AuthShell>

<style>
  .auth-link.back {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    align-self: flex-start;
    color: var(--muted);
    margin-bottom: -4px;
  }
  .auth-link.alt { align-self: center; }
  .code { letter-spacing: 0.2em; }
</style>
