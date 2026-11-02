<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { Modal, Button, StatusChip } from '$lib/components/ui';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import OperationSteps from './OperationSteps.svelte';
  import CancelOpButton from './CancelOpButton.svelte';
  import { api, type Operation } from '$lib/api';
  import { operations, kindLabel, isActive, opRef, failedStep, elapsed } from '$lib/stores/operations.svelte';
  import { errorHint, shortMessage } from '$lib/operationsProgress';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { fmtDateTime, fmtTime } from '$lib/i18n';

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

  const STATUS: Record<string, () => string> = {
    queued: m.ops_st_queued,
    running: m.ops_st_running,
    succeeded: m.ops_st_succeeded,
    failed: m.ops_st_failed,
    cancelled: m.ops_st_cancelled
  };
  const statusTone: Record<string, Tone> = { queued: 'off', running: 'acc', succeeded: 'ok', failed: 'bad', cancelled: 'off' };
  const failed = $derived(op ? failedStep(op) : undefined);
  const stoppedAt = $derived(op?.status === 'cancelled' ? op.steps.find((s) => s.status === 'cancelled') : undefined);
  const reason = $derived(op?.status === 'failed' ? shortMessage(op.error ?? failed?.detail, 300) : '');
  const hint = $derived(op?.status === 'failed' ? errorHint(op.error ?? failed?.detail) : '');

  function when(iso?: string) {
    return iso ? fmtDateTime(iso) : '—';
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
        void api.containers.doUpdate(id).catch((e) => toast.error(m.ops_update_failed(), (e as Error).message));
        const next = await found;
        if (next) await operations.open(next.id);
      } else if (op.kind === 'backup.run') {
        const run = await api.backups.getRun(Number(refId(op, 'backup-run:')));
        const next = await (await operations.startBackup(run.job_id)).op;
        if (next) await operations.open(next.id);
      }
    } catch (e) {
      toast.error(m.ops_retry_failed(), (e as Error).message);
    } finally {
      busy = false;
    }
  }
  function close() {
    operations.close();
  }
</script>

<Modal bind:open title={op ? `${kindLabel(op.kind)} · ${op.target}` : m.ops_dialog_title()} maxWidth="max-w-2xl" onclose={close}>
  {#if op}
    <div class="head">
      <StatusChip tone={statusTone[op.status] ?? 'off'} label={STATUS[op.status]?.() ?? op.status} />
      <span class="dm-sub">
        {op.actor_name ? m.ops_by({ name: op.actor_name }) : op.actor?.startsWith('api-token:') ? m.ops_by_token() : op.actor ? '' : m.ops_scheduled()}
        {op.started_at ? ` · ${m.ops_started_at({ when: when(op.started_at) })}` : ` · ${m.ops_queued_at({ when: when(op.created_at) })}`}
        {elapsed(op.started_at, op.finished_at) ? ` · ${elapsed(op.started_at, op.finished_at)}` : ''}
      </span>
    </div>

    {#if op.status === 'failed'}
      <div class="fail" role="alert">
        <b>{failed ? m.ops_failed_at({ step: failed.label }) : m.common_failed()}</b>
        {#if reason}<p title={op.error ?? ''}>{reason}</p>{/if}
        {#if hint}<p class="hint">{hint}</p>{/if}
      </div>
    {:else if op.status === 'cancelled'}
      <div class="stopped">
        <b>{stoppedAt ? m.ops_cancelled_during({ step: stoppedAt.label }) : m.ops_cancelled_before()}</b>
      </div>
    {:else if op.cancel_requested}
      <div class="stopped" role="status"><b>{m.ops_cancelling()}</b> {m.ops_cancelling_detail()}</div>
    {/if}

    <section>
      <h3 class="dm-h2">{m.ops_steps()}</h3>
      <OperationSteps {op} />
    </section>

    <section class="logsec">
      <h3 class="dm-h2">{m.ops_log()} <span class="dm-sub">{log.length ? m.ops_log_lines({ count: log.length }) : ''}{isActive(op) ? ` · ${m.ops_close_keeps_running()}` : ''}</span></h3>
      <div class="log dm-mono" bind:this={logEl} onscroll={onScroll} aria-live="off">
        {#each log as l, i (i)}
          <div class="ln {l.level}"><span class="t">{fmtTime(l.ts)}</span><span class="x">{l.text}</span></div>
        {:else}
          <div class="dm-dim">{isActive(op) ? m.ops_waiting_output() : m.ops_no_log()}</div>
        {/each}
      </div>
    </section>
  {:else}
    <div class="dm-empty">{m.common_loading()}</div>
  {/if}

  {#snippet footer()}
    {#if op}
      {#if objectHref}<Button variant="ghost" onclick={() => { const h = objectHref; close(); goto(h); }}>{op.kind === 'proxy.site' ? m.ops_open_proxy() : op.kind === 'image.update' ? m.ops_open_container() : m.ops_open_run()}</Button>{/if}
      <span class="dm-sp"></span>
      <CancelOpButton {op} size="md" />
      {#if canRetry}<Button variant="primary" loading={busy} onclick={retry}>{m.common_retry()}</Button>{/if}
      <Button variant="secondary" onclick={close}>{m.common_close()}</Button>
    {/if}
  {/snippet}
</Modal>

<style>
  .head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 14px; }
  section { margin-top: 16px; }
  .fail { border: 1px solid color-mix(in srgb, var(--bad) 45%, transparent); background: color-mix(in srgb, var(--bad) 8%, transparent); border-radius: var(--r); padding: 10px 12px; }
  .fail b { color: var(--bad); }
  .fail p { margin: 4px 0 0; font-size: var(--fs-sm); overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
  .fail p.hint { color: var(--fg); display: block; }
  .stopped { border: 1px solid var(--border); background: var(--field); border-radius: var(--r); padding: 10px 12px; font-size: var(--fs-sm); }
  .logsec { display: flex; flex-direction: column; }
  .log { height: 220px; overflow: auto; background: var(--code, var(--field)); border-radius: var(--r-sm); padding: 8px 10px; font-size: 11.5px; line-height: 1.55; }
  .ln { display: grid; grid-template-columns: 70px minmax(0, 1fr); gap: 8px; }
  .ln .t { color: var(--subtle); }
  .ln .x { white-space: pre-wrap; overflow-wrap: anywhere; }
  .ln.warn .x { color: var(--warn); }
  .ln.error .x { color: var(--bad); }
</style>
