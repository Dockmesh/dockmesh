<script lang="ts">
  // Authentication — editorial rebuild based on
  // `Dockmesh Wizard/authentication.jsx`. Split into private components
  // for clarity (provider modal + per-kind fields + group-mapping list).
  //
  // Tier C: all four SSO kinds (OIDC + OAuth2 + SAML + LDAP) have
  // backend CRUD endpoints and ship with shared field naming. Each
  // provider list is fetched in parallel; the UI shows them in one
  // unified list with a kind chip so admins see everything at a glance.
  //
  // Permission gating uses v1 RBAC `user.manage` for now; v2.1 migration
  // happens in the consolidated backend slice.
  import {
    api, ApiError,
    type CustomRole, type OIDCProvider, type OIDCProviderInput,
    type SAMLProvider, type LDAPProvider, type OAuth2Provider,
    type PasswordPolicy, type SignInConfig,
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { EditorialPage, Eyebrow, Field } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Plus, Trash2, Edit2, Globe, Copy,
  } from 'lucide-svelte';
  import { detectOIDCTemplate, OIDC_TEMPLATES } from './_types';
  import ProviderModal from './_ProviderModal.svelte';

  type AnyProvider =
    | (OIDCProvider & { kind: 'oidc' })
    | (OAuth2Provider & { kind: 'oauth2' })
    | (SAMLProvider & { kind: 'saml' })
    | (LDAPProvider & { kind: 'ldap' });

  let providers = $state<AnyProvider[]>([]);
  let providersLoading = $state(true);
  let roles = $state<CustomRole[]>([]);
  let policy = $state<PasswordPolicy | null>(null);
  let policyDirty = $state(false);
  let policySaving = $state(false);

  // Sessions + sign-in flow settings — persisted under the auth.* keys
  // in the settings store. Loaded once on mount; dirty-tracking on edit;
  // save = full snapshot PUT.
  let signin = $state<SignInConfig | null>(null);
  let signinDirty = $state(false);
  let signinSaving = $state(false);

  let showModal = $state(false);
  let editingProvider = $state<AnyProvider | null>(null);

  async function loadProviders() {
    providersLoading = true;
    try {
      const [oidc, oauth2, saml, ldap] = await Promise.all([
        api.oidc.listAdmin().catch(() => [] as OIDCProvider[]),
        api.oauth2.listAdmin().catch(() => [] as OAuth2Provider[]),
        api.saml.listAdmin().catch(() => [] as SAMLProvider[]),
        api.ldap.listAdmin().catch(() => [] as LDAPProvider[]),
      ]);
      providers = [
        ...oidc.map((p) => ({ ...p, kind: 'oidc' as const })),
        ...oauth2.map((p) => ({ ...p, kind: 'oauth2' as const })),
        ...saml.map((p) => ({ ...p, kind: 'saml' as const })),
        ...ldap.map((p) => ({ ...p, kind: 'ldap' as const })),
      ];
    } catch (err) {
      toast.error('Failed to load providers', err instanceof ApiError ? err.message : undefined);
    } finally {
      providersLoading = false;
    }
  }
  async function loadRoles() {
    try { roles = await api.roles.list(); } catch { /* fallback to built-ins inside the picker */ }
  }
  async function loadPolicy() {
    try { policy = await api.auth.getPolicy(); policyDirty = false; } catch { /* read-only flow */ }
  }
  async function loadSignin() {
    try { signin = await api.auth.getSignInConfig(); signinDirty = false; } catch { /* read-only flow */ }
  }
  function setSigninField<K extends keyof SignInConfig>(k: K, v: SignInConfig[K]) {
    if (!signin) return;
    signin = { ...signin, [k]: v };
    signinDirty = true;
  }
  async function saveSignin() {
    if (!signin) return;
    signinSaving = true;
    try {
      signin = await api.auth.setSignInConfig(signin);
      signinDirty = false;
      toast.success('Session settings saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      signinSaving = false;
    }
  }

  function openNew() {
    editingProvider = null;
    showModal = true;
  }

  function openEdit(p: AnyProvider) {
    editingProvider = p;
    showModal = true;
  }

  async function deleteProvider(p: AnyProvider) {
    if (!(await confirm.ask({
      title: 'Delete provider',
      message: `Delete provider "${p.display_name}"?`,
      body: "Users who signed in via this provider must fall back to password login until it's re-added.",
      confirmLabel: 'Delete', danger: true,
    }))) return;
    try {
      switch (p.kind) {
        case 'oidc':   await api.oidc.delete(p.id); break;
        case 'oauth2': await api.oauth2.delete(p.id); break;
        case 'saml':   await api.saml.delete(p.id); break;
        case 'ldap':   await api.ldap.delete(p.id); break;
      }
      toast.success('Provider deleted', p.slug);
      await loadProviders();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // toggleEnabled re-PUTs the row with `enabled` flipped. Each kind has
  // its own field set; we forward whatever was loaded so the update
  // doesn't accidentally clear unrelated fields. Secret fields stay
  // empty — backend treats empty as "keep existing".
  async function toggleEnabled(p: AnyProvider) {
    try {
      switch (p.kind) {
        case 'oidc': {
          const payload: OIDCProviderInput = {
            slug: p.slug,
            display_name: p.display_name,
            issuer_url: p.issuer_url,
            client_id: p.client_id,
            client_secret: '',
            scopes: p.scopes,
            group_claim: p.group_claim,
            admin_group: p.admin_group,
            operator_group: p.operator_group,
            default_role: p.default_role,
            enabled: !p.enabled,
          };
          await api.oidc.update(p.id, payload);
          break;
        }
        case 'oauth2':
          await api.oauth2.update(p.id, {
            slug: p.slug,
            display_name: p.display_name,
            authorization_url: p.authorization_url,
            token_url: p.token_url,
            userinfo_url: p.userinfo_url,
            client_id: p.client_id,
            client_secret: '',
            scopes: p.scopes,
            username_field: p.username_field,
            email_field: p.email_field,
            groups_field: p.groups_field,
            default_role: p.default_role,
            group_mappings: p.group_mappings,
            enabled: !p.enabled,
          });
          break;
        case 'saml':
          await api.saml.update(p.id, {
            slug: p.slug,
            display_name: p.display_name,
            idp_metadata_xml: '', // keep existing
            nameid_format: p.nameid_format,
            username_attribute: p.username_attribute,
            email_attribute: p.email_attribute,
            groups_attribute: p.groups_attribute,
            default_role: p.default_role,
            group_mappings: p.group_mappings,
            enabled: !p.enabled,
          });
          break;
        case 'ldap':
          await api.ldap.update(p.id, {
            slug: p.slug,
            display_name: p.display_name,
            host: p.host,
            port: p.port,
            tls: p.tls,
            skip_verify: p.skip_verify,
            bind_dn: p.bind_dn ?? '',
            bind_password: '',
            user_search_base: p.user_search_base,
            user_search_filter: p.user_search_filter,
            username_attribute: p.username_attribute,
            email_attribute: p.email_attribute,
            group_search_base: p.group_search_base ?? '',
            group_membership_attribute: p.group_membership_attribute,
            default_role: p.default_role,
            group_mappings: p.group_mappings,
            enabled: !p.enabled,
          });
          break;
      }
      toast.success(p.enabled ? 'Disabled' : 'Enabled', p.slug);
      await loadProviders();
    } catch (err) {
      toast.error('Toggle failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // ── Password policy editing ────────────────────────────────────────
  function setPolicyField<K extends keyof PasswordPolicy>(k: K, v: PasswordPolicy[K]) {
    if (!policy) return;
    policy = { ...policy, [k]: v };
    policyDirty = true;
  }
  function toggleUpperLower() {
    if (!policy) return;
    const both = policy.require_upper && policy.require_lower;
    policy = { ...policy, require_upper: !both, require_lower: !both };
    policyDirty = true;
  }
  function toggleRotation() {
    if (!policy) return;
    setPolicyField('rotation_days', policy.rotation_days > 0 ? 0 : 90);
  }
  async function savePolicy() {
    if (!policy) return;
    policySaving = true;
    try {
      policy = await api.auth.setPolicy(policy);
      policyDirty = false;
      toast.success('Password policy saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      policySaving = false;
    }
  }

  // ── Helpers ────────────────────────────────────────────────────────
  // Each kind gets its own icon set; OIDC dispatches through the
  // template detector so Keycloak/Google/Azure render distinct icons,
  // while the other kinds use one icon per kind for now.
  const KIND_ICON: Record<AnyProvider['kind'], string> = {
    oidc: '🔐',
    oauth2: '🐙',
    saml: '🪟',
    ldap: '📂',
  };
  const KIND_LABEL: Record<AnyProvider['kind'], string> = {
    oidc: 'OIDC',
    oauth2: 'OAuth2',
    saml: 'SAML',
    ldap: 'LDAP',
  };

  function providerIcon(p: AnyProvider): string {
    if (p.kind === 'oidc') {
      const id = detectOIDCTemplate(p.issuer_url);
      const t = OIDC_TEMPLATES.find((t) => t.id === id);
      if (t) return t.icon;
    }
    return KIND_ICON[p.kind];
  }

  function providerTypeLabel(p: AnyProvider): string {
    if (p.kind === 'oidc') {
      const id = detectOIDCTemplate(p.issuer_url);
      return OIDC_TEMPLATES.find((t) => t.id === id)?.label ?? 'OpenID Connect';
    }
    return KIND_LABEL[p.kind];
  }

  function providerSubLabel(p: AnyProvider): string {
    switch (p.kind) {
      case 'oidc':   return p.issuer_url;
      case 'oauth2': return p.authorization_url;
      case 'saml':   return p.entity_id;
      case 'ldap':   return `${p.host}:${p.port}`;
    }
  }

  function copyText(s: string) {
    copyWithToast(s, 'Copied');
  }

  const callbackBase = $derived(typeof window !== 'undefined' ? window.location.origin : '');
  const enabledProviders = $derived(providers.filter((p) => p.enabled));

  $effect(() => {
    if (allowed('system.update')) {
      loadProviders();
      loadRoles();
      loadPolicy();
      loadSignin();
    } else {
      providersLoading = false;
    }
  });
</script>

<EditorialPage>
  <section class="auth">
    <!-- ───────────────────────── Header ───────────────────────── -->
    <header class="auth-header">
      <div class="auth-header-text">
        <h1 class="ed-title auth-title">Authentication</h1>
        <p class="ed-subtitle auth-subtitle">
          {#if providers.length === 0}
            No SSO providers configured · local password only
          {:else}
            {enabledProviders.length} active provider{enabledProviders.length === 1 ? '' : 's'} · local password fallback
          {/if}
        </p>
      </div>
    </header>

    {#if !allowed('system.update')}
      <div class="dm-card auth-permission-block">
        <Globe size={18} strokeWidth={1.5} />
        <div>
          <div class="auth-perm-title">Admin-only</div>
          <p class="ed-subtitle auth-perm-blurb">
            Authentication configuration requires the <em>user.manage</em> permission.
          </p>
        </div>
      </div>
    {:else}
      <!-- ───────────────── 01 · Identity providers ─────────────── -->
      <section class="auth-section">
        <Eyebrow>01 · Identity providers</Eyebrow>

        <div class="auth-providers-toolbar">
          <span class="auth-providers-meta">
            {providers.length} configured · {enabledProviders.length} active
          </span>
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            onclick={openNew}
          >
            <Plus size={12} strokeWidth={1.5} /> Add provider
          </button>
        </div>

        {#if providersLoading}
          <Skeleton width="100%" height="6rem" />
        {:else if providers.length === 0}
          <div class="auth-empty">
            <Eyebrow>empty</Eyebrow>
            <p class="auth-empty-title">No providers configured.</p>
            <p class="ed-subtitle auth-empty-blurb">
              Click „Add provider" to pick a kind (OIDC / OAuth2 / SAML / LDAP) and a template.
            </p>
          </div>
        {:else}
          <div class="auth-providers">
            {#each providers as p (p.kind + '-' + p.id)}
              {@const sub = providerSubLabel(p)}
              <div class="auth-provider" class:auth-provider-disabled={!p.enabled}>
                <span class="auth-provider-icon">{providerIcon(p)}</span>

                <div class="auth-provider-text">
                  <div class="auth-provider-name-line">
                    <span class="auth-provider-kind-pill">{KIND_LABEL[p.kind]}</span>
                    <span class="auth-provider-name">{p.display_name}</span>
                    {#if !p.enabled}
                      <span class="dm-pill dm-pill-neutral auth-provider-pill">
                        <span class="dm-pill-dot"></span>disabled
                      </span>
                    {/if}
                  </div>
                  <div class="auth-provider-sub">
                    <span class="auth-provider-slug">slug: {p.slug}</span>
                    <span class="auth-provider-type">{providerTypeLabel(p)}</span>
                  </div>
                </div>

                <div class="auth-provider-issuer" title={sub}>
                  {sub}
                </div>

                <div class="auth-provider-role">
                  <span class="auth-provider-role-label">default role</span>
                  <span class="dm-pill dm-pill-neutral auth-provider-pill">
                    <span class="dm-pill-dot"></span>{p.default_role}
                  </span>
                </div>

                <div class="auth-provider-actions">
                  <button
                    type="button"
                    class="dm-btn dm-btn-ghost dm-btn-xs"
                    onclick={() => toggleEnabled(p)}
                  >
                    {p.enabled ? 'Disable' : 'Enable'}
                  </button>
                  <button
                    type="button"
                    class="dm-btn dm-btn-ghost dm-btn-xs"
                    onclick={() => openEdit(p)}
                    title="Edit"
                    aria-label="Edit"
                  >
                    <Edit2 size={11} strokeWidth={1.5} />
                  </button>
                  <button
                    type="button"
                    class="dm-btn dm-btn-ghost dm-btn-xs auth-revoke-btn"
                    onclick={() => deleteProvider(p)}
                    title="Delete"
                    aria-label="Delete"
                  >
                    <Trash2 size={11} strokeWidth={1.5} />
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <div class="auth-callback-hint">
          <div class="auth-callback-title">Callback URLs</div>
          <div class="auth-callback-row">
            <code class="auth-callback-code">{callbackBase}/api/v1/auth/oidc/<em>{`{slug}`}</em>/callback</code>
            <button
              type="button"
              class="dm-btn dm-btn-ghost dm-btn-xs"
              onclick={() => copyText(`${callbackBase}/api/v1/auth/oidc/{slug}/callback`)}
            >
              <Copy size={11} strokeWidth={1.5} /> copy
            </button>
          </div>
          <p class="auth-callback-blurb">
            OAuth2 / SAML / LDAP get their own callback shapes once their backend ships. Replace
            <code class="auth-inline-code">{`{slug}`}</code> with the provider's slug.
          </p>
        </div>
      </section>

      <!-- ──────────── 02 · Local password policy ───────────────── -->
      <section class="auth-section">
        <Eyebrow>02 · Local password policy</Eyebrow>

        {#if policy}
          <div class="dm-card auth-policy-card">
            <div class="auth-policy-preview-text">
              Password must be at least
              <em class="ed-accent">{policy.min_length}</em> characters
              {#if policy.require_upper && policy.require_lower}, contain <em class="ed-accent">upper + lower</em> case{/if}
              {#if policy.require_digit}, include a <em class="ed-accent">digit</em>{/if}
              {#if policy.require_symbol}, include a <em class="ed-accent">symbol</em>{/if}.
              {#if policy.rotation_days > 0}
                Rotate every <em class="ed-accent">{policy.rotation_days}d</em>.
              {:else}
                No forced rotation.
              {/if}
            </div>

            <div class="auth-chips">
              <span class="auth-chip auth-chip-active">
                ✓ min {policy.min_length} chars
              </span>
              <button
                type="button"
                class="auth-chip"
                class:auth-chip-active={policy.require_upper && policy.require_lower}
                onclick={toggleUpperLower}
              >
                {policy.require_upper && policy.require_lower ? '✓' : '○'} upper + lower
              </button>
              <button
                type="button"
                class="auth-chip"
                class:auth-chip-active={policy.require_digit}
                onclick={() => setPolicyField('require_digit', !policy!.require_digit)}
              >
                {policy.require_digit ? '✓' : '○'} digit
              </button>
              <button
                type="button"
                class="auth-chip"
                class:auth-chip-active={policy.require_symbol}
                onclick={() => setPolicyField('require_symbol', !policy!.require_symbol)}
              >
                {policy.require_symbol ? '✓' : '○'} symbol
              </button>
              <button type="button" class="auth-chip" disabled>
                ○ no reuse
              </button>
              <button
                type="button"
                class="auth-chip"
                class:auth-chip-active={policy.rotation_days > 0}
                onclick={toggleRotation}
              >
                {policy.rotation_days > 0 ? `✓ rotate ${policy.rotation_days}d` : '○ no rotation'}
              </button>
            </div>

            <div class="auth-policy-grid">
              <Field label="Minimum length">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="6"
                  max="128"
                  value={policy.min_length}
                  oninput={(e) => setPolicyField('min_length', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
              <Field label="Rotation (days)" hint="0 = no forced rotation.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="0"
                  max="365"
                  value={policy.rotation_days}
                  oninput={(e) => setPolicyField('rotation_days', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
              <Field label="Lockout · max attempts" hint="Failed sign-ins before lock.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="3"
                  max="50"
                  value={policy.lockout_max_attempts}
                  oninput={(e) => setPolicyField('lockout_max_attempts', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
              <Field label="Lockout · duration (min)" hint="Auto-unlock after.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="1"
                  max="1440"
                  value={policy.lockout_duration_minutes}
                  oninput={(e) => setPolicyField('lockout_duration_minutes', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
            </div>

            {#if policyDirty}
              <div class="auth-policy-actions">
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-sm"
                  onclick={loadPolicy}
                  disabled={policySaving}
                >
                  Discard
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-primary dm-btn-sm"
                  onclick={savePolicy}
                  disabled={policySaving}
                >
                  {policySaving ? 'Saving…' : 'Save policy'}
                </button>
              </div>
            {/if}
          </div>
        {:else}
          <Skeleton width="100%" height="8rem" />
        {/if}
      </section>

      <!-- ─────── 03 · Sessions & sign-in flow ─────── -->
      <section class="auth-section">
        <Eyebrow>03 · Sessions &amp; sign-in flow</Eyebrow>

        {#if signin}
          <div class="dm-card auth-policy-card">
            <div class="auth-policy-preview-text">
              Sessions expire after
              <em class="ed-accent">{signin.session_absolute_ttl_hr}h</em>
              {#if signin.session_idle_ttl_min > 0}
                or <em class="ed-accent">{signin.session_idle_ttl_min}min</em> of inactivity, whichever comes first.
              {:else}
                regardless of activity.
              {/if}
              Each user may hold at most
              <em class="ed-accent">{signin.session_max_per_user > 0 ? signin.session_max_per_user : 'unlimited'}</em>
              concurrent sessions{#if signin.session_max_per_user > 0} — the oldest get evicted on new sign-in{/if}.
              Revoked + expired rows are hard-deleted after 30 days.
            </div>

            <div class="auth-policy-grid">
              <Field label="Idle timeout (min)" hint="Revoke after N minutes without a refresh. 0 = no idle expiry.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="0"
                  max="1440"
                  value={signin.session_idle_ttl_min}
                  oninput={(e) => setSigninField('session_idle_ttl_min', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
              <Field label="Absolute lifetime (h)" hint="Hard cap on a single session before re-auth.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="1"
                  max="720"
                  value={signin.session_absolute_ttl_hr}
                  oninput={(e) => setSigninField('session_absolute_ttl_hr', parseInt((e.target as HTMLInputElement).value) || 1)}
                />
              </Field>
              <Field label="Remember-me (days)" hint="How long a 'stay signed in' session lives.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="0"
                  max="365"
                  value={signin.session_remember_me_days}
                  oninput={(e) => setSigninField('session_remember_me_days', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
              <Field label="Max sessions per user" hint="0 = unlimited. Oldest by last-seen gets evicted first.">
                <input
                  type="number"
                  class="dm-input acc-input-mono"
                  min="0"
                  max="200"
                  value={signin.session_max_per_user}
                  oninput={(e) => setSigninField('session_max_per_user', parseInt((e.target as HTMLInputElement).value) || 0)}
                />
              </Field>
            </div>

            <div class="auth-toggles">
              <label class="auth-toggle">
                <input
                  type="checkbox"
                  class="auth-toggle-checkbox"
                  checked={signin.require_tfa_for_admin}
                  onchange={(e) => setSigninField('require_tfa_for_admin', (e.target as HTMLInputElement).checked)}
                />
                <div class="auth-toggle-text">
                  <div class="auth-toggle-label">Require 2FA for admin role</div>
                  <div class="auth-toggle-hint">Admin users must enrol TOTP. Block sign-in until then.</div>
                </div>
              </label>
              <label class="auth-toggle">
                <input
                  type="checkbox"
                  class="auth-toggle-checkbox"
                  checked={signin.allow_local_password}
                  onchange={(e) => setSigninField('allow_local_password', (e.target as HTMLInputElement).checked)}
                />
                <div class="auth-toggle-text">
                  <div class="auth-toggle-label">Allow local password sign-in</div>
                  <div class="auth-toggle-hint">If off, only OIDC/OAuth2/SAML/LDAP providers work.</div>
                </div>
              </label>
              <label class="auth-toggle">
                <input
                  type="checkbox"
                  class="auth-toggle-checkbox"
                  checked={signin.auto_create_on_sso}
                  onchange={(e) => setSigninField('auto_create_on_sso', (e.target as HTMLInputElement).checked)}
                />
                <div class="auth-toggle-text">
                  <div class="auth-toggle-label">Auto-create accounts on first SSO sign-in</div>
                  <div class="auth-toggle-hint">Lands new users in the per-provider default role.</div>
                </div>
              </label>
              <label class="auth-toggle">
                <input
                  type="checkbox"
                  class="auth-toggle-checkbox"
                  checked={signin.allow_self_register}
                  onchange={(e) => setSigninField('allow_self_register', (e.target as HTMLInputElement).checked)}
                />
                <div class="auth-toggle-text">
                  <div class="auth-toggle-label">Allow self-registration</div>
                  <div class="auth-toggle-hint">A "create account" link on the sign-in page.</div>
                </div>
              </label>
            </div>

            {#if signinDirty}
              <div class="auth-policy-actions">
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-sm"
                  onclick={loadSignin}
                  disabled={signinSaving}
                >
                  Discard
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-primary dm-btn-sm"
                  onclick={saveSignin}
                  disabled={signinSaving}
                >
                  {signinSaving ? 'Saving…' : 'Save settings'}
                </button>
              </div>
            {/if}
          </div>
        {:else}
          <Skeleton width="100%" height="12rem" />
        {/if}
      </section>
    {/if}
  </section>
</EditorialPage>

<ProviderModal
  bind:open={showModal}
  editing={editingProvider}
  {roles}
  {callbackBase}
  onSaved={loadProviders}
/>

<style>
  .auth {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .auth-header {
    display: flex;
    align-items: flex-end;
    gap: 24px;
    flex-wrap: wrap;
  }
  .auth-header-text { min-width: 0; max-width: 70ch; }
  .auth-title {
    font-size: 30px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .auth-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }

  /* ── Permission gate ────────────────────────────────────────── */
  .auth-permission-block {
    padding: 22px;
    display: flex;
    gap: 14px;
    align-items: flex-start;
    color: var(--fg-muted);
  }
  .auth-perm-title {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .auth-perm-blurb { margin-top: 4px; }

  /* ── Sections ───────────────────────────────────────────────── */
  .auth-section { display: flex; flex-direction: column; gap: 14px; }

  /* ── Providers ──────────────────────────────────────────────── */
  .auth-providers-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }
  .auth-providers-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .auth-providers {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .auth-provider {
    padding: 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    display: grid;
    grid-template-columns: 36px minmax(0, 1.4fr) minmax(0, 1.2fr) 130px 150px;
    gap: 14px;
    align-items: center;
  }
  .auth-provider-disabled { opacity: 0.55; }
  .auth-provider-icon {
    width: 36px;
    height: 36px;
    font-size: 18px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    flex-shrink: 0;
  }
  .auth-provider-text { min-width: 0; }
  .auth-provider-name-line {
    display: flex;
    align-items: center;
    gap: 7px;
    flex-wrap: wrap;
  }
  .auth-provider-kind-pill {
    padding: 0 6px;
    font-family: var(--font-mono);
    font-size: 9.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg-subtle);
  }
  .auth-provider-name {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .auth-provider-pill { padding: 0 5px; font-size: 9.5px; }
  .auth-provider-sub {
    display: flex;
    gap: 8px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .auth-provider-issuer {
    min-width: 0;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .auth-provider-role {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .auth-provider-role-label {
    font-family: var(--font-mono);
    font-size: 9.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
  }
  .auth-provider-actions {
    display: inline-flex;
    justify-content: flex-end;
    gap: 4px;
  }
  .auth-revoke-btn { color: var(--color-danger-400); }

  /* ── Empty ──────────────────────────────────────────────────── */
  .auth-empty {
    padding: 32px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
  }
  .auth-empty-title {
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
    margin: 8px 0 0;
  }
  .auth-empty-blurb {
    margin: 0;
    max-width: 50ch;
  }

  /* ── Callback URL hint ─────────────────────────────────────── */
  .auth-callback-hint {
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 5px;
    color: var(--fg-muted);
  }
  .auth-callback-title {
    font-size: 12px;
    font-weight: 500;
    color: var(--fg);
    margin-bottom: 4px;
  }
  .auth-callback-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .auth-callback-code {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--accent-fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1;
  }
  .auth-callback-blurb {
    margin: 6px 0 0;
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .auth-inline-code {
    font-family: var(--font-mono);
    font-size: 11px;
    background: var(--bg-elevated);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border);
  }

  /* ── Password policy card ──────────────────────────────────── */
  .auth-policy-card {
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .auth-policy-preview-text {
    padding: 10px 12px;
    border: 1px solid var(--border-subtle);
    background: var(--bg-elevated);
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.7;
  }
  .auth-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .auth-chip {
    padding: 5px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 999px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    cursor: pointer;
    text-decoration: line-through;
    transition: border-color 0.12s, background 0.12s, color 0.12s;
  }
  .auth-chip-active {
    background: color-mix(in srgb, var(--color-success-500) 12%, var(--surface));
    border-color: color-mix(in srgb, var(--color-success-500) 40%, var(--border));
    color: var(--color-success-400);
    text-decoration: none;
  }
  .auth-chip:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .auth-policy-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
  }
  .auth-policy-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    padding-top: 14px;
    border-top: 1px solid var(--border-subtle);
  }

  /* ── Sessions sliceblock ───────────────────────────────────── */
  .auth-sliceblock {
    padding: 18px;
    background: var(--surface);
    border: 1px dashed var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .auth-toggles {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding-top: 14px;
    border-top: 1px solid var(--border-subtle);
  }
  .auth-toggle {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    cursor: pointer;
  }
  .auth-toggle-checkbox {
    margin-top: 3px;
    width: 14px;
    height: 14px;
    accent-color: var(--color-brand-500);
    flex-shrink: 0;
    cursor: pointer;
  }
  .auth-toggle-text { flex: 1; min-width: 0; }
  .auth-toggle-label {
    font-size: 12.5px;
    color: var(--fg);
  }
  .auth-toggle-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }

  /* Reuse helper */
  :global(.acc-input-mono) { font-family: var(--font-mono); }

  @media (max-width: 880px) {
    .auth-provider {
      grid-template-columns: 36px minmax(0, 1fr) auto;
      grid-auto-flow: row;
    }
    .auth-provider-issuer,
    .auth-provider-role { grid-column: 2 / -1; }
    .auth-policy-grid { grid-template-columns: 1fr; }
  }
</style>
