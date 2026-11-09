<script lang="ts">
  // SAML 2.0 form fields. Used inside _ProviderModal.svelte.
  // Slice 1: UI complete; save button disabled by parent.
  import type { CustomRole } from '$lib/api';
  import type { SAMLConfig } from './_types';
  import { Field } from '$lib/components/editorial';
  import { Copy } from 'lucide-svelte';
  import GroupMappingList from './_GroupMappingList.svelte';
  import { m } from '$lib/paraglide/messages.js';

  interface Props {
    config: SAMLConfig;
    editing: boolean;
    roles: CustomRole[];
    callbackBase: string;
    helpText?: string;
    onCopy: (s: string) => void;
  }

  let {
    config = $bindable(),
    editing,
    roles,
    callbackBase,
    helpText,
    onCopy,
  }: Props = $props();

  type IdpSource = 'url' | 'xml';
  let idpSource = $state<IdpSource>('url');

  const acsUrl = $derived(
    `${callbackBase}/api/v1/auth/saml/${config.slug || '{slug}'}/acs`,
  );
  const sloUrl = $derived(
    `${callbackBase}/api/v1/auth/saml/${config.slug || '{slug}'}/slo`,
  );
  const metadataUrl = $derived(
    `${callbackBase}/api/v1/auth/saml/${config.slug || '{slug}'}/metadata`,
  );

  // Auto-derive SP entity ID if blank.
  $effect(() => {
    if (!config.sp_entity_id && config.slug) {
      config.sp_entity_id = `${callbackBase}/saml/${config.slug}`;
    }
  });
</script>

