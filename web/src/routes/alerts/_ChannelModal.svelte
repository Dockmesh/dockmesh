<script lang="ts">
  // Notification-channel create/edit. Renders type-specific structured
  // fields based on the selected channel type (9 supported types).
  import { api, ApiError, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { EditorialModal, Eyebrow, Field } from '$lib/components/editorial';

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
    { id: 'webhook',  label: 'Generic Webhook',
      fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://example.com/hook' }] },
    { id: 'ntfy',     label: 'ntfy.sh',
      fields: [
        { key: 'url', label: 'Topic URL', placeholder: 'https://ntfy.sh/my-topic' },
        { key: 'priority', label: 'Priority (1-5)', placeholder: '3' },
      ] },
    { id: 'discord',  label: 'Discord',
      fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://discord.com/api/webhooks/...' }] },
    { id: 'slack',    label: 'Slack',
      fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://hooks.slack.com/services/...' }] },
    { id: 'teams',    label: 'Microsoft Teams',
      fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://outlook.office.com/webhook/...' }] },
    { id: 'gotify',   label: 'Gotify',
      fields: [
        { key: 'url', label: 'Server URL', placeholder: 'https://gotify.example.com' },
        { key: 'token', label: 'App Token', placeholder: 'APP_TOKEN', secret: true },
      ] },
    { id: 'email',    label: 'Email (SMTP)',
      fields: [
        { key: 'host', label: 'SMTP Host', placeholder: 'smtp.example.com' },
        { key: 'port', label: 'Port', placeholder: '587' },
        { key: 'username', label: 'Username' },
        { key: 'password', label: 'Password', secret: true },
        { key: 'from', label: 'From', placeholder: 'alerts@example.com' },
        { key: 'to', label: 'To (comma-separated)', placeholder: 'admin@example.com' },
      ] },
    { id: 'pagerduty', label: 'PagerDuty',
      fields: [
        { key: 'integration_key', label: 'Integration Key (Events v2 routing key)', placeholder: 'R01A2B3C4D5...' },
        { key: 'client', label: 'Client name (optional)', placeholder: 'Dockmesh' },
        { key: 'client_url', label: 'Client URL (optional)', placeholder: 'https://dockmesh.example.com' },
      ] },
    { id: 'pushover', label: 'Pushover',
      fields: [
        { key: 'app_token', label: 'App token', placeholder: 'azGDORePK8gMaC0QOYAMyEEuzJnyUi', secret: true },
        { key: 'user_key',  label: 'User key',  placeholder: 'uQiRzpo4DXghDmr9QzzfQu27cmVRsG', secret: true },
        { key: 'device',    label: 'Device (optional)', placeholder: 'iphone' },
        { key: 'sound',     label: 'Sound (optional)', placeholder: 'siren' },
      ] },
  ];

  let formType = $state('ntfy');
  let formName = $state('');
  let formEnabled = $state(true);
  let formFields = $state<Record<string, string>>({});
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
        formFields = fields;
      } else {
        formType = 'ntfy';
        formName = '';
        formEnabled = true;
        formFields = {};
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
      const input = { type: formType, name: formName.trim(), config, enabled: formEnabled };
      if (editing) {
        await api.alerts.updateChannel(editing.id, input);
        toast.success('Updated', formName);
      } else {
        await api.alerts.createChannel(input);
        toast.success('Created', formName);
      }
      open = false;
      onSaved();
    } catch (e2) {
      err = e2 instanceof ApiError ? e2.message : 'save failed';
    } finally {
      saving = false;
    }
  }
</script>

<EditorialModal
  bind:open
  eyebrow={editing ? 'Edit channel' : 'New channel'}
  width={580}
>
  {#snippet title()}
    {#if editing}
      Edit <em class="ed-accent">{editing.name}</em>
    {:else}
      Add a <em>notification</em> channel
    {/if}
  {/snippet}

  <form id="ch-form" class="cm-form" onsubmit={save}>
    <div class="cm-grid-2">
      <Field label="Type">
        <select
          class="dm-input cm-input"
          value={formType}
          onchange={(e) => { formType = (e.target as HTMLSelectElement).value; onTypeChange(); }}
          disabled={editing !== null}
        >
          {#each CHANNEL_TYPES as t (t.id)}
            <option value={t.id}>{t.label}</option>
          {/each}
        </select>
      </Field>
      <Field label="Name" hint="Free-form, shown in the channel list.">
        <input class="dm-input cm-input" bind:value={formName} placeholder="My alerts channel" />
      </Field>
    </div>

    <div class="cm-fieldset">{activeType.label} configuration</div>
    <div class="cm-fields">
      {#each activeType.fields as f (f.key)}
        <Field label={f.label}>
          <input
            class="dm-input cm-input"
            type={f.secret ? 'password' : 'text'}
            placeholder={f.placeholder ?? ''}
            value={formFields[f.key] ?? ''}
            oninput={(e) => (formFields = { ...formFields, [f.key]: (e.target as HTMLInputElement).value })}
          />
        </Field>
      {/each}
    </div>

    <label class="cm-enabled">
      <input type="checkbox" bind:checked={formEnabled} />
      <span>Enabled — accept notifications</span>
    </label>

    {#if err}
      <div class="cm-error" role="alert">{err}</div>
    {/if}
  </form>

  {#snippet footer()}
    <span></span>
    <div class="cm-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (open = false)}>
        Cancel
      </button>
      <button
        type="submit"
        form="ch-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={saving || !formName.trim()}
      >
        {saving ? 'Saving…' : editing ? 'Save' : 'Create channel'}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .cm-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .cm-form :global(.dm-input) {
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .cm-input { font-family: var(--font-mono); }
  .cm-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .cm-fieldset {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    margin-top: 4px;
  }
  .cm-fields {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .cm-enabled {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    color: var(--fg);
    cursor: pointer;
  }
  .cm-enabled input { accent-color: var(--color-brand-500); }
  .cm-error {
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    border-radius: 4px;
    color: var(--color-danger-400);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .cm-actions {
    display: flex;
    gap: 8px;
  }
  @media (max-width: 640px) {
    .cm-grid-2 { grid-template-columns: 1fr; }
  }
</style>
