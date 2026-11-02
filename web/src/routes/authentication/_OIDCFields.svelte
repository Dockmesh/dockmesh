<script lang="ts">
  // OIDC-specific form fields. Used inside _ProviderModal.svelte.
  // Slice 1: this is the only kind that's actually wired to the backend.
  import { api, ApiError, type CustomRole } from '$lib/api';
  import type { OIDCConfig } from './_types';
  import { Field } from '$lib/components/editorial';
  import { Repeat, CheckCircle2, XCircle, Copy } from 'lucide-svelte';
  import GroupMappingList from './_GroupMappingList.svelte';
  import { m } from '$lib/paraglide/messages.js';

  interface Props {
    config: OIDCConfig;
    editing: boolean;
    roles: CustomRole[];
    callbackBase: string;
    issuerHint?: string;
    helpText?: string;
    onCopy: (s: string) => void;
  }

  let {
    config = $bindable(),
    editing,
    roles,
    callbackBase,
    issuerHint = 'https://auth.example',
    helpText,
    onCopy,
  }: Props = $props();

  let testState = $state<'idle' | 'testing' | 'ok' | 'fail'>('idle');
  let testMessage = $state('');

  // Reset test state on issuer change.
  let lastTestedIssuer = '';
  $effect(() => {
    if (config.issuer_url !== lastTestedIssuer) {
      testState = 'idle';
      testMessage = '';
    }
  });

  async function runDiscovery() {
    const url = config.issuer_url.trim();
    if (!url) {
      testState = 'fail';
      testMessage = m.authn_oidc_enter_issuer();
      return;
    }
    testState = 'testing';
    testMessage = '';
    lastTestedIssuer = url;
    try {
      const res = await api.oidc.testDiscovery(url);
      if (res.ok) {
        testState = 'ok';
        testMessage = m.authn_oidc_discovery_ok({ issuer: res.issuer ?? url });
      } else {
        testState = 'fail';
        testMessage = res.error || m.authn_oidc_discovery_failed();
      }
    } catch (err) {
      testState = 'fail';
      testMessage = err instanceof ApiError ? err.message : String(err);
    }
  }

  const callbackUrl = $derived(
    `${callbackBase}/api/v1/auth/oidc/${config.slug || '{slug}'}/callback`,
  );
</script>

{#if helpText}
  <div class="oidc-help">
    <span class="oidc-help-icon">ⓘ</span>
    <span class="oidc-help-text">{helpText}</span>
  </div>
{/if}

<Field label={m.authn_issuer_url()} hint={m.authn_issuer_url_hint()}>
  {#snippet right()}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={runDiscovery}
      disabled={testState === 'testing' || !config.issuer_url.trim()}
    >
      <Repeat size={11} strokeWidth={1.5} />
      {testState === 'testing' ? m.authn_testing() : m.authn_test_discovery()}
    </button>
  {/snippet}
  <input
    class="dm-input acc-input-mono"
    bind:value={config.issuer_url}
    placeholder={issuerHint}
  />
</Field>

{#if testState === 'ok' || testState === 'fail'}
  <div class="oidc-test-result" data-state={testState}>
    {#if testState === 'ok'}
      <CheckCircle2 size={14} strokeWidth={1.6} />
    {:else}
      <XCircle size={14} strokeWidth={1.6} />
    {/if}
    <span>{testMessage}</span>
  </div>
{/if}

<div class="oidc-grid-2">
  <Field label={m.authn_client_id()}>
    <input class="dm-input acc-input-mono" bind:value={config.client_id} placeholder="dockmesh" />
  </Field>
  <Field
    label={editing ? m.authn_client_secret_keep() : m.authn_client_secret()}
    hint={m.authn_stored_encrypted()}
  >
    <input
      class="dm-input acc-input-mono"
      type="password"
      bind:value={config.client_secret}
      placeholder={editing ? '••••••••' : m.authn_client_secret_ph()}
    />
  </Field>
</div>

<Field label={m.authn_scopes()} hint={m.authn_oidc_scopes_hint()}>
  <input class="dm-input acc-input-mono" bind:value={config.scopes} />
</Field>

<Field label={m.authn_group_claim()} hint={m.authn_group_claim_hint()}>
  <input class="dm-input acc-input-mono" bind:value={config.group_claim} placeholder="groups" />
</Field>

<GroupMappingList
  bind:mappings={config.group_mappings}
  bind:defaultRole={config.default_role}
  {roles}
  groupClaim={config.group_claim}
  hint={m.authn_oidc_mapping_hint()}
/>

<Field label={m.authn_callback_url()} hint={m.authn_callback_url_hint()}>
  {#snippet right()}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={() => onCopy(callbackUrl)}
    >
      <Copy size={11} strokeWidth={1.5} /> {m.authn_copy_lc()}
    </button>
  {/snippet}
  <input class="dm-input acc-input-mono" readonly value={callbackUrl} />
</Field>

<style>
  .oidc-help {
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
  .oidc-help-icon { flex-shrink: 0; font-size: 13px; line-height: 1; }
  .oidc-help-text { min-width: 0; }
  .oidc-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .oidc-test-result {
    padding: 10px 12px;
    border-radius: 4px;
    font-size: 11.5px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .oidc-test-result[data-state="ok"] {
    border: 1px solid color-mix(in srgb, var(--color-success-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 5%, var(--surface));
    color: var(--color-success-400);
  }
  .oidc-test-result[data-state="fail"] {
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    color: var(--color-danger-400);
  }
  @media (max-width: 720px) {
    .oidc-grid-2 { grid-template-columns: 1fr; }
  }
</style>
