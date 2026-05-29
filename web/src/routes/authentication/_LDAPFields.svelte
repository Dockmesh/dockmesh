<script lang="ts">
  // LDAP / Active Directory form fields. Used inside _ProviderModal.svelte.
  // Slice 1: UI complete; save button disabled by parent.
  import type { CustomRole } from '$lib/api';
  import type { LDAPConfig } from './_types';
  import { ldapDefaultsForMode } from './_types';
  import { Field } from '$lib/components/editorial';
  import GroupMappingList from './_GroupMappingList.svelte';

  interface Props {
    config: LDAPConfig;
    editing: boolean;
    roles: CustomRole[];
    helpText?: string;
  }

  let {
    config = $bindable(),
    editing,
    roles,
    helpText,
  }: Props = $props();

  function setMode(mode: 'openldap' | 'ad') {
    if (config.mode === mode) return;
    const defaults = ldapDefaultsForMode(mode);
    config = { ...config, mode, ...defaults };
  }

  function setTLS(tls: 'ldaps' | 'starttls' | 'none') {
    config.tls = tls;
    // Auto-adjust default port when switching TLS mode for new configs.
    if (!editing) {
      if (tls === 'ldaps') config.port = 636;
      else if (tls === 'starttls' || tls === 'none') config.port = 389;
    }
  }
</script>

{#if helpText}
  <div class="ldap-help">
    <span class="ldap-help-icon">ⓘ</span>
    <span class="ldap-help-text">{helpText}</span>
  </div>
{/if}

<div class="ldap-fieldset">Directory mode</div>
<div class="ldap-mode" role="radiogroup">
  <button
    type="button"
    class="ldap-mode-card"
    class:ldap-mode-active={config.mode === 'openldap'}
    onclick={() => setMode('openldap')}
  >
    <span class="ldap-mode-icon">📂</span>
    <div class="ldap-mode-text">
      <span class="ldap-mode-label">OpenLDAP</span>
      <span class="ldap-mode-blurb">389 DS · FreeIPA · uid-based filter.</span>
    </div>
  </button>
  <button
    type="button"
    class="ldap-mode-card"
    class:ldap-mode-active={config.mode === 'ad'}
    onclick={() => setMode('ad')}
  >
    <span class="ldap-mode-icon">🪟</span>
    <div class="ldap-mode-text">
      <span class="ldap-mode-label">Active Directory</span>
      <span class="ldap-mode-blurb">Microsoft AD · sAMAccountName-based filter.</span>
    </div>
  </button>
</div>

<div class="ldap-fieldset">Connection</div>
<div class="ldap-grid-host">
  <Field label="Host">
    <input
      class="dm-input acc-input-mono"
      bind:value={config.host}
      placeholder={config.mode === 'ad' ? 'dc01.corp.example' : 'ldap.example.com'}
    />
  </Field>
  <Field label="Port">
    <input
      type="number"
      class="dm-input acc-input-mono"
      bind:value={config.port}
      min="1"
      max="65535"
    />
  </Field>
</div>

<Field label="TLS mode">
  <div class="ldap-tls" role="radiogroup">
    {#each [
      { id: 'ldaps' as const, label: 'LDAPS', port: 636 },
      { id: 'starttls' as const, label: 'StartTLS', port: 389 },
      { id: 'none' as const, label: 'None (insecure)', port: 389 },
    ] as opt}
      <button
        type="button"
        class="ldap-tls-btn"
        class:ldap-tls-active={config.tls === opt.id}
        onclick={() => setTLS(opt.id)}
      >
        {opt.label}
      </button>
    {/each}
  </div>
</Field>

