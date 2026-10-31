<script lang="ts">
  // Restore a volume or stack run: destination name, host (defaults to
  // the host the backup was taken on) and replace vs. merge. The server
  // verifies the whole archive before it writes anything.
  import { untrack } from 'svelte';
  import { api, ApiError, type BackupRun, type HostInfo } from '$lib/api';
  import { Button, Input, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Undo2 } from 'lucide-svelte';

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

  async function submit() {
    if (!run || !canSubmit) return;
    busy = true;
    try {
      const report = isStack
        ? await api.backups.restoreStack(run.id, { target_stack: target.trim(), host_id: hostId, mode })
        : await api.backups.restore(run.id, { dest_volume: target.trim(), host_id: hostId, mode });
      const what = isStack
        ? `${report.files_restored.length} file(s), ${report.volumes_restored.length} volume(s) into ${report.stack_name}`
        : target.trim();
      toast.success('Restored', what);
      for (const w of report.warnings ?? []) toast.warning('Restore warning', w);
      onclose();
    } catch (err) {
      toast.error('Restore failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
</script>

<Modal open={!!run} title={isStack ? 'Restore stack backup' : 'Restore volume backup'} maxWidth="max-w-lg" onclose={onclose}>
  {#if run}
    <div class="space-y-4">
      <Input
        label={isStack ? 'Stack name' : 'Volume name'}
        bind:value={target}
        hint={isStack
          ? 'Files go to this stack; its volumes are restored on the host below.'
          : "Created if it doesn't exist."}
      />
      <label class="block">
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Host</span>
        <select class="dm-input text-sm" bind:value={hostId}>
          {#if !hosts.some((h) => h.id === 'local')}<option value="local">local</option>{/if}
          {#if originHost && !hosts.some((h) => h.id === originHost)}<option value={originHost}>{originHost} (offline)</option>{/if}
          {#each hosts as h (h.id)}
            <option value={h.id}>{h.name}{h.id === (originHost || 'local') ? ' (backed up here)' : ''}</option>
          {/each}
        </select>
      </label>
      <fieldset class="space-y-2">
        <legend class="text-xs font-medium text-[var(--fg-muted)] mb-1.5">Existing data</legend>
        <label class="flex items-start gap-2 text-sm">
          <input type="radio" bind:group={mode} value="replace" />
          <span><strong>Replace</strong> — empty the volume{isStack ? 's' : ''} first, so the result matches the backup. Containers using {isStack ? 'them' : 'it'} must be stopped.</span>
        </label>
        <label class="flex items-start gap-2 text-sm">
          <input type="radio" bind:group={mode} value="merge" />
          <span><strong>Merge</strong> — overwrite the files in the backup, keep everything else.</span>
        </label>
      </fieldset>
      {#if inPlace}
        <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)]">
          This restores over the original {isStack ? 'stack' : 'volume'}. The archive is checked first; if it is damaged, nothing is changed.
        </div>
      {/if}
      <div>
        <label for="restore-confirm" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Type <span class="font-mono">{target.trim()}</span> to confirm</label>
        <input id="restore-confirm" type="text" class="dm-input text-sm font-mono" bind:value={confirmText} placeholder={target.trim()} />
      </div>
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={onclose}>Cancel</Button>
    <Button variant="danger" onclick={submit} disabled={!canSubmit}>
      <Undo2 class="w-4 h-4" /> {busy ? 'Restoring…' : 'Restore'}
    </Button>
  {/snippet}
</Modal>
