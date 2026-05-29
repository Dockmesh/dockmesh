<script lang="ts">
  // Channels tab — notification channels as Editorial card grid with
  // type icons, test-fire button, used-by-rule count, enable toggle,
  // edit/delete actions.
  import { api, ApiError, type AlertRule, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import {
    Bell, Plus, Trash2, Edit2, Send, Webhook, Mail, MessageSquare,
  } from 'lucide-svelte';

  interface Props {
    channels: NotificationChannel[];
    rules: AlertRule[];
    loading: boolean;
    onEdit: (c: NotificationChannel) => void;
    onCreate: () => void;
    onReload: () => Promise<void>;
  }

  let { channels, rules, loading, onEdit, onCreate, onReload }: Props = $props();

  let testing = $state<Record<number, 'running' | 'ok' | 'fail'>>({});

  const TYPE_ICON: Record<string, string> = {
    webhook:   '🔗',
    ntfy:      '🔔',
    discord:   '🟣',
    slack:     '🟢',
    teams:     '🟦',
    gotify:    '📡',
    email:     '📧',
    pagerduty: '🟠',
    pushover:  '🟡',
  };

  function rulesUsing(id: number): number {
    return rules.filter((r) => r.channel_ids?.includes(id)).length;
  }

  function configSummary(c: NotificationChannel): string {
    const cfg: any = c.config ?? {};
    if (cfg.url) return cfg.url;
    if (cfg.host) return `${cfg.host}:${cfg.port ?? 587}`;
    if (cfg.integration_key) return 'PagerDuty integration';
    if (cfg.user_key) return `Pushover user: ${String(cfg.user_key).slice(0, 8)}…`;
    return c.type;
  }

  async function toggleEnabled(c: NotificationChannel) {
    try {
      await api.alerts.updateChannel(c.id, {
        type: c.type,
        name: c.name,
        config: c.config,
        enabled: !c.enabled,
      });
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function testFire(c: NotificationChannel) {
    testing = { ...testing, [c.id]: 'running' };
    try {
      await api.alerts.testChannel(c.id);
      testing = { ...testing, [c.id]: 'ok' };
      toast.success('Test sent', c.name);
    } catch (err) {
      testing = { ...testing, [c.id]: 'fail' };
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      setTimeout(() => {
        testing = { ...testing, [c.id]: undefined as any };
      }, 2000);
    }
  }

  async function deleteChannel(c: NotificationChannel) {
    const inUse = rulesUsing(c.id);
    if (!(await confirm.ask({
      title: 'Delete notification channel',
      message: `Delete channel "${c.name}"?`,
      body: inUse > 0
        ? `${inUse} rule${inUse === 1 ? '' : 's'} route${inUse === 1 ? 's' : ''} to this channel and will stop firing until reassigned.`
        : 'Cannot be undone.',
      confirmLabel: 'Delete', danger: true,
    }))) return;
    try {
      await api.alerts.deleteChannel(c.id);
      toast.success('Deleted', c.name);
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<div class="ct">
  <div class="ct-toolbar">
    <span class="ct-meta">
      {channels.length} channel{channels.length === 1 ? '' : 's'}
    </span>
    <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={onCreate}>
      <Plus size={12} strokeWidth={1.5} /> New channel
    </button>
  </div>

  {#if loading && channels.length === 0}
    <Skeleton width="100%" height="6rem" />
  {:else if channels.length === 0}
    <div class="ct-empty">
      <Bell size={20} strokeWidth={1.4} />
      <p class="ct-empty-title">No notification channels</p>
      <p class="ed-subtitle ct-empty-blurb">
        Add a webhook, ntfy, Discord, Slack, Teams, Gotify, email, PagerDuty or Pushover destination.
      </p>
      <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={onCreate}>
        <Plus size={12} strokeWidth={1.5} /> First channel
      </button>
    </div>
  {:else}
    <div class="ct-grid">
      {#each channels as c (c.id)}
        {@const using = rulesUsing(c.id)}
        {@const tState = testing[c.id]}
        <article class="ct-card" class:ct-card-disabled={!c.enabled}>
          <div class="ct-card-head">
            <span class="ct-icon">{TYPE_ICON[c.type] ?? '🔔'}</span>
            <div class="ct-card-text">
              <div class="ct-card-name-line">
                <span class="ct-card-name">{c.name}</span>
                {#if !c.enabled}
                  <span class="ct-pill ct-pill-neutral">disabled</span>
                {/if}
              </div>
              <span class="ct-card-type">{c.type}</span>
            </div>
          </div>

          <div class="ct-card-target" title={configSummary(c)}>
            {configSummary(c)}
          </div>

          <div class="ct-card-foot">
            <span class="ct-card-using">
              {using > 0 ? `${using} rule${using === 1 ? '' : 's'}` : 'unused'}
            </span>
            <div class="ct-card-actions">
              <button
                type="button"
                class="dm-btn dm-btn-ghost dm-btn-xs"
                onclick={() => testFire(c)}
                disabled={!c.enabled || tState === 'running'}
                title="Send test notification"
              >
                {#if tState === 'running'}
                  …
                {:else if tState === 'ok'}
                  <Send size={11} strokeWidth={1.8} /> sent
                {:else if tState === 'fail'}
                  failed
                {:else}
                  <Send size={11} strokeWidth={1.5} /> Test
                {/if}
              </button>
              <label class="ct-toggle">
                <input type="checkbox" checked={c.enabled} onchange={() => toggleEnabled(c)} />
                <span class="ct-toggle-track">
                  <span class="ct-toggle-knob"></span>
                </span>
              </label>
              <button
                type="button"
                class="ct-icon-btn"
                onclick={() => onEdit(c)}
                title="Edit"
                aria-label="Edit"
              >
                <Edit2 size={13} strokeWidth={1.7} />
              </button>
              <button
                type="button"
                class="ct-icon-btn ct-icon-danger"
                onclick={() => deleteChannel(c)}
                title="Delete"
                aria-label="Delete"
              >
                <Trash2 size={13} strokeWidth={1.7} />
              </button>
            </div>
          </div>
        </article>
      {/each}
    </div>
  {/if}
</div>

<style>
  .ct {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .ct-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }
  .ct-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }

  .ct-empty {
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
  .ct-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .ct-empty-blurb { margin: 4px 0 12px; max-width: 50ch; }

  .ct-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: 10px;
  }
  .ct-card {
    padding: 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .ct-card-disabled { opacity: 0.55; }

  .ct-card-head {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .ct-icon {
    width: 32px;
    height: 32px;
    font-size: 16px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    flex-shrink: 0;
  }
  .ct-card-text { min-width: 0; }
  .ct-card-name-line {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .ct-card-name {
    font-size: 13px;
    font-weight: 500;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ct-card-type {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .ct-pill {
    padding: 1px 6px;
    border: 1px solid var(--border-subtle);
    border-radius: 999px;
    font-family: var(--font-mono);
    font-size: 9.5px;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
  }
  .ct-pill-neutral { color: var(--fg-subtle); }

  .ct-card-target {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 6px 8px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
  }

  .ct-card-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding-top: 4px;
    border-top: 1px solid var(--border-subtle);
  }
  .ct-card-using {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .ct-card-actions {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .ct-icon-btn {
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
  .ct-icon-btn:hover {
    color: var(--fg);
    background: var(--surface-hover);
    border-color: var(--border);
  }
  .ct-icon-danger { color: var(--color-danger-400); }
  .ct-icon-danger:hover {
    color: var(--color-danger-400);
    background: color-mix(in srgb, var(--color-danger-500) 12%, transparent);
    border-color: color-mix(in srgb, var(--color-danger-500) 35%, transparent);
  }

  /* iOS-style toggle */
  .ct-toggle {
    position: relative;
    display: inline-flex;
    align-items: center;
    cursor: pointer;
  }
  .ct-toggle input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .ct-toggle-track {
    width: 28px;
    height: 16px;
    border-radius: 999px;
    background: var(--border-strong);
    position: relative;
    transition: background 150ms;
  }
  .ct-toggle-knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 12px;
    height: 12px;
    background: white;
    border-radius: 999px;
    transition: left 150ms;
  }
  .ct-toggle input:checked + .ct-toggle-track {
    background: var(--color-brand-500);
  }
  .ct-toggle input:checked + .ct-toggle-track .ct-toggle-knob {
    left: 14px;
  }
</style>