{#if helpText}
  <div class="saml-help">
    <span class="saml-help-icon">ⓘ</span>
    <span class="saml-help-text">{helpText}</span>
  </div>
{/if}

<div class="saml-fieldset">{m.authn_saml_idp_metadata()}</div>
<div class="saml-source-toggle" role="tablist">
  <button
    type="button"
    class="saml-source-btn"
    class:saml-source-active={idpSource === 'url'}
    onclick={() => (idpSource = 'url')}
  >
    {m.authn_saml_metadata_url()}
  </button>
  <button
    type="button"
    class="saml-source-btn"
    class:saml-source-active={idpSource === 'xml'}
    onclick={() => (idpSource = 'xml')}
  >
    {m.authn_saml_paste_xml()}
  </button>
</div>

{#if idpSource === 'url'}
  <Field label={m.authn_saml_idp_url()} hint={m.authn_saml_idp_url_hint()}>
    <input
      class="dm-input acc-input-mono"
      bind:value={config.idp_metadata_url}
      placeholder="https://idp.example/saml/metadata"
    />
  </Field>
{:else}
  <Field label={m.authn_saml_idp_xml()} hint={m.authn_saml_idp_xml_hint({ tag: '<EntityDescriptor>' })}>
    <textarea
      class="dm-input acc-input-mono saml-xml"
      bind:value={config.idp_metadata_xml}
      rows="6"
      placeholder={'<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" ...>'}
    ></textarea>
  </Field>
{/if}

<div class="saml-fieldset">{m.authn_saml_sp()}</div>
<Field label={m.authn_saml_sp_entity()} hint={m.authn_saml_sp_entity_hint()}>
  <input
    class="dm-input acc-input-mono"
    bind:value={config.sp_entity_id}
    placeholder={`${callbackBase}/saml/${config.slug || '{slug}'}`}
  />
</Field>

<div class="saml-grid-2">
  <Field label={m.authn_saml_acs()} hint={m.authn_saml_acs_hint()}>
    {#snippet right()}
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-xs"
        onclick={() => onCopy(acsUrl)}
      >
        <Copy size={11} strokeWidth={1.5} /> {m.authn_copy_lc()}
      </button>
    {/snippet}
    <input class="dm-input acc-input-mono" readonly value={acsUrl} />
  </Field>
  <Field label={m.authn_saml_slo()} hint={m.authn_saml_slo_hint()}>
    {#snippet right()}
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-xs"
        onclick={() => onCopy(sloUrl)}
      >
        <Copy size={11} strokeWidth={1.5} /> {m.authn_copy_lc()}
      </button>
    {/snippet}
    <input class="dm-input acc-input-mono" readonly value={sloUrl} />
  </Field>
</div>

{#if callbackBase.startsWith('http://')}
  <div class="saml-help">
    <span class="saml-help-icon">!</span>
    <span class="saml-help-text">
      {m.authn_saml_http_warn()}
    </span>
  </div>
{/if}

<Field label={m.authn_saml_sp_metadata()} hint={m.authn_saml_sp_metadata_hint()}>
  {#snippet right()}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={() => onCopy(metadataUrl)}
    >
      <Copy size={11} strokeWidth={1.5} /> {m.authn_copy_lc()}
    </button>
  {/snippet}
  <input class="dm-input acc-input-mono" readonly value={metadataUrl} />
</Field>

<div class="saml-fieldset">{m.authn_saml_signing()}</div>
<Field label={m.authn_saml_signing_cert()} hint={m.authn_saml_signing_cert_hint()}>
  <textarea
    class="dm-input acc-input-mono saml-cert"
    bind:value={config.signing_cert}
    rows="4"
    placeholder={'-----BEGIN CERTIFICATE-----\nMIID…\n-----END CERTIFICATE-----'}
  ></textarea>
</Field>
<Field label={m.authn_saml_enc_cert()} hint={m.authn_saml_enc_cert_hint()}>
  <textarea
    class="dm-input acc-input-mono saml-cert"
    bind:value={config.encryption_cert}
    rows="3"
    placeholder={'-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----'}
  ></textarea>
</Field>

<div class="saml-fieldset">{m.authn_attr_mapping()}</div>
<div class="saml-grid-2">
  <Field
    label={m.authn_saml_nameid()}
    hint={m.authn_saml_nameid_hint()}
  >
    <select class="dm-input acc-input-mono" bind:value={config.nameid_format}>
      <option value="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent">persistent</option>
      <option value="urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified">unspecified</option>
      <option value="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">emailAddress</option>
    </select>
  </Field>
  <Field
    label={m.authn_saml_subject()}
    hint={m.authn_saml_subject_hint()}
  >
    <input
      class="dm-input acc-input-mono"
      bind:value={config.subject_attribute}
      placeholder="http://schemas.microsoft.com/identity/claims/objectidentifier"
    />
  </Field>
</div>

<div class="saml-grid-3">
  <Field label={m.authn_username_attr()}>
    <input class="dm-input acc-input-mono" bind:value={config.username_attribute} placeholder="NameID" />
  </Field>
  <Field label={m.authn_email_attr()}>
    <input class="dm-input acc-input-mono" bind:value={config.email_attribute} placeholder="email" />
  </Field>
  <Field label={m.authn_groups_attr()}>
    <input class="dm-input acc-input-mono" bind:value={config.groups_attribute} placeholder="groups" />
  </Field>
</div>

<GroupMappingList
  bind:mappings={config.group_mappings}
  bind:defaultRole={config.default_role}
  {roles}
  groupClaim={config.groups_attribute}
  title={m.authn_saml_gml_title()}
  hint={m.authn_saml_gml_hint()}
/>

<style>
  .saml-help {
    padding: 8px 10px;
    background: var(--bg-elevated);
    border: 1px dashed var(--border);
    border-radius: 4px;
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.55;
    display: flex;
    gap: 8px;
    align-items: flex-start;
  }
  .saml-help-icon { flex-shrink: 0; font-size: 13px; line-height: 1; }
  .saml-help-text { min-width: 0; }
  .saml-fieldset {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-top: 6px;
  }
  .saml-source-toggle {
    display: inline-flex;
    padding: 2px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 4px;
    align-self: flex-start;
  }
  .saml-source-btn {
    padding: 5px 12px;
    background: transparent;
    border: 1px solid transparent;
    border-radius: 3px;
    color: var(--fg-muted);
    font-size: 12px;
    cursor: pointer;
    font: inherit;
  }
  .saml-source-active {
    background: var(--surface);
    border-color: var(--border);
    color: var(--fg);
    font-weight: 500;
  }
  .saml-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .saml-grid-3 {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
  }
  .saml-xml,
  .saml-cert {
    font-size: 11px;
    line-height: 1.4;
    resize: vertical;
  }
  @media (max-width: 720px) {
    .saml-grid-2,
    .saml-grid-3 { grid-template-columns: 1fr; }
  }
</style>