{#if config.tls !== 'none'}
  <label class="ldap-toggle">
    <input type="checkbox" bind:checked={config.skip_verify} />
    <span class="ldap-toggle-text">
      Skip TLS verification
      <span class="ldap-toggle-warn">— accepts any certificate. Self-signed only.</span>
    </span>
  </label>
{/if}

<div class="ldap-fieldset">Bind credentials</div>
<Field label="Bind DN" hint="Account used by Dockmesh to perform searches.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.bind_dn}
    placeholder={config.mode === 'ad'
      ? 'CN=DockmeshSvc,OU=Service Accounts,DC=corp,DC=example'
      : 'cn=dockmesh,ou=services,dc=example,dc=com'}
  />
</Field>
<Field
  label={editing ? 'Bind password (leave blank to keep)' : 'Bind password'}
  hint="Stored encrypted at rest."
>
  <input
    class="dm-input acc-input-mono"
    type="password"
    bind:value={config.bind_password}
    placeholder={editing ? '••••••••' : ''}
  />
</Field>

<div class="ldap-fieldset">User search</div>
<Field label="User search base" hint="DN under which to search for users.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.user_search_base}
    placeholder={config.mode === 'ad'
      ? 'OU=Users,DC=corp,DC=example'
      : 'ou=people,dc=example,dc=com'}
  />
</Field>
<Field label="User search filter" hint="%s is replaced with the login username.">
  <input class="dm-input acc-input-mono" bind:value={config.user_search_filter} />
</Field>

<div class="ldap-grid-2">
  <Field label="Username attribute">
    <input class="dm-input acc-input-mono" bind:value={config.username_attribute} />
  </Field>
  <Field label="Email attribute">
    <input class="dm-input acc-input-mono" bind:value={config.email_attribute} />
  </Field>
</div>

<div class="ldap-fieldset">Group lookup</div>
<Field label="Group search base" hint="DN under which to search for groups. Optional.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.group_search_base}
    placeholder={config.mode === 'ad'
      ? 'OU=Groups,DC=corp,DC=example'
      : 'ou=groups,dc=example,dc=com'}
  />
</Field>
<Field label="Group membership attribute" hint="memberOf for AD; cn or memberOf depending on schema for OpenLDAP.">
  <input class="dm-input acc-input-mono" bind:value={config.group_membership_attribute} />
</Field>

<GroupMappingList
  bind:mappings={config.group_mappings}
  bind:defaultRole={config.default_role}
  {roles}
  groupClaim="group DN or CN"
  title="Group → role mappings"
  hint="Match group DNs or CNs returned by the membership lookup. First match wins."
/>

<style>
  .ldap-help {
    padding: 8px 10px;
    background: var(--bg-elevated);
    border: 1px dashed var(--border);
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.55;
    display: flex;
    gap: 8px;
    align-items: flex-start;
  }
  .ldap-help-icon { flex-shrink: 0; font-size: 13px; line-height: 1; }
  .ldap-help-text { min-width: 0; }
  .ldap-fieldset {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    margin-top: 6px;
  }
  .ldap-mode {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .ldap-mode-card {
    padding: 10px 12px;
    display: flex;
    align-items: center;
    gap: 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    color: var(--fg-muted);
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .ldap-mode-card:hover { border-color: var(--border-strong); }
  .ldap-mode-active {
    background: var(--accent-bg);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--fg);
  }
  .ldap-mode-icon {
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
  .ldap-mode-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .ldap-mode-label { font-size: 12.5px; font-weight: 500; }
  .ldap-mode-blurb { font-family: var(--font-mono); font-size: 10px; color: var(--fg-subtle); }
  .ldap-grid-host {
    display: grid;
    grid-template-columns: 1fr 110px;
    gap: 10px;
  }
  .ldap-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .ldap-tls {
    display: inline-flex;
    padding: 2px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .ldap-tls-btn {
    padding: 5px 12px;
    background: transparent;
    border: 1px solid transparent;
    border-radius: 3px;
    color: var(--fg-muted);
    font-size: 12px;
    cursor: pointer;
    font: inherit;
  }
  .ldap-tls-active {
    background: var(--surface);
    border-color: var(--border);
    color: var(--fg);
    font-weight: 500;
  }
  .ldap-toggle {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: 12.5px;
    color: var(--fg);
  }
  .ldap-toggle input { accent-color: var(--color-warning-500); }
  .ldap-toggle-text { font-size: 12px; }
  .ldap-toggle-warn { color: var(--color-warning-400); font-size: 11px; }

  @media (max-width: 720px) {
    .ldap-mode,
    .ldap-grid-host,
    .ldap-grid-2 { grid-template-columns: 1fr; }
  }
</style>
