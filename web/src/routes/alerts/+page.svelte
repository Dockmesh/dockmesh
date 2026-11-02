<script lang="ts">
  import { fmtAgo } from '$lib/format';
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { sevTone, sevLabel } from './ruleKinds';
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime, fmtNumber } from '$lib/i18n';
  import { urlTab } from '$lib/urlTab.svelte';
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

  const tabs = urlTab<Tab>(['rules', 'channels', 'history'], 'rules');
  const tab = $derived(tabs.current);
  const setTab = (t: string) => tabs.set(t);

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
    if (!allowed('alerts.view')) { chLoading = false; return; }
    chLoading = true;
    try { channels = await api.alerts.listChannels(); }
    catch (err) { toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined); }
    finally { chLoading = false; }
  }
  async function loadRules() {
    if (!allowed('alerts.view')) { rulesLoading = false; return; }
    rulesLoading = true;
    try { rules = await api.alerts.listRules(); }
    catch (err) { toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined); }
    finally { rulesLoading = false; }
  }
  async function loadHistory() {
    histLoading = true;
    if (!allowed('alerts.history')) { histLoading = false; return; }
    try { history = await api.alerts.history(500); }
    catch (err) { toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined); }
    finally { histLoading = false; }
  }

  $effect(() => {
    if (!allowed('alerts.view') && !allowed('alerts.history')) { goto('/'); return; }
    // All three up front, so every tab counter is real from the start.
    // Untracked: request() reads the access token, and a token refresh
    // must not reload the page.
    untrack(() => {
      Promise.all([loadRules(), loadChannels(), loadHistory()]);
      api.hosts.list().then((h) => (hosts = h)).catch(() => (hosts = []));
    });
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
    return `${fmtNumber(e.value, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} %${e.threshold != null ? ` / ${fmtNumber(e.threshold)} %` : ''}`;
  }

  const HCOLS = '110px minmax(160px, 1fr) minmax(140px, 0.9fr) 110px minmax(220px, 1.6fr) 150px';

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
    <h1>{m.alerts_title()}</h1>
    <span class="dm-sub">
      {#if rules.length === 0}
        {m.alerts_sub_no_rules()}
      {:else}
        {#if firingCount > 0}<span class="al-fire">{m.alerts_n_firing({ n: firingCount })}</span>{:else}{m.alerts_nothing_firing()}{/if}
        · {m.alerts_n_rules_active({ count: enabledRules })}{#if mutedRules > 0} · {m.alerts_n_muted({ n: mutedRules })}{/if}
      {/if}
    </span>
    <span class="dm-sp"></span>
    {#if tab === 'rules'}
      {#if allowed('alerts.create')}<button type="button" class="dm-btn dm-btn-primary" onclick={openNewRule}><Plus size={14} strokeWidth={2} /> {m.alerts_new_rule()}</button>{/if}
    {:else if tab === 'channels'}
      {#if allowed('alerts.create')}<button type="button" class="dm-btn dm-btn-primary" onclick={openNewChannel}><Plus size={14} strokeWidth={2} /> {m.alerts_new_channel()}</button>{/if}
    {:else}
      <button type="button" class="dm-btn" onclick={exportCSV} disabled={filteredHistory.length === 0}><Download size={14} strokeWidth={1.8} /> {m.alerts_export_csv()}</button>
    {/if}
  </div>

  <Tabs
    label={m.alerts_title()}
    active={tab}
    onchange={setTab}
    tabs={[
      { key: 'rules', label: m.alerts_tab_rules(), count: rules.length, dot: firingCount > 0 },
      { key: 'channels', label: m.alerts_tab_channels(), count: channels.length, dot: channels.some((c) => c.last_error_at && (!c.last_sent_at || new Date(c.last_error_at) > new Date(c.last_sent_at))) },
      { key: 'history', label: m.alerts_tab_history(), count: history.length }
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
      <EmptyState icon={Activity} title={m.alerts_history_empty_title()} description={m.alerts_history_empty_desc()} />
    {:else}
      <div class="dm-toolbar">
        <input type="search" class="dm-filter al-search" placeholder={m.alerts_history_filter_ph()} aria-label={m.alerts_history_filter_label()} bind:value={histSearch} />
        <Segmented
          label={m.common_status()}
          size="sm"
          bind:value={histStatus}
          options={[
            { key: 'all', label: m.common_all() },
            { key: 'fired', label: m.alerts_fired(), count: history.filter((e) => e.status === 'fired').length },
            { key: 'resolved', label: m.alerts_resolved(), count: history.filter((e) => e.status === 'resolved').length }
          ]}
        />
        <span class="dm-sp"></span>
        <span class="dm-num dm-dim">{m.alerts_n_of_total({ n: filteredHistory.length, total: history.length })}</span>
      </div>
      <div class="dm-tbl" role="table" aria-label={m.alerts_history_table_label()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {HCOLS}">
          <span role="columnheader">{m.common_status()}</span>
          <span role="columnheader">{m.alerts_col_rule()}</span>
          <span role="columnheader">{m.alerts_col_object()}</span>
          <span role="columnheader">{m.common_value()}</span>
          <span role="columnheader">{m.alerts_col_message()}</span>
          <span role="columnheader">{m.common_time()}</span>
        </div>
        {#each filteredHistory as e (e.id)}
          {@const sev = ruleSeverity.get(e.rule_id)}
          {@const fired = e.status === 'fired'}
          <div class="dm-tr" role="row" style="grid-template-columns: {HCOLS}">
            <span role="cell">
              {#if fired}<StatusChip tone={sev ? sevTone(sev) : 'bad'} label={sev ? sevLabel(sev) : m.alerts_fired_lc()} title={m.alerts_fired()} />{:else}<StatusChip tone="ok" label={m.alerts_resolved()} />{/if}
            </span>
            <span role="cell" class="dm-nm">
              <span class="dm-t" title={e.rule_name}>{e.rule_name}</span>
              {#if e.notified === false}<small title={m.alerts_silenced_title()}>{m.alerts_silenced()}</small>{/if}
              {#if e.repeats}<small title={m.alerts_repeats_title({ n: e.repeats })}>×{e.repeats + 1}</small>{/if}
            </span>
            <span role="cell" class="dm-mono al-obj" title={e.host_id ? m.alerts_obj_on_host({ name: e.container_name, host: hostName(e.host_id) }) : e.container_name}>
              {e.container_name}{#if e.host_id}<span class="dm-dim"> · {hostName(e.host_id)}</span>{/if}
            </span>
            <span role="cell" class="dm-num" class:bad={fired}>{fmtValue(e)}</span>
            <span role="cell" class="dm-dim" title={e.message}>{e.message}</span>
            <span role="cell" class="dm-num dm-dim" title={fmtDateTime(e.occurred_at)}>{fmtAgo(e.occurred_at)}</span>
          </div>
        {:else}
          <div class="dm-empty">{m.alerts_history_no_match()}</div>
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
