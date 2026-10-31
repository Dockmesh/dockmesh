<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Download, RefreshCw, ShieldCheck } from 'lucide-svelte';

  let rotating = $state(false);
  let result = $state<{ reencrypted: number; old_recipient: string; new_recipient: string } | null>(null);

  onMount(() => {
    if (!allowed('system.update')) goto('/', { replace: true });
  });

  async function rotate() {
    const ok = await confirm.ask({
      title: 'Rotate encryption key?',
      message: 'Generates a new age key and re-encrypts every stack .env.age.',
      body: 'Backups encrypted with the old key are not re-encrypted. Keep the old key export until they have been replaced.',
      confirmLabel: 'Rotate',
      danger: true
    });
    if (!ok) return;
    rotating = true;
    try {
      result = await api.secrets.rotate();
      toast.success('Key rotated', `${result.reencrypted} .env.age re-encrypted`);
    } catch (err) {
      toast.error('Rotate failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      rotating = false;
    }
  }
</script>

<div class="dm-page">
  <div class="dm-ph">
    <div>
      <h1>Encryption key</h1>
      <div class="dm-sub">Key backup and rotation</div>
    </div>
  </div>

  <div>
    <div class="dm-form-sec">
      <div>
        <h4>Key backup</h4>
        <p>The key encrypts secrets, stack .env files and backup archives. Without it, backups cannot be restored on a new server.</p>
      </div>
      <div>
        <a class="dm-btn dm-btn-primary" href="/api/v1/system/backup-key/export" download="dockmesh-backup-key.txt">
          <Download size={14} /> Download key backup
        </a>
        <span class="dm-sub">
          After a full loss, import this key on the new host before running <code class="dm-mono">dockmesh restore</code>.
          <a href="https://dockmesh.dev/docs/operations/disaster-recovery/" target="_blank" rel="noopener">Recovery playbook</a>
        </span>
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>Rotate key</h4>
        <p>Creates a new key and re-encrypts every stack .env.age with it.</p>
      </div>
      <div>
        <button type="button" class="dm-btn" onclick={rotate} disabled={rotating}>
          <RefreshCw size={13} /> {rotating ? 'Rotating…' : 'Rotate key…'}
        </button>
        {#if result}
          <div class="enc-res" role="status">
            <span class="enc-ok"><ShieldCheck size={13} /> Rotation complete · {result.reencrypted} .env.age re-encrypted</span>
            <dl class="dm-kv">
              <dt>Old key</dt><dd class="dm-mono">{result.old_recipient}</dd>
              <dt>New key</dt><dd class="dm-mono">{result.new_recipient}</dd>
            </dl>
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .enc-res { display: flex; flex-direction: column; gap: 8px; width: min(620px, 100%); }
  .enc-ok { color: var(--ok); display: inline-flex; align-items: center; gap: 6px; }
</style>
