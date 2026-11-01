<script lang="ts">
  import { api, ApiError, type AlertRule, type NotificationChannel } from '$lib/api';
  import { appliesTo, kindLabel, kindOf, ruleSentence, ruleToInput, sevTone } from './ruleKinds';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { EmptyState, Skeleton, StatusChip, Switch } from '$lib/components/ui';
  import Menu from '$lib/components/ui/Menu.svelte';
  import { BellRing, Plus } from 'lucide-svelte';

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
    if (!ts) return 'never';
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)} min ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)} h ago`;
    return `${Math.floor(secs / 86400)} d ago`;
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

  async function deleteRule(r: AlertRule) {
    if (
      !(await confirm.ask({
        title: 'Delete alert rule',
        message: `Delete rule "${r.name}"?`,
        body: 'Cannot be undone. Alerts it already raised stay in the history.',
        confirmLabel: 'Delete',
        danger: true
      }))
    )
      return;
    try {
      await api.alerts.deleteRule(r.id);
      toast.success('Deleted', r.name);
      await onReload();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
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
    title="No alert rules yet"
    description="A rule watches container metrics and events, hosts, disks, backups or certificates and notifies a channel."
  >
    {#snippet action()}
      <button type="button" class="dm-btn dm-btn-primary" onclick={onCreate}><Plus size={14} strokeWidth={2} /> New rule</button>
    {/snippet}
  </EmptyState>
{:else}
  <div class="dm-toolbar">
    <input type="search" class="dm-filter rt-search" placeholder="Filter rules…" aria-label="Filter rules" bind:value={filter} />
  </div>
  <div class="dm-tbl" role="table" aria-label="Alert rules">
    <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
      <span role="columnheader"></span>
      <span role="columnheader">Severity</span>
      <span role="columnheader">Rule</span>
      <span role="columnheader">Fires when</span>
      <span role="columnheader">Applies to</span>
      <span role="columnheader">Channels</span>
      <span role="columnheader">Last fired</span>
      <span role="columnheader">On</span>
      <span role="columnheader"></span>
    </div>
    {#each sortedRules as r (r.id)}
      {@const muted = isMuted(r)}
      {@const sentence = capital(ruleSentence(r, hostName))}
      {@const scope = appliesTo(r, hostName)}
      {@const chs = channelNames(r)}
      <div class="dm-tr" class:rt-off={!r.enabled} role="row" style="grid-template-columns: {COLS}">
        <span role="cell"><span class="dm-dot {dotTone(r)}" title={r.firing_since ? `Firing since ${new Date(r.firing_since).toLocaleString()}` : r.enabled ? 'Quiet' : 'Paused'}></span></span>
        <span role="cell"><StatusChip tone={sevTone(r.severity || 'warning')} label={r.severity || 'warning'} /></span>
        <span role="cell" class="dm-nm">
          <button type="button" class="rt-name dm-t" title={r.name} onclick={() => onEdit(r)}>{r.name}</button>
          <small>
            {r.builtin ? 'built-in' : 'custom'} · {kindLabel(kindOf(r))}{#if r.firing_since}<span class="rt-fire"> · firing</span>{/if}{#if muted} · muted until {new Date(r.muted_until!).toLocaleString()}{/if}
          </small>
        </span>
        <span role="cell" class="dm-dim" title={sentence}>{sentence}</span>
        <span role="cell" class="dm-dim" title={scope}>{scope}</span>
        <span role="cell" class="dm-num" class:dm-dim={!chs} title={chs || 'No channel: fires are only recorded in the history'}>
          {(r.channel_ids ?? []).length ? `${(r.channel_ids ?? []).length} · ${chs}` : 'none'}
        </span>
        <span role="cell" class="dm-num dm-dim" title={r.last_triggered_at ? new Date(r.last_triggered_at).toLocaleString() : ''}>{fmtRel(r.last_triggered_at)}</span>
        <span role="cell" class="dm-tr-menu">
          <Switch checked={r.enabled} label={r.enabled ? `Pause ${r.name}` : `Resume ${r.name}`} hideLabel onchange={(v) => update(r, { enabled: v })} />
        </span>
        <span role="cell" class="dm-tr-menu">
          <Menu
            label="Actions for {r.name}"
            items={[
              { label: 'Edit…', onclick: () => onEdit(r) },
              { label: 'Duplicate', onclick: () => duplicate(r) },
              muted
                ? { label: 'Unmute', onclick: () => update(r, { muted_until: '' }, 'Unmuted') }
                : { label: 'Mute for 1 hour', onclick: () => update(r, { muted_until: new Date(Date.now() + 3600000).toISOString() }, 'Muted for 1 h') },
              ...(r.builtin
                ? []
                : [{ separator: true }, { label: 'Delete…', danger: true, onclick: () => deleteRule(r) }])
            ]}
          />
        </span>
      </div>
    {:else}
      <div class="dm-empty">No rules match “{filter}”.</div>
    {/each}
  </div>
  <p class="dm-sub">Where alerts are sent is set up once under Channels. A paused rule is not evaluated; a muted rule is evaluated and recorded but notifies nobody.</p>
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
