<script lang="ts">
  // Provider modal — orchestrates kind-picker → template-picker → form.
  // All four kinds (OIDC / OAuth2 / SAML / LDAP) have functional save
  // paths through their respective Tier-C backend services.
  import {
    api, ApiError,
    type CustomRole,
    type OIDCProvider, type OIDCProviderInput,
    type OAuth2Provider, type OAuth2ProviderInput,
    type SAMLProvider, type SAMLProviderInput,
    type LDAPProvider, type LDAPProviderInput,
  } from '$lib/api';
  import { EditorialModal } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { ChevronLeft } from 'lucide-svelte';
  import {
    type ProviderKind,
    type OIDCConfig, type OAuth2Config, type SAMLConfig, type LDAPConfig,
    OIDC_TEMPLATES, OAUTH2_TEMPLATES, SAML_TEMPLATES,
    detectOIDCTemplate,
    newOIDCConfig, newOAuth2Config, newSAMLConfig, newLDAPConfig,
    ldapDefaultsForMode,
  } from './_types';
  import OIDCFields from './_OIDCFields.svelte';
  import OAuth2Fields from './_OAuth2Fields.svelte';
  import SAMLFields from './_SAMLFields.svelte';
  import LDAPFields from './_LDAPFields.svelte';
  import { m } from '$lib/paraglide/messages.js';

  type AnyProvider =
    | (OIDCProvider & { kind: 'oidc' })
    | (OAuth2Provider & { kind: 'oauth2' })
    | (SAMLProvider & { kind: 'saml' })
    | (LDAPProvider & { kind: 'ldap' });

  interface Props {
    open: boolean;
    /** When set, the modal opens in edit-mode for that provider. */
    editing?: AnyProvider | null;
    roles: CustomRole[];
    callbackBase: string;
    onSaved: () => void;
  }

  let {
    open = $bindable(false),
    editing = null,
    roles,
    callbackBase,
    onSaved,
  }: Props = $props();

  // ── Modal flow ──────────────────────────────────────────────────────
  type Mode = 'kind' | 'template' | 'form';
  let mode = $state<Mode>('kind');
  let kind = $state<ProviderKind | null>(null);
  let template = $state<string | null>(null);
  let busy = $state(false);

  // One config per kind — we keep them resident so the user can flip
  // between kinds in the picker without losing typed input.
  let oidc = $state<OIDCConfig>(newOIDCConfig());
  let oauth2 = $state<OAuth2Config>(newOAuth2Config());
  let saml = $state<SAMLConfig>(newSAMLConfig());
  let ldap = $state<LDAPConfig>(newLDAPConfig());

  // Reset / hydrate on open. Tracks the modal's open prop.
  let lastOpen = false;
  $effect(() => {
    if (open && !lastOpen) {
      if (editing) {
        kind = editing.kind;
        switch (editing.kind) {
          case 'oidc':
            template = detectOIDCTemplate(editing.issuer_url);
            oidc = oidcConfigFromProvider(editing);
            break;
          case 'oauth2':
            template = 'generic';
            oauth2 = oauth2ConfigFromProvider(editing);
            break;
          case 'saml':
            template = 'generic';
            saml = samlConfigFromProvider(editing);
            break;
          case 'ldap':
            template = null;
            ldap = ldapConfigFromProvider(editing);
            break;
        }
        mode = 'form';
      } else {
        // Fresh add — reset state and start at kind picker.
        oidc = newOIDCConfig();
        oauth2 = newOAuth2Config();
        saml = newSAMLConfig();
        ldap = newLDAPConfig();
        kind = null;
        template = null;
        mode = 'kind';
      }
    }
    lastOpen = open;
  });

  // Mirrors the server: subjects are unique only within one identity
  // provider, so saving another one unlinks the provider's accounts.
  function urlKey(u: string): string {
    const s = u.trim().replace(/\/+$/, '');
    try {
      const p = new URL(s);
      return `${p.protocol}//${p.host}${p.pathname}${p.search}`;
    } catch {
      return s;
    }
  }

  const unlinkNote = m.authn_unlink_note();

  const unlinkWarning = $derived.by(() => {
    if (!editing || editing.kind !== kind) return '';
    switch (editing.kind) {
      case 'oidc':
        return urlKey(editing.issuer_url) !== urlKey(oidc.issuer_url)
          ? m.authn_unlink_issuer({ note: unlinkNote })
          : '';
      case 'oauth2':
        return urlKey(editing.userinfo_url) !== urlKey(oauth2.userinfo_url) ||
          (editing.subject_field ?? '').trim() !== oauth2.subject_field.trim()
          ? m.authn_unlink_oauth2({ note: unlinkNote })
          : '';
      case 'ldap':
        return editing.host.trim().toLowerCase() !== ldap.host.trim().toLowerCase() || editing.port !== ldap.port ||
          (editing.subject_attribute ?? '').trim().toLowerCase() !== ldap.subject_attribute.trim().toLowerCase()
          ? m.authn_unlink_ldap({ note: unlinkNote })
          : '';
      case 'saml':
        return saml.idp_metadata_xml.trim() !== '' &&
          saml.idp_metadata_xml.trim() !== (editing.idp_metadata_xml ?? '').trim()
          ? m.authn_unlink_saml()
          : '';
    }
    return '';
  });

  function oidcConfigFromProvider(p: OIDCProvider): OIDCConfig {
    const mappings = (p.group_mappings ?? []).map((gm) => ({ group: gm.group_value, role: gm.role_name, scope: gm.scope }));
    return {
      kind: 'oidc',
      slug: p.slug,
      display_name: p.display_name,
      enabled: p.enabled,
      issuer_url: p.issuer_url,
      client_id: p.client_id,
      client_secret: '',
      scopes: p.scopes,
      group_claim: p.group_claim ?? 'groups',
      default_role: p.default_role,
      allow_email_link: !!p.allow_email_link,
      group_mappings: mappings,
    };
  }

  function oauth2ConfigFromProvider(p: OAuth2Provider): OAuth2Config {
    return {
      kind: 'oauth2',
      slug: p.slug,
      display_name: p.display_name,
      enabled: p.enabled,
      authorization_url: p.authorization_url,
      token_url: p.token_url,
      userinfo_url: p.userinfo_url,
      client_id: p.client_id,
      client_secret: '',
      scopes: p.scopes,
      subject_field: p.subject_field ?? '',
      username_field: p.username_field,
      email_field: p.email_field,
      groups_field: p.groups_field,
      default_role: p.default_role,
      allow_email_link: !!p.allow_email_link,
      group_mappings: p.group_mappings,
    };
  }

  function samlConfigFromProvider(p: SAMLProvider): SAMLConfig {
    return {
      kind: 'saml',
      slug: p.slug,
      display_name: p.display_name,
      enabled: p.enabled,
      idp_metadata_url: '',
      idp_metadata_xml: p.idp_metadata_xml ?? '',
      sp_entity_id: p.entity_id,
      signing_cert: '',
      encryption_cert: '',
      nameid_format: p.nameid_format,
      subject_attribute: p.subject_attribute ?? '',
      username_attribute: p.username_attribute,
      email_attribute: p.email_attribute,
      groups_attribute: p.groups_attribute,
      default_role: p.default_role,
      allow_email_link: !!p.allow_email_link,
      group_mappings: p.group_mappings,
    };
  }

  function ldapConfigFromProvider(p: LDAPProvider): LDAPConfig {
    return {
      kind: 'ldap',
      slug: p.slug,
      display_name: p.display_name,
      enabled: p.enabled,
      mode: p.user_search_filter.includes('sAMAccountName') ? 'ad' : 'openldap',
      host: p.host,
      port: p.port,
      tls: p.tls,
      skip_verify: p.skip_verify,
      bind_dn: p.bind_dn ?? '',
      bind_password: '',
      user_search_base: p.user_search_base,
      user_search_filter: p.user_search_filter,
      subject_attribute: p.subject_attribute ?? '',
      username_attribute: p.username_attribute,
      email_attribute: p.email_attribute,
      group_search_base: p.group_search_base ?? '',
      group_membership_attribute: p.group_membership_attribute,
      default_role: p.default_role,
      allow_email_link: !!p.allow_email_link,
      group_mappings: p.group_mappings,
    };
  }

  function pickKind(k: ProviderKind) {
    kind = k;
    template = null;
    if (k === 'ldap') {
      // No template step — mode switch in the form.
      mode = 'form';
    } else {
      mode = 'template';
    }
  }

  function pickTemplate(id: string) {
    template = id;
    if (kind === 'oidc') {
      const t = OIDC_TEMPLATES.find((x) => x.id === id);
      if (t) {
        oidc.scopes = t.scopes;
        if (!oidc.display_name) oidc.display_name = t.label;
      }
    } else if (kind === 'oauth2') {
      const t = OAUTH2_TEMPLATES.find((x) => x.id === id);
      if (t) {
        oauth2.authorization_url = t.authorization_url;
        oauth2.token_url = t.token_url;
        oauth2.userinfo_url = t.userinfo_url;
        oauth2.scopes = t.scopes;
        oauth2.subject_field = t.subject_field;
        oauth2.username_field = t.username_field;
        oauth2.email_field = t.email_field;
        oauth2.groups_field = t.groups_field;
        if (!oauth2.display_name) oauth2.display_name = t.label;
      }
    } else if (kind === 'saml') {
      const t = SAML_TEMPLATES.find((x) => x.id === id);
      if (t) {
        saml.idp_metadata_url = t.idp_metadata_url_hint;
        saml.username_attribute = t.username_attribute;
        saml.email_attribute = t.email_attribute;
        saml.groups_attribute = t.groups_attribute;
        saml.nameid_format = t.nameid_format;
        saml.subject_attribute = t.subject_attribute;
        if (!saml.display_name) saml.display_name = t.label;
      }
    }
    mode = 'form';
  }

  function back() {
    if (mode === 'form' && !editing) {
      if (kind === 'ldap') {
        mode = 'kind';
        kind = null;
      } else {
        mode = 'template';
      }
    } else if (mode === 'template') {
      mode = 'kind';
      kind = null;
      template = null;
    }
  }

  function close() {
    open = false;
  }

  function copyText(s: string) {
    copyWithToast(s, m.common_copied());
  }

  // ── Validation ──────────────────────────────────────────────────────
  const commonValid = $derived(
    activeConfig().slug.trim().length > 0 &&
    activeConfig().display_name.trim().length > 0,
  );

  const oidcValid = $derived(
    commonValid &&
    oidc.issuer_url.trim().length > 0 &&
    oidc.client_id.trim().length > 0,
  );

  const oauth2Valid = $derived(
    commonValid &&
    oauth2.authorization_url.trim().length > 0 &&
    oauth2.token_url.trim().length > 0 &&
    oauth2.userinfo_url.trim().length > 0 &&
    oauth2.client_id.trim().length > 0,
  );

  const samlValid = $derived(
    commonValid &&
    saml.idp_metadata_xml.trim().length > 0,
  );

  const ldapValid = $derived(
    commonValid &&
    ldap.host.trim().length > 0 &&
    ldap.user_search_base.trim().length > 0,
  );

  function activeConfig() {
    if (kind === 'oidc') return oidc;
    if (kind === 'oauth2') return oauth2;
    if (kind === 'saml') return saml;
    if (kind === 'ldap') return ldap;
    return oidc; // default
  }

  // save() dispatches per-kind. Each branch builds the backend payload
  // from the in-modal config and calls the matching CRUD endpoint.
  async function save() {
    if (!kind) return;
    busy = true;
    try {
      switch (kind) {
        case 'oidc':   await saveOIDC();   break;
        case 'oauth2': await saveOAuth2(); break;
        case 'saml':   await saveSAML();   break;
        case 'ldap':   await saveLDAP();   break;
      }
      open = false;
      onSaved();
    } catch (err) {
      toast.error(m.authn_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function saveOIDC() {
    const payload: OIDCProviderInput = {
      slug: oidc.slug.trim(),
      display_name: oidc.display_name.trim(),
      issuer_url: oidc.issuer_url.trim(),
      client_id: oidc.client_id.trim(),
      client_secret: oidc.client_secret,
      scopes: oidc.scopes.trim() || 'openid profile email',
      group_claim: oidc.group_claim.trim() || undefined,
      group_mappings: oidc.group_mappings
        .filter((gm) => gm.group && gm.role)
        .map((gm) => ({ group_value: gm.group, role_name: gm.role, scope: gm.scope ?? { type: 'global' } })),
      default_role: oidc.default_role,
      allow_email_link: oidc.allow_email_link,
      enabled: oidc.enabled,
    };
    if (editing && editing.kind === 'oidc') {
      await api.oidc.update(editing.id, payload);
      toast.success(m.authn_provider_updated(), oidc.slug);
    } else {
      await api.oidc.create(payload);
      toast.success(m.authn_provider_added(), oidc.slug);
    }
  }

  async function saveOAuth2() {
    const payload: OAuth2ProviderInput = {
      slug: oauth2.slug.trim(),
      display_name: oauth2.display_name.trim(),
      authorization_url: oauth2.authorization_url.trim(),
      token_url: oauth2.token_url.trim(),
      userinfo_url: oauth2.userinfo_url.trim(),
      client_id: oauth2.client_id.trim(),
      client_secret: oauth2.client_secret,
      scopes: oauth2.scopes.trim(),
      subject_field: oauth2.subject_field.trim(),
      username_field: oauth2.username_field.trim() || 'username',
      email_field: oauth2.email_field.trim() || 'email',
      groups_field: oauth2.groups_field.trim(),
      default_role: oauth2.default_role,
      allow_email_link: oauth2.allow_email_link,
      group_mappings: oauth2.group_mappings.filter((gm) => gm.group && gm.role),
      enabled: oauth2.enabled,
    };
    if (editing && editing.kind === 'oauth2') {
      await api.oauth2.update(editing.id, payload);
      toast.success(m.authn_provider_updated(), oauth2.slug);
    } else {
      await api.oauth2.create(payload);
      toast.success(m.authn_provider_added(), oauth2.slug);
    }
  }

  async function saveSAML() {
    const payload: SAMLProviderInput = {
      slug: saml.slug.trim(),
      display_name: saml.display_name.trim(),
      idp_metadata_xml: saml.idp_metadata_xml.trim(),
      nameid_format: saml.nameid_format,
      subject_attribute: saml.subject_attribute.trim(),
      username_attribute: saml.username_attribute.trim() || 'NameID',
      email_attribute: saml.email_attribute.trim() || 'email',
      groups_attribute: saml.groups_attribute.trim() || 'groups',
      default_role: saml.default_role,
      allow_email_link: saml.allow_email_link,
      group_mappings: saml.group_mappings.filter((gm) => gm.group && gm.role),
      enabled: saml.enabled,
    };
    if (editing && editing.kind === 'saml') {
      await api.saml.update(editing.id, payload);
      toast.success(m.authn_provider_updated(), saml.slug);
    } else {
      await api.saml.create(payload);
      toast.success(m.authn_provider_added(), saml.slug);
    }
  }

  async function saveLDAP() {
    const payload: LDAPProviderInput = {
      slug: ldap.slug.trim(),
      display_name: ldap.display_name.trim(),
      host: ldap.host.trim(),
      port: ldap.port,
      tls: ldap.tls,
      skip_verify: ldap.skip_verify,
      bind_dn: ldap.bind_dn.trim(),
      bind_password: ldap.bind_password,
      user_search_base: ldap.user_search_base.trim(),
      user_search_filter: ldap.user_search_filter.trim(),
      subject_attribute: ldap.subject_attribute.trim(),
      username_attribute: ldap.username_attribute.trim() || 'uid',
      email_attribute: ldap.email_attribute.trim() || 'mail',
      group_search_base: ldap.group_search_base.trim(),
      group_membership_attribute: ldap.group_membership_attribute.trim() || 'memberOf',
      default_role: ldap.default_role,
      allow_email_link: ldap.allow_email_link,
      group_mappings: ldap.group_mappings.filter((gm) => gm.group && gm.role),
      enabled: ldap.enabled,
    };
    if (editing && editing.kind === 'ldap') {
      await api.ldap.update(editing.id, payload);
      toast.success(m.authn_provider_updated(), ldap.slug);
    } else {
      await api.ldap.create(payload);
      toast.success(m.authn_provider_added(), ldap.slug);
    }
  }

  // ── Display helpers ─────────────────────────────────────────────────
  const KIND_META: Record<ProviderKind, { label: string; icon: string; blurb: string }> = {
    oidc:   { label: 'OIDC',   icon: '🔐', blurb: 'OpenID Connect — Keycloak, Authelia, Azure AD, Google, GitLab' },
    oauth2: { label: 'OAuth2', icon: '🐙', blurb: 'OAuth 2.0 — GitHub, GitLab OAuth, Google OAuth' },
    saml:   { label: 'SAML',   icon: '🪟', blurb: 'SAML 2.0 — Azure AD, AD FS, Okta, Authentik' },
    ldap:   { label: 'LDAP',   icon: '📂', blurb: 'LDAP / Active Directory — OpenLDAP, AD, FreeIPA' },
  };

  const currentKindMeta = $derived(kind ? KIND_META[kind] : null);

  const currentTemplateLabel = $derived.by(() => {
    if (!template || !kind) return null;
    if (kind === 'oidc')   return OIDC_TEMPLATES.find((t) => t.id === template)?.label ?? null;
    if (kind === 'oauth2') return OAUTH2_TEMPLATES.find((t) => t.id === template)?.label ?? null;
    if (kind === 'saml')   return SAML_TEMPLATES.find((t) => t.id === template)?.label ?? null;
    return null;
  });

  const currentHelpText = $derived.by(() => {
    if (!template || !kind) return undefined;
    if (kind === 'oidc')   return OIDC_TEMPLATES.find((t) => t.id === template)?.help;
    if (kind === 'oauth2') return OAUTH2_TEMPLATES.find((t) => t.id === template)?.help;
    if (kind === 'saml')   return SAML_TEMPLATES.find((t) => t.id === template)?.help;
    return undefined;
  });

  const issuerHint = $derived.by(() => {
    if (kind !== 'oidc' || !template) return undefined;
    return OIDC_TEMPLATES.find((t) => t.id === template)?.issuer_hint;
  });

  const eyebrow = $derived(
    mode === 'kind' ? m.authn_add_provider() :
    mode === 'template' ? m.authn_add_kind_provider({ kind: currentKindMeta?.label ?? '' }) :
    editing ? m.authn_edit_provider() :
    m.authn_new_kind_provider({ kind: currentKindMeta?.label ?? '' }),
  );

  const saveDisabled = $derived(
    busy ||
    kind === null ||
    (kind === 'oidc' && !oidcValid) ||
    (kind === 'oauth2' && !oauth2Valid) ||
    (kind === 'saml' && !samlValid) ||
    (kind === 'ldap' && !ldapValid),
  );
</script>

<EditorialModal
  bind:open
  {eyebrow}
  width={640}
  onclose={() => { open = false; }}
>
  {#snippet title()}
    {#if mode === 'kind'}
      {m.authn_pick_type_before()} <em>{m.authn_pick_type_em()}</em> {m.authn_pick_type_after()}
    {:else if mode === 'template'}
      {m.authn_pick_tpl_before()} <em>{currentKindMeta?.label}</em> {m.authn_pick_tpl_after()}
    {:else if editing}
      {m.authn_edit_title_before()} <em class="ed-accent">{editing.display_name}</em> {m.authn_edit_title_after()}
    {:else}
      {m.authn_connect_before()} <em class="ed-accent">{currentTemplateLabel ?? currentKindMeta?.label ?? m.authn_provider_lc()}</em> {m.authn_connect_after()}
    {/if}
  {/snippet}

  {#if mode === 'kind'}
    <div class="pm-kind-grid">
      {#each ['oidc', 'oauth2', 'saml', 'ldap'] as k (k)}
        {@const meta = KIND_META[k as ProviderKind]}
        <button
          type="button"
          class="pm-kind-card"
          onclick={() => pickKind(k as ProviderKind)}
        >
          <span class="pm-kind-icon">{meta.icon}</span>
          <div class="pm-kind-text">
            <span class="pm-kind-label">{meta.label}</span>
            <span class="pm-kind-blurb">{meta.blurb}</span>
          </div>
        </button>
      {/each}
    </div>
  {:else if mode === 'template'}
    <div class="pm-template-grid">
      {#if kind === 'oidc'}
        {#each OIDC_TEMPLATES as t (t.id)}
          <button type="button" class="pm-template-card" onclick={() => pickTemplate(t.id)}>
            <span class="pm-template-icon">{t.icon}</span>
            <div class="pm-template-text">
              <span class="pm-template-label">{t.label}</span>
              <span class="pm-template-hint">{t.issuer_hint}</span>
            </div>
          </button>
        {/each}
      {:else if kind === 'oauth2'}
        {#each OAUTH2_TEMPLATES as t (t.id)}
          <button type="button" class="pm-template-card" onclick={() => pickTemplate(t.id)}>
            <span class="pm-template-icon">{t.icon}</span>
            <div class="pm-template-text">
              <span class="pm-template-label">{t.label}</span>
              <span class="pm-template-hint">{t.authorization_url}</span>
            </div>
          </button>
        {/each}
      {:else if kind === 'saml'}
        {#each SAML_TEMPLATES as t (t.id)}
          <button type="button" class="pm-template-card" onclick={() => pickTemplate(t.id)}>
            <span class="pm-template-icon">{t.icon}</span>
            <div class="pm-template-text">
              <span class="pm-template-label">{t.label}</span>
              <span class="pm-template-hint">{t.idp_metadata_url_hint}</span>
            </div>
          </button>
        {/each}
      {/if}
    </div>
  {:else}
    <form id="pm-form" class="pm-form" onsubmit={(e) => { e.preventDefault(); save(); }}>
      <div class="pm-grid-2 pm-grid-narrow-wide">
        <div class="pm-field">
          <span class="pm-label">{m.authn_slug()}</span>
          <input
            class="dm-input acc-input-mono"
            value={activeConfig().slug}
            oninput={(e) => {
              const v = (e.target as HTMLInputElement).value;
              if (kind === 'oidc') oidc.slug = v;
              else if (kind === 'oauth2') oauth2.slug = v;
              else if (kind === 'saml') saml.slug = v;
              else if (kind === 'ldap') ldap.slug = v;
            }}
            disabled={editing !== null}
            placeholder="auth"
          />
          <span class="pm-hint">{m.authn_slug_hint()}</span>
        </div>
        <div class="pm-field">
          <span class="pm-label">{m.authn_display_name()}</span>
          <input
            class="dm-input"
            value={activeConfig().display_name}
            oninput={(e) => {
              const v = (e.target as HTMLInputElement).value;
              if (kind === 'oidc') oidc.display_name = v;
              else if (kind === 'oauth2') oauth2.display_name = v;
              else if (kind === 'saml') saml.display_name = v;
              else if (kind === 'ldap') ldap.display_name = v;
            }}
            placeholder={currentTemplateLabel ?? currentKindMeta?.label ?? 'IdP'}
          />
          <span class="pm-hint">{m.authn_display_name_hint()}</span>
        </div>
      </div>

      <label class="pm-enabled">
        <input
          type="checkbox"
          checked={activeConfig().enabled}
          onchange={(e) => {
            const v = (e.target as HTMLInputElement).checked;
            if (kind === 'oidc') oidc.enabled = v;
            else if (kind === 'oauth2') oauth2.enabled = v;
            else if (kind === 'saml') saml.enabled = v;
            else if (kind === 'ldap') ldap.enabled = v;
          }}
        />
        <span>{m.authn_enabled_accept()}</span>
      </label>

      {#if kind === 'oidc' || kind === 'oauth2'}
      <label class="pm-enabled">
        <input
          type="checkbox"
          checked={activeConfig().allow_email_link}
          onchange={(e) => {
            const v = (e.target as HTMLInputElement).checked;
            if (kind === 'oidc') oidc.allow_email_link = v;
            else if (kind === 'oauth2') oauth2.allow_email_link = v;
            else if (kind === 'saml') saml.allow_email_link = v;
            else if (kind === 'ldap') ldap.allow_email_link = v;
          }}
        />
        <span>{m.authn_link_email()}</span>
      </label>
      {/if}

      {#if kind === 'oidc'}
        <OIDCFields
          bind:config={oidc}
          editing={editing !== null}
          {roles}
          {callbackBase}
          issuerHint={issuerHint}
          helpText={currentHelpText}
          onCopy={copyText}
        />
      {:else if kind === 'oauth2'}
        <OAuth2Fields
          bind:config={oauth2}
          editing={false}
          {roles}
          {callbackBase}
          helpText={currentHelpText}
          onCopy={copyText}
        />
      {:else if kind === 'saml'}
        <SAMLFields
          bind:config={saml}
          editing={false}
          {roles}
          {callbackBase}
          helpText={currentHelpText}
          onCopy={copyText}
        />
      {:else if kind === 'ldap'}
        <LDAPFields
          bind:config={ldap}
          editing={false}
          {roles}
        />
      {/if}

      {#if unlinkWarning}
        <p class="pm-unlink" role="alert">{unlinkWarning}</p>
      {/if}
    </form>
  {/if}

  {#snippet footer()}
    {#if mode === 'kind'}
      <span></span>
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={close}>
        {m.common_cancel()}
      </button>
    {:else if mode === 'template'}
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm pm-back" onclick={back}>
        <ChevronLeft size={11} strokeWidth={1.5} /> {m.common_back()}
      </button>
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={close}>
        {m.common_cancel()}
      </button>
    {:else}
      {#if !editing}
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm pm-back" onclick={back}>
          <ChevronLeft size={11} strokeWidth={1.5} /> {m.common_back()}
        </button>
      {:else}
        <span></span>
      {/if}
      <div class="pm-form-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={close}>
          {m.common_cancel()}
        </button>
        <button
          type="submit"
          form="pm-form"
          class="dm-btn dm-btn-primary dm-btn-sm"
          disabled={saveDisabled}
        >
          {busy ? m.common_saving() : editing ? m.common_save() : m.authn_add_provider()}
        </button>
      </div>
    {/if}
  {/snippet}
</EditorialModal>

<style>
  .pm-kind-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  .pm-kind-card {
    padding: 14px 16px;
    display: flex;
    align-items: center;
    gap: 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 5px;
    cursor: pointer;
    text-align: left;
    color: inherit;
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .pm-kind-card:hover {
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }
  .pm-kind-icon {
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
  .pm-kind-text { display: flex; flex-direction: column; min-width: 0; }
  .pm-kind-label {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .pm-kind-blurb {
    font-size: 11.5px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pm-template-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }
  .pm-template-card {
    padding: 10px 12px;
    display: flex;
    align-items: center;
    gap: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    color: inherit;
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .pm-template-card:hover { border-color: var(--border-strong); }
  .pm-template-icon {
    width: 28px;
    height: 28px;
    font-size: 16px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    flex-shrink: 0;
  }
  .pm-template-text { display: flex; flex-direction: column; min-width: 0; }
  .pm-template-label {
    font-size: 12.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .pm-template-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pm-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .pm-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .pm-grid-narrow-wide { grid-template-columns: 0.7fr 1fr; }

  .pm-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .pm-label {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .pm-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.5;
  }
  .pm-enabled {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    color: var(--fg);
    cursor: pointer;
  }
  .pm-enabled input { accent-color: var(--color-brand-500); }

  .pm-back { color: var(--fg-subtle); }
  .pm-unlink {
    margin: 0;
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 45%, var(--border));
    border-radius: 4px;
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
    color: var(--fg);
    font-size: 12px;
    line-height: 1.5;
  }
  .pm-form-actions {
    display: flex;
    gap: 8px;
  }

  @media (max-width: 720px) {
    .pm-kind-grid,
    .pm-template-grid,
    .pm-grid-2,
    .pm-grid-narrow-wide { grid-template-columns: 1fr; }
  }
</style>
