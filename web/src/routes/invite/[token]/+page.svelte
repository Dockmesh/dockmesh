<script lang="ts">
  // Public invite redeem page. The token in the URL is shown by the
  // backend without auth and consumed by submitting username + password.
  // No SMTP — admin shared this link manually.
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { auth } from '$lib/stores/auth.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { Eyebrow, Field } from '$lib/components/editorial';
  import { ArrowRight, Sun, Moon, ShieldAlert } from 'lucide-svelte';

  const token = $derived($page.params.token);

  let preview = $state<{ role: string; scope_tags: string[]; email_hint?: string; expires_at: string } | null>(null);
  let loadError = $state<string | null>(null);
  let loading = $state(true);

  // Form state
  let username = $state('');
  let email = $state('');
  let password = $state('');
  let submitting = $state(false);
  let formError = $state('');

  // Theme — same toggle as the login page so the invite lands in
  // whatever the operator's UA prefers.
  let theme = $state<'light' | 'dark'>('dark');
  $effect(() => {
    if (typeof window === 'undefined') return;
    const stored = localStorage.getItem('dockmesh.theme');
    if (stored === 'light' || stored === 'dark') theme = stored;
  });
  $effect(() => {
    if (typeof document === 'undefined') return;
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('dockmesh.theme', theme);
  });

  async function load() {
    loading = true;
    loadError = null;
    try {
      preview = await api.users.previewInvite(token);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 410) loadError = 'This invitation has expired or has already been used.';
        else if (err.status === 404) loadError = 'This invitation link is not valid.';
        else loadError = err.message;
      } else {
        loadError = 'Could not load invitation.';
      }
    } finally {
      loading = false;
    }
  }
  $effect(() => { token; load(); });

  async function submit(e: Event) {
    e.preventDefault();
    if (!preview) return;
    formError = '';
    submitting = true;
    try {
      const res = await api.users.acceptInvite(token, { username, email: email || undefined, password });
      auth.setSession(res.user as any, res.access_token, res.refresh_token);
      toast.success('Welcome to dockmesh', res.user.username);
      goto('/');
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 409) formError = 'That username or email is already taken.';
        else if (err.status === 410) formError = 'This invitation has just expired or been used.';
        else formError = err.message;
      } else {
        formError = 'Sign-up failed.';
      }
    } finally {
      submitting = false;
    }
  }

  let canSubmit = $derived(!submitting && username.trim().length >= 2 && password.length >= 8);
</script>

<div class="login-shell">
  <button
    type="button"
    class="theme-toggle login-chrome-tr"
    onclick={() => (theme = theme === 'dark' ? 'light' : 'dark')}
    aria-label="Toggle theme"
    title={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
  >
    {#if theme === 'dark'}
      <Sun size={13} strokeWidth={1.5} />
    {:else}
      <Moon size={13} strokeWidth={1.5} />
    {/if}
  </button>

  <div class="login-stack">
    <div class="login-brand">
      <img src="/logo-mark.svg" alt="" width="28" height="28" />
      <span>Dockmesh</span>
    </div>

    <div class="login-card">
      <Eyebrow active>Accept invitation</Eyebrow>

      {#if loading}
        <p class="invite-blurb">Loading invitation…</p>
      {:else if loadError}
        <div class="invite-error">
          <ShieldAlert size={16} strokeWidth={1.5} />
          <span>{loadError}</span>
        </div>
        <p class="invite-blurb">
          Ask the admin who sent you the link to generate a new one.
        </p>
      {:else if preview}
        <p class="invite-blurb">
          {#if preview.email_hint}
            This link was prepared for <strong>{preview.email_hint}</strong>.
            Make sure you're the right person before continuing.
          {:else}
            You've been invited to dockmesh.
          {/if}
        </p>
        <div class="invite-meta">
          <span class="invite-meta-label">role</span>
          <span class="invite-meta-value">{preview.role}</span>
          {#if preview.scope_tags?.length}
            <span class="invite-meta-label">scope</span>
            <span class="invite-meta-value">{preview.scope_tags.join(', ')}</span>
          {/if}
        </div>

        <form class="login-fields" onsubmit={submit}>
          <Field label="Username">
            <input
              class="ed-input ed-input-mono"
              bind:value={username}
              disabled={submitting}
              autocomplete="username"
              autocapitalize="off"
              spellcheck="false"
            />
          </Field>

          <Field label="Email (optional)">
            <input
              class="ed-input ed-input-mono"
              type="email"
              bind:value={email}
              disabled={submitting}
              autocomplete="email"
              placeholder={preview.email_hint ?? ''}
            />
          </Field>

          <Field label="Password" hint="At least 8 characters. You can change it later from Account settings.">
            <input
              class="ed-input ed-input-mono"
              type="password"
              bind:value={password}
              disabled={submitting}
              autocomplete="new-password"
            />
          </Field>

          {#if formError}
            <p class="login-error" role="alert">{formError}</p>
          {/if}

          <button
            type="submit"
            class="dm-btn dm-btn-primary login-submit"
            disabled={!canSubmit}
          >
            {submitting ? 'Creating account…' : 'Accept invitation'}
            <ArrowRight size={14} strokeWidth={1.5} />
          </button>
        </form>
      {/if}
    </div>

    <p class="login-footer">
      Dockmesh · self-hosted · {new Date().getFullYear()} · AGPL-3.0
    </p>
  </div>
</div>

<style>
  .login-stack {
    position: relative;
    z-index: 2;
    width: 420px;
    max-width: calc(100vw - 32px);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 20px;
  }
  .login-brand {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    color: var(--fg);
    font-size: 17px;
    font-weight: 600;
    letter-spacing: -0.005em;
  }
  .login-card {
    width: 100%;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 28px 32px 32px;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04), 0 8px 24px -12px rgba(0, 0, 0, 0.12);
  }
  .login-chrome-tr {
    position: absolute;
    z-index: 4;
    top: 28px;
    right: 32px;
  }
  .login-fields {
    margin-top: 18px;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .login-submit { margin-top: 6px; padding: 0.65rem 1rem; }
  .login-error {
    margin: 0;
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    border-radius: 5px;
    color: var(--color-danger-400);
    font-size: 12.5px;
    line-height: 1.5;
  }
  .login-footer {
    margin: 8px 0 0;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.14em;
    text-transform: uppercase;
    text-align: center;
  }
  .invite-blurb {
    margin: 14px 0 0;
    color: var(--fg-muted);
    font-size: 13.5px;
    line-height: 1.55;
  }
  .invite-error {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 14px 0 0;
    padding: 10px 12px;
    color: var(--color-danger-400);
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 30%, transparent);
    border-radius: 5px;
    font-size: 13px;
  }
  .invite-meta {
    display: grid;
    grid-template-columns: auto 1fr;
    column-gap: 12px;
    row-gap: 4px;
    margin-top: 14px;
    padding: 10px 12px;
    border: 1px dashed var(--border-strong);
    border-radius: 5px;
    background: var(--surface-hover);
  }
  .invite-meta-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    line-height: 1.8;
  }
  .invite-meta-value {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    line-height: 1.8;
  }
</style>
