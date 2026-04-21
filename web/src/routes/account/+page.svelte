<script lang="ts">
  // Account — personal profile, password, 2FA, active sessions.
  // Extracted from Settings so the user-avatar menu can deep-link
  // here directly instead of opening Settings → Account tab.
  import { api, ApiError } from '$lib/api';
  import { Card, Button, Input, Modal, Badge, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { ShieldCheck, ShieldOff, Copy, Trash2 } from 'lucide-svelte';

  let me = $state<any>(null);
  let currentPassword = $state('');
  let newPassword = $state('');

  let mfaOpen = $state(false);
  let mfaStep = $state<'qr' | 'recovery'>('qr');
  let mfaEnroll = $state<{ secret: string; url: string; qr_data_url: string } | null>(null);
  let mfaCode = $state('');
  let mfaRecovery = $state<string[]>([]);
  let mfaBusy = $state(false);

  let sessions = $state<import('$lib/api').Session[]>([]);
  let sessionsBusy = $state(false);

  async function loadMe() {
    try {
      me = await api.users.me();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
    await loadSessions();
  }

  async function loadSessions() {
    try { sessions = await api.auth.sessions(); } catch { /* ignore */ }
  }

  async function revokeSession(familyID: string, isCurrent: boolean) {
    if (isCurrent) {
      if (!(await confirm.ask({ title: 'Revoke current session', message: 'Revoking the current session will log you out on the next refresh.', body: 'Continue?', confirmLabel: 'Revoke', danger: true }))) return;
    } else {
      if (!(await confirm.ask({ title: 'Revoke session', message: 'Revoke this session?', body: 'Any client using it (browser, CLI, script) will be logged out on next request.', confirmLabel: 'Revoke', danger: true }))) return;
    }
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

  function fmtSessionAgent(ua?: string): string {
    if (!ua) return 'unknown';
    const cli = ua.match(/^([a-zA-Z0-9_.-]+)\/([0-9.]+)/);
    if (cli && !ua.includes('Mozilla')) return `${cli[1]} ${cli[2]}`;
    let os = 'unknown OS';
    if (/Windows NT 10\.0/i.test(ua)) os = 'Windows';
    else if (/Windows NT 11/i.test(ua)) os = 'Windows 11';
    else if (/Mac OS X/i.test(ua)) os = 'macOS';
    else if (/Linux/i.test(ua)) os = 'Linux';
    else if (/Android/i.test(ua)) os = 'Android';
    else if (/iPhone|iPad|iOS/i.test(ua)) os = 'iOS';
    let browser = 'unknown browser';
    const chrome = ua.match(/Chrome\/(\d+)/);
    const firefox = ua.match(/Firefox\/(\d+)/);
    const safari = ua.match(/Version\/(\d+).*Safari/);
    const edge = ua.match(/Edg\/(\d+)/);
    if (edge) browser = `Edge ${edge[1]}`;
    else if (chrome) browser = `Chrome ${chrome[1]}`;
    else if (firefox) browser = `Firefox ${firefox[1]}`;
    else if (safari) browser = `Safari ${safari[1]}`;
    if (browser === 'unknown browser' && os === 'unknown OS') {
      return ua.length > 60 ? ua.slice(0, 57) + '…' : ua;
    }
    return `${browser} on ${os}`;
  }

  async function changeOwnPassword(e: Event) {
    e.preventDefault();
    if (currentPassword.length === 0) { toast.error('Current password required'); return; }
    if (newPassword.length < 8) { toast.error('Password too short', 'min 8 characters'); return; }
    try {
      await api.users.changePassword(me.id, newPassword, currentPassword);
      toast.success('Password updated');
      currentPassword = ''; newPassword = '';
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function startMFAEnroll() {
    mfaBusy = true;
    mfaStep = 'qr'; mfaCode = ''; mfaRecovery = [];
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
    if (!(await confirm.ask({ title: 'Disable 2FA', message: 'Disable two-factor authentication?', body: 'Your account is only protected by the password again. You can re-enable 2FA any time.', confirmLabel: 'Disable', danger: true }))) return;
    try {
      await api.mfa.disable();
      toast.success('2FA disabled');
      await loadMe();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function closeMFA() {
    mfaOpen = false; mfaEnroll = null; mfaCode = ''; mfaRecovery = []; mfaStep = 'qr';
  }

  function copyText(s: string) {
    if (typeof navigator !== 'undefined' && navigator.clipboard) {
      navigator.clipboard.writeText(s);
      toast.info('Copied');
    }
  }

  $effect(() => { loadMe(); });
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-2xl font-semibold tracking-tight">Profile &amp; security</h2>
    <p class="text-sm text-[var(--fg-muted)] mt-0.5">
      Your account, password, two-factor authentication and active sessions.
    </p>
  </div>

  {#if me}
    <div class="max-w-2xl space-y-6">
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
        {#if me.email || me.created_at}
          <div class="grid grid-cols-2 gap-4 text-xs">
            {#if me.email}
              <div>
                <div class="text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-0.5">Email</div>
                <div class="font-mono text-sm">{me.email}</div>
              </div>
            {/if}
            {#if me.created_at}
              <div>
                <div class="text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-0.5">Member since</div>
                <div class="font-mono text-sm">{new Date(me.created_at).toLocaleDateString()}</div>
              </div>
            {/if}
          </div>
        {/if}
      </Card>

      <Card class="p-5">
        <h3 class="font-semibold text-sm uppercase tracking-wider text-[var(--fg-muted)] mb-4">Security</h3>
        <div class="space-y-5">
          <div class="flex items-start justify-between gap-4">
            <div>
              <div class="text-sm font-medium">Password</div>
              <p class="text-xs text-[var(--fg-muted)] mt-0.5">Set a new password (minimum 8 characters).</p>
            </div>
            <form onsubmit={changeOwnPassword} class="flex items-center gap-2">
              <input type="text" value={me?.username ?? ''} autocomplete="username" readonly tabindex="-1" aria-hidden="true"
                style="position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0,0,0,0); white-space: nowrap; border: 0;" />
              <input type="password" placeholder="Current password" bind:value={currentPassword} autocomplete="current-password" class="dm-input text-sm !py-1.5 !w-48" />
              <input type="password" placeholder="New password" bind:value={newPassword} autocomplete="new-password" class="dm-input text-sm !py-1.5 !w-48" />
              <Button variant="primary" size="sm" type="submit" disabled={newPassword.length < 8 || currentPassword.length === 0}>Update</Button>
            </form>
          </div>
          <div class="border-t border-[var(--border)]"></div>
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

      <Card class="p-5 space-y-3">
        <div>
          <h3 class="text-sm font-semibold">Active sessions</h3>
          <p class="text-xs text-[var(--fg-muted)] mt-0.5">
            Each row is a logged-in browser or CLI. Revoking a session logs that client out on its next refresh.
          </p>
        </div>
        {#if sessions.length === 0}
          <p class="text-xs text-[var(--fg-muted)]">No active sessions.</p>
        {:else}
          <ul class="text-sm divide-y divide-[var(--border)]">
            {#each sessions as s (s.family_id)}
              <li class="py-2 flex items-start gap-3">
                <div class="flex-1 min-w-0">
                  <div class="font-mono text-xs truncate" title={s.user_agent}>
                    {fmtSessionAgent(s.user_agent)}
                    {#if s.is_current}<Badge variant="info">current</Badge>{/if}
                    {#if s.revoked_at}<Badge variant="default">revoked</Badge>{/if}
                  </div>
                  <div class="text-[10px] text-[var(--fg-muted)] mt-0.5">
                    {s.ip || 'unknown ip'} · created {new Date(s.created_at).toLocaleString()}
                    {#if !s.revoked_at} · expires {new Date(s.expires_at).toLocaleString()}{/if}
                  </div>
                </div>
                {#if !s.revoked_at}
                  <button
                    class="p-1.5 rounded text-[var(--color-danger-400)] hover:bg-[var(--surface-hover)]"
                    onclick={() => revokeSession(s.family_id, s.is_current)}
                    disabled={sessionsBusy} title="Revoke this session" aria-label="Revoke session">
                    <Trash2 class="w-3.5 h-3.5" />
                  </button>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </Card>
    </div>
  {:else}
    <Skeleton width="100%" height="12rem" />
  {/if}
</section>

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
        <Input label="6-digit code" bind:value={mfaCode} placeholder="000000" autocomplete="one-time-code" inputmode="numeric" />
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
