<script lang="ts">
  // Notification-channel create/edit. Renders type-specific structured
  // fields based on the selected channel type (9 supported types).
  import { LANGUAGES, isLocale } from '$lib/i18n';
  import { api, ApiError, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Modal, Switch } from '$lib/components/ui';
  import { m } from '$lib/paraglide/messages.js';
  import { channelConfig, channelTypes } from '$lib/notifyChannels';

  interface Props {
    open: boolean;
    editing: NotificationChannel | null;
    onSaved: () => void;
  }

  let {
    open = $bindable(false),
    editing,
    onSaved,
  }: Props = $props();

  const CHANNEL_TYPES = channelTypes();

  let formType = $state('ntfy');
  let formName = $state('');
  let formEnabled = $state(true);
  let formFields = $state<Record<string, string>>({});
  let formLanguage = $state('en');
  let saving = $state(false);
  let err = $state<string | null>(null);

  const activeType = $derived(CHANNEL_TYPES.find((t) => t.id === formType) ?? CHANNEL_TYPES[0]);

  // Hydrate when opening.
  let lastOpen = false;
  let lastEditingId: number | null = null;
  $effect(() => {
    if (open && (!lastOpen || lastEditingId !== (editing?.id ?? null))) {
      if (editing) {
        formType = editing.type;
        formName = editing.name;
        formEnabled = editing.enabled;
        const cfg = (typeof editing.config === 'object' && editing.config !== null) ? editing.config : {};
        const fields: Record<string, string> = {};
        for (const [k, v] of Object.entries(cfg)) {
          fields[k] = Array.isArray(v) ? v.join(', ') : String(v ?? '');
        }
        delete fields.language;
        formFields = fields;
        formLanguage = isLocale(cfg.language) ? cfg.language : 'en';
      } else {
        formType = 'ntfy';
        formName = '';
        formEnabled = true;
        formFields = {};
        formLanguage = 'en';
      }
      err = null;
      lastEditingId = editing?.id ?? null;
    }
    lastOpen = open;
  });

  function onTypeChange() {
    formFields = {};
  }

  async function save(e: Event) {
    e.preventDefault();
    if (!formName.trim()) return;
    saving = true;
    err = null;
    try {
      const config = channelConfig(activeType, formFields, formLanguage);
      const input = { type: formType, name: formName.trim(), config, enabled: formEnabled };
      if (editing) {
        await api.alerts.updateChannel(editing.id, input);
        toast.success(m.common_updated(), formName);
      } else {
        await api.alerts.createChannel(input);
        toast.success(m.common_created(), formName);
      }
      open = false;
      onSaved();
    } catch (e2) {
      err = e2 instanceof ApiError ? e2.message : m.alerts_save_failed_lc();
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:open title={editing ? m.alerts_edit_name({ name: editing.name }) : m.alerts_ch_new_title()} maxWidth="max-w-xl" {footer}>
  <form id="ch-form" class="cm" onsubmit={save}>
    <div class="cm-2">
      <div class="dm-field">
        <label for="cm-type">{m.common_type()}</label>
        <select
          id="cm-type"
          class="dm-input"
          value={formType}
          onchange={(e) => { formType = (e.target as HTMLSelectElement).value; onTypeChange(); }}
          disabled={editing !== null}
        >
          {#each CHANNEL_TYPES as t (t.id)}
            <option value={t.id}>{t.label}</option>
          {/each}
        </select>
      </div>
      <div class="dm-field">
        <label for="cm-name">{m.common_name()}</label>
        <input id="cm-name" class="dm-input" bind:value={formName} placeholder={m.alerts_ch_name_ph()} maxlength="200" />
      </div>
    </div>

    {#each activeType.fields as f (f.key)}
      <div class="dm-field">
        <label for="cm-{f.key}">{f.label}</label>
        <input
          id="cm-{f.key}"
          class="dm-input"
          class:dm-mono={!f.secret}
          type={f.secret ? 'password' : 'text'}
          autocomplete="off"
          placeholder={f.placeholder ?? ''}
          value={formFields[f.key] ?? ''}
          oninput={(e) => (formFields = { ...formFields, [f.key]: (e.target as HTMLInputElement).value })}
        />
      </div>
    {/each}

    <div class="dm-field">
      <label for="cm-language">{m.lang_label()}</label>
      <select id="cm-language" class="dm-input" bind:value={formLanguage}>
        {#each LANGUAGES as l (l.id)}
          <option value={l.id}>{l.label}</option>
        {/each}
      </select>
      <span class="dm-sub">{formType === 'email' ? m.lead_channel_language_hint_email() : m.lead_channel_language_hint()}</span>
    </div>

    <Switch bind:checked={formEnabled} label={m.alerts_ch_enabled_label()} />

    {#if err}<div class="cm-err" role="alert">{err}</div>{/if}
  </form>
</Modal>

{#snippet footer()}
  <button type="button" class="dm-btn" onclick={() => (open = false)}>{m.common_cancel()}</button>
  <button type="submit" form="ch-form" class="dm-btn dm-btn-primary" disabled={saving || !formName.trim()}>
    {saving ? m.common_saving() : editing ? m.alerts_ch_save() : m.alerts_ch_create()}
  </button>
{/snippet}

<style>
  .cm { display: flex; flex-direction: column; gap: 14px; }
  .cm-2 { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
  .cm-err { color: var(--bad); font-size: var(--fs-sm); }
  @media (max-width: 640px) {
    .cm-2 { grid-template-columns: 1fr; }
  }
</style>
