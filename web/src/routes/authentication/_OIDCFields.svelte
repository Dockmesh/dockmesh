<script lang="ts">
  // OIDC-specific form fields. Used inside _ProviderModal.svelte.
  // Slice 1: this is the only kind that's actually wired to the backend.
  import { api, ApiError, type CustomRole } from '$lib/api';
  import type { OIDCConfig } from './_types';
  import { Field } from '$lib/components/editorial';
  import { Repeat, CheckCircle2, XCircle, Copy } from 'lucide-svelte';
  import GroupMappingList from './_GroupMappingList.svelte';

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
      testMessage = 'Enter an issuer URL first.';
      return;
    }
    testState = 'testing';
    testMessage = '';
    lastTestedIssuer = url;
    try {
      const res = await api.oidc.testDiscovery(url);
      if (res.ok) {
        testState = 'ok';
        testMessage = `Discovery OK — issuer: ${res.issuer ?? url}`;
      } else {
        testState = 'fail';
        testMessage = res.error || 'Discovery failed.';
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

<Field label="Issuer URL" hint="The /.well-known/openid-configuration is fetched from this base.">
  {#snippet right()}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={runDiscovery}
      disabled={testState === 'testing' || !config.issuer_url.trim()}
    >
      <Repeat size={11} strokeWidth={1.5} />
      {testState === 'testing' ? 'Testing…' : 'Test discovery'}
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
  <Field label="Client ID">
    <input class="dm-input acc-input-mono" bind:value={config.client_id} placeholder="dockmesh" />
  </Field>
  <Field
    label={editing ? 'Client secret (leave blank to keep)' : 'Client secret'}
    hint="Stored encrypted at rest."
  >
    <input
      class="dm-input acc-input-mono"
      type="password"
      bind:value={config.client_secret}
      placeholder={editing ? '••••••••' : 'application client secret'}
    />
  </Field>
</div>

<Field label="Scopes" hint="Space-separated. openid is required.">
  <input class="dm-input acc-input-mono" bind:value={config.scopes} />
</Field>

<Field label="Group claim" hint="JWT claim that carries the user's groups (e.g. groups, roles, hd).">
  <input class="dm-input acc-input-mono" bind:value={config.group_claim} placeholder="groups" />
</Field>

<GroupMappingList
  bind:mappings={config.group_mappings}
  bind:defaultRole={config.default_role}
  {roles}
  groupClaim={config.group_claim}
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
  .oidc-help {
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
    font-family: var(--font-mono);
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
