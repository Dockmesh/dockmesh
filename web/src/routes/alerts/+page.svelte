<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import type { NotificationChannel, AlertRule, AlertRuleInput, AlertHistoryEntry } from '$lib/api';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Bell, Plus, Trash2, Send, Activity, BellRing, BellOff, AlertTriangle, CheckCircle2 } from 'lucide-svelte';

  type Tab = 'rules' | 'channels' | 'history';
  let tab = $state<Tab>('rules');

  // ---------- Channels ----------
  let channels = $state<NotificationChannel[]>([]);
  let chLoading = $state(false);
  let showCh = $state(false);
  let editCh = $state<NotificationChannel | null>(null);
  let chForm = $state({
    type: 'ntfy',
    name: '',
    enabled: true,
    config_json: '{"url":"https://ntfy.sh/your-topic"}'
  });

  const channelTypes = [
    { value: 'webhook', label: 'Generic Webhook', placeholder: '{"url":"https://example.com/hook"}' },
    { value: 'ntfy', label: 'ntfy.sh', placeholder: '{"url":"https://ntfy.sh/topic","priority":3}' },
    { value: 'discord', label: 'Discord', placeholder: '{"url":"https://discord.com/api/webhooks/..."}' },
    { value: 'slack', label: 'Slack', placeholder: '{"url":"https://hooks.slack.com/services/..."}' },
    { value: 'teams', label: 'Microsoft Teams', placeholder: '{"url":"https://outlook.office.com/webhook/..."}' },
    { value: 'gotify', label: 'Gotify', placeholder: '{"url":"https://gotify.example.com","token":"APP_TOKEN"}' },
    { value: 'email', label: 'Email (SMTP)', placeholder: '{"host":"smtp.example.com","port":587,"username":"u","password":"p","from":"alerts@x.io","to":["admin@x.io"]}' }
  ];

  async function loadChannels() {
    chLoading = true;
    try {
      channels = await api.alerts.listChannels();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      chLoading = false;
    }
  }

  function resetCh() {
    chForm = { type: 'ntfy', name: '', enabled: true, config_json: '{"url":"https://ntfy.sh/your-topic"}' };
    editCh = null;
  }

  function openNewCh() {
    resetCh();
    showCh = true;
  }

  function openEditCh(c: NotificationChannel) {
    editCh = c;
    chForm = {
      type: c.type,
      name: c.name,
      enabled: c.enabled,
      config_json: JSON.stringify(c.config, null, 2)
    };
    showCh = true;
  }

  async function saveCh(e: Event) {
    e.preventDefault();
    let parsed: any;
    try {
      parsed = JSON.parse(chForm.config_json);
    } catch {
      toast.error('Invalid JSON in config');
      return;
    }
    try {
      const input = { type: chForm.type, name: chForm.name, config: parsed, enabled: chForm.enabled };
      if (editCh) {
        await api.alerts.updateChannel(editCh.id, input);
        toast.success('Channel updated', chForm.name);
      } else {
        await api.alerts.createChannel(input);
        toast.success('Channel created', chForm.name);
      }
      showCh = false;
      resetCh();
      await loadChannels();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteCh(c: NotificationChannel) {
    if (!confirm(`Delete channel "${c.name}"?`)) return;
    try {
      await api.alerts.deleteChannel(c.id);
      toast.success('Deleted', c.name);
      await loadChannels();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function testCh(c: NotificationChannel) {
    try {
      await api.alerts.testChannel(c.id);
      toast.success('Test sent', c.name);
    } catch (err) {
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // ---------- Rules ----------
  let rules = $state<AlertRule[]>([]);
  let rulesLoading = $state(false);
  let showRule = $state(false);
  let editRule = $state<AlertRule | null>(null);
  let ruleForm = $state<AlertRuleInput>({
    name: '',
    container_filter: '*',
    metric: 'cpu_percent',
    operator: 'gt',
    threshold: 80,
    duration_seconds: 60,
    channel_ids: [],
    enabled: true
  });

  async function loadRules() {
    rulesLoading = true;
    try {
      rules = await api.alerts.listRules();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      rulesLoading = false;
    }
  }

  function resetRule() {
    ruleForm = {
      name: '',
      container_filter: '*',
      metric: 'cpu_percent',
      operator: 'gt',
      threshold: 80,
      duration_seconds: 60,
      channel_ids: [],
      enabled: true
    };
    editRule = null;
  }

  function openNewRule() {
    resetRule();
    showRule = true;
  }

  function openEditRule(r: AlertRule) {
    editRule = r;
    ruleForm = {
      name: r.name,
      container_filter: r.container_filter,
      metric: r.metric,
      operator: r.operator,
      threshold: r.threshold,
      duration_seconds: r.duration_seconds,
      channel_ids: r.channel_ids ?? [],
      enabled: r.enabled
    };
    showRule = true;
  }

  async function saveRule(e: Event) {
    e.preventDefault();
    try {
      if (editRule) {
        await api.alerts.updateRule(editRule.id, ruleForm);
        toast.success('Rule updated', ruleForm.name);
      } else {
        await api.alerts.createRule(ruleForm);
        toast.success('Rule created', ruleForm.name);
      }
      showRule = false;
      resetRule();
      await loadRules();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteRule(r: AlertRule) {
    if (!confirm(`Delete rule "${r.name}"?`)) return;
    try {
      await api.alerts.deleteRule(r.id);
      toast.success('Deleted', r.name);
      await loadRules();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function toggleChannelInRule(id: number) {
    if (ruleForm.channel_ids.includes(id)) {
      ruleForm.channel_ids = ruleForm.channel_ids.filter((x) => x !== id);
    } else {
      ruleForm.channel_ids = [...ruleForm.channel_ids, id];
    }
  }

  // ---------- History ----------
  let history = $state<AlertHistoryEntry[]>([]);
  let historyLoading = $state(false);
  async function loadHistory() {
    historyLoading = true;
    try {
      history = await api.alerts.history(100);
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      historyLoading = false;
    }
  }

  function fmtTs(ts: string): string {
    return new Date(ts).toLocaleString();
  }

  $effect(() => {
    if (tab === 'channels') loadChannels();
    else if (tab === 'rules') {
      loadRules();
      loadChannels(); // we need them for the rule form
    } else if (tab === 'history') loadHistory();
  });

  const placeholderForType = $derived(
    channelTypes.find((t) => t.value === chForm.type)?.placeholder ?? '{}'
  );
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-2xl font-semibold tracking-tight">Alerts</h2>
    <p class="text-sm text-[var(--fg-muted)] mt-0.5">
      Define rules over container metrics, fan out to notification channels.
    </p>
  </div>

  <div class="border-b border-[var(--border)] flex gap-1">
    {#snippet tabBtn(id: Tab, label: string, Icon: any)}
      <button
        class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2
               {tab === id
          ? 'border-[var(--color-brand-500)] text-[var(--fg)]'
          : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (tab = id)}
      >
        <Icon class="w-3.5 h-3.5" /> {label}
      </button>
    {/snippet}
    {@render tabBtn('rules', 'Rules', BellRing)}
    {@render tabBtn('channels', 'Channels', Bell)}
    {@render tabBtn('history', 'History', Activity)}
  </div>

  {#if tab === 'rules'}
    <div class="flex justify-between items-center">
      <div class="text-sm text-[var(--fg-muted)]">
        {rules.length} {rules.length === 1 ? 'rule' : 'rules'}
      </div>
      <Button variant="primary" onclick={openNewRule}>
        <Plus class="w-4 h-4" /> New rule
      </Button>
    </div>

    {#if rulesLoading && rules.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
    {:else if rules.length === 0}
      <Card>
        <EmptyState
          icon={BellRing}
          title="No alert rules"
          description="Create a rule to monitor container CPU or memory and get notified when a threshold is breached."
        />
      </Card>
    {:else}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each rules as r}
            <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)]">
              <div class="w-10 h-10 rounded-lg flex items-center justify-center shrink-0
                         {r.firing_since
                ? 'bg-[color-mix(in_srgb,var(--color-danger-500)_15%,transparent)] text-[var(--color-danger-400)]'
                : r.enabled
                  ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)]'
                  : 'bg-[var(--surface)] text-[var(--fg-muted)]'}">
                {#if r.firing_since}
                  <AlertTriangle class="w-5 h-5" />
                {:else if r.enabled}
                  <BellRing class="w-5 h-5" />
                {:else}
                  <BellOff class="w-5 h-5" />
                {/if}
              </div>
              <button class="flex-1 min-w-0 text-left" onclick={() => openEditRule(r)}>
                <div class="font-medium text-sm flex items-center gap-2">
                  {r.name}
                  {#if !r.enabled}<Badge variant="default">disabled</Badge>{/if}
                  {#if r.firing_since}<Badge variant="danger" dot>firing</Badge>{/if}
                </div>
                <div class="text-xs text-[var(--fg-muted)] font-mono truncate">
                  {r.container_filter} · {r.metric} {r.operator === 'gt' ? '>' : '<'} {r.threshold} for {r.duration_seconds}s
                </div>
              </button>
              <Button size="xs" variant="ghost" onclick={() => deleteRule(r)} aria-label="Delete">
                <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
              </Button>
            </div>
          {/each}
        </div>
      </Card>
    {/if}
  {:else if tab === 'channels'}
    <div class="flex justify-between items-center">
      <div class="text-sm text-[var(--fg-muted)]">
        {channels.length} {channels.length === 1 ? 'channel' : 'channels'}
      </div>
      <Button variant="primary" onclick={openNewCh}>
        <Plus class="w-4 h-4" /> New channel
      </Button>
    </div>

    {#if chLoading && channels.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
    {:else if channels.length === 0}
      <Card>
        <EmptyState
          icon={Bell}
          title="No notification channels"
          description="Add a webhook, ntfy, Discord, Slack, Teams, Gotify or SMTP destination to receive alerts."
        />
      </Card>
    {:else}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each channels as c}
            <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)]">
              <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
                <Bell class="w-5 h-5" />
              </div>
              <button class="flex-1 min-w-0 text-left" onclick={() => openEditCh(c)}>
                <div class="font-medium text-sm flex items-center gap-2">
                  {c.name}
                  <Badge variant="info">{c.type}</Badge>
                  {#if !c.enabled}<Badge variant="default">disabled</Badge>{/if}
                </div>
              </button>
              <Button size="xs" variant="ghost" onclick={() => testCh(c)} title="Send test">
                <Send class="w-3.5 h-3.5" />
              </Button>
              <Button size="xs" variant="ghost" onclick={() => deleteCh(c)} aria-label="Delete">
                <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
              </Button>
            </div>
          {/each}
        </div>
      </Card>
    {/if}
  {:else if tab === 'history'}
    {#if historyLoading && history.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
    {:else if history.length === 0}
      <Card>
        <EmptyState icon={Activity} title="No alerts triggered yet" description="History appears here when a rule fires or resolves." />
      </Card>
    {:else}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each history as e}
            <div class="flex items-center gap-3 px-5 py-3">
              <div class="w-8 h-8 rounded-lg flex items-center justify-center shrink-0
                          {e.status === 'fired'
                ? 'bg-[color-mix(in_srgb,var(--color-danger-500)_15%,transparent)] text-[var(--color-danger-400)]'
                : 'bg-[color-mix(in_srgb,var(--color-success-500)_15%,transparent)] text-[var(--color-success-400)]'}">
                {#if e.status === 'fired'}
                  <AlertTriangle class="w-4 h-4" />
                {:else}
                  <CheckCircle2 class="w-4 h-4" />
                {/if}
              </div>
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium truncate">{e.rule_name}</div>
                <div class="text-xs text-[var(--fg-muted)] font-mono truncate">{e.message}</div>
              </div>
              <div class="text-xs text-[var(--fg-subtle)] shrink-0">{fmtTs(e.occurred_at)}</div>
            </div>
          {/each}
        </div>
      </Card>
    {/if}
  {/if}
</section>

<!-- channel modal -->
<Modal bind:open={showCh} title={editCh ? 'Edit channel' : 'Add channel'} maxWidth="max-w-xl" onclose={resetCh}>
  <form onsubmit={saveCh} class="space-y-3" id="ch-form">
    <div class="grid grid-cols-2 gap-3">
      <div>
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Type</span>
        <select class="dm-input" bind:value={chForm.type} disabled={editCh !== null}>
          {#each channelTypes as t}
            <option value={t.value}>{t.label}</option>
          {/each}
        </select>
      </div>
      <Input label="Name" bind:value={chForm.name} />
    </div>

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Config (JSON)</span>
      <textarea
        class="dm-input font-mono text-xs h-32 resize-y"
        bind:value={chForm.config_json}
        placeholder={placeholderForType}
      ></textarea>
      <span class="block text-xs text-[var(--fg-subtle)] mt-1 font-mono">{placeholderForType}</span>
    </div>

    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={chForm.enabled} class="accent-[var(--color-brand-500)]" />
      Enabled
    </label>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showCh = false; resetCh(); }}>Cancel</Button>
    <Button variant="primary" type="submit" form="ch-form">{editCh ? 'Save' : 'Create'}</Button>
  {/snippet}
</Modal>

<!-- rule modal -->
<Modal bind:open={showRule} title={editRule ? 'Edit rule' : 'Add rule'} maxWidth="max-w-xl" onclose={resetRule}>
  <form onsubmit={saveRule} class="space-y-3" id="rule-form">
    <Input label="Name" bind:value={ruleForm.name} placeholder="High CPU on web stack" />
    <Input label="Container filter" bind:value={ruleForm.container_filter} hint="* matches all running containers, otherwise exact name" />
    <div class="grid grid-cols-3 gap-3">
      <div>
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Metric</span>
        <select class="dm-input" bind:value={ruleForm.metric}>
          <option value="cpu_percent">CPU %</option>
          <option value="mem_percent">Memory %</option>
        </select>
      </div>
      <div>
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Operator</span>
        <select class="dm-input" bind:value={ruleForm.operator}>
          <option value="gt">greater than</option>
          <option value="lt">less than</option>
        </select>
      </div>
      <Input label="Threshold" type="number" bind:value={ruleForm.threshold as any} />
    </div>
    <Input
      label="Duration (seconds)"
      type="number"
      bind:value={ruleForm.duration_seconds as any}
      hint="condition must hold this long before firing"
    />

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Notify on</span>
      {#if channels.length === 0}
        <div class="text-xs text-[var(--fg-subtle)]">No channels yet. Create one in the Channels tab first.</div>
      {:else}
        <div class="space-y-1">
          {#each channels as c}
            <label class="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={ruleForm.channel_ids.includes(c.id)}
                onchange={() => toggleChannelInRule(c.id)}
                class="accent-[var(--color-brand-500)]"
              />
              {c.name} <span class="text-xs text-[var(--fg-muted)]">({c.type})</span>
            </label>
          {/each}
        </div>
      {/if}
    </div>

    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={ruleForm.enabled} class="accent-[var(--color-brand-500)]" />
      Enabled
    </label>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showRule = false; resetRule(); }}>Cancel</Button>
    <Button variant="primary" type="submit" form="rule-form">{editRule ? 'Save' : 'Create'}</Button>
  {/snippet}
</Modal>
