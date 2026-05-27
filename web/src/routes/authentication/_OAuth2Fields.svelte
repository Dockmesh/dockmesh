<script lang="ts">
  // OAuth2-specific form fields. Used inside _ProviderModal.svelte.
  // Slice 1: UI complete; save button is disabled by parent because
  // backend OAuth2 endpoints aren't built yet.
  import type { CustomRole } from '$lib/api';
  import type { OAuth2Config } from './_types';
  import { Field } from '$lib/components/editorial';
  import { Copy } from 'lucide-svelte';
  import GroupMappingList from './_GroupMappingList.svelte';

  interface Props {
    config: OAuth2Config;
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

  const callbackUrl = $derived(
    `${callbackBase}/api/v1/auth/oauth2/${config.slug || '{slug}'}/callback`,
  );
</script>

{#if helpText}
  <div class="oa-help">
    <span class="oa-help-icon">ⓘ</span>
    <span class="oa-help-text">{helpText}</span>
  </div>
{/if}

<Field label="Authorization URL" hint="OAuth 2.0 authorization endpoint.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.authorization_url}
    placeholder="https://provider.example/oauth/authorize"
  />
</Field>

<Field label="Token URL" hint="Where the code is exchanged for an access token.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.token_url}
    placeholder="https://provider.example/oauth/token"
  />
</Field>

<Field label="Userinfo URL" hint="Endpoint returning the authenticated user's profile JSON.">
  <input
    class="dm-input acc-input-mono"
    bind:value={config.userinfo_url}
    placeholder="https://provider.example/oauth/userinfo"
  />
</Field>

<div class="oa-grid-2">
  <Field label="Client ID">
    <input class="dm-input acc-input-mono" bind:value={config.client_id} />
  </Field>
  <Field
    label={editing ? 'Client secret (leave blank to keep)' : 'Client secret'}
    hint="Stored encrypted at rest."
  >
    <input
      class="dm-input acc-input-mono"
      type="password"
      bind:value={config.client_secret}
      placeholder={editing ? '••••••••' : ''}
    />
  </Field>
</div>

<Field label="Scopes" hint="Space-separated. Provider-specific.">
  <input class="dm-input acc-input-mono" bind:value={config.scopes} />
</Field>

<div class="oa-fieldset">User-info attribute mapping</div>
<div class="oa-grid-3">
  <Field label="Username field" hint="Property in userinfo JSON.">
    <input class="dm-input acc-input-mono" bind:value={config.username_field} placeholder="login" />
  </Field>
  <Field label="Email field">
    <input class="dm-input acc-input-mono" bind:value={config.email_field} placeholder="email" />
  </Field>
  <Field label="Groups field" hint="Optional. Often a separate /orgs lookup needed.">
    <input class="dm-input acc-input-mono" bind:value={config.groups_field} placeholder="groups" />
  </Field>
</div>

<GroupMappingList
  bind:mappings={config.group_mappings}
  bind:defaultRole={config.default_role}
  {roles}
  groupClaim={config.groups_field}
/>

<Field label="Callback URL" hint="Configure this on the provider side.">
  {#snippet right()}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={() => onCopy(callbackUrl)}
    >
      <Copy size={11} strokeWidth={1.5} /> copy
    </button>
  {/snippet}
  <input class="dm-input acc-input-mono" readonly value={callbackUrl} />
</Field>

<style>
  .oa-help {
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
  .oa-help-icon { flex-shrink: 0; font-size: 13px; line-height: 1; }
  .oa-help-text { min-width: 0; }
  .oa-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .oa-grid-3 {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
  }
  .oa-fieldset {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    margin-top: 6px;
  }
  @media (max-width: 720px) {
    .oa-grid-2,
    .oa-grid-3 { grid-template-columns: 1fr; }
  }
</style>
