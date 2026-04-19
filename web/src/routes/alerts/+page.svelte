<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { api, ApiError } from '$lib/api';
  import type { NotificationChannel, AlertRule, AlertRuleInput, AlertHistoryEntry } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Bell, Plus, Trash2, Send, Activity, BellRing, BellOff, AlertTriangle, CheckCircle2,
    Search, Copy, Power
  } from 'lucide-svelte';

  // URL-driven tab state
  type Tab = 'rules' | 'channels' | 'history';
  let tab = $state<Tab>((new URLSearchParams($page.url.search).get('tab') as Tab) || 'rules');

  // ---------- Channels ----------
  let channels = $state<NotificationChannel[]>([]);
  let chLoading = $state(false);
  let showCh = $state(false);
  let editCh = $state<NotificationChannel | null>(null);
  let chForm = $state({ type: 'ntfy', name: '', enabled: true, config_json: '{"url":"https://ntfy.sh/your-topic"}' });

  const channelTypes = [
    { value: 'webhook', label: 'Generic Webhook', fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://example.com/hook' }] },
    { value: 'ntfy', label: 'ntfy.sh', fields: [{ key: 'url', label: 'Topic URL', placeholder: 'https://ntfy.sh/my-topic' }, { key: 'priority', label: 'Priority (1-5)', placeholder: '3' }] },
    { value: 'discord', label: 'Discord', fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://discord.com/api/webhooks/...' }] },
    { value: 'slack', label: 'Slack', fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://hooks.slack.com/services/...' }] },
    { value: 'teams', label: 'Microsoft Teams', fields: [{ key: 'url', label: 'Webhook URL', placeholder: 'https://outlook.office.com/webhook/...' }] },
    { value: 'gotify', label: 'Gotify', fields: [{ key: 'url', label: 'Server URL', placeholder: 'https://gotify.example.com' }, { key: 'token', label: 'App Token', placeholder: 'APP_TOKEN' }] },
    { value: 'email', label: 'Email (SMTP)', fields: [{ key: 'host', label: 'SMTP Host', placeholder: 'smtp.example.com' }, { key: 'port', label: 'Port', placeholder: '587' }, { key: 'username', label: 'Username', placeholder: '' }, { key: 'password', label: 'Password', placeholder: '' }, { key: 'from', label: 'From', placeholder: 'alerts@example.com' }, { key: 'to', label: 'To (comma-separated)', placeholder: 'admin@example.com' }] },
    { value: 'pagerduty', label: 'PagerDuty', fields: [{ key: 'integration_key', label: 'Integration Key (Events v2 routing key)', placeholder: 'R01A2B3C4D5E6F7G8H9I0J1K2L3M4N5O6' }, { key: 'client', label: 'Client name (optional)', placeholder: 'Dockmesh' }, { key: 'client_url', label: 'Client URL (optional)', placeholder: 'https://dockmesh.example.com' }] },
    { value: 'pushover', label: 'Pushover', fields: [{ key: 'app_token', label: 'App token', placeholder: 'azGDORePK8gMaC0QOYAMyEEuzJnyUi' }, { key: 'user_key', label: 'User key', placeholder: 'uQiRzpo4DXghDmr9QzzfQu27cmVRsG' }, { key: 'device', label: 'Device (optional)', placeholder: 'iphone' }, { key: 'sound', label: 'Sound (optional)', placeholder: 'siren' }] }
  ];

  // Structured config for the form — parsed from JSON on edit, serialized on save
  let chConfigFields = $state<Record<string, string>>({});

  async function loadChannels() {
    chLoading = true;
    try { channels = await api.alerts.listChannels(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { chLoading = false; }
  }

  function resetCh() { chForm = { type: 'ntfy', name: '', enabled: true, config_json: '{}' }; editCh = null; chConfigFields = {}; }

  function openNewCh() {
    resetCh();
    showCh = true;
  }

  function openEditCh(c: NotificationChannel) {
    editCh = c;
    chForm = { type: c.type, name: c.name, enabled: c.enabled, config_json: JSON.stringify(c.config, null, 2) };
    // Parse config into structured fields
    const cfg = typeof c.config === 'object' ? c.config : {};
    const fields: Record<string, string> = {};
    for (const [k, v] of Object.entries(cfg)) {
      fields[k] = Array.isArray(v) ? v.join(', ') : String(v ?? '');
    }
    chConfigFields = fields;
    showCh = true;
  }

  const activeChType = $derived(channelTypes.find(t => t.value === chForm.type));

  async function saveCh(e: Event) {
    e.preventDefault();
    // Build config from structured fields
    const config: Record<string, any> = {};
    for (const f of activeChType?.fields ?? []) {
      let val: any = chConfigFields[f.key] ?? '';
      if (f.key === 'port' || f.key === 'priority') val = parseInt(val) || 0;
      else if (f.key === 'to') val = val.split(',').map((s: string) => s.trim()).filter(Boolean);
      config[f.key] = val;
    }
    try {
      const input = { type: chForm.type, name: chForm.name, config, enabled: chForm.enabled };
      if (editCh) { await api.alerts.updateChannel(editCh.id, input); toast.success('Updated', chForm.name); }
      else { await api.alerts.createChannel(input); toast.success('Created', chForm.name); }
      showCh = false; resetCh(); await loadChannels();
    } catch (err) { toast.error('Save failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function deleteCh(c: NotificationChannel) {
    if (!(await confirm.ask({ title: 'Delete notification channel', message: `Delete channel "${c.name}"?`, body: 'Rules that route to this channel will stop firing notifications until you reassign them.', confirmLabel: 'Delete', danger: true }))) return;
    try { await api.alerts.deleteChannel(c.id); toast.success('Deleted'); await loadChannels(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function testCh(c: NotificationChannel) {
    try { await api.alerts.testChannel(c.id); toast.success('Test sent', c.name); } catch (err) { toast.error('Test failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function toggleChEnabled(c: NotificationChannel) {
    try {
      await api.alerts.updateChannel(c.id, { ...c, config: c.config, enabled: !c.enabled });
      await loadChannels();
    } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  function channelName(id: number): string { return channels.find(c => c.id === id)?.name ?? `#${id}`; }
  function rulesUsingChannel(id: number): number { return rules.filter(r => r.channel_ids?.includes(id)).length; }

  // ---------- Rules ----------
  let rules = $state<AlertRule[]>([]);
  let rulesLoading = $state(false);
  let showRule = $state(false);
  let editRule = $state<AlertRule | null>(null);
  let ruleForm = $state<AlertRuleInput>({ name: '', container_filter: '*', metric: 'cpu_percent', operator: 'gt', threshold: 80, duration_seconds: 60, channel_ids: [], enabled: true, severity: 'warning', cooldown_seconds: 300, muted_until: '' });

  async function loadRules() {
    rulesLoading = true;
    try { rules = await api.alerts.listRules(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { rulesLoading = false; }
  }

  function resetRule() { ruleForm = { name: '', container_filter: '*', metric: 'cpu_percent', operator: 'gt', threshold: 80, duration_seconds: 60, channel_ids: [], enabled: true, severity: 'warning', cooldown_seconds: 300, muted_until: '' }; editRule = null; }

  function openNewRule() { resetRule(); showRule = true; }
  function openEditRule(r: AlertRule) {
    editRule = r;
    ruleForm = { name: r.name, container_filter: r.container_filter, metric: r.metric, operator: r.operator, threshold: r.threshold, duration_seconds: r.duration_seconds, channel_ids: r.channel_ids ?? [], enabled: r.enabled, severity: r.severity || 'warning', cooldown_seconds: r.cooldown_seconds || 300, muted_until: r.muted_until ?? '' };
    showRule = true;
  }
  function duplicateRule(r: AlertRule) {
    editRule = null;
    ruleForm = { name: r.name + ' (copy)', container_filter: r.container_filter, metric: r.metric, operator: r.operator, threshold: r.threshold, duration_seconds: r.duration_seconds, channel_ids: r.channel_ids ?? [], enabled: false, severity: r.severity || 'warning', cooldown_seconds: r.cooldown_seconds || 300, muted_until: '' };
    showRule = true;
  }

  async function saveRule(e: Event) {
    e.preventDefault();
    try {
      if (editRule) { await api.alerts.updateRule(editRule.id, ruleForm); toast.success('Updated', ruleForm.name); }
      else { await api.alerts.createRule(ruleForm); toast.success('Created', ruleForm.name); }
      showRule = false; resetRule(); await loadRules();
    } catch (err) { toast.error('Save failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function deleteRule(r: AlertRule) {
    if (!(await confirm.ask({ title: 'Delete alert rule', message: `Delete rule "${r.name}"?`, confirmLabel: 'Delete', danger: true }))) return;
    try { await api.alerts.deleteRule(r.id); toast.success('Deleted'); await loadRules(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function toggleRuleEnabled(r: AlertRule) {
    try {
      const input: AlertRuleInput = { name: r.name, container_filter: r.container_filter, metric: r.metric, operator: r.operator, threshold: r.threshold, duration_seconds: r.duration_seconds, channel_ids: r.channel_ids ?? [], enabled: !r.enabled, severity: r.severity || 'warning', cooldown_seconds: r.cooldown_seconds || 300, muted_until: r.muted_until ?? '' };
      await api.alerts.updateRule(r.id, input);
      await loadRules();
    } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function muteRule(r: AlertRule, hours: number) {
    try {
      const until = new Date(Date.now() + hours * 3600000).toISOString();
      const input: AlertRuleInput = { name: r.name, container_filter: r.container_filter, metric: r.metric, operator: r.operator, threshold: r.threshold, duration_seconds: r.duration_seconds, channel_ids: r.channel_ids ?? [], enabled: r.enabled, severity: r.severity || 'warning', cooldown_seconds: r.cooldown_seconds || 300, muted_until: until };
      await api.alerts.updateRule(r.id, input);
      toast.success(`Muted for ${hours}h`, r.name);
      await loadRules();
    } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function unmuteRule(r: AlertRule) {
    try {
      const input: AlertRuleInput = { name: r.name, container_filter: r.container_filter, metric: r.metric, operator: r.operator, threshold: r.threshold, duration_seconds: r.duration_seconds, channel_ids: r.channel_ids ?? [], enabled: r.enabled, severity: r.severity || 'warning', cooldown_seconds: r.cooldown_seconds || 300, muted_until: '' };
      await api.alerts.updateRule(r.id, input);
      toast.success('Unmuted', r.name);
      await loadRules();
    } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  function isMuted(r: AlertRule): boolean {
    return !!r.muted_until && new Date(r.muted_until) > new Date();
  }

  function toggleChannelInRule(id: number) {
    if (ruleForm.channel_ids.includes(id)) ruleForm.channel_ids = ruleForm.channel_ids.filter(x => x !== id);
    else ruleForm.channel_ids = [...ruleForm.channel_ids, id];
  }

  const firingCount = $derived(rules.filter(r => r.firing_since).length);

  // ---------- History ----------
  let history = $state<AlertHistoryEntry[]>([]);
  let historyLoading = $state(false);
  let histSearch = $state('');
  let histStatus = $state<'all' | 'fired' | 'resolved'>('all');

  async function loadHistory() {
    historyLoading = true;
    try { history = await api.alerts.history(500); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { historyLoading = false; }
  }

  const filteredHistory = $derived(
    history.filter(e => {
      if (histStatus !== 'all' && e.status !== histStatus) return false;
      if (!histSearch.trim()) return true;
      const q = histSearch.toLowerCase();
      return e.rule_name.toLowerCase().includes(q) || e.container_name.toLowerCase().includes(q) || e.message.toLowerCase().includes(q);
    })
  );

  function fmtTs(ts: string): string { return new Date(ts).toLocaleString(); }
  function fmtRelTime(ts?: string): string {
    if (!ts) return '—';
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }

  $effect(() => {
    if (!allowed('user.manage')) { goto('/'); return; }
    if (tab === 'channels') loadChannels();
    else if (tab === 'rules') { loadRules(); loadChannels(); }
    else if (tab === 'history') loadHistory();
  });
</script>

<section class="space-y-4">
  <!-- Header with summary -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Alerts</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {rules.length} rule{rules.length === 1 ? '' : 's'} · {channels.length} channel{channels.length === 1 ? '' : 's'}
        {#if firingCount > 0}
          · <span class="text-[var(--color-danger-400)] font-medium">{firingCount} firing</span>
        {/if}
      </p>
    </div>
  </div>

  <!-- Tabs -->
  <div class="border-b border-[var(--border)] flex gap-1">
    {#snippet tabBtn(id: Tab, label: string, Icon: any, badge?: number)}
      <button
        class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2
               {tab === id ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (tab = id)}
      >
        <Icon class="w-3.5 h-3.5" /> {label}
        {#if badge && badge > 0}
          <span class="text-[10px] px-1.5 py-0.5 rounded-full bg-[var(--color-danger-500)] text-white font-medium">{badge}</span>
        {/if}
      </button>
    {/snippet}
    {@render tabBtn('rules', 'Rules', BellRing, firingCount)}
    {@render tabBtn('channels', 'Channels', Bell)}
    {@render tabBtn('history', 'History', Activity)}
  </div>

  <!-- ===== RULES TAB ===== -->
  {#if tab === 'rules'}
    <div class="flex justify-between items-center">
      <span class="text-sm text-[var(--fg-muted)]">{rules.length} rule{rules.length === 1 ? '' : 's'}</span>
      <Button variant="primary" onclick={openNewRule}><Plus class="w-4 h-4" /> New rule</Button>
    </div>

    {#if rulesLoading && rules.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="3rem" /></Card>
    {:else if rules.length === 0}
      <Card><EmptyState icon={BellRing} title="No alert rules" description="Create a rule to monitor container metrics and get notified when thresholds are breached." /></Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-center px-3 py-3 w-10">Status</th>
                <th class="text-left px-3 py-3">Name</th>
                <th class="text-left px-3 py-3">Condition</th>
                <th class="text-left px-3 py-3">Channels</th>
                <th class="text-left px-3 py-3">Last Triggered</th>
                <th class="text-center px-3 py-3">Enabled</th>
                <th class="text-right px-3 py-3 w-28">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each rules as r (r.id)}
                <tr class="hover:bg-[var(--surface-hover)] {r.firing_since ? 'bg-[color-mix(in_srgb,var(--color-danger-500)_5%,transparent)]' : ''}">
                  <td class="px-3 py-2.5 text-center">
                    {#if r.firing_since}
                      <span class="w-2.5 h-2.5 rounded-full bg-[var(--color-danger-500)] inline-block animate-pulse" title="Firing since {fmtTs(r.firing_since)}"></span>
                    {:else if r.enabled}
                      <span class="w-2 h-2 rounded-full bg-[var(--color-success-500)] inline-block"></span>
                    {:else}
                      <span class="w-2 h-2 rounded-full bg-[var(--fg-subtle)] inline-block"></span>
                    {/if}
                  </td>
                  <td class="px-3 py-2.5">
                    <button class="text-left" onclick={() => openEditRule(r)}>
                      <div class="font-medium text-sm flex items-center gap-1.5">
                        {r.name}
                        <Badge variant={r.severity === 'critical' ? 'danger' : r.severity === 'info' ? 'info' : 'warning'}>{r.severity}</Badge>
                        {#if r.builtin}<Badge variant="info">built-in</Badge>{/if}
                        {#if isMuted(r)}<Badge variant="default">muted</Badge>{/if}
                      </div>
                      <div class="text-[10px] text-[var(--fg-muted)] font-mono">{r.container_filter}</div>
                    </button>
                  </td>
                  <td class="px-3 py-2.5 font-mono text-xs text-[var(--fg-muted)]">
                    {r.metric.replace('_', ' ')} {r.operator === 'gt' ? '>' : '<'} {r.threshold}% for {r.duration_seconds}s
                    {#if r.cooldown_seconds}<span class="text-[var(--fg-subtle)]"> · {r.cooldown_seconds}s cooldown</span>{/if}
                  </td>
                  <td class="px-3 py-2.5">
                    <div class="flex flex-wrap gap-1">
                      {#each r.channel_ids ?? [] as cid}
                        <span class="text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">{channelName(cid)}</span>
                      {/each}
                      {#if !r.channel_ids?.length}<span class="text-[10px] text-[var(--fg-subtle)]">none</span>{/if}
                    </div>
                  </td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtRelTime(r.last_triggered_at)}</td>
                  <td class="px-3 py-2.5 text-center">
                    <label class="relative inline-flex items-center cursor-pointer">
                      <input type="checkbox" class="sr-only peer" checked={r.enabled} onchange={() => toggleRuleEnabled(r)} />
                      <div class="w-8 h-4.5 bg-[var(--surface)] border border-[var(--border)] rounded-full peer-checked:bg-[var(--color-brand-500)] peer-checked:border-[var(--color-brand-500)] after:content-[''] after:absolute after:top-[1px] after:left-[1px] after:bg-white after:rounded-full after:h-3.5 after:w-3.5 after:transition-transform peer-checked:after:translate-x-3.5"></div>
                    </label>
                  </td>
                  <td class="px-3 py-2.5">
                    <div class="flex gap-0.5 justify-end">
                      {#if isMuted(r)}
                        <button class="p-1.5 rounded-md text-[var(--color-warning-400)] hover:bg-[var(--surface-hover)]" title="Unmute" onclick={() => unmuteRule(r)}>
                          <BellRing class="w-3.5 h-3.5" />
                        </button>
                      {:else}
                        <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Mute 1h" onclick={() => muteRule(r, 1)}>
                          <BellOff class="w-3.5 h-3.5" />
                        </button>
                      {/if}
                      <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Duplicate" onclick={() => duplicateRule(r)}>
                        <Copy class="w-3.5 h-3.5" />
                      </button>
                      {#if r.builtin}
                        <button class="p-1.5 rounded-md text-[var(--fg-subtle)] cursor-not-allowed" title="Built-in rule cannot be deleted — disable it instead" disabled aria-label="Cannot delete built-in rule">
                          <Trash2 class="w-3.5 h-3.5" />
                        </button>
                      {:else}
                        <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteRule(r)}>
                          <Trash2 class="w-3.5 h-3.5" />
                        </button>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}

  <!-- ===== CHANNELS TAB ===== -->
  {:else if tab === 'channels'}
    <div class="flex justify-between items-center">
      <span class="text-sm text-[var(--fg-muted)]">{channels.length} channel{channels.length === 1 ? '' : 's'}</span>
      <Button variant="primary" onclick={openNewCh}><Plus class="w-4 h-4" /> New channel</Button>
    </div>

    {#if chLoading && channels.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="3rem" /></Card>
    {:else if channels.length === 0}
      <Card><EmptyState icon={Bell} title="No notification channels" description="Add a webhook, ntfy, Discord, Slack, Teams, Gotify or SMTP destination." /></Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-left px-5 py-3">Name</th>
                <th class="text-left px-3 py-3">Type</th>
                <th class="text-right px-3 py-3">Used by</th>
                <th class="text-center px-3 py-3">Enabled</th>
                <th class="text-right px-3 py-3 w-28">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each channels as c (c.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-5 py-2.5">
                    <button class="text-left" onclick={() => openEditCh(c)}>
                      <div class="font-medium text-sm">{c.name}</div>
                    </button>
                  </td>
                  <td class="px-3 py-2.5"><Badge variant="info">{c.type}</Badge></td>
                  <td class="px-3 py-2.5 text-right text-xs text-[var(--fg-muted)] tabular-nums">{rulesUsingChannel(c.id)} rule{rulesUsingChannel(c.id) === 1 ? '' : 's'}</td>
                  <td class="px-3 py-2.5 text-center">
                    <label class="relative inline-flex items-center cursor-pointer">
                      <input type="checkbox" class="sr-only peer" checked={c.enabled} onchange={() => toggleChEnabled(c)} />
                      <div class="w-8 h-4.5 bg-[var(--surface)] border border-[var(--border)] rounded-full peer-checked:bg-[var(--color-brand-500)] peer-checked:border-[var(--color-brand-500)] after:content-[''] after:absolute after:top-[1px] after:left-[1px] after:bg-white after:rounded-full after:h-3.5 after:w-3.5 after:transition-transform peer-checked:after:translate-x-3.5"></div>
                    </label>
                  </td>
                  <td class="px-3 py-2.5">
                    <div class="flex gap-0.5 justify-end">
                      <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Test" onclick={() => testCh(c)}>
                        <Send class="w-3.5 h-3.5" />
                      </button>
                      <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteCh(c)}>
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}

  <!-- ===== HISTORY TAB ===== -->
  {:else if tab === 'history'}
    <div class="flex flex-wrap items-center gap-3">
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input type="search" placeholder="Search rule, container, message…" bind:value={histSearch} class="dm-input pl-8 pr-3 py-1.5 text-sm w-full" />
      </div>
      <div class="flex gap-1 text-xs">
        {#each [['all', 'All'], ['fired', 'Fired'], ['resolved', 'Resolved']] as [key, label]}
          <button
            class="px-2.5 py-1 rounded-full border transition-colors {histStatus === key
              ? 'bg-[var(--surface)] border-[var(--border-strong)] text-[var(--fg)]'
              : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-hover)]'}"
            onclick={() => (histStatus = key as typeof histStatus)}
          >{label}</button>
        {/each}
      </div>
      <button
        class="dm-btn dm-btn-secondary dm-btn-sm"
        onclick={() => {
          const csv = ['Time,Status,Rule,Container,Value,Threshold,Message']
            .concat(filteredHistory.map(e => `"${e.occurred_at}","${e.status}","${e.rule_name}","${e.container_name}",${e.value ?? ''},${e.threshold ?? ''},"${(e.message ?? '').replace(/"/g, '""')}"`))
            .join('\n');
          const blob = new Blob([csv], { type: 'text/csv' });
          const a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = `dockmesh-alerts-${new Date().toISOString().slice(0, 10)}.csv`;
          a.click();
        }}
      >Export CSV</button>
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">{filteredHistory.length} / {history.length}</span>
    </div>

    {#if historyLoading && history.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="3rem" /></Card>
    {:else if history.length === 0}
      <Card><EmptyState icon={Activity} title="No alerts triggered yet" description="History appears here when a rule fires or resolves." /></Card>
    {:else if filteredHistory.length === 0}
      <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No alerts match this filter.</Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-center px-3 py-3 w-10">Status</th>
                <th class="text-left px-3 py-3">Rule</th>
                <th class="text-left px-3 py-3">Container</th>
                <th class="text-left px-3 py-3">Value</th>
                <th class="text-left px-3 py-3">Message</th>
                <th class="text-left px-3 py-3">Time</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each filteredHistory as e (e.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-3 py-2.5 text-center">
                    {#if e.status === 'fired'}
                      <AlertTriangle class="w-4 h-4 text-[var(--color-danger-400)] inline" />
                    {:else}
                      <CheckCircle2 class="w-4 h-4 text-[var(--color-success-400)] inline" />
                    {/if}
                  </td>
                  <td class="px-3 py-2.5 font-medium text-sm">{e.rule_name}</td>
                  <td class="px-3 py-2.5 font-mono text-xs text-[var(--fg-muted)]">{e.container_name}</td>
                  <td class="px-3 py-2.5">
                    {#if e.value}
                      <span class="font-mono text-xs {e.status === 'fired' ? 'text-[var(--color-danger-400)]' : 'text-[var(--fg-muted)]'}">
                        {e.value.toFixed(1)}% <span class="text-[var(--fg-subtle)]">/ {e.threshold}%</span>
                      </span>
                    {:else}
                      <span class="text-xs text-[var(--fg-subtle)]">—</span>
                    {/if}
                  </td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)] truncate max-w-[200px]" title={e.message}>{e.message}</td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)] whitespace-nowrap">{fmtTs(e.occurred_at)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}
  {/if}
</section>

<!-- Channel modal with structured fields -->
<Modal bind:open={showCh} title={editCh ? 'Edit channel' : 'Add channel'} maxWidth="max-w-lg" onclose={resetCh}>
  <form onsubmit={saveCh} class="space-y-4" id="ch-form">
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="ch-type" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Type</label>
        <select id="ch-type" class="dm-input text-sm" bind:value={chForm.type} disabled={editCh !== null}
          onchange={() => { chConfigFields = {}; }}>
          {#each channelTypes as t}
            <option value={t.value}>{t.label}</option>
          {/each}
        </select>
      </div>
      <Input label="Name" bind:value={chForm.name} placeholder="My alerts channel" />
    </div>

    <!-- Type-specific structured fields -->
    {#if activeChType}
      <fieldset class="space-y-3">
        <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">{activeChType.label} Configuration</legend>
        {#each activeChType.fields as f}
          <div>
            <label for="ch-{f.key}" class="block text-xs font-medium text-[var(--fg-muted)] mb-1">{f.label}</label>
            <input
              id="ch-{f.key}"
              type={f.key === 'password' ? 'password' : 'text'}
              class="dm-input text-sm font-mono"
              placeholder={f.placeholder}
              value={chConfigFields[f.key] ?? ''}
              oninput={(e) => { chConfigFields = { ...chConfigFields, [f.key]: (e.target as HTMLInputElement).value }; }}
            />
          </div>
        {/each}
      </fieldset>
    {/if}

    <label class="flex items-center gap-2 text-sm cursor-pointer">
      <input type="checkbox" bind:checked={chForm.enabled} class="accent-[var(--color-brand-500)]" />
      Enabled
    </label>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showCh = false; resetCh(); }}>Cancel</Button>
    <Button variant="primary" type="submit" form="ch-form">{editCh ? 'Save' : 'Create'}</Button>
  {/snippet}
</Modal>

<!-- Rule modal -->
<Modal bind:open={showRule} title={editRule ? 'Edit rule' : 'Add rule'} maxWidth="max-w-xl" onclose={resetRule}>
  <form onsubmit={saveRule} class="space-y-4" id="rule-form">
    <Input label="Name" bind:value={ruleForm.name} placeholder="High CPU on web stack" />
    <Input label="Container filter" bind:value={ruleForm.container_filter} hint="* = all containers, or exact name" />
    <div class="grid grid-cols-3 gap-3">
      <div>
        <label for="rule-metric" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Metric</label>
        <select id="rule-metric" class="dm-input text-sm" bind:value={ruleForm.metric}>
          <option value="cpu_percent">CPU %</option>
          <option value="mem_percent">Memory %</option>
        </select>
      </div>
      <div>
        <label for="rule-op" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Operator</label>
        <select id="rule-op" class="dm-input text-sm" bind:value={ruleForm.operator}>
          <option value="gt">greater than</option>
          <option value="lt">less than</option>
        </select>
      </div>
      <Input label="Threshold %" type="number" bind:value={ruleForm.threshold as any} />
    </div>
    <Input label="Duration (seconds)" type="number" bind:value={ruleForm.duration_seconds as any} hint="Condition must hold this long before firing" />

    <div class="grid grid-cols-2 gap-3">
      <div>
        <label for="rule-severity" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Severity</label>
        <select id="rule-severity" class="dm-input text-sm" bind:value={ruleForm.severity}>
          <option value="critical">Critical</option>
          <option value="warning">Warning</option>
          <option value="info">Info</option>
        </select>
      </div>
      <Input label="Cooldown (seconds)" type="number" bind:value={ruleForm.cooldown_seconds as any} hint="Suppress re-notify for this long" />
    </div>

    {#if editRule}
      <div>
        <label for="rule-mute" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Mute until (optional)</label>
        <input id="rule-mute" type="datetime-local" class="dm-input text-sm"
          value={ruleForm.muted_until ? ruleForm.muted_until.slice(0, 16) : ''}
          oninput={(e) => { const v = (e.target as HTMLInputElement).value; ruleForm.muted_until = v ? new Date(v).toISOString() : ''; }}
        />
        <span class="text-[10px] text-[var(--fg-subtle)]">Rule evaluates but notifications are suppressed while muted</span>
      </div>
    {/if}

    <div>
      <div class="text-xs font-medium text-[var(--fg-muted)] mb-1.5">Notify via</div>
      {#if channels.length === 0}
        <div class="text-xs text-[var(--fg-subtle)]">No channels yet — create one in the Channels tab.</div>
      {:else}
        <div class="space-y-1 max-h-32 overflow-auto">
          {#each channels as c}
            <label class="flex items-center gap-2 text-sm cursor-pointer hover:bg-[var(--surface-hover)] px-2 py-1 rounded">
              <input type="checkbox" checked={ruleForm.channel_ids.includes(c.id)} onchange={() => toggleChannelInRule(c.id)} class="accent-[var(--color-brand-500)]" />
              {c.name} <span class="text-xs text-[var(--fg-muted)]">({c.type})</span>
            </label>
          {/each}
        </div>
      {/if}
    </div>

    <label class="flex items-center gap-2 text-sm cursor-pointer">
      <input type="checkbox" bind:checked={ruleForm.enabled} class="accent-[var(--color-brand-500)]" />
      Enabled
    </label>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showRule = false; resetRule(); }}>Cancel</Button>
    <Button variant="primary" type="submit" form="rule-form">{editRule ? 'Save' : 'Create'}</Button>
  {/snippet}
</Modal>
