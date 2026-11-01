<script lang="ts">
  import { api, ApiError, type AlertRule, type NotificationChannel } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { EmptyState, Skeleton, Switch } from '$lib/components/ui';
  import Menu from '$lib/components/ui/Menu.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { Bell, Plus, Send } from 'lucide-svelte';

  const COLS = '14px minmax(170px, 1.1fr) minmax(220px, 1.6fr) 90px minmax(160px, 1fr) 84px 44px 36px';

  interface Props {
    channels: NotificationChannel[];
    rules: AlertRule[];
    loading: boolean;
    onEdit: (c: NotificationChannel) => void;
    onCreate: () => void;
    onReload: () => Promise<void>;
  }

  let { channels, rules, loading, onEdit, onCreate, onReload }: Props = $props();
  const canCreate = $derived(allowed('alerts.create'));
  const canUpdate = $derived(allowed('alerts.update'));
  const canDelete = $derived(allowed('alerts.delete'));

  let testing = $state<Record<number, 'running' | 'ok' | 'fail'>>({});

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

  function deliveryFailing(c: NotificationChannel): boolean {
    if (!c.last_error_at) return false;
    return !c.last_sent_at || new Date(c.last_error_at) > new Date(c.last_sent_at);
  }

  async function toggleEnabled(c: NotificationChannel) {
    try {
      await api.alerts.updateChannel(c.id, {
        type: c.type,
        name: c.name,
        config: c.config,
        enabled: !c.enabled,
        mute_schedule: c.mute_schedule ?? '',
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

  function lastDelivery(c: NotificationChannel): string {
    if (deliveryFailing(c)) return `failed ${new Date(c.last_error_at!).toLocaleString()}`;
    if (c.last_sent_at) return `sent ${new Date(c.last_sent_at).toLocaleString()}`;
    return 'nothing sent yet';
  }
</script>

{#if loading && channels.length === 0}
  <div class="dm-tbl">
    {#each Array(3) as _, i (i)}
      <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div>
    {/each}
  </div>
{:else if channels.length === 0}
  <EmptyState
    icon={Bell}
    title="No notification channels"
    description="Add a webhook, ntfy, Discord, Slack, Teams, Gotify, email, PagerDuty or Pushover destination."
  >
    {#snippet action()}
      {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={onCreate}><Plus size={14} strokeWidth={2} /> New channel</button>{/if}
    {/snippet}
  </EmptyState>
{:else}
  <div class="dm-tbl" role="table" aria-label="Notification channels">
    <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
      <span role="columnheader"></span>
      <span role="columnheader">Channel</span>
      <span role="columnheader">Sends to</span>
      <span role="columnheader">Used by</span>
      <span role="columnheader">Last delivery</span>
      <span role="columnheader"></span>
      <span role="columnheader">On</span>
      <span role="columnheader"></span>
    </div>
    {#each channels as c (c.id)}
      {@const using = rulesUsing(c.id)}
      {@const tState = testing[c.id]}
      {@const failing = deliveryFailing(c)}
      <div class="dm-tr" role="row" style="grid-template-columns: {COLS}">
        <span role="cell"><span class="dm-dot {!c.enabled ? '' : failing ? 'bad' : 'ok'}"></span></span>
        <span role="cell" class="dm-nm">
          {#if canUpdate}<button type="button" class="ct-name dm-t" title={c.name} onclick={() => onEdit(c)}>{c.name}</button>{:else}<span class="dm-t" title={c.name}>{c.name}</span>{/if}
          <small>{c.type}{c.enabled ? '' : ' · disabled'}</small>
        </span>
        <span role="cell" class="dm-mono dm-dim ct-dest" title={configSummary(c)}>{configSummary(c)}</span>
        <span role="cell" class="dm-num" class:dm-dim={using === 0}>{using > 0 ? `${using} rule${using === 1 ? '' : 's'}` : 'unused'}</span>
        <span role="cell" class="dm-num" class:bad={failing} class:dm-dim={!failing} title={failing ? c.last_error : undefined}>{lastDelivery(c)}</span>
        <span role="cell" class="dm-tr-menu">
          <button
            type="button"
            class="dm-btn dm-btn-sm dm-btn-ghost"
            onclick={() => testFire(c)}
            disabled={!canUpdate || !c.enabled || tState === 'running'}
            title="Send a test notification"
          >
            {#if tState === 'running'}Sending…{:else if tState === 'ok'}Sent{:else if tState === 'fail'}Failed{:else}<Send size={12} strokeWidth={1.8} /> Test{/if}
          </button>
        </span>
        <span role="cell" class="dm-tr-menu">
          <Switch checked={c.enabled} disabled={!canUpdate} label={c.enabled ? `Disable ${c.name}` : `Enable ${c.name}`} hideLabel onchange={() => toggleEnabled(c)} />
        </span>
        <span role="cell" class="dm-tr-menu">
          {#if canUpdate || canDelete}
            <Menu
              label="Actions for {c.name}"
              items={[
                ...(canUpdate ? [{ label: 'Edit…', onclick: () => onEdit(c) }] : []),
                ...(canUpdate && canDelete ? [{ separator: true }] : []),
                ...(canDelete ? [{ label: 'Delete…', danger: true, onclick: () => deleteChannel(c) }] : [])
              ]}
            />
          {/if}
        </span>
      </div>
    {/each}
  </div>
  {#each channels.filter(deliveryFailing) as c (c.id)}
    <p class="ct-err" role="status"><b>{c.name}</b>: last delivery failed — {c.last_error}</p>
  {/each}
{/if}

<style>
  .ct-name {
    all: unset;
    cursor: pointer;
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ct-name:hover { text-decoration: underline; }
  .ct-name:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .ct-dest { font-size: 12px; }
  .ct-err { margin: 0; font-size: var(--fs-sm); color: var(--bad); overflow-wrap: anywhere; }
</style>
