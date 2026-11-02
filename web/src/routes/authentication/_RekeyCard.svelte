<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { notices } from '$lib/stores/notices.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { fmtDate } from '$lib/i18n';
  import { m } from '$lib/paraglide/messages.js';

  interface Props {
    kind: 'saml' | 'sso';
  }

  let { kind }: Props = $props();

  const label = $derived(kind === 'saml' ? 'SAML' : 'OAuth2/LDAP');
  const client = $derived(kind === 'saml' ? api.saml : api.sso);

  let accounts = $state<string[]>([]);
  let until = $state('');

  async function load() {
    try {
      const p = await client.rekeyPending();
      accounts = p.accounts;
      until = p.until ? fmtDate(p.until) : '';
    } catch {
      accounts = [];
    }
  }

  $effect(() => {
    load();
  });

  async function end() {
    const n = accounts.length;
    if (!(await confirm.ask({
      title: m.authn_rekey_end_title({ label }),
      message: m.authn_rekey_end_message({ count: n, label }),
      body: kind === 'saml'
        ? m.authn_rekey_body_saml()
        : m.authn_rekey_body_sso(),
      confirmLabel: m.authn_rekey_end(), danger: true,
    }))) return;
    try {
      const r = await client.endRekey();
      toast.success(m.authn_rekey_ended(), m.authn_rekey_ended_count({ count: r.ended }));
      await load();
      notices.refresh();
    } catch (err) {
      toast.error(m.authn_rekey_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

{#if accounts.length > 0}
  <div class="dm-card rekey">
    <div class="rekey-text">
      <div class="rekey-title">
        {m.authn_rekey_title({ count: accounts.length, label })}
      </div>
      <p class="rekey-body">
        {#if kind === 'saml'}
          {until ? m.authn_rekey_saml_until({ until }) : m.authn_rekey_saml()}
        {:else}
          {until ? m.authn_rekey_sso_until({ until }) : m.authn_rekey_sso()}
        {/if}
      </p>
      <p class="rekey-accounts">{accounts.join(', ')}</p>
    </div>
    <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={end}>{m.authn_rekey_end()}</button>
  </div>
{/if}

<style>
  .rekey {
    display: flex;
    gap: 16px;
    align-items: flex-start;
    justify-content: space-between;
    padding: 14px 16px;
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
  }
  .rekey-text { min-width: 0; }
  .rekey-title { font-weight: 600; color: var(--fg); }
  .rekey-body { margin-top: 4px; color: var(--fg-muted); font-size: 13px; line-height: 1.5; }
  .rekey-accounts { margin-top: 6px; font-family: var(--font-mono); font-size: 12px; color: var(--fg); word-break: break-word; }
</style>
