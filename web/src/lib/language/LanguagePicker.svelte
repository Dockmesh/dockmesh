<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, ApiError } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { LANGUAGES, applyLanguagePref, isLocale } from '$lib/i18n';

  let { value = '' }: { value?: string } = $props();
  let current = $state('');
  let busy = $state(false);

  $effect(() => {
    current = isLocale(value) ? value : '';
  });

  async function change(e: Event) {
    const next = (e.currentTarget as HTMLSelectElement).value;
    busy = true;
    try {
      await api.users.setLanguage(next);
      current = next;
      applyLanguagePref(next);
    } catch (err) {
      toast.error(m.lead_language_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
</script>

<select class="dm-input" value={current} disabled={busy} onchange={change} data-testid="language-select">
  <option value="">{m.lang_auto()}</option>
  {#each LANGUAGES as l (l.id)}
    <option value={l.id}>{l.label}</option>
  {/each}
</select>
