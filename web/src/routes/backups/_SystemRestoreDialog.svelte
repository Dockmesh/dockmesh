<script lang="ts">
  // Restore the whole server from a system backup: stage it (from a run
  // or an uploaded file), show the checks, then restart to swap it in.
  // Every user, key and stack is replaced by the backup's.
  import { untrack } from 'svelte';
  import { api, ApiError, type BackupRun, type StageSystemRestoreResult } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { waitForRestart } from '$lib/restart';

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
        error = result.sanity.summary ?? 'the backup failed the restore checks';
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
      toast.error('Restore failed', err instanceof ApiError ? err.message : undefined);
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
</script>

<Modal {open} title="Restore the whole system" maxWidth="max-w-lg" onclose={phase === 'staged' ? discard : onclose}>
  <div class="space-y-4 text-sm">
    <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)]">
      Replaces this server's database, users, keys, settings and stack files with the backup's,
      then restarts Dockmesh. Volumes and running containers are not touched. The replaced files
      are kept in the data directory under <span class="font-mono">.pre-restore</span>.
    </div>

    {#if phase === 'pick' || phase === 'staging'}
      {#if run}
        <p>Backup: <span class="font-mono">{run.job_name}</span>, run #{run.id}{run.finished_at ? `, ${new Date(run.finished_at).toLocaleString()}` : ''}.</p>
      {:else}
        <label class="block">
          <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">System backup file (.tar.gz or .tar.gz.age)</span>
          <input type="file" accept=".gz,.age,.tar" onchange={(e) => onfile(e, 'file')} />
        </label>
        <label class="block">
          <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Backup key file (only for encrypted backups of another install)</span>
          <input type="file" accept=".txt,.key" onchange={(e) => onfile(e, 'key')} />
        </label>
      {/if}
      {#if error}<p class="text-[var(--color-danger-400)]">{error}</p>{/if}
      {#if result?.sanity && !result.sanity.passed}
        <ul class="text-xs space-y-1">
          {#each result.sanity.checks as c (c.name)}
            <li class:text-[var(--color-danger-400)]={c.status === 'fail'}>{c.status} · {c.name}{c.message ? ` — ${c.message}` : ''}</li>
          {/each}
        </ul>
      {/if}
    {:else if phase === 'staged' && result}
      <p>The backup was unpacked and checked ({result.counts?.files ?? 0} files). Nothing has been replaced yet.</p>
      <ul class="text-xs space-y-1">
        {#each result.sanity?.checks ?? [] as c (c.name)}
          <li class:text-[var(--color-warning-400)]={c.status === 'warn'}>{c.status} · {c.name}{c.message ? ` — ${c.message}` : ''}</li>
        {/each}
      </ul>
      <p>Restart now to finish. You will sign in again with an account of the restored install.</p>
    {:else if phase === 'restarting'}
      <p>Restarting Dockmesh and swapping the restored files in… This page reloads when the server is back.</p>
    {/if}
  </div>
  {#snippet footer()}
    {#if phase === 'staged'}
      <Button variant="secondary" onclick={discard}>Discard</Button>
      <Button variant="danger" onclick={apply}>Restart and restore</Button>
    {:else if phase !== 'restarting'}
      <Button variant="secondary" onclick={onclose}>Cancel</Button>
      <Button variant="danger" onclick={stage} disabled={phase === 'staging' || (!run && !file)}>
        {phase === 'staging' ? 'Checking…' : 'Check backup'}
      </Button>
    {/if}
  {/snippet}
</Modal>
