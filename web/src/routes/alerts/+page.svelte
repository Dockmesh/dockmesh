<script lang="ts">
  import { goto } from '$app/navigation';
  import { sevTone } from './ruleKinds';
  import { setTabParam } from '$lib/tabs';
  import { page } from '$app/state';
  import { api, ApiError } from '$lib/api';
  import type { AlertRule, NotificationChannel, AlertHistoryEntry, HostInfo } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { EmptyState, Segmented, Skeleton, StatusChip, Tabs } from '$lib/components/ui';
  import { Activity, Download, Plus } from 'lucide-svelte';
  import RulesTab from './_RulesTab.svelte';
  import ChannelsTab from './_ChannelsTab.svelte';
  import RuleModal from './_RuleModal.svelte';
  import ChannelModal from './_ChannelModal.svelte';

  type Tab = 'rules' | 'channels' | 'history';

  let tab = $state<Tab>((new URLSearchParams(page.url.search).get('tab') as Tab) || 'rules');
  function setTab(t: string) {
    tab = t as Tab;
    setTabParam(t, 'rules');
  }

  let channels = $state<NotificationChannel[]>([]);
  let rules = $state<AlertRule[]>([]);
  let history = $state<AlertHistoryEntry[]>([]);
  let hosts = $state<HostInfo[]>([]);
  let chLoading = $state(false);
  let rulesLoading = $state(false);
  let histLoading = $state(false);

  let ruleModalOpen = $state(false);
  let editingRule = $state<AlertRule | null>(null);
  let channelModalOpen = $state(false);
  let editingChannel = $state<NotificationChannel | null>(null);

  let histSearch = $state('');
  let histStatus = $state<string>('all');

  async function loadChannels() {
    chLoading = true;
    try { channels = await api.alerts.listChannels(); }
    catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
    finally { chLoading = false; }
  }
  async function loadRules() {
    rulesLoading = true;
    try { rules = await api.alerts.listRules(); }
    catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
    finally { rulesLoading = false; }
  }
  async function loadHistory() {
    histLoading = true;
    try { history = await api.alerts.history(500); }
    catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
    finally { histLoading = false; }
  }

  $effect(() => {
    if (!allowed('alerts.update')) { goto('/'); return; }
    // All three up front, so every tab counter is real from the start.
    Promise.all([loadRules(), loadChannels(), loadHistory()]);
    api.hosts.list().then((h) => (hosts = h)).catch(() => (hosts = []));
  });

  $effect(() => autoRefresh(() => { loadRules(); loadChannels(); loadHistory(); }, 10_000));

  const hostName = (id: string) => hosts.find((h) => h.id === id)?.name ?? id;

  const firingCount = $derived(rules.filter((r) => r.firing_since).length);
  const enabledRules = $derived(rules.filter((r) => r.enabled).length);
  const mutedRules = $derived(rules.filter((r) => r.muted_until && new Date(r.muted_until) > new Date()).length);

  function openNewRule() { editingRule = null; ruleModalOpen = true; }
  function openEditRule(r: AlertRule) { editingRule = r; ruleModalOpen = true; }
  function openNewChannel() { editingChannel = null; channelModalOpen = true; }
  function openEditChannel(c: NotificationChannel) { editingChannel = c; channelModalOpen = true; }

  const filteredHistory = $derived(
    history.filter((e) => {
      if (histStatus !== 'all' && e.status !== histStatus) return false;
      const q = histSearch.trim().toLowerCase();
      if (!q) return true;
      return e.rule_name.toLowerCase().includes(q) || e.container_name.toLowerCase().includes(q) || e.message.toLowerCase().includes(q);
    })
  );

  const ruleSeverity = $derived(new Map(rules.map((r) => [r.id, r.severity || 'warning'])));
  function fmtValue(e: AlertHistoryEntry): string {
    if ((e.kind && e.kind !== 'metric' && e.kind !== 'disk_usage') || e.value == null) return '';
    return `${e.value.toFixed(1)} %${e.threshold != null ? ` / ${e.threshold} %` : ''}`;
  }

  const HCOLS = '14px 84px 84px minmax(160px, 1fr) minmax(140px, 0.9fr) 110px minmax(220px, 1.6fr) 150px';

  function exportCSV() {
    const csv = ['Time,Status,Rule,Container,Value,Threshold,Message']
      .concat(
        filteredHistory.map(
          (e) =>
            `"${e.occurred_at}","${e.status}","${e.rule_name}","${e.container_name}",${e.value ?? ''},${e.threshold ?? ''},"${(e.message ?? '').replace(/"/g, '""')}"`
        )
      )
      .join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `dockmesh-alerts-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
  }
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Alerts</h1>
    <span class="dm-sub">
      {#if rules.length === 0}
        No alert rules yet
      {:else}
        {#if firingCount > 0}<span class="al-fire">{firingCount} firing</span>{:else}nothing firing{/if}
        · {enabledRules} rule{enabledRules === 1 ? '' : 's'} active{#if mutedRules > 0} · {mutedRules} muted{/if}
      {/if}
    </span>
    <span class="dm-sp"></span>
    {#if tab === 'rules'}
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNewRule}><Plus size={14} strokeWidth={2} /> New rule</button>
    {:else if tab === 'channels'}
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNewChannel}><Plus size={14} strokeWidth={2} /> New channel</button>
    {:else}
      <button type="button" class="dm-btn" onclick={exportCSV} disabled={filteredHistory.length === 0}><Download size={14} strokeWidth={1.8} /> Export CSV</button>
    {/if}
  </div>

  <Tabs
    label="Alerts"
    active={tab}
    onchange={setTab}
    tabs={[
      { key: 'rules', label: 'Rules', count: rules.length, dot: firingCount > 0 },
      { key: 'channels', label: 'Channels', count: channels.length, dot: channels.some((c) => c.last_error_at && (!c.last_sent_at || new Date(c.last_error_at) > new Date(c.last_sent_at))) },
      { key: 'history', label: 'History', count: history.length }
    ]}
  />

  {#if tab === 'rules'}
    <RulesTab {rules} {channels} {hostName} loading={rulesLoading} onEdit={openEditRule} onCreate={openNewRule} onReload={loadRules} />
  {:else if tab === 'channels'}
    <ChannelsTab {channels} {rules} loading={chLoading} onEdit={openEditChannel} onCreate={openNewChannel} onReload={loadChannels} />
  {:else}
    {#if histLoading && history.length === 0}
      <div class="dm-tbl">
        {#each Array(4) as _, i (i)}
          <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div>
        {/each}
      </div>
    {:else if history.length === 0}
      <EmptyState icon={Activity} title="No alerts raised yet" description="The history fills when a rule fires or resolves." />
    {:else}
      <div class="dm-toolbar">
        <input type="search" class="dm-filter al-search" placeholder="Filter rule, object, message…" aria-label="Filter history" bind:value={histSearch} />
        <Segmented
          label="Status"
          size="sm"
          bind:value={histStatus}
          options={[
            { key: 'all', label: 'All' },
            { key: 'fired', label: 'Fired', count: history.filter((e) => e.status === 'fired').length },
            { key: 'resolved', label: 'Resolved', count: history.filter((e) => e.status === 'resolved').length }
          ]}
        />
        <span class="dm-sp"></span>
        <span class="dm-num dm-dim">{filteredHistory.length} of {history.length}</span>
      </div>
      <div class="dm-tbl" role="table" aria-label="Alert history">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {HCOLS}">
          <span role="columnheader"></span>
          <span role="columnheader">Severity</span>
          <span role="columnheader">Status</span>
          <span role="columnheader">Rule</span>
          <span role="columnheader">Object</span>
          <span role="columnheader">Value</span>
          <span role="columnheader">Message</span>
          <span role="columnheader">Time</span>
        </div>
        {#each filteredHistory as e (e.id)}
          {@const sev = ruleSeverity.get(e.rule_id)}
          {@const fired = e.status === 'fired'}
          <div class="dm-tr" role="row" style="grid-template-columns: {HCOLS}">
            <span role="cell"><span class="dm-dot {fired ? (sev === 'critical' ? 'bad' : 'warn') : 'ok'}"></span></span>
            <span role="cell">{#if sev}<StatusChip tone={sevTone(sev)} label={sev} />{:else}<span class="dm-dim">—</span>{/if}</span>
            <span role="cell"><StatusChip tone={fired ? 'bad' : 'ok'} label={fired ? 'fired' : 'resolved'} /></span>
            <span role="cell" class="dm-nm">
              <span class="dm-t" title={e.rule_name}>{e.rule_name}</span>
              {#if e.notified === false}<small title="Recorded without notifying channels (muted or in cooldown)">silenced</small>{/if}
            </span>
            <span role="cell" class="dm-mono al-obj" title={e.host_id ? `${e.container_name} on ${hostName(e.host_id)}` : e.container_name}>
              {e.container_name}{#if e.host_id}<span class="dm-dim"> · {hostName(e.host_id)}</span>{/if}
            </span>
            <span role="cell" class="dm-num" class:bad={fired}>{fmtValue(e)}</span>
            <span role="cell" class="dm-dim" title={e.message}>{e.message}</span>
            <span role="cell" class="dm-num dm-dim">{new Date(e.occurred_at).toLocaleString()}</span>
          </div>
        {:else}
          <div class="dm-empty">No alerts match this filter.</div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<RuleModal bind:open={ruleModalOpen} editing={editingRule} {channels} {hosts} {hostName} onSaved={loadRules} />
<ChannelModal bind:open={channelModalOpen} editing={editingChannel} onSaved={loadChannels} />

<style>
  .al-fire { color: var(--bad); font-weight: 600; }
  .al-search { width: 280px; }
  .al-obj { font-size: 12px; }
</style>
