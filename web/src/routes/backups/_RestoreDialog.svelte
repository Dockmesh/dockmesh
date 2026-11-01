<script lang="ts">
  // Restore a volume or stack run: destination name, host (defaults to
  // the host the backup was taken on) and replace vs. merge. The server
  // verifies the whole archive before it writes anything.
  import { untrack } from 'svelte';
  import { api, ApiError, type BackupRun, type HostInfo, type BackupRestoreReport } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { operations, opRef } from '$lib/stores/operations.svelte';
  import { Undo2, ShieldCheck } from 'lucide-svelte';

  interface Props {
    run: BackupRun | null;
    // 'alongside' suggests a copy (<name>-restored), 'in-place' the original.
    preset?: 'in-place' | 'alongside';
    // Host the backup was taken on; '' or undefined = central server.
    originHost?: string;
    onclose: () => void;
  }
  let { run, preset = 'alongside', originHost = '', onclose }: Props = $props();

  const isStack = $derived(!!run?.sources.some((s) => s.type === 'stack'));
  const sourceName = $derived(run?.sources[0]?.name ?? '');

  let target = $state('');
  let hostId = $state('local');
  let mode = $state<'replace' | 'merge'>('replace');
  let confirmText = $state('');
  let busy = $state(false);
  let hosts = $state<HostInfo[]>([]);

  $effect(() => {
    if (!run) return;
    untrack(() => {
      target = preset === 'in-place' ? sourceName : `${sourceName}-restored`;
      hostId = originHost || 'local';
      mode = 'replace';
      confirmText = '';
      api.hosts
        .list()
        .then((list) => (hosts = list.filter((h) => h.kind !== 'all' && h.status === 'online')))
        .catch(() => (hosts = []));
    });
  });

  const inPlace = $derived(target.trim() === sourceName && hostId === (originHost || 'local'));
  const canSubmit = $derived(!!target.trim() && confirmText === target.trim() && !busy);

  function reportLine(report: BackupRestoreReport): string {
    return isStack
      ? `${report.files_restored.length} file(s), ${report.volumes_restored.length} volume(s) into ${report.stack_name}`
      : target.trim();
  }

  // The restore request stays open until the restore ends. Once its
  // operation shows up, the dialog hands over to the progress dialog;
  // a request refused before any work started never gets one and is
  // reported here instead.
  async function submit() {
    if (!run || !canSubmit) return;
    busy = true;
    const r = run;
    const ref = opRef.backupRun(r.id);
    const since = Date.now();
    let report: BackupRestoreReport | undefined;
    let error: unknown;
    const req = (isStack
      ? api.backups.restoreStack(r.id, { target_stack: target.trim(), host_id: hostId, mode })
      : api.backups.restore(r.id, { dest_volume: target.trim(), host_id: hostId, mode })
    ).then(
      (rep) => (report = rep),
      (e) => (error = e)
    );
    let op = await Promise.race([operations.find(ref, 'backup.restore', since, 6000), req.then(() => undefined)]);
    if (!op) op = await operations.find(ref, 'backup.restore', since, 1000);
    if (op) {
      busy = false;
      onclose();
      void operations.open(op.id);
      await req;
      if (report) {
        for (const w of report.warnings ?? []) toast.warning('Restore warning', w);
      }
      return;
    }
    await req;
    busy = false;
    if (report) {
      toast.success('Restored', reportLine(report));
      for (const w of report.warnings ?? []) toast.warning('Restore warning', w);
      onclose();
    } else {
      toast.error('Restore failed', error instanceof ApiError ? error.message : undefined);
    }
  }
</script>

<Modal open={!!run} title={run ? `Restore run #${run.id}` : 'Restore'} maxWidth="max-w-lg" onclose={onclose}>
  {#if run}
    <div class="rs">
      <div class="dm-field">
        <label for="rs-target">{isStack ? 'Stack name' : 'Volume name'}</label>
        <input id="rs-target" class="dm-filter dm-mono" bind:value={target} autocomplete="off" />
        <small class="dm-sub">
          {isStack ? 'Files go to this stack; its volumes are restored on the host below.' : "Created if it doesn't exist."}
        </small>
      </div>
      <div class="dm-field">
        <label for="rs-host">Host</label>
        <select id="rs-host" class="dm-filter" bind:value={hostId}>
          {#if !hosts.some((h) => h.id === 'local')}<option value="local">local</option>{/if}
          {#if originHost && !hosts.some((h) => h.id === originHost)}<option value={originHost}>{originHost} (offline)</option>{/if}
          {#each hosts as h (h.id)}
            <option value={h.id}>{h.name}{h.id === (originHost || 'local') ? ' (backed up here)' : ''}</option>
          {/each}
        </select>
      </div>
      <fieldset class="dm-field">
        <legend>Existing data</legend>
        <label class="opt" class:on={mode === 'replace'}>
          <input type="radio" bind:group={mode} value="replace" />
          <span><b>Replace</b> <span class="dm-dim">swaps the contents for the backup's once it extracted completely, so the result matches the backup; needs free space for both while it runs. Containers using {isStack ? 'them' : 'it'} must be stopped.</span></span>
        </label>
        <label class="opt" class:on={mode === 'merge'}>
          <input type="radio" bind:group={mode} value="merge" />
          <span><b>Merge</b> <span class="dm-dim">overwrites the files in the backup, keeps everything else.</span></span>
        </label>
      </fieldset>
      {#if inPlace}
        <p class="warn" role="note">
          This restores over the original {isStack ? 'stack' : 'volume'}. If the archive is damaged or the restore fails, nothing is changed.
        </p>
      {/if}
      <div class="dm-field">
        <label for="rs-confirm">Type <span class="dm-mono">{target.trim()}</span> to confirm</label>
        <input id="rs-confirm" type="text" class="dm-filter dm-mono" bind:value={confirmText} placeholder={target.trim()} autocomplete="off" />
      </div>
      <p class="ok"><ShieldCheck size={14} /> The archive is checked against its checksum before anything is extracted.</p>
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={onclose}>Cancel</Button>
    <Button variant="danger" onclick={submit} disabled={!canSubmit} loading={busy}>
      <Undo2 size={14} /> {inPlace && mode === 'replace' ? 'Replace and restore' : 'Restore'}
    </Button>
  {/snippet}
</Modal>

<style>
  .rs { display: flex; flex-direction: column; gap: 14px; }
  fieldset { border: 0; padding: 0; margin: 0; gap: 6px; }
  legend { font-size: 12px; color: var(--muted); font-weight: 500; margin-bottom: 4px; }
  .opt { display: flex; gap: 8px; align-items: flex-start; padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--r); cursor: pointer; font-size: var(--fs-sm); }
  .opt.on { border-color: var(--accent); background: var(--accent-soft); }
  .opt input { margin-top: 3px; }
  .warn { margin: 0; padding: 8px 10px; border-radius: var(--r); font-size: var(--fs-sm); color: var(--warn); border: 1px solid color-mix(in srgb, var(--warn) 40%, transparent); background: color-mix(in srgb, var(--warn) 8%, transparent); }
  .ok { margin: 0; display: flex; gap: 6px; align-items: center; color: var(--subtle); font-size: var(--fs-sm); }
  .ok :global(svg) { color: var(--ok); flex: none; }
</style>
