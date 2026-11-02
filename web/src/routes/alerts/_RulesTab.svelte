<script lang="ts">
  import { api, ApiError, type AlertRule, type NotificationChannel } from '$lib/api';
  import { appliesTo, kindLabel, kindOf, ruleSentence, ruleToInput } from './ruleKinds';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { EmptyState, Skeleton, Switch, InfoTip } from '$lib/components/ui';
  import { statusWord } from '$lib/components/ui/StatusChip.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { BellRing, Plus } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime } from '$lib/i18n';

  interface Props {
    rules: AlertRule[];
    channels: NotificationChannel[];
    loading: boolean;
    hostName: (id: string) => string;
    onEdit: (r: AlertRule) => void;
    onCreate: () => void;
    onReload: () => Promise<void>;
  }

  let { rules, channels, loading, hostName, onEdit, onCreate, onReload }: Props = $props();
  const canCreate = $derived(allowed('alerts.create'));
  const canUpdate = $derived(allowed('alerts.update'));
  const canDelete = $derived(allowed('alerts.delete'));

  const COLS = '14px 84px minmax(170px, 1.2fr) minmax(220px, 1.6fr) minmax(140px, 1fr) minmax(90px, 0.7fr) 96px 44px 36px';

  let filter = $state('');

  // Firing first, then enabled, then by name.
  const sortedRules = $derived(
    [...rules]
      .filter((r) => {
        const q = filter.trim().toLowerCase();
        if (!q) return true;
        return r.name.toLowerCase().includes(q) || ruleSentence(r, hostName).toLowerCase().includes(q);
      })
      .sort((a, b) => {
        const f = (a.firing_since ? 0 : 1) - (b.firing_since ? 0 : 1);
        if (f) return f;
        const e = (a.enabled ? 0 : 1) - (b.enabled ? 0 : 1);
        if (e) return e;
        return a.name.localeCompare(b.name);
      })
  );

  function dotTone(r: AlertRule): string {
    if (!r.enabled) return '';
    if (r.firing_since) return 'bad';
    return 'ok';
  }
  function isMuted(r: AlertRule): boolean {
    return !!r.muted_until && new Date(r.muted_until) > new Date();
  }
  function channelNames(r: AlertRule): string {
    return (r.channel_ids ?? []).map((id) => channels.find((c) => c.id === id)?.name ?? `#${id}`).join(', ');
  }
  function fmtRel(ts?: string): string {
    if (!ts) return m.common_never();
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return m.time_just_now();
    if (secs < 3600) return m.time_min_ago({ n: Math.floor(secs / 60) });
    if (secs < 86400) return m.time_h_ago({ n: Math.floor(secs / 3600) });
    return m.time_d_ago({ n: Math.floor(secs / 86400) });
  }
  function capital(s: string): string {
    return s ? s[0].toUpperCase() + s.slice(1) : s;
  }

  async function update(r: AlertRule, patch: Partial<ReturnType<typeof ruleToInput>>, ok?: string) {
    try {
      await api.alerts.updateRule(r.id, { ...ruleToInput(r), ...patch });
      if (ok) toast.success(ok, r.name);
      await onReload();
    } catch (err) {
      toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function duplicate(r: AlertRule) {
    try {
      const input = { ...ruleToInput(r), name: `${r.name} ${m.alerts_copy_suffix()}`, enabled: false };
      await api.alerts.createRule(input);
      toast.success(m.alerts_duplicated(), input.name);
      await onReload();
    } catch (err) {
      toast.error(m.alerts_duplicate_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteRule(r: AlertRule) {
    if (
      !(await confirm.ask({
        title: m.alerts_delete_rule_title(),
        message: m.alerts_delete_rule_message({ name: r.name }),
        body: m.alerts_delete_rule_body(),
        confirmLabel: m.common_delete(),
        danger: true
      }))
    )
      return;
    try {
      await api.alerts.deleteRule(r.id);
      toast.success(m.alerts_deleted(), r.name);
      await onReload();
    } catch (err) {
      toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

{#if loading && rules.length === 0}
  <div class="dm-tbl">
    {#each Array(4) as _, i (i)}
      <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div>
    {/each}
  </div>
{:else if rules.length === 0}
  <EmptyState
    icon={BellRing}
    title={m.alerts_sub_no_rules()}
    description={m.alerts_rules_empty_desc()}
  >
    {#snippet action()}
      {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={onCreate}><Plus size={14} strokeWidth={2} /> {m.alerts_new_rule()}</button>{/if}
    {/snippet}
  </EmptyState>
{:else}
  <div class="dm-toolbar">
    <input type="search" class="dm-filter rt-search" placeholder={m.alerts_filter_rules_ph()} aria-label={m.alerts_filter_rules()} bind:value={filter} />
  </div>
  <div class="dm-tbl" role="table" aria-label={m.alerts_rules_table_label()}>
    <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
      <span role="columnheader"></span>
      <span role="columnheader">{m.alerts_col_severity()}</span>
      <span role="columnheader">{m.alerts_col_rule()}</span>
      <span role="columnheader">{m.alerts_col_fires_when()}</span>
      <span role="columnheader">{m.alerts_col_applies_to()}</span>
      <span role="columnheader">{m.alerts_tab_channels()}</span>
      <span role="columnheader">{m.alerts_col_last_fired()}</span>
      <span role="columnheader">{m.alerts_col_on()}</span>
      <span role="columnheader"></span>
    </div>
    {#each sortedRules as r (r.id)}
      {@const muted = isMuted(r)}
      {@const sentence = capital(ruleSentence(r, hostName))}
      {@const scope = appliesTo(r, hostName)}
      {@const chs = channelNames(r)}
      <div class="dm-tr" class:rt-off={!r.enabled} role="row" style="grid-template-columns: {COLS}">
        <span role="cell"><span class="dm-dot {dotTone(r)}" title={r.firing_since ? m.alerts_firing_since({ at: fmtDateTime(r.firing_since) }) : r.enabled ? m.alerts_quiet() : m.alerts_paused()}></span></span>
        <span role="cell" class="dm-dim">{statusWord(r.severity || 'warning')}</span>
        <span role="cell" class="dm-nm">
          {#if canUpdate}<button type="button" class="rt-name dm-t" title={r.name} onclick={() => onEdit(r)}>{r.name}</button>{:else}<span class="dm-t" title={r.name}>{r.name}</span>{/if}
          <small>
            {r.builtin ? m.alerts_builtin() : m.alerts_custom()} · {kindLabel(kindOf(r))}{#if r.firing_since}<span class="rt-fire"> · {m.alerts_firing()}</span>{/if}{#if muted} · {m.alerts_muted_until({ at: fmtDateTime(r.muted_until!) })}{/if}
          </small>
        </span>
        <span role="cell" class="dm-dim" title={sentence}>{sentence}</span>
        <span role="cell" class="dm-dim" title={scope}>{scope}</span>
        <span role="cell" class="dm-num" class:dm-dim={!chs} title={chs || m.alerts_no_channel_title()}>
          {(r.channel_ids ?? []).length ? `${(r.channel_ids ?? []).length} · ${chs}` : m.alerts_none_lc()}
        </span>
        <span role="cell" class="dm-num dm-dim" title={r.last_triggered_at ? fmtDateTime(r.last_triggered_at) : ''}>{fmtRel(r.last_triggered_at)}</span>
        <span role="cell" class="dm-tr-menu">
          <Switch checked={r.enabled} disabled={!canUpdate} label={r.enabled ? m.alerts_pause_rule({ name: r.name }) : m.alerts_resume_rule({ name: r.name })} hideLabel onchange={(v) => update(r, { enabled: v })} />
        </span>
        <span role="cell" class="dm-tr-menu">
          {#if canUpdate || canCreate || (canDelete && !r.builtin)}
            <Menu
              label={m.alerts_actions_for({ name: r.name })}
              items={[
                ...(canUpdate ? [{ label: m.alerts_edit_ellipsis(), onclick: () => onEdit(r) }] : []),
                ...(canCreate ? [{ label: m.alerts_duplicate(), onclick: () => duplicate(r) }] : []),
                ...(!canUpdate
                  ? []
                  : muted
                    ? [{ label: m.alerts_unmute(), onclick: () => update(r, { muted_until: '' }, m.alerts_unmuted()) }]
                    : [{ label: m.alerts_mute_1h(), onclick: () => update(r, { muted_until: new Date(Date.now() + 3600000).toISOString() }, m.alerts_muted_1h()) }]),
                ...(r.builtin || !canDelete ? [] : [{ separator: true }, { label: m.common_delete_ellipsis(), danger: true, onclick: () => deleteRule(r) }])
              ]}
            />
          {/if}
        </span>
      </div>
    {:else}
      <div class="dm-empty">{m.alerts_rules_no_match({ filter })}</div>
    {/each}
  </div>
  <p class="dm-sub">{m.alerts_rules_footer()} <InfoTip text={m.alerts_rules_footer_tip()} /></p>
{/if}

<style>
  .rt-search { width: 260px; }
  .rt-name {
    all: unset;
    cursor: pointer;
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rt-name:hover { text-decoration: underline; }
  .rt-name:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .rt-fire { color: var(--bad); font-weight: 600; }
  .rt-off .dm-nm .rt-name { color: var(--muted); }
  p.dm-sub { margin: 0; }
</style>
