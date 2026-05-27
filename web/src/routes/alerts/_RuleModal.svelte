<script lang="ts">
  // Sentence-style alert-rule builder. Renders the rule as a natural-
  // language sentence ("fire when [CPU %] is [greater than] [80] for
  // at least [60s]") with inline select/input controls. Same fields
  // underneath as the legacy form, just better wired for reading.
  import { api, ApiError, type AlertRule, type AlertRuleInput, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { EditorialModal, Eyebrow, Field } from '$lib/components/editorial';
  import { Check, Plus } from 'lucide-svelte';

  interface Props {
    open: boolean;
    editing: AlertRule | null;
    channels: NotificationChannel[];
    onSaved: () => void;
  }

  let {
    open = $bindable(false),
    editing,
    channels,
    onSaved,
  }: Props = $props();

  const DEFAULT: AlertRuleInput = {
    name: '',
    container_filter: '*',
    metric: 'cpu_percent',
    operator: 'gt',
    threshold: 80,
    duration_seconds: 60,
    channel_ids: [],
    enabled: true,
    severity: 'warning',
    cooldown_seconds: 300,
    muted_until: '',
  };

  let form = $state<AlertRuleInput>({ ...DEFAULT });
  let saving = $state(false);
  let err = $state<string | null>(null);

  // Hydrate when modal opens.
  let lastOpen = false;
  let lastEditingId: number | null = null;
  $effect(() => {
    if (open && (!lastOpen || lastEditingId !== (editing?.id ?? null))) {
      if (editing) {
        form = {
          name: editing.name,
          container_filter: editing.container_filter,
          metric: editing.metric,
          operator: editing.operator,
          threshold: editing.threshold,
          duration_seconds: editing.duration_seconds,
          channel_ids: editing.channel_ids ?? [],
          enabled: editing.enabled,
          severity: editing.severity || 'warning',
          cooldown_seconds: editing.cooldown_seconds || 300,
          muted_until: editing.muted_until ?? '',
        };
      } else {
        form = { ...DEFAULT };
      }
      err = null;
      lastEditingId = editing?.id ?? null;
    }
    lastOpen = open;
  });

  function toggleChannel(id: number) {
    if (form.channel_ids.includes(id)) {
      form.channel_ids = form.channel_ids.filter((c) => c !== id);
    } else {
      form.channel_ids = [...form.channel_ids, id];
    }
  }

  function setSeverity(s: 'critical' | 'warning' | 'info') {
    form.severity = s;
  }

  async function save(e: Event) {
    e.preventDefault();
    if (!form.name.trim()) return;
    saving = true;
    err = null;
    try {
      if (editing) {
        await api.alerts.updateRule(editing.id, form);
        toast.success('Updated', form.name);
      } else {
        await api.alerts.createRule(form);
        toast.success('Created', form.name);
      }
      open = false;
      onSaved();
    } catch (e2) {
      err = e2 instanceof ApiError ? e2.message : 'save failed';
    } finally {
      saving = false;
    }
  }

  // Severity tone helper — matches the Rules tab card styling.
  const severityTone = $derived.by(() => {
    const map = {
      critical: { fg: 'var(--color-danger-400)', border: 'var(--color-danger-500)',  bg: 'color-mix(in srgb, var(--color-danger-500) 8%, var(--surface))' },
      warning:  { fg: 'var(--color-warning-400)', border: 'var(--color-warning-500)', bg: 'color-mix(in srgb, var(--color-warning-500) 8%, var(--surface))' },
      info:     { fg: 'var(--accent-fg)',         border: 'var(--accent)',             bg: 'color-mix(in srgb, var(--accent) 8%, var(--surface))' },
    } as const;
    return map[form.severity];
  });

  // Operator labels for the sentence.
  const opLabels: Record<string, string> = {
    gt: 'is greater than',
    lt: 'is less than',
  };

  const metricLabels: Record<string, string> = {
    cpu_percent: 'CPU %',
    mem_percent: 'Memory %',
  };

  const durationOptions = [
    { value: 0,    label: '0s — immediately' },
    { value: 30,   label: '30s' },
    { value: 60,   label: '1m' },
    { value: 120,  label: '2m' },
    { value: 300,  label: '5m' },
    { value: 600,  label: '10m' },
    { value: 1800, label: '30m' },
    { value: 3600, label: '1h' },
  ];

  const cooldownOptions = [
    { value: 60,    label: '1m' },
    { value: 300,   label: '5m' },
    { value: 900,   label: '15m' },
    { value: 1800,  label: '30m' },
    { value: 3600,  label: '1h' },
    { value: 21600, label: '6h' },
    { value: 86400, label: '24h' },
  ];
</script>

<EditorialModal
  bind:open
  eyebrow={editing ? 'Edit rule' : 'New rule'}
  width={680}
>
  {#snippet title()}
    {#if editing}
      Edit <em class="ed-accent">{editing.name}</em>
    {:else}
      Build a new <em>alert</em> rule
    {/if}
  {/snippet}

  <form id="rule-form" class="rm-form" onsubmit={save}>
    <Field label="Rule name">
      <input
        class="dm-input rm-input"
        bind:value={form.name}
        placeholder="High CPU on web stack"
      />
    </Field>

    <Field label="Severity">
      <div class="rm-severity">
        {#each [
          { id: 'info'     as const, label: 'info',     color: 'var(--accent-fg)' },
          { id: 'warning'  as const, label: 'warning',  color: 'var(--color-warning-400)' },
          { id: 'critical' as const, label: 'critical', color: 'var(--color-danger-400)' },
        ] as opt}
          <button
            type="button"
            class="rm-sev"
            class:rm-sev-active={form.severity === opt.id}
            data-sev={opt.id}
            onclick={() => setSeverity(opt.id)}
          >
            {opt.label}
          </button>
        {/each}
      </div>
    </Field>

    <!-- Sentence-style condition builder -->
    <div
      class="rm-condition"
      style="border-color: {severityTone.border}; background: {severityTone.bg};"
    >
      <div class="rm-condition-eyebrow" style="color: {severityTone.fg};">
        condition
      </div>
      <div class="rm-condition-sentence">
        <span class="rm-cond-word">fire when</span>
        <select class="rm-cond-select" bind:value={form.metric}>
          <option value="cpu_percent">CPU %</option>
          <option value="mem_percent">Memory %</option>
        </select>
        <select class="rm-cond-select rm-cond-op" bind:value={form.operator}>
          <option value="gt">is greater than</option>
          <option value="lt">is less than</option>
        </select>
        <input
          type="number"
          class="rm-cond-num"
          min="0"
          max="100"
          bind:value={form.threshold}
        />
        <span class="rm-cond-unit">%</span>
        <span class="rm-cond-word">for at least</span>
        <select
          class="rm-cond-select rm-cond-duration"
          value={form.duration_seconds}
          onchange={(e) => (form.duration_seconds = parseInt((e.target as HTMLSelectElement).value, 10) || 0)}
        >
          {#each durationOptions as d (d.value)}
            <option value={d.value}>{d.label}</option>
          {/each}
        </select>
      </div>
    </div>

    <div class="rm-grid-2">
      <Field label="Container filter" hint="* matches all, or exact container name.">
        <input
          class="dm-input rm-input"
          bind:value={form.container_filter}
          placeholder="*"
        />
      </Field>
      <Field label="Cooldown" hint="Suppress re-notify for this long after firing.">
        <select
          class="dm-input rm-input"
          value={form.cooldown_seconds}
          onchange={(e) => (form.cooldown_seconds = parseInt((e.target as HTMLSelectElement).value, 10) || 0)}
        >
          {#each cooldownOptions as c (c.value)}
            <option value={c.value}>{c.label}</option>
          {/each}
        </select>
      </Field>
    </div>

    <Field label="Notify these channels" hint={channels.length === 0 ? 'No channels yet — create one in the Channels tab.' : 'Pick one or more channels — every matching channel gets the alert.'}>
      {#if channels.length === 0}
        <div class="rm-no-channels">
          <Plus size={11} strokeWidth={1.5} />
          Create a channel first
        </div>
      {:else}
        <div class="rm-channel-grid">
          {#each channels as c (c.id)}
            {@const active = form.channel_ids.includes(c.id)}
            <button
              type="button"
              class="rm-channel-card"
              class:rm-channel-active={active}
              onclick={() => toggleChannel(c.id)}
              disabled={!c.enabled}
              title={!c.enabled ? 'Channel is disabled' : undefined}
            >
              <span class="rm-channel-type">{c.type}</span>
              <span class="rm-channel-name">{c.name}</span>
              {#if active}
                <Check size={11} strokeWidth={1.8} class="rm-channel-check" />
              {/if}
              {#if !c.enabled}
                <span class="rm-channel-disabled">disabled</span>
              {/if}
            </button>
          {/each}
        </div>
      {/if}
    </Field>

    {#if editing}
      <Field label="Mute until (optional)" hint="Rule still evaluates, notifications suppressed.">
        <input
          type="datetime-local"
          class="dm-input rm-input"
          value={form.muted_until ? form.muted_until.slice(0, 16) : ''}
          oninput={(e) => {
            const v = (e.target as HTMLInputElement).value;
            form.muted_until = v ? new Date(v).toISOString() : '';
          }}
        />
      </Field>
    {/if}

    <label class="rm-enabled">
      <input type="checkbox" bind:checked={form.enabled} />
      <span>Enabled — evaluate this rule and notify channels</span>
    </label>

    {#if err}
      <div class="rm-error" role="alert">{err}</div>
    {/if}
  </form>

  {#snippet footer()}
    <span></span>
    <div class="rm-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (open = false)}>
        Cancel
      </button>
      <button
        type="submit"
        form="rule-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={saving || !form.name.trim()}
      >
        {saving ? 'Saving…' : editing ? 'Save' : 'Create rule'}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .rm-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .rm-form :global(.dm-input) {
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .rm-input {
    font-family: var(--font-mono);
  }
  .rm-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }

  /* Severity picker */
  .rm-severity {
    display: flex;
    gap: 6px;
  }
  .rm-sev {
    flex: 1;
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-muted);
    transition: border-color 120ms, background 120ms, color 120ms;
  }
  .rm-sev[data-sev="info"].rm-sev-active {
    border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
    background: color-mix(in srgb, var(--accent) 8%, var(--surface));
    color: var(--accent-fg);
  }
  .rm-sev[data-sev="warning"].rm-sev-active {
    border-color: color-mix(in srgb, var(--color-warning-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 8%, var(--surface));
    color: var(--color-warning-400);
  }
  .rm-sev[data-sev="critical"].rm-sev-active {
    border-color: color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, var(--surface));
    color: var(--color-danger-400);
  }
  .rm-sev:hover { color: var(--fg); }

  /* Sentence builder */
  .rm-condition {
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 5px;
    transition: border-color 200ms, background 200ms;
  }
  .rm-condition-eyebrow {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    margin-bottom: 10px;
  }
  .rm-condition-sentence {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg);
    line-height: 1.7;
  }
  .rm-cond-word { color: var(--fg-subtle); }
  .rm-cond-select {
    padding: 4px 8px;
    background: var(--bg);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 12px;
    cursor: pointer;
  }
  .rm-cond-select:focus {
    outline: 2px solid color-mix(in srgb, var(--accent) 40%, transparent);
    outline-offset: 1px;
  }
  .rm-cond-op { min-width: 120px; }
  .rm-cond-duration { min-width: 90px; }
  .rm-cond-num {
    width: 80px;
    padding: 4px 8px;
    background: var(--bg);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .rm-cond-unit { color: var(--fg-subtle); }

  /* Channels picker */
  .rm-no-channels {
    padding: 10px 12px;
    border: 1px dashed var(--border);
    border-radius: 4px;
    font-size: 11.5px;
    color: var(--fg-subtle);
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .rm-channel-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }
  .rm-channel-card {
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 8px;
    text-align: left;
    color: var(--fg-muted);
    transition: border-color 120ms, background 120ms;
  }
  .rm-channel-card:hover:not(:disabled) {
    border-color: var(--border-strong);
  }
  .rm-channel-card:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .rm-channel-active {
    background: var(--accent-bg);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--fg);
  }
  .rm-channel-type {
    font-family: var(--font-mono);
    font-size: 9.5px;
    padding: 1px 5px;
    border-radius: 3px;
    color: var(--fg-subtle);
    border: 1px solid var(--border-subtle);
    letter-spacing: 0.04em;
    text-transform: uppercase;
    flex-shrink: 0;
  }
  .rm-channel-name {
    flex: 1;
    min-width: 0;
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rm-channel-card :global(.rm-channel-check) {
    color: var(--accent-fg);
    flex-shrink: 0;
  }
  .rm-channel-disabled {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--fg-subtle);
    margin-left: auto;
  }

  .rm-enabled {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
    color: var(--fg);
    cursor: pointer;
    padding-top: 4px;
  }
  .rm-enabled input { accent-color: var(--color-brand-500); }

  .rm-error {
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    border-radius: 4px;
    color: var(--color-danger-400);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }

  .rm-actions {
    display: flex;
    gap: 8px;
  }

  @media (max-width: 720px) {
    .rm-grid-2,
    .rm-channel-grid { grid-template-columns: 1fr; }
  }
</style>
