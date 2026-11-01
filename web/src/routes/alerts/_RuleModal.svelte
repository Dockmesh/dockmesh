<script lang="ts">
  import { api, ApiError, type AlertRule, type AlertRuleInput, type HostInfo, type NotificationChannel } from '$lib/api';
  import {
    KINDS, KIND_GROUPS, appliesTo, isContainerKind, isHostBoundKind, kindLabel, ruleSentence, ruleToInput, type RuleKind
  } from './ruleKinds';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { Modal, Segmented, Switch } from '$lib/components/ui';
  import { BellRing } from 'lucide-svelte';

  interface Props {
    open: boolean;
    editing: AlertRule | null;
    channels: NotificationChannel[];
    hosts: HostInfo[];
    hostName: (id: string) => string;
    onSaved: () => void;
  }

  let { open = $bindable(false), editing, channels, hosts, hostName, onSaved }: Props = $props();

  const DEFAULT: AlertRuleInput = {
    name: '',
    kind: 'metric',
    container_filter: '*',
    scope_host: '',
    scope_tag: '',
    scope_stack: '',
    metric: 'cpu_percent',
    operator: 'gt',
    threshold: 80,
    duration_seconds: 60,
    channel_ids: [],
    enabled: true,
    severity: 'warning',
    cooldown_seconds: 300,
    muted_until: '',
    mute_schedule: ''
  };

  // Defaults applied when the kind of a new rule changes.
  const KIND_DEFAULTS: Partial<Record<RuleKind, Partial<AlertRuleInput>>> = {
    metric: { metric: 'cpu_percent', operator: 'gt', threshold: 80, duration_seconds: 60 },
    restart_loop: { threshold: 3, duration_seconds: 600 },
    host_offline: { duration_seconds: 300, severity: 'critical' },
    disk_usage: { threshold: 90 },
    backup_failed: { severity: 'critical' },
    cert_expiry: { threshold: 14 }
  };

  let form = $state<AlertRuleInput>({ ...DEFAULT });
  let stacks = $state<string[]>([]);
  let jobs = $state<string[]>([]);
  let sites = $state<string[]>([]);
  let saving = $state(false);
  let err = $state<string | null>(null);

  const kind = $derived((form.kind || 'metric') as RuleKind);
  const kindMeta = $derived(KINDS.find((k) => k.id === kind) ?? KINDS[0]);
  const tags = $derived([...new Set(hosts.flatMap((h) => h.tags ?? []))].sort());

  let lastOpen = false;
  let lastEditingId: number | null = null;
  $effect(() => {
    if (open && (!lastOpen || lastEditingId !== (editing?.id ?? null))) {
      form = editing ? ruleToInput(editing) : { ...DEFAULT, channel_ids: [] };
      err = null;
      lastEditingId = editing?.id ?? null;
      loadChoices();
    }
    lastOpen = open;
  });

  // Picker contents; each source is optional and only loaded when the
  // caller may read it.
  function loadChoices() {
    if (allowed('stacks.view')) api.stacks.list().then((l) => (stacks = l.map((s) => s.name).sort())).catch(() => (stacks = []));
    if (allowed('backups.view')) api.backups.listJobs().then((l) => (jobs = l.map((j) => j.name).sort())).catch(() => (jobs = []));
    if (allowed('proxy.view')) api.proxy.listRoutes().then((l) => (sites = l.map((r) => r.host).sort())).catch(() => (sites = []));
  }

  function setKind(k: RuleKind) {
    const keepScope = isHostBoundKind(k) && isHostBoundKind(kind);
    form = {
      ...form,
      kind: k,
      metric: '',
      operator: '',
      threshold: 0,
      duration_seconds: 0,
      container_filter: '*',
      scope_host: keepScope ? form.scope_host : '',
      scope_tag: keepScope ? form.scope_tag : '',
      scope_stack: isContainerKind(k) && isContainerKind(kind) ? form.scope_stack : '',
      severity: 'warning',
      ...(KIND_DEFAULTS[k] ?? {})
    };
  }

  // "Applies to" is one picker; its value encodes which scope field is
  // set. A stored rule that combines several scopes keeps them under
  // "current".
  interface ScopeOpt { value: string; label: string }
  const scopeValue = $derived.by(() => {
    if (kind === 'backup_failed' || kind === 'backup_missed' || kind === 'cert_expiry') return 'f:' + (form.container_filter || '*');
    const set = [form.scope_host && 'h', form.scope_tag && 't', isContainerKind(kind) && form.scope_stack && 's'].filter(Boolean);
    if (set.length > 1) return 'current';
    if (form.scope_host) return 'h:' + form.scope_host;
    if (form.scope_tag) return 't:' + form.scope_tag;
    if (isContainerKind(kind) && form.scope_stack) return 's:' + form.scope_stack;
    return 'all';
  });
  const scopeOpts = $derived.by<ScopeOpt[]>(() => {
    const out: ScopeOpt[] = [];
    const filterList = (all: string, names: string[], noun: string) => {
      out.push({ value: 'f:*', label: all });
      for (const n of names) out.push({ value: 'f:' + n, label: `${noun} ${n}` });
      const cur = form.container_filter;
      if (cur && cur !== '*' && !names.includes(cur)) out.push({ value: 'f:' + cur, label: `${noun} ${cur}` });
    };
    if (kind === 'backup_failed' || kind === 'backup_missed') filterList('All backup jobs', jobs, 'Job');
    else if (kind === 'cert_expiry') filterList('All proxy sites', sites, 'Site');
    else {
      if (scopeValue === 'current') out.push({ value: 'current', label: `Current: ${appliesTo(form, hostName)}` });
      out.push({ value: 'all', label: isContainerKind(kind) ? 'All containers' : kind === 'host_offline' ? 'All agents' : 'All hosts' });
      if (isContainerKind(kind)) for (const s of stacks) out.push({ value: 's:' + s, label: `Stack ${s}` });
      for (const h of hosts.filter((h) => h.kind !== 'all')) out.push({ value: 'h:' + h.id, label: isContainerKind(kind) ? `Containers on ${h.name}` : h.name });
      for (const t of tags) out.push({ value: 't:' + t, label: `Hosts with tag ${t}` });
      if (isContainerKind(kind) && form.scope_stack && !stacks.includes(form.scope_stack) && scopeValue !== 'current')
        out.push({ value: 's:' + form.scope_stack, label: `Stack ${form.scope_stack}` });
    }
    return out;
  });
  function setScope(v: string) {
    if (v === 'current') return;
    const [k, ...rest] = v.split(':');
    const val = rest.join(':');
    if (k === 'f') {
      form.container_filter = val || '*';
      return;
    }
    form.scope_host = k === 'h' ? val : '';
    form.scope_tag = k === 't' ? val : '';
    form.scope_stack = k === 's' ? val : '';
  }
  const hasNameFilter = $derived(kind !== 'backup_failed' && kind !== 'backup_missed' && kind !== 'cert_expiry');

  const durations = [
    { value: 0, label: 'immediately' },
    { value: 30, label: '30 s' },
    { value: 60, label: '1 min' },
    { value: 120, label: '2 min' },
    { value: 300, label: '5 min' },
    { value: 600, label: '10 min' },
    { value: 1800, label: '30 min' },
    { value: 3600, label: '1 h' }
  ];
  const cooldowns = [
    { value: 60, label: '1 min' },
    { value: 300, label: '5 min' },
    { value: 900, label: '15 min' },
    { value: 1800, label: '30 min' },
    { value: 3600, label: '1 h' },
    { value: 21600, label: '6 h' },
    { value: 86400, label: '24 h' }
  ];
  function withCurrent(list: { value: number; label: string }[], cur: number) {
    return list.some((d) => d.value === cur) ? list : [...list, { value: cur, label: `${cur} s` }].sort((a, b) => a.value - b.value);
  }
  function num(e: Event): number {
    return parseInt((e.target as HTMLSelectElement).value, 10) || 0;
  }

  const sentence = $derived(ruleSentence(form, hostName));
  const defaultName = $derived(`${kindLabel(kind)} · ${appliesTo(form, hostName)}`);

  function toggleChannel(id: number) {
    form.channel_ids = form.channel_ids.includes(id) ? form.channel_ids.filter((c) => c !== id) : [...form.channel_ids, id];
  }

  async function save(e: Event) {
    e.preventDefault();
    saving = true;
    err = null;
    const input = { ...form, name: form.name.trim() || defaultName.slice(0, 200) };
    try {
      if (editing) {
        await api.alerts.updateRule(editing.id, input);
        toast.success('Rule saved', input.name);
      } else {
        await api.alerts.createRule(input);
        toast.success('Rule created', input.name);
      }
      open = false;
      onSaved();
    } catch (e2) {
      err = e2 instanceof ApiError ? e2.message : 'Saving failed';
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:open title={editing ? `Edit ${editing.name}` : 'New alert rule'} maxWidth="max-w-2xl" {footer}>
  <form id="rule-form" class="rm" onsubmit={save}>
    <div class="rm-2">
      <div class="dm-field">
        <label for="rm-kind">Watch</label>
        <select id="rm-kind" class="dm-input" value={kind} disabled={!!editing} onchange={(e) => setKind((e.target as HTMLSelectElement).value as RuleKind)}>
          {#each KIND_GROUPS as g (g.group)}
            <optgroup label={g.group}>
              {#each g.kinds as k (k)}<option value={k}>{kindLabel(k)}</option>{/each}
            </optgroup>
          {/each}
        </select>
        {#if editing}<span class="rm-hint">The type of an existing rule cannot be changed.</span>{/if}
      </div>
      <div class="dm-field">
        <label for="rm-scope">Applies to</label>
        {#if kind === 'metric'}
          <input id="rm-scope" class="dm-input" bind:value={form.container_filter} placeholder="*" />
          <span class="rm-hint">Containers on this server. {kindMeta.targetHint}</span>
        {:else}
          <select id="rm-scope" class="dm-input" value={scopeValue} onchange={(e) => setScope((e.target as HTMLSelectElement).value)}>
            {#each scopeOpts as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
          </select>
        {/if}
      </div>
    </div>

    <div class="dm-field">
      <span class="rm-label">Condition</span>
      <div class="rm-cond">
        {#if kind === 'metric'}
          <select class="dm-input rm-w" aria-label="Metric" bind:value={form.metric}>
            <option value="cpu_percent">CPU</option>
            <option value="mem_percent">Memory</option>
          </select>
          <select class="dm-input rm-w" aria-label="Comparison" bind:value={form.operator}>
            <option value="gt">above</option>
            <option value="lt">below</option>
          </select>
          <input type="number" class="dm-input rm-n" aria-label="Threshold in percent" min="1" max="100" bind:value={form.threshold} />
          <span>% for at least</span>
          <select class="dm-input rm-w" aria-label="Duration" value={form.duration_seconds} onchange={(e) => (form.duration_seconds = num(e))}>
            {#each withCurrent(durations, form.duration_seconds) as d (d.value)}<option value={d.value}>{d.label}</option>{/each}
          </select>
        {:else if kind === 'restart_loop'}
          <span>More than</span>
          <input type="number" class="dm-input rm-n" aria-label="Restarts" min="2" bind:value={form.threshold} />
          <span>restarts within</span>
          <select class="dm-input rm-w" aria-label="Window" value={form.duration_seconds} onchange={(e) => (form.duration_seconds = num(e))}>
            {#each withCurrent(durations.filter((d) => d.value >= 60), form.duration_seconds) as d (d.value)}<option value={d.value}>{d.label}</option>{/each}
          </select>
        {:else if kind === 'host_offline'}
          <span>Offline for at least</span>
          <select class="dm-input rm-w" aria-label="Duration" value={form.duration_seconds} onchange={(e) => (form.duration_seconds = num(e))}>
            {#each withCurrent(durations, form.duration_seconds) as d (d.value)}<option value={d.value}>{d.label}</option>{/each}
          </select>
        {:else if kind === 'disk_usage'}
          <span>At least</span>
          <input type="number" class="dm-input rm-n" aria-label="Percent full" min="1" max="100" bind:value={form.threshold} />
          <span>% full</span>
        {:else if kind === 'cert_expiry'}
          <span>Expires in less than</span>
          <input type="number" class="dm-input rm-n" aria-label="Days" min="1" bind:value={form.threshold} />
          <span>days</span>
          <span class="rm-hint">Caddy renews 30 days ahead, so this only fires when renewal keeps failing.</span>
        {:else if kind === 'container_stopped'}
          <span class="rm-hint">Fires on a non-zero exit, not on stops through Dockmesh or docker stop.</span>
        {:else if kind === 'backup_missed'}
          <span class="rm-hint">Fires when a job has no successful run within its schedule.</span>
        {:else}
          <span class="rm-hint">Fires on every such event; there is nothing to set.</span>
        {/if}
      </div>
    </div>

    {#if hasNameFilter && kind !== 'metric'}
      <div class="dm-field">
        <label for="rm-filter">{kindMeta.target}</label>
        <input id="rm-filter" class="dm-input dm-mono" bind:value={form.container_filter} placeholder="*" />
        <span class="rm-hint">{kindMeta.targetHint}</span>
      </div>
    {/if}

    <div class="rm-result" aria-live="polite">
      <BellRing size={14} strokeWidth={1.8} />
      <span>Fires when {sentence}.</span>
    </div>

    <div class="rm-2">
      <div class="dm-field">
        <span class="rm-label">Severity</span>
        <Segmented
          label="Severity"
          value={form.severity}
          onchange={(k) => (form.severity = k as AlertRuleInput['severity'])}
          options={[
            { key: 'info', label: 'Info' },
            { key: 'warning', label: 'Warning' },
            { key: 'critical', label: 'Critical' }
          ]}
        />
      </div>
      <div class="dm-field">
        <label for="rm-name">Name <span class="rm-opt">optional</span></label>
        <input id="rm-name" class="dm-input" bind:value={form.name} placeholder={defaultName} maxlength="200" />
      </div>
    </div>

    <div class="dm-field">
      <span class="rm-label">Notify</span>
      {#if channels.length === 0}
        <span class="rm-hint">No channels yet; until you add one, fires are only recorded.</span>
      {:else}
        <div class="rm-opts">
          {#each channels as c (c.id)}
            <label class="rm-opt-row" class:rm-dis={!c.enabled} title={!c.enabled ? 'Channel is disabled' : undefined}>
              <input type="checkbox" checked={form.channel_ids.includes(c.id)} disabled={!c.enabled} onchange={() => toggleChannel(c.id)} />
              <span>{c.name}</span>
              <span class="dm-dim">{c.type}{c.enabled ? '' : ' · disabled'}</span>
            </label>
          {/each}
        </div>
      {/if}
      <span class="rm-hint rm-inline">
        Repeats at most every
        <select class="dm-input rm-sm" aria-label="Repeat interval" value={form.cooldown_seconds} onchange={(e) => (form.cooldown_seconds = num(e))}>
          {#each withCurrent(cooldowns, form.cooldown_seconds) as c (c.value)}<option value={c.value}>{c.label}</option>{/each}
        </select>
        while it keeps firing, and sends “resolved” when it stops.
      </span>
    </div>

    {#if editing}
      <div class="dm-field">
        <label for="rm-mute">Mute until <span class="rm-opt">optional</span></label>
        <input
          id="rm-mute"
          type="datetime-local"
          class="dm-input rm-mute"
          value={form.muted_until ? form.muted_until.slice(0, 16) : ''}
          oninput={(e) => {
            const v = (e.target as HTMLInputElement).value;
            form.muted_until = v ? new Date(v).toISOString() : '';
          }}
        />
        <span class="rm-hint">The rule keeps evaluating and recording; channels are not notified.</span>
      </div>
    {/if}

    <Switch bind:checked={form.enabled} label="Enabled — evaluate this rule and notify its channels" />

    {#if err}<div class="rm-err" role="alert">{err}</div>{/if}
  </form>
</Modal>

{#snippet footer()}
  <button type="button" class="dm-btn" onclick={() => (open = false)}>Cancel</button>
  <button type="submit" form="rule-form" class="dm-btn dm-btn-primary" disabled={saving}>
    {saving ? 'Saving…' : editing ? 'Save rule' : 'Create rule'}
  </button>
{/snippet}

<style>
  .rm { display: flex; flex-direction: column; gap: 14px; }
  .rm-2 { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
  .rm-label { font-size: 12px; color: var(--muted); font-weight: 500; }
  .rm-opt { color: var(--subtle); font-weight: 400; }
  .rm-hint { font-size: var(--fs-xs); color: var(--subtle); }
  .rm-cond { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-height: 34px; }
  .rm-w { width: auto; min-width: 96px; }
  .rm-n { width: 76px; }
  .rm-sm { width: auto; min-height: 26px; height: 26px; padding: 0 6px; display: inline-block; }
  .rm-inline { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 4px; }
  .rm-mute { max-width: 240px; }
  .rm-result {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 10px 12px;
    border-radius: var(--r);
    background: var(--accent-soft);
    color: var(--fg);
  }
  .rm-result :global(svg) { color: var(--accent); flex: none; margin-top: 2px; }
  .rm-opts { display: flex; flex-direction: column; gap: 2px; }
  .rm-opt-row { display: flex; align-items: center; gap: 8px; min-height: 28px; cursor: pointer; }
  .rm-opt-row.rm-dis { cursor: not-allowed; color: var(--subtle); }
  .rm-err { color: var(--bad); font-size: var(--fs-sm); }
  @media (max-width: 640px) { .rm-2 { grid-template-columns: minmax(0, 1fr); } }
</style>
