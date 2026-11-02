<script lang="ts">
  // Restore the whole server from a system backup: stage it (from a run
  // or an uploaded file), show the checks, then restart to swap it in.
  // Every user, key and stack is replaced by the backup's.
  import { untrack } from 'svelte';
  import { api, ApiError, type BackupRun, type StageSystemRestoreResult } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { waitForRestart } from '$lib/restart';
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime } from '$lib/i18n';

  interface Props {
    open: boolean;
    // Restore this run; without it the dialog asks for a file.
    run?: BackupRun | null;
    onclose: () => void;
  }
  let { open, run = null, onclose }: Props = $props();

  let phase = $state<'pick' | 'staging' | 'staged' | 'restarting'>('pick');
  let result = $state<StageSystemRestoreResult | null>(null);
  let error = $state('');
  let file = $state<File | null>(null);
  let key = $state<File | null>(null);

  $effect(() => {
    if (!open) return;
    untrack(() => {
      phase = 'pick';
      result = null;
      error = '';
      file = null;
      key = null;
    });
  });

  async function stage() {
    phase = 'staging';
    error = '';
    try {
      result = run
        ? await api.backups.stageSystemRestoreFromRun(run.id)
        : await api.backups.stageSystemRestoreUpload(file!, key);
      if (result.sanity && !result.sanity.passed) {
        error = result.sanity.summary ?? m.backups_sysrestore_checks_failed();
        phase = 'pick';
        return;
      }
      phase = 'staged';
    } catch (err) {
      error = err instanceof ApiError ? err.message : String(err);
      phase = 'pick';
    }
  }

  async function apply() {
    phase = 'restarting';
    try {
      await api.backups.applySystemRestore();
    } catch (err) {
      toast.error(m.backups_restore_failed(), err instanceof ApiError ? err.message : undefined);
      phase = 'staged';
      return;
    }
    await waitForRestart();
    // Sessions belong to the replaced install.
    window.location.href = '/login';
  }

  async function discard() {
    try {
      await api.backups.discardSystemRestore();
    } catch {
      /* nothing staged any more */
    }
    onclose();
  }

  function onfile(e: Event, which: 'file' | 'key') {
    const f = (e.currentTarget as HTMLInputElement).files?.[0] ?? null;
    if (which === 'file') file = f;
    else key = f;
  }

  function checkStatus(s: string): string {
    if (s === 'ok') return m.backups_check_ok();
    if (s === 'warn') return m.backups_check_warn();
    if (s === 'fail') return m.backups_check_fail();
    return s;
  }
</script>

<Modal {open} title={m.backups_sysrestore_title()} maxWidth="max-w-lg" onclose={phase === 'staged' ? discard : onclose}>
  <div class="space-y-4 text-sm">
    <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)]">
      {m.backups_sysrestore_warning_before()} (<span class="font-mono">.pre-restore</span>).
    </div>

    {#if phase === 'pick' || phase === 'staging'}
      {#if run}
        <p>{m.backups_sysrestore_backup_label()} <span class="font-mono">{run.job_name}</span>{m.backups_sysrestore_run_suffix({ id: run.id })}{run.finished_at ? `, ${fmtDateTime(run.finished_at)}` : ''}.</p>
      {:else}
        <label class="block">
          <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.backups_sysrestore_file_label()}</span>
          <input type="file" accept=".gz,.age,.tar" onchange={(e) => onfile(e, 'file')} />
        </label>
        <label class="block">
          <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.backups_sysrestore_key_label()}</span>
          <input type="file" accept=".txt,.key" onchange={(e) => onfile(e, 'key')} />
        </label>
      {/if}
      {#if error}<p class="text-[var(--color-danger-400)]">{error}</p>{/if}
      {#if result?.sanity && !result.sanity.passed}
        <ul class="text-xs space-y-1">
          {#each result.sanity.checks as c (c.name)}
            <li class:text-[var(--color-danger-400)]={c.status === 'fail'}>{checkStatus(c.status)} · {c.name}{c.message ? ` — ${c.message}` : ''}</li>
          {/each}
        </ul>
      {/if}
    {:else if phase === 'staged' && result}
      <p>{m.backups_sysrestore_staged({ files: result.counts?.files ?? 0 })}</p>
      <ul class="text-xs space-y-1">
        {#each result.sanity?.checks ?? [] as c (c.name)}
          <li class:text-[var(--color-warning-400)]={c.status === 'warn'}>{checkStatus(c.status)} · {c.name}{c.message ? ` — ${c.message}` : ''}</li>
        {/each}
      </ul>
      <p>{m.backups_sysrestore_restart_hint()}</p>
    {:else if phase === 'restarting'}
      <p>{m.backups_sysrestore_restarting()}</p>
    {/if}
  </div>
  {#snippet footer()}
    {#if phase === 'staged'}
      <Button variant="secondary" onclick={discard}>{m.backups_discard()}</Button>
      <Button variant="danger" onclick={apply}>{m.backups_restart_and_restore()}</Button>
    {:else if phase !== 'restarting'}
      <Button variant="secondary" onclick={onclose}>{m.common_cancel()}</Button>
      <Button variant="danger" onclick={stage} disabled={phase === 'staging' || (!run && !file)}>
        {phase === 'staging' ? m.backups_checking() : m.backups_check_backup()}
      </Button>
    {/if}
  {/snippet}
</Modal>
