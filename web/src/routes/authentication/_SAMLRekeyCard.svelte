<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { notices } from '$lib/stores/notices.svelte';
  import { toast } from '$lib/stores/toast.svelte';

  let accounts = $state<string[]>([]);
  let until = $state('');

  async function load() {
    try {
      const p = await api.saml.rekeyPending();
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
    if (!(await confirm.ask({
      title: 'End SAML re-keying',
      message: `Stop re-keying ${accounts.length} SAML account${accounts.length === 1 ? '' : 's'}?`,
      body: 'These accounts then no longer pick up their new subject at sign-in; an admin has to remove and recreate them if they are still needed.',
      confirmLabel: 'End re-keying', danger: true,
    }))) return;
    try {
      const r = await api.saml.endRekey();
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
        {accounts.length} SAML account{accounts.length === 1 ? '' : 's'} not re-keyed yet
      </div>
      <p class="rekey-body">
        Created through SAML before the upgrade and not signed in since. Each takes its new subject at its next
        sign-in with a matching email{until ? ` until ${until}` : ''}; until then a user of the same identity provider
        with one of these emails can sign in to it.
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
