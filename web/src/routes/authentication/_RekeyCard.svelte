<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { notices } from '$lib/stores/notices.svelte';
  import { toast } from '$lib/stores/toast.svelte';

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
      until = p.until?.slice(0, 10) ?? '';
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
      title: `End ${label} re-keying`,
      message: `Stop re-keying ${n} ${label} account${n === 1 ? '' : 's'}?`,
      body: kind === 'saml'
        ? 'These accounts then no longer pick up their new subject at sign-in; an admin has to remove and recreate them if they are still needed.'
        : 'These accounts then can no longer sign in through their provider; an admin has to remove and recreate them if they are still needed.',
      confirmLabel: 'End re-keying', danger: true,
    }))) return;
    try {
      const r = await client.endRekey();
      toast.success('Re-keying ended', `${r.ended} account${r.ended === 1 ? '' : 's'}`);
      await load();
      notices.refresh();
    } catch (err) {
      toast.error('Ending re-keying failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

{#if accounts.length > 0}
  <div class="dm-card rekey">
    <div class="rekey-text">
      <div class="rekey-title">
        {accounts.length} {label} account{accounts.length === 1 ? '' : 's'} not re-keyed yet
      </div>
      <p class="rekey-body">
        {#if kind === 'saml'}
          Created through SAML before the upgrade and not signed in since. Each takes its new subject at its next
          sign-in with a matching email{until ? ` until ${until}` : ''}; until then a user of the same identity provider
          with one of these emails can sign in to it.
        {:else}
          Linked before the upgrade and not signed in since. Each moves to its provider's immutable user id at its
          next sign-in{until ? ` until ${until}` : ''}; when the login name or directory entry changed since, the email
          has to match the account as well.
        {/if}
      </p>
      <p class="rekey-accounts">{accounts.join(', ')}</p>
    </div>
    <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={end}>End re-keying</button>
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
