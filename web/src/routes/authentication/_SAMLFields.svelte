<script lang="ts">
  // SAML 2.0 form fields. Used inside _ProviderModal.svelte.
  // Slice 1: UI complete; save button disabled by parent.
  import type { CustomRole } from '$lib/api';
  import type { SAMLConfig } from './_types';
  import { Field } from '$lib/components/editorial';
  import { Copy } from 'lucide-svelte';
  import GroupMappingList from './_GroupMappingList.svelte';

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

<div class="saml-fieldset">Identity provider metadata</div>
<div class="saml-source-toggle" role="tablist">
  <button
    type="button"
    class="saml-source-btn"
    class:saml-source-active={idpSource === 'url'}
    onclick={() => (idpSource = 'url')}
  >
    Metadata URL
  </button>
  <button
    type="button"
    class="saml-source-btn"
    class:saml-source-active={idpSource === 'xml'}
    onclick={() => (idpSource = 'xml')}
  >
    Paste XML
  </button>
</div>

{#if idpSource === 'url'}
  <Field label="IdP metadata URL" hint="Dockmesh fetches and refreshes it periodically.">
    <input
      class="dm-input acc-input-mono"
      bind:value={config.idp_metadata_url}
      placeholder="https://idp.example/saml/metadata"
    />
  </Field>
{:else}
  <Field label="IdP metadata XML" hint="Paste the entire <EntityDescriptor> document.">
    <textarea
      class="dm-input acc-input-mono saml-xml"
      bind:value={config.idp_metadata_xml}
      rows="6"
      placeholder={'<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" ...>'}
    ></textarea>
  </Field>
{/if}

<div class="saml-fieldset">Service provider</div>
<Field label="SP entity ID" hint="Dockmesh's identifier toward the IdP. Defaults to the metadata URL.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.sp_entity_id}
    placeholder={`${callbackBase}/saml/${config.slug || '{slug}'}`}
  />
</Field>

<div class="saml-grid-2">
  <Field label="ACS URL (read-only)" hint="Assertion Consumer Service. Configure this on the IdP.">
    {#snippet right()}
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-xs"
        onclick={() => onCopy(acsUrl)}
      >
        <Copy size={11} strokeWidth={1.5} /> copy
      </button>
    {/snippet}
    <input class="dm-input acc-input-mono" readonly value={acsUrl} />
  </Field>
  <Field label="SLO URL (read-only)" hint="Single Logout endpoint.">
    {#snippet right()}
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-xs"
        onclick={() => onCopy(sloUrl)}
      >
        <Copy size={11} strokeWidth={1.5} /> copy
      </button>
    {/snippet}
    <input class="dm-input acc-input-mono" readonly value={sloUrl} />
  </Field>
</div>

<Field label="SP metadata URL (read-only)" hint="Hand this to the IdP for auto-config.">
  {#snippet right()}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={() => onCopy(metadataUrl)}
    >
      <Copy size={11} strokeWidth={1.5} /> copy
    </button>
  {/snippet}
  <input class="dm-input acc-input-mono" readonly value={metadataUrl} />
</Field>

<div class="saml-fieldset">Signing &amp; encryption</div>
<Field label="Signing certificate (PEM)" hint="IdP cert used to verify SAML responses. Optional if metadata XML embeds it.">
  <textarea
    class="dm-input acc-input-mono saml-cert"
    bind:value={config.signing_cert}
    rows="4"
    placeholder={'-----BEGIN CERTIFICATE-----\nMIID…\n-----END CERTIFICATE-----'}
  ></textarea>
</Field>
<Field label="Encryption certificate (PEM)" hint="Optional. Required only if the IdP encrypts assertions.">
  <textarea
    class="dm-input acc-input-mono saml-cert"
    bind:value={config.encryption_cert}
    rows="3"
    placeholder={'-----BEGIN CERTIFICATE-----\n…\n-----END CERTIFICATE-----'}
  ></textarea>
</Field>

<div class="saml-fieldset">Attribute mapping</div>
<Field label="NameID format">
  <select class="dm-input acc-input-mono" bind:value={config.nameid_format}>
    <option value="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">emailAddress</option>
    <option value="urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified">unspecified</option>
    <option value="urn:oasis:names:tc:SAML:2.0:nameid-format:persistent">persistent</option>
    <option value="urn:oasis:names:tc:SAML:2.0:nameid-format:transient">transient</option>
  </select>
</Field>

<div class="saml-grid-3">
  <Field label="Username attribute">
    <input class="dm-input acc-input-mono" bind:value={config.username_attribute} placeholder="NameID" />
  </Field>
  <Field label="Email attribute">
    <input class="dm-input acc-input-mono" bind:value={config.email_attribute} placeholder="email" />
  </Field>
  <Field label="Groups attribute">
    <input class="dm-input acc-input-mono" bind:value={config.groups_attribute} placeholder="groups" />
  </Field>
</div>

<GroupMappingList
  bind:mappings={config.group_mappings}
  bind:defaultRole={config.default_role}
  {roles}
  groupClaim={config.groups_attribute}
  title="SAML attribute → role mappings"
  hint="Match values from the configured groups attribute. First match wins."
/>

<style>
  .saml-help {
    padding: 8px 10px;
    background: var(--bg);
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
  .saml-help-icon { flex-shrink: 0; font-size: 13px; line-height: 1; }
  .saml-help-text { min-width: 0; }
  .saml-fieldset {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
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
