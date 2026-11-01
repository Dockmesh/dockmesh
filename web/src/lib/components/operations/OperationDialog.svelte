<script lang="ts">
  import { tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { Modal, Button, StatusChip } from '$lib/components/ui';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import OperationSteps from './OperationSteps.svelte';
  import CancelOpButton from './CancelOpButton.svelte';
  import { api, type Operation } from '$lib/api';
  import { operations, kindLabel, isActive, opRef, failedStep, elapsed } from '$lib/stores/operations.svelte';
  import { shortMessage } from '$lib/operationsProgress';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';

  // Steps and live log of one operation. Mounted once in the layout and
  // opened from anywhere through the operations store; closing it leaves
  // the work running, the top-bar indicator opens it again.
  const op = $derived(operations.get(operations.dialogId));
  const log = $derived(op ? operations.logOf(op.id) : []);
  let open = $state(false);
  $effect(() => {
    open = !!operations.dialogId;
  });

  let follow = $state(true);
  let logEl = $state<HTMLElement | null>(null);
  $effect(() => {
    void log.length;
    if (!follow || !logEl) return;
    tick().then(() => logEl && (logEl.scrollTop = logEl.scrollHeight));
  });
  function onScroll() {
    if (!logEl) return;
    follow = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 24;
  }

  const statusTone: Record<string, Tone> = { queued: 'off', running: 'acc', succeeded: 'ok', failed: 'bad', cancelled: 'off' };
  const failed = $derived(op ? failedStep(op) : undefined);
  const stoppedAt = $derived(op?.status === 'cancelled' ? op.steps.find((s) => s.status === 'cancelled') : undefined);
  const reason = $derived(op?.status === 'failed' ? shortMessage(op.error ?? failed?.detail, 160) : '');

  function when(iso?: string) {
    return iso ? new Date(iso).toLocaleString() : '—';
  }

  const refId = (o: Operation, prefix: string) => (o.ref?.startsWith(prefix) ? o.ref.slice(prefix.length) : '');

  // Where the operation's object lives in the app.
  const objectHref = $derived.by(() => {
    if (!op) return '';
    if (op.kind === 'image.update') return `/containers/${refId(op, 'container:')}`;
    if (op.kind === 'backup.run' || op.kind === 'backup.restore') return `/backups/runs/${refId(op, 'backup-run:')}`;
    if (op.kind === 'proxy.site') return `/proxy/${encodeURIComponent(refId(op, 'proxy-site:'))}`;
    return '';
  });

  // Retry repeats the original action through its own endpoint, so it
  // needs the same permission as starting it.
  const canRetry = $derived.by(() => {
    if (!op || isActive(op) || op.status === 'succeeded') return false;
    if (op.kind === 'image.update') return allowed('containers.upgrade');
    if (op.kind === 'backup.run') return allowed('backups.run');
    return false;
  });
  let busy = $state(false);
  async function retry() {
    if (!op) return;
    busy = true;
    const since = Date.now();
    try {
      if (op.kind === 'image.update') {
        const id = refId(op, 'container:');
        const found = operations.find(opRef.container(id), 'image.update', since);
        void api.containers.doUpdate(id).catch((e) => toast.error('Update failed', (e as Error).message));
        const next = await found;
        if (next) await operations.open(next.id);
      } else if (op.kind === 'backup.run') {
        const run = await api.backups.getRun(Number(refId(op, 'backup-run:')));
        const next = await (await operations.startBackup(run.job_id)).op;
        if (next) await operations.open(next.id);
      }
    } catch (e) {
      toast.error('Could not start it again', (e as Error).message);
    } finally {
      busy = false;
    }
  }
  function close() {
    operations.close();
  }
</script>

<Modal bind:open title={op ? `${kindLabel(op.kind)} · ${op.target}` : 'Operation'} maxWidth="max-w-2xl" onclose={close}>
  {#if op}
    <div class="head">
      <StatusChip tone={statusTone[op.status] ?? 'off'} label={op.status} />
      <span class="dm-sub">
        {op.actor_name ? `by ${op.actor_name}` : op.actor?.startsWith('api-token:') ? 'by an API token' : op.actor ? '' : 'scheduled'}
        {op.started_at ? ` · started ${when(op.started_at)}` : ` · queued ${when(op.created_at)}`}
        {elapsed(op.started_at, op.finished_at) ? ` · ${elapsed(op.started_at, op.finished_at)}` : ''}
      </span>
    </div>

    {#if op.status === 'failed'}
      <div class="fail" role="alert">
        <b>{failed ? `Failed at “${failed.label}”` : 'Failed'}</b>
        {#if reason}<p title={op.error ?? ''}>{reason}</p>{/if}
      </div>
    {:else if op.status === 'cancelled'}
      <div class="stopped">
        <b>{stoppedAt ? `Cancelled during “${stoppedAt.label}”` : 'Cancelled before it started'}</b>
      </div>
    {:else if op.cancel_requested}
      <div class="stopped" role="status"><b>Cancelling…</b> the current step is stopping.</div>
    {/if}

    <section>
      <h3 class="dm-h2">Steps</h3>
      <OperationSteps {op} />
    </section>

    <section class="logsec">
      <h3 class="dm-h2">Log <span class="dm-sub">{log.length ? `${log.length} lines` : ''}{isActive(op) ? ' · closing this keeps it running' : ''}</span></h3>
      <div class="log dm-mono" bind:this={logEl} onscroll={onScroll} aria-live="off">
        {#each log as l, i (i)}
          <div class="ln {l.level}"><span class="t">{new Date(l.ts).toLocaleTimeString()}</span><span class="x">{l.text}</span></div>
        {:else}
          <div class="dm-dim">{isActive(op) ? 'Waiting for output…' : 'No log lines.'}</div>
        {/each}
      </div>
    </section>
  {:else}
    <div class="dm-empty">Loading…</div>
  {/if}

  {#snippet footer()}
    {#if op}
      {#if objectHref}<Button variant="ghost" onclick={() => { const h = objectHref; close(); goto(h); }}>Open {op.kind === 'proxy.site' ? 'reverse proxy' : op.kind === 'image.update' ? 'container' : 'run'}</Button>{/if}
      <span class="dm-sp"></span>
      <CancelOpButton {op} size="md" />
      {#if canRetry}<Button variant="primary" loading={busy} onclick={retry}>Retry</Button>{/if}
      <Button variant="secondary" onclick={close}>Close</Button>
    {/if}
  {/snippet}
</Modal>

<style>
  .head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 14px; }
  section { margin-top: 16px; }
  .fail { border: 1px solid color-mix(in srgb, var(--bad) 45%, transparent); background: color-mix(in srgb, var(--bad) 8%, transparent); border-radius: var(--r); padding: 10px 12px; }
  .fail b { color: var(--bad); }
  .fail p { margin: 4px 0 0; font-size: var(--fs-sm); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .stopped { border: 1px solid var(--border); background: var(--field); border-radius: var(--r); padding: 10px 12px; font-size: var(--fs-sm); }
  .logsec { display: flex; flex-direction: column; }
  .log { height: 220px; overflow: auto; background: var(--code, var(--field)); border-radius: var(--r-sm); padding: 8px 10px; font-size: 11.5px; line-height: 1.55; }
  .ln { display: grid; grid-template-columns: 70px minmax(0, 1fr); gap: 8px; }
  .ln .t { color: var(--subtle); }
  .ln .x { white-space: pre-wrap; overflow-wrap: anywhere; }
  .ln.warn .x { color: var(--warn); }
  .ln.error .x { color: var(--bad); }
</style>
