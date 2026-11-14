<script lang="ts">
  import { onDestroy } from 'svelte';
  import { api, ApiError, type UpdateInstallState, type UpdateResult } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Modal, StatusChip } from '$lib/components/ui';
  import { m } from '$lib/paraglide/messages.js';

  interface Props {
    open: boolean;
    version: string;
    security: string[];
    // Set when the page opened on an install that was already running.
    resume?: UpdateInstallState | null;
    ondone?: (res: UpdateResult | null) => void;
  }

  let { open = $bindable(false), version, security, resume = null, ondone }: Props = $props();

  type Step = 'confirm' | 'downloading' | 'waiting_for_stacks' | 'applying' | 'restarting' | 'checking' | 'result';
  let step = $state<Step>('confirm');
  let result = $state<UpdateResult | null>(null);
  let error = $state('');
  let starting = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let resumed = false;
  // A result from before this install started belongs to an earlier one.
  let since = 0;

  const pollEvery = 2000;
  const busy = $derived(step !== 'confirm' && step !== 'result');

  $effect(() => {
    if (open && resume && !resumed) {
      resumed = true;
      track(resume);
      schedule();
    }
  });

  onDestroy(() => clearTimeout(timer));

  async function start() {
    since = 0;
    starting = true;
    error = '';
    try {
      track(await api.system.installUpdate(version));
      schedule();
    } catch (err) {
      error = err instanceof ApiError ? err.message : String(err);
    } finally {
      starting = false;
    }
  }

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(poll, pollEvery);
  }

  async function poll() {
    try {
      track(await api.system.updateInstallState());
    } catch {
      // The root updater stops the server to swap the binary; the
      // requests fail until a version is up again.
      step = 'restarting';
    }
    if (step !== 'result') schedule();
  }

  function track(st: UpdateInstallState) {
    if (st.started && !since) since = Date.parse(st.started);
    const last = st.last_result;
    if (last && last.to === version && !st.pending && Date.parse(last.finished) >= since) {
      finish(last, '');
      return;
    }
    switch (st.phase) {
      case 'downloading':
      case 'waiting_for_stacks':
      case 'applying':
        step = st.phase;
        return;
      case 'failed':
        if (st.version === version) {
          finish(null, st.error ?? '');
          return;
        }
    }
    if (st.pending) {
      step = 'checking';
    } else if (step === 'restarting') {
      // Up again without a result for this version: the request was never
      // taken, or the result went to an earlier process.
      step = 'checking';
    }
  }

  function finish(res: UpdateResult | null, err: string) {
    clearTimeout(timer);
    result = res;
    error = err;
    step = 'result';
    ondone?.(res);
  }

  // Closing while an install runs only hides the dialog; the poll goes on
  // so the page still learns the result.
  function close() {
    open = false;
    if (busy) return;
    clearTimeout(timer);
    step = 'confirm';
    result = null;
    error = '';
  }

  const stepLabel = $derived.by(() => {
    switch (step) {
      case 'downloading': return m.settings_update_step_downloading();
      case 'waiting_for_stacks': return m.settings_update_step_waiting();
      case 'applying': return m.settings_update_step_applying();
      case 'restarting': return m.settings_update_step_restarting();
      case 'checking': return m.settings_update_step_checking();
    }
    return '';
  });

  const steps: Step[] = ['downloading', 'waiting_for_stacks', 'applying', 'restarting', 'checking'];
  const stepIndex = $derived(steps.indexOf(step));

  const outcome = $derived.by<{ tone: 'ok' | 'warn' | 'bad'; title: string; body: string }>(() => {
    if (!result) return { tone: 'bad', title: m.settings_update_not_installed(), body: error };
    const to = result.to ?? version;
    switch (result.status) {
      case 'updated':
        return { tone: 'ok', title: m.settings_update_done({ version: to }), body: m.settings_update_done_body() };
      case 'rolled_back':
        return { tone: 'warn', title: m.settings_update_rolled_back({ version: to }), body: m.settings_update_rolled_back_body({ from: result.from, reason: result.reason ?? '' }) };
      case 'refused':
        return { tone: 'warn', title: m.settings_update_refused({ version: to }), body: result.reason ?? '' };
    }
    return { tone: 'bad', title: m.settings_update_failed({ version: to }), body: m.settings_update_failed_body({ reason: result.reason ?? '' }) };
  });
</script>

<Modal bind:open title={m.settings_update_dialog_title({ version })} maxWidth="max-w-lg" onclose={close} {footer}>
  <div class="ud" data-testid="update-dialog" data-step={step}>
    {#if step === 'confirm'}
      {#if security.length}
        <div class="ud-sec" data-testid="update-security">
          <StatusChip tone="bad" label={m.settings_security_release()} />
          <ul>
            {#each security as s (s)}<li>{s}</li>{/each}
          </ul>
        </div>
      {/if}
      <p>{m.settings_update_confirm_body()}</p>
      <p class="ud-muted">{m.settings_update_confirm_window()}</p>
      {#if error}
        <span class="ud-err" role="alert" data-testid="update-error">{error}</span>
      {/if}
    {:else if step === 'result'}
      <div class="ud-res" data-testid="update-result" data-status={result?.status ?? 'error'}>
        <StatusChip tone={outcome.tone} label={outcome.title} />
        {#if outcome.body}<p>{outcome.body}</p>{/if}
        {#if result && allowed('audit.view')}
          <a href="/audit?category=system" data-testid="update-audit">{m.settings_update_audit_link()}</a>
        {/if}
      </div>
    {:else}
      <div class="ud-steps" data-testid="update-progress" aria-hidden="true">
        {#each steps as s, i (s)}
          <span class:done={i < stepIndex} class:cur={i === stepIndex}></span>
        {/each}
      </div>
      <p class="ud-cur" role="status">{stepLabel}</p>
      <p class="ud-muted">{m.settings_update_keep_open()}</p>
    {/if}
  </div>
</Modal>

{#snippet footer()}
  {#if step === 'confirm'}
    <button type="button" class="dm-btn" onclick={close}>{m.common_cancel()}</button>
    <button type="button" class="dm-btn dm-btn-primary" data-testid="update-start" onclick={start} disabled={starting}>
      {m.settings_update_now()}
    </button>
  {:else if step === 'result'}
    {#if result?.status === 'updated'}
      <button type="button" class="dm-btn dm-btn-primary" onclick={() => location.reload()}>{m.settings_update_reload()}</button>
    {/if}
    <button type="button" class="dm-btn" data-testid="update-close" onclick={close}>{m.common_close()}</button>
  {/if}
{/snippet}

<style>
  .ud { display: flex; flex-direction: column; gap: 10px; }
  .ud p { margin: 0; }
  .ud-muted { color: var(--muted); font-size: var(--fs-sm); }
  .ud-err { color: var(--bad); font-size: var(--fs-sm); }
  .ud-sec {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 10px;
    border: 1px solid var(--bad);
    border-radius: var(--r);
  }
  .ud-sec ul { margin: 0; padding-left: 18px; font-size: var(--fs-sm); }
  .ud-res { display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
  .ud-steps { display: flex; gap: 4px; }
  .ud-steps span { flex: 1; height: 4px; border-radius: 2px; background: var(--border); }
  .ud-steps span.done { background: var(--ok); }
  .ud-steps span.cur { background: var(--accent); }
  .ud-cur { font-weight: 600; }
</style>
