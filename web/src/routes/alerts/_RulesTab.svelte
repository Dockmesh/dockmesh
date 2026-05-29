<script lang="ts">
  // Rules tab — alert rules as Editorial cards with severity-coded
  // left border, sentence-style condition display, channels-used pills,
  // and per-row actions (mute / unmute / duplicate / enable-toggle / delete).
  import { api, ApiError, type AlertRule, type AlertRuleInput, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Eyebrow } from '$lib/components/editorial';
  import { Skeleton } from '$lib/components/ui';
  import {
    Plus, Trash2, Edit2, Copy, BellRing, BellOff, Power, PowerOff, BellRing as BellOn,
  } from 'lucide-svelte';

  interface Props {
    rules: AlertRule[];
    channels: NotificationChannel[];
    loading: boolean;
    onEdit: (r: AlertRule) => void;
    onCreate: () => void;
    onReload: () => Promise<void>;
  }

  let { rules, channels, loading, onEdit, onCreate, onReload }: Props = $props();

  // Sort: firing first, then enabled, then by name.
  const sortedRules = $derived(
    [...rules].sort((a, b) => {
      const aFiring = a.firing_since ? 0 : 1;
      const bFiring = b.firing_since ? 0 : 1;
      if (aFiring !== bFiring) return aFiring - bFiring;
      const aEnabled = a.enabled ? 0 : 1;
      const bEnabled = b.enabled ? 0 : 1;
      if (aEnabled !== bEnabled) return aEnabled - bEnabled;
      return a.name.localeCompare(b.name);
    }),
  );

  function channelName(id: number): string {
    return channels.find((c) => c.id === id)?.name ?? `#${id}`;
  }
  function channelType(id: number): string {
    return channels.find((c) => c.id === id)?.type ?? '';
  }

  function isMuted(r: AlertRule): boolean {
    return !!r.muted_until && new Date(r.muted_until) > new Date();
  }

  function metricLabel(m: string): string {
    if (m === 'cpu_percent') return 'CPU %';
    if (m === 'mem_percent') return 'Memory %';
    return m;
  }
  function opLabel(op: string): string {
    if (op === 'gt') return '>';
    if (op === 'lt') return '<';
    return op;
  }
  function fmtDuration(secs: number): string {
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.round(secs / 60)}m`;
    return `${Math.round(secs / 3600)}h`;
  }
  function fmtRelTime(ts?: string): string {
    if (!ts) return 'never';
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }

  function ruleToInput(r: AlertRule): AlertRuleInput {
    return {
      name: r.name,
      container_filter: r.container_filter,
      metric: r.metric,
      operator: r.operator,
      threshold: r.threshold,
      duration_seconds: r.duration_seconds,
      channel_ids: r.channel_ids ?? [],
      enabled: r.enabled,
      severity: r.severity || 'warning',
      cooldown_seconds: r.cooldown_seconds || 300,
      muted_until: r.muted_until ?? '',
    };
  }

  async function toggleEnabled(r: AlertRule) {
    try {
      await api.alerts.updateRule(r.id, { ...ruleToInput(r), enabled: !r.enabled });
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function duplicate(r: AlertRule) {
    try {
      const input = { ...ruleToInput(r), name: r.name + ' (copy)', enabled: false };
      await api.alerts.createRule(input);
      toast.success('Duplicated', input.name);
      await onReload();
    } catch (err) {
      toast.error('Duplicate failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function mute(r: AlertRule, hours: number) {
    try {
      const until = new Date(Date.now() + hours * 3600000).toISOString();
      await api.alerts.updateRule(r.id, { ...ruleToInput(r), muted_until: until });
      toast.success(`Muted for ${hours}h`, r.name);
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function unmute(r: AlertRule) {
    try {
      await api.alerts.updateRule(r.id, { ...ruleToInput(r), muted_until: '' });
      toast.success('Unmuted', r.name);
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteRule(r: AlertRule) {
    if (!(await confirm.ask({
      title: 'Delete alert rule',
      message: `Delete rule "${r.name}"?`,
      body: 'Cannot be undone. Already-fired notifications stay in history.',
      confirmLabel: 'Delete', danger: true,
    }))) return;
    try {
      await api.alerts.deleteRule(r.id);
      toast.success('Deleted', r.name);
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<div class="rt">
  <div class="rt-toolbar">
    <span class="rt-meta">
      {rules.length} rule{rules.length === 1 ? '' : 's'}
    </span>
    <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={onCreate}>
      <Plus size={12} strokeWidth={1.5} /> New rule
    </button>
  </div>

  {#if loading && rules.length === 0}
    <Skeleton width="100%" height="6rem" />
  {:else if rules.length === 0}
    <div class="rt-empty">
      <BellRing size={20} strokeWidth={1.4} />
      <p class="rt-empty-title">No alert rules yet</p>
      <p class="ed-subtitle rt-empty-blurb">
        Create a rule to monitor container metrics and notify a channel when thresholds are breached.
      </p>
      <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={onCreate}>
        <Plus size={12} strokeWidth={1.5} /> First rule
      </button>
    </div>
  {:else}
    <div class="rt-list">
      {#each sortedRules as r (r.id)}
        {@const muted = isMuted(r)}
        {@const firing = !!r.firing_since}
        <article
          class="rt-card"
          data-severity={r.severity || 'warning'}
          class:rt-card-firing={firing}
          class:rt-card-disabled={!r.enabled}
        >
          <div class="rt-card-main">
            <div class="rt-card-head">
              <span class="rt-sev-badge" data-sev={r.severity || 'warning'}>
                {r.severity || 'warning'}
              </span>
              <span class="rt-name">{r.name}</span>
              {#if firing}
                <span class="rt-pill rt-pill-fire">
                  <span class="rt-pill-dot"></span>firing
                </span>
              {/if}
              {#if !r.enabled}
                <span class="rt-pill rt-pill-neutral">
                  <span class="rt-pill-dot"></span>paused
                </span>
              {/if}
              {#if muted}
                <span class="rt-pill rt-pill-neutral">
                  <span class="rt-pill-dot"></span>muted
                </span>
              {/if}
              {#if r.builtin}
                <span class="rt-pill rt-pill-neutral">
                  <span class="rt-pill-dot"></span>built-in
                </span>
              {/if}
            </div>

            <div class="rt-card-condition">
              <span class="rt-cond-word">fire when</span>
              <span class="rt-cond-chip">{metricLabel(r.metric)}</span>
              <span class="rt-cond-op">{opLabel(r.operator)}</span>
              <span class="rt-cond-chip">{r.threshold}%</span>
              {#if r.duration_seconds > 0}
                <span class="rt-cond-word">for</span>
                <span class="rt-cond-chip">{fmtDuration(r.duration_seconds)}</span>
              {/if}
            </div>

            <div class="rt-card-meta">
              <span class="rt-meta-item">filter: <em>{r.container_filter}</em></span>
              {#if r.cooldown_seconds}
                <span class="rt-meta-item">cooldown: {fmtDuration(r.cooldown_seconds)}</span>
              {/if}
              <span class="rt-meta-item">last fired: {fmtRelTime(r.last_triggered_at)}</span>
            </div>
          </div>

          <div class="rt-card-channels">
            {#if (r.channel_ids ?? []).length === 0}
              <span class="rt-no-channels">no channels</span>
            {:else}
              {#each (r.channel_ids ?? []).slice(0, 3) as cid}
                <span class="rt-channel-pill">
                  <span class="rt-channel-type">{channelType(cid)}</span>
                  <span class="rt-channel-name">{channelName(cid)}</span>
                </span>
              {/each}
              {#if (r.channel_ids ?? []).length > 3}
                <span class="rt-channel-more">+{(r.channel_ids ?? []).length - 3}</span>
              {/if}
            {/if}
          </div>

          <div class="rt-card-actions">
            <button
              type="button"
              class="rt-icon-btn"
              onclick={() => toggleEnabled(r)}
              title={r.enabled ? 'Pause' : 'Resume'}
              aria-label={r.enabled ? 'Pause' : 'Resume'}
            >
              {#if r.enabled}
                <PowerOff size={13} strokeWidth={1.7} />
              {:else}
                <Power size={13} strokeWidth={1.7} />
              {/if}
            </button>
            {#if muted}
              <button
                type="button"
                class="rt-icon-btn rt-icon-warn"
                onclick={() => unmute(r)}
                title="Unmute"
                aria-label="Unmute"
              >
                <BellOn size={13} strokeWidth={1.7} />
              </button>
            {:else}
              <button
                type="button"
                class="rt-icon-btn"
                onclick={() => mute(r, 1)}
                title="Mute 1h"
                aria-label="Mute 1h"
              >
                <BellOff size={13} strokeWidth={1.7} />
              </button>
            {/if}
            <button
              type="button"
              class="rt-icon-btn"
              onclick={() => duplicate(r)}
              title="Duplicate"
              aria-label="Duplicate"
            >
              <Copy size={13} strokeWidth={1.7} />
            </button>
            <button
              type="button"
              class="rt-icon-btn"
              onclick={() => onEdit(r)}
              title="Edit"
              aria-label="Edit"
            >
              <Edit2 size={13} strokeWidth={1.7} />
            </button>
            {#if !r.builtin}
              <button
                type="button"
                class="rt-icon-btn rt-icon-danger"
                onclick={() => deleteRule(r)}
                title="Delete"
                aria-label="Delete"
              >
                <Trash2 size={13} strokeWidth={1.7} />
              </button>
            {/if}
          </div>
        </article>
      {/each}
    </div>
  {/if}
</div>

<style>
  .rt {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .rt-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }
  .rt-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }

  .rt-empty {
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
  .rt-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .rt-empty-blurb { margin: 4px 0 12px; max-width: 50ch; }

  .rt-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  /* Cards */
  .rt-card {
    padding: 14px 16px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-left-width: 3px;
    border-radius: 5px;
    display: grid;
    grid-template-columns: minmax(0, 1.6fr) minmax(0, 1.2fr) auto;
    gap: 16px;
    align-items: center;
  }
  .rt-card[data-severity="critical"] {
    border-left-color: var(--color-danger-500);
  }
  .rt-card[data-severity="warning"] {
    border-left-color: var(--color-warning-500);
  }
  .rt-card[data-severity="info"] {
    border-left-color: var(--accent);
  }
  .rt-card-firing {
    background: color-mix(in srgb, var(--color-danger-500) 5%, var(--surface));
    border-color: color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    border-left-color: var(--color-danger-500);
  }
  .rt-card-disabled { opacity: 0.6; }

  .rt-card-main { min-width: 0; display: flex; flex-direction: column; gap: 6px; }

  .rt-card-head {
    display: flex;
    align-items: center;
    gap: 7px;
    flex-wrap: wrap;
  }
  .rt-sev-badge {
    font-family: var(--font-mono);
    font-size: 9.5px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    padding: 1px 6px;
    border-radius: 999px;
    border: 1px solid var(--border-subtle);
  }
  .rt-sev-badge[data-sev="critical"] {
    color: var(--color-danger-400);
    border-color: color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
  }
  .rt-sev-badge[data-sev="warning"] {
    color: var(--color-warning-400);
    border-color: color-mix(in srgb, var(--color-warning-500) 45%, var(--border));
  }
  .rt-sev-badge[data-sev="info"] {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
  }
  .rt-name {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }

  .rt-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 6px;
    border-radius: 999px;
    border: 1px solid var(--border-subtle);
    font-family: var(--font-mono);
    font-size: 9.5px;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
  }
  .rt-pill-dot {
    width: 4px;
    height: 4px;
    border-radius: 999px;
    background: currentColor;
  }
  .rt-pill-fire {
    color: var(--color-danger-400);
    border-color: color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
  }
  .rt-pill-fire .rt-pill-dot { animation: rt-blink 1.2s infinite; }
  @keyframes rt-blink {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.3; }
  }

  /* Condition sentence */
  .rt-card-condition {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg);
  }
  .rt-cond-word { color: var(--fg-subtle); }
  .rt-cond-chip {
    padding: 1px 6px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg);
  }
  .rt-cond-op { color: var(--fg-subtle); font-weight: 500; }

  .rt-card-meta {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .rt-meta-item em {
    color: var(--fg-muted);
    font-style: normal;
  }

  /* Channels column */
  .rt-card-channels {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    align-self: center;
    min-width: 0;
  }
  .rt-no-channels {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .rt-channel-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 6px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    font-size: 10.5px;
    color: var(--fg-muted);
  }
  .rt-channel-type {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .rt-channel-name {
    font-family: var(--font-mono);
    font-size: 10.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 110px;
  }
  .rt-channel-more {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
  }

  /* Actions */
  .rt-card-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    align-self: center;
  }
  .rt-icon-btn {
    width: 28px;
    height: 28px;
    background: transparent;
    border: 1px solid transparent;
    border-radius: 4px;
    color: var(--fg-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition: color 120ms, background 120ms, border-color 120ms;
  }
  .rt-icon-btn:hover {
    color: var(--fg);
    background: var(--surface-hover);
    border-color: var(--border);
  }
  .rt-icon-warn {
    color: var(--color-warning-400);
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 25%, transparent);
  }
  .rt-icon-warn:hover {
    color: var(--color-warning-400);
    background: color-mix(in srgb, var(--color-warning-500) 16%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, transparent);
  }
  .rt-icon-danger { color: var(--color-danger-400); }
  .rt-icon-danger:hover {
    color: var(--color-danger-400);
    background: color-mix(in srgb, var(--color-danger-500) 12%, transparent);
    border-color: color-mix(in srgb, var(--color-danger-500) 35%, transparent);
  }

  @media (max-width: 880px) {
    .rt-card {
      grid-template-columns: 1fr;
      gap: 10px;
    }
  }
</style>
