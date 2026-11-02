<script lang="ts">
  // Notification-channel create/edit. Renders type-specific structured
  // fields based on the selected channel type (9 supported types).
  import { LANGUAGES, isLocale } from '$lib/i18n';
  import { api, ApiError, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Modal, Switch } from '$lib/components/ui';
  import { m } from '$lib/paraglide/messages.js';

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

  interface FieldDef { key: string; label: string; placeholder?: string; secret?: boolean; }

  const CHANNEL_TYPES: Array<{ id: string; label: string; fields: FieldDef[] }> = [
    { id: 'webhook',  label: m.alerts_ch_generic_webhook(),
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://example.com/hook' }] },
    { id: 'ntfy',     label: 'ntfy.sh',
      fields: [
        { key: 'url', label: m.alerts_ch_topic_url(), placeholder: 'https://ntfy.sh/my-topic' },
        { key: 'priority', label: m.alerts_ch_priority(), placeholder: '3' },
      ] },
    { id: 'discord',  label: 'Discord',
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://discord.com/api/webhooks/...' }] },
    { id: 'slack',    label: 'Slack',
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://hooks.slack.com/services/...' }] },
    { id: 'teams',    label: 'Microsoft Teams',
      fields: [{ key: 'url', label: m.alerts_ch_webhook_url(), placeholder: 'https://outlook.office.com/webhook/...' }] },
    { id: 'gotify',   label: 'Gotify',
      fields: [
        { key: 'url', label: m.alerts_ch_server_url(), placeholder: 'https://gotify.example.com' },
        { key: 'token', label: m.alerts_ch_app_token_gotify(), placeholder: 'APP_TOKEN', secret: true },
      ] },
    { id: 'email',    label: m.alerts_ch_email(),
      fields: [
        { key: 'host', label: m.alerts_ch_smtp_host(), placeholder: 'smtp.example.com' },
        { key: 'port', label: m.common_port(), placeholder: '587' },
        { key: 'username', label: m.alerts_ch_username() },
        { key: 'password', label: m.alerts_ch_password(), secret: true },
        { key: 'from', label: m.alerts_ch_from(), placeholder: 'alerts@example.com' },
        { key: 'to', label: m.alerts_ch_to(), placeholder: 'admin@example.com' },
      ] },
    { id: 'pagerduty', label: 'PagerDuty',
      fields: [
        { key: 'integration_key', label: m.alerts_ch_integration_key(), placeholder: 'R01A2B3C4D5...' },
        { key: 'client', label: m.alerts_ch_client_name(), placeholder: 'Dockmesh' },
        { key: 'client_url', label: m.alerts_ch_client_url(), placeholder: 'https://dockmesh.example.com' },
      ] },
    { id: 'pushover', label: 'Pushover',
      fields: [
        { key: 'app_token', label: m.alerts_ch_app_token(), placeholder: 'azGDORePK8gMaC0QOYAMyEEuzJnyUi', secret: true },
        { key: 'user_key',  label: m.alerts_ch_user_key(),  placeholder: 'uQiRzpo4DXghDmr9QzzfQu27cmVRsG', secret: true },
        { key: 'device',    label: m.alerts_ch_device(), placeholder: 'iphone' },
        { key: 'sound',     label: m.alerts_ch_sound(), placeholder: 'siren' },
      ] },
  ];

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
      const config: Record<string, any> = {};
      for (const f of activeType.fields) {
        let val: any = formFields[f.key] ?? '';
        if (f.key === 'port' || f.key === 'priority') {
          val = parseInt(val, 10) || 0;
        } else if (f.key === 'to') {
          val = val.split(',').map((s: string) => s.trim()).filter(Boolean);
        }
        config[f.key] = val;
      }
      config.language = formLanguage;
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
