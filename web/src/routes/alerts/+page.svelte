<script lang="ts">
  // Alerts — rules, channels, history. URL-driven sub-tabs, 10s
  // auto-refresh on the active tab. Tabs and modals split into private
  // components for clarity.
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { api, ApiError } from '$lib/api';
  import type {
    AlertRule, NotificationChannel, AlertHistoryEntry,
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { EditorialPage, Eyebrow } from '$lib/components/editorial';
  import { Skeleton } from '$lib/components/ui';
  import {
    Bell, BellRing, Activity, Search, AlertTriangle, CheckCircle2, Download,
  } from 'lucide-svelte';
  import RulesTab from './_RulesTab.svelte';
  import ChannelsTab from './_ChannelsTab.svelte';
  import RuleModal from './_RuleModal.svelte';
  import ChannelModal from './_ChannelModal.svelte';

  type Tab = 'rules' | 'channels' | 'history';

  // URL-driven tab state with two-way binding (replaceState so back/fwd
  // navigates the page itself, not the tab).
  let tab = $state<Tab>(
    (new URLSearchParams($page.url.search).get('tab') as Tab) || 'rules',
  );
  function setTab(t: Tab) {
    tab = t;
    const url = new URL($page.url);
    url.searchParams.set('tab', t);
    goto(url.pathname + url.search, { replaceState: true, noScroll: true, keepFocus: true });
  }

  let channels = $state<NotificationChannel[]>([]);
  let rules = $state<AlertRule[]>([]);
  let history = $state<AlertHistoryEntry[]>([]);
  let chLoading = $state(false);
  let rulesLoading = $state(false);
  let histLoading = $state(false);

  // Modal state
  let ruleModalOpen = $state(false);
  let editingRule = $state<AlertRule | null>(null);
  let channelModalOpen = $state(false);
  let editingChannel = $state<NotificationChannel | null>(null);

  // History filters
  let histSearch = $state('');
  let histStatus = $state<'all' | 'fired' | 'resolved'>('all');

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
    // Load all three on mount so the Rules / Channels / History tab
    // counters show real numbers from the start, not "0" until each
    // tab is clicked.
    Promise.all([loadRules(), loadChannels(), loadHistory()]);
  });

  // Poll every list every 10s so all counters + the firing-since
  // indicator stay fresh, not just the active tab.
  $effect(() => {
    const refresh = () => { loadRules(); loadChannels(); loadHistory(); };
    return autoRefresh(refresh, 10_000);
  });

  // Stats for header subtitle.
  const firingCount = $derived(rules.filter((r) => r.firing_since).length);
  const enabledRules = $derived(rules.filter((r) => r.enabled).length);
  const mutedRules = $derived(rules.filter((r) => r.muted_until && new Date(r.muted_until) > new Date()).length);

  // Modal openers
  function openNewRule() { editingRule = null; ruleModalOpen = true; }
  function openEditRule(r: AlertRule) { editingRule = r; ruleModalOpen = true; }
  function openNewChannel() { editingChannel = null; channelModalOpen = true; }
  function openEditChannel(c: NotificationChannel) { editingChannel = c; channelModalOpen = true; }

  // ── History helpers ──────────────────────────────────────────────────
  const filteredHistory = $derived(
    history.filter((e) => {
      if (histStatus !== 'all' && e.status !== histStatus) return false;
      if (!histSearch.trim()) return true;
      const q = histSearch.toLowerCase();
      return e.rule_name.toLowerCase().includes(q) ||
        e.container_name.toLowerCase().includes(q) ||
        e.message.toLowerCase().includes(q);
    }),
  );

  function fmtTs(ts: string): string {
    return new Date(ts).toLocaleString();
  }

  function exportCSV() {
    const csv = ['Time,Status,Rule,Container,Value,Threshold,Message']
      .concat(filteredHistory.map((e) =>
        `"${e.occurred_at}","${e.status}","${e.rule_name}","${e.container_name}",${e.value ?? ''},${e.threshold ?? ''},"${(e.message ?? '').replace(/"/g, '""')}"`,
      ))
      .join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `dockmesh-alerts-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
  }
</script>

<EditorialPage>
  <section class="al">
    <!-- ─────────────────── Header ─────────────────── -->
    <header class="al-header">
      <div class="al-header-text">
        <h1 class="ed-title al-title">Alerts</h1>
        <p class="ed-subtitle al-subtitle">
          {#if rules.length === 0}
            No alert rules yet
          {:else}
            {enabledRules} active rule{enabledRules === 1 ? '' : 's'} ·
            {#if firingCount > 0}
              <span style="color: var(--color-danger-400);">{firingCount} firing now</span>
            {:else}
              all quiet
            {/if}{#if mutedRules > 0} · {mutedRules} muted{/if}
          {/if}
        </p>
      </div>
    </header>

    <!-- ─────────────────── Sub-Tabs ─────────────────── -->
    <div class="ed-tabs" role="tablist">
      <button
        type="button"
        class="ed-tab"
        class:active={tab === 'rules'}
        onclick={() => setTab('rules')}
      >
        <BellRing size={13} strokeWidth={1.5} />
        Rules
        <span class="count">{rules.length}</span>
        {#if firingCount > 0}
          <span class="al-tab-fire">{firingCount}</span>
        {/if}
      </button>
      <button
        type="button"
        class="ed-tab"
        class:active={tab === 'channels'}
        onclick={() => setTab('channels')}
      >
        <Bell size={13} strokeWidth={1.5} />
        Channels
        <span class="count">{channels.length}</span>
      </button>
      <button
        type="button"
        class="ed-tab"
        class:active={tab === 'history'}
        onclick={() => setTab('history')}
      >
        <Activity size={13} strokeWidth={1.5} />
        History
        <span class="count">{history.length}</span>
      </button>
    </div>

    <!-- ─────────────────── Body ─────────────────── -->
    {#if tab === 'rules'}
      <RulesTab
        {rules}
        {channels}
        loading={rulesLoading}
        onEdit={openEditRule}
        onCreate={openNewRule}
        onReload={loadRules}
      />
    {:else if tab === 'channels'}
      <ChannelsTab
        {channels}
        {rules}
        loading={chLoading}
        onEdit={openEditChannel}
        onCreate={openNewChannel}
        onReload={loadChannels}
      />
    {:else if tab === 'history'}
      <div class="al-history">
        <div class="al-history-toolbar">
          <div class="al-history-search">
            <Search size={12} strokeWidth={1.5} class="al-search-icon" />
            <input
              type="search"
              placeholder="search rule · container · message…"
              bind:value={histSearch}
              class="ed-underline-input al-search-input"
            />
          </div>

          <div class="al-history-filter" role="tablist">
            {#each [
              { id: 'all'      as const, label: 'all' },
              { id: 'fired'    as const, label: 'fired' },
              { id: 'resolved' as const, label: 'resolved' },
            ] as opt}
              <button
                type="button"
                class="al-filter-pill"
                class:al-filter-active={histStatus === opt.id}
                onclick={() => (histStatus = opt.id)}
              >
                {opt.label}
              </button>
            {/each}
          </div>

          <span class="al-history-meta">
            {filteredHistory.length} / {history.length}
          </span>

          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-sm"
            onclick={exportCSV}
            disabled={filteredHistory.length === 0}
          >
            <Download size={12} strokeWidth={1.5} /> Export CSV
          </button>
        </div>

        {#if histLoading && history.length === 0}
          <Skeleton width="100%" height="6rem" />
        {:else if history.length === 0}
          <div class="al-empty">
            <Activity size={20} strokeWidth={1.4} />
            <p class="al-empty-title">No alerts triggered yet</p>
            <p class="ed-subtitle al-empty-blurb">
              History appears here when a rule fires or resolves.
            </p>
          </div>
        {:else if filteredHistory.length === 0}
          <div class="al-empty">
            <Eyebrow>no match</Eyebrow>
            <p class="al-empty-title">No alerts match this filter.</p>
          </div>
        {:else}
          <div class="al-history-list">
            {#each filteredHistory as e (e.id)}
              <article
                class="al-history-row"
                class:al-history-fired={e.status === 'fired'}
              >
                <span class="al-history-status">
                  {#if e.status === 'fired'}
                    <AlertTriangle size={12} strokeWidth={1.6} class="al-icon-fired" />
                  {:else}
                    <CheckCircle2 size={12} strokeWidth={1.6} class="al-icon-resolved" />
                  {/if}
                </span>
                <div class="al-history-text">
                  <div class="al-history-name">{e.rule_name}</div>
                  <div class="al-history-message">{e.message}</div>
                </div>
                <div class="al-history-target">
                  <span class="al-history-container">{e.container_name}</span>
                  {#if e.value !== undefined && e.value !== null}
                    <span class="al-history-value">
                      {e.value.toFixed(1)}%
                      {#if e.threshold !== undefined && e.threshold !== null}
                        <span class="al-history-threshold">/ {e.threshold}%</span>
                      {/if}
                    </span>
                  {/if}
                </div>
                <span class="al-history-time">{fmtTs(e.occurred_at)}</span>
              </article>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  </section>
</EditorialPage>

<RuleModal
  bind:open={ruleModalOpen}
  editing={editingRule}
  {channels}
  onSaved={loadRules}
/>

<ChannelModal
  bind:open={channelModalOpen}
  editing={editingChannel}
  onSaved={loadChannels}
/>

<style>
  .al {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .al-header {
    display: flex;
    align-items: flex-end;
    gap: 24px;
    flex-wrap: wrap;
  }
  .al-header-text { min-width: 0; max-width: 70ch; }
  .al-title {
    font-size: 28px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .al-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }

  /* ── Sub-tabs use global .ed-tabs / .ed-tab / .count from app.css.
        Only the "firing" pill stays local — it's a status indicator,
        not a count badge, and red+rounded is intentional. ───────── */
  .al-tab-fire {
    font-size: 9.5px;
    padding: 1px 6px;
    border-radius: 999px;
    background: var(--color-danger-500);
    color: white;
    font-weight: 500;
  }

  /* ── History tab ────────────────────────────────────────────── */
  .al-history {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .al-history-toolbar {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .al-history-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .al-history-search :global(.al-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .al-search-input {
    padding-left: 18px;
    font-size: 12.5px;
    font-family: var(--font-mono);
  }

  .al-history-filter {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }
  .al-filter-pill {
    padding: 4px 10px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    line-height: 1.2;
    color: var(--fg-subtle);
    background: transparent;
    border: 0;
    border-right: 1px solid var(--border);
    cursor: pointer;
  }
  .al-filter-pill:last-child { border-right: 0; }
  .al-filter-pill:hover { color: var(--fg); }
  .al-filter-active {
    background: var(--bg-elevated);
    color: var(--fg);
  }

  .al-history-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-left: auto;
  }

  .al-history-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .al-history-row {
    display: grid;
    grid-template-columns: 24px minmax(0, 1.4fr) minmax(0, 1.2fr) 150px;
    gap: 14px;
    align-items: center;
    padding: 10px 14px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--surface);
  }
  .al-history-fired {
    border-left: 3px solid var(--color-danger-500);
    background: color-mix(in srgb, var(--color-danger-500) 4%, var(--surface));
  }
  .al-history-status :global(.al-icon-fired) { color: var(--color-danger-400); }
  .al-history-status :global(.al-icon-resolved) { color: var(--color-success-400); }

  .al-history-text { min-width: 0; }
  .al-history-name {
    font-size: 13px;
    font-weight: 500;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .al-history-message {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .al-history-target {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .al-history-container {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .al-history-value {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--color-danger-400);
  }
  .al-history-fired .al-history-value { color: var(--color-danger-400); }
  .al-history-row:not(.al-history-fired) .al-history-value { color: var(--fg-muted); }
  .al-history-threshold { color: var(--fg-subtle); }

  .al-history-time {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-align: right;
  }

  /* ── Empty ──────────────────────────────────────────────────── */
  .al-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    color: var(--fg-subtle);
  }
  .al-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .al-empty-blurb { margin: 4px 0 0; max-width: 50ch; }

  @media (max-width: 880px) {
    .al-history-row {
      grid-template-columns: 24px 1fr;
      grid-auto-flow: row;
    }
    .al-history-target,
    .al-history-time { grid-column: 2 / -1; text-align: left; }
  }
</style>
