<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Download, RefreshCw, ShieldCheck } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  let rotating = $state(false);
  let result = $state<{ reencrypted: number; old_recipient: string; new_recipient: string } | null>(null);

  onMount(() => {
    if (!allowed('system.update')) goto('/', { replace: true });
  });

  let exporting = $state(false);

  async function exportKey() {
    exporting = true;
    try {
      await api.secrets.exportKey();
    } catch (err) {
      toast.error(m.settings_key_download_failed(), err instanceof ApiError ? err.message : String(err));
    } finally {
      exporting = false;
    }
  }

  async function rotate() {
    const ok = await confirm.ask({
      title: m.settings_rotate_title(),
      message: m.settings_rotate_message(),
      body: m.settings_rotate_body(),
      confirmLabel: m.settings_rotate(),
      danger: true
    });
    if (!ok) return;
    rotating = true;
    try {
      result = await api.secrets.rotate();
      toast.success(m.settings_key_rotated(), m.settings_n_reencrypted({ n: result.reencrypted }));
    } catch (err) {
      toast.error(m.settings_rotate_failed(), err instanceof ApiError ? err.message : String(err));
    } finally {
      rotating = false;
    }
  }
</script>

<div class="dm-page">
  <div class="dm-ph">
    <div>
      <h1>{m.settings_enc_title()}</h1>
      <div class="dm-sub">{m.settings_enc_sub()}</div>
    </div>
  </div>

  <div>
    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_key_backup()}</h4>
        <p>{m.settings_key_backup_hint()}</p>
      </div>
      <div>
        <button type="button" class="dm-btn dm-btn-primary" onclick={exportKey} disabled={exporting}>
          <Download size={14} /> {exporting ? m.settings_downloading() : m.settings_download_key()}
        </button>
        <span class="dm-sub">
          {m.settings_restore_before()} <code class="dm-mono">dockmesh restore</code>{m.settings_restore_after()}
          <a href="https://dockmesh.dev/docs/operations/disaster-recovery/" target="_blank" rel="noopener">{m.settings_playbook()}</a>
        </span>
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_rotate_key()}</h4>
        <p>{m.settings_rotate_key_hint()}</p>
      </div>
      <div>
        <button type="button" class="dm-btn" onclick={rotate} disabled={rotating}>
          <RefreshCw size={13} /> {rotating ? m.settings_rotating() : m.settings_rotate_key_btn()}
        </button>
        {#if result}
          <div class="enc-res" role="status">
            <span class="enc-ok"><ShieldCheck size={13} /> {m.settings_rotation_complete({ n: result.reencrypted })}</span>
            <dl class="dm-kv">
              <dt>{m.settings_old_key()}</dt><dd class="dm-mono">{result.old_recipient}</dd>
              <dt>{m.settings_new_key()}</dt><dd class="dm-mono">{result.new_recipient}</dd>
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
