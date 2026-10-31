<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, ApiError, type UpdateStatus } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { StatusChip, Switch } from '$lib/components/ui';
  import { Copy, RefreshCw } from 'lucide-svelte';
  import { createSettingsForm } from '../settingsForm.svelte';

  const form = createSettingsForm();
  let status = $state<UpdateStatus | null>(null);
  let checking = $state(false);
  const upgradeOneLiner = 'curl -fsSL https://get.dockmesh.dev | sudo bash && sudo systemctl restart dockmesh';

  onMount(() => {
    if (!allowed('system.update')) {
      goto('/', { replace: true });
      return;
    }
    form.load();
    api.system
      .updateStatus()
      .then((s) => (status = s))
      .catch(() => {});
  });

  async function checkNow() {
    checking = true;
    try {
      status = await api.system.checkUpdateNow();
      toast.success('Check complete', status?.update_available ? `Update available: ${status.latest_version}` : 'Up to date');
    } catch (err) {
      toast.error('Check failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      checking = false;
    }
  }

  function fmtRelative(iso: string | undefined): string {
    if (!iso) return 'never';
    const secs = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)} min ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)} h ago`;
    return `${Math.floor(secs / 86400)} d ago`;
  }

  const verdict = $derived.by<{ tone: 'ok' | 'warn' | 'bad' | 'off' | 'acc'; label: string }>(() => {
    if (!status) return { tone: 'off', label: '—' };
    if (status.error) return { tone: 'bad', label: 'check failed' };
    if (status.is_dev_build && status.latest_version) return { tone: 'warn', label: 'dev build' };
    if (status.update_available) return { tone: 'acc', label: 'update available' };
    if (!status.enabled) return { tone: 'off', label: 'checks off' };
    return { tone: 'ok', label: 'up to date' };
  });
</script>

<div class="dm-page">
  <div class="dm-ph">
    <div>
      <h1>Updates</h1>
      <div class="dm-sub">Dockmesh version and automatic update checks</div>
    </div>
  </div>

  <div>
    <div class="dm-form-sec">
      <div>
        <h4>Dockmesh version</h4>
        <p>Running {status?.current_version ?? '…'}. Updates keep your data and stacks.</p>
      </div>
      <div>
        <div class="up-row">
          <StatusChip tone={verdict.tone} label={verdict.label} />
          <span class="dm-sub">latest {status?.latest_version || '—'} · checked {fmtRelative(status?.checked_at)}</span>
          <button type="button" class="dm-btn dm-btn-sm" onclick={checkNow} disabled={checking}>
            <RefreshCw size={12} class={checking ? 'up-spin' : ''} /> {checking ? 'Checking…' : 'Check now'}
          </button>
        </div>
        {#if status?.error}
          <span class="up-err" role="alert">{status.error}</span>
        {/if}
        {#if status?.update_available}
          <div class="up-box">
            <b>{status.is_dev_build ? `Release ${status.latest_version} available` : `Update to ${status.latest_version}`}</b>
            {#if status.release_url}
              <a href={status.release_url} target="_blank" rel="noopener">Release notes</a>
            {/if}
            <span class="dm-sub">Run on this host; the service restart takes a few seconds:</span>
            <div class="up-cmd">
              <code class="dm-mono">{upgradeOneLiner}</code>
              <button type="button" class="dm-btn dm-btn-sm" onclick={() => copyWithToast(upgradeOneLiner, 'Command copied')}>
                <Copy size={12} /> Copy
              </button>
            </div>
          </div>
        {/if}
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>Automatic update checks</h4>
        <p>Looks for new releases on GitHub. Turn off for air-gapped installs.</p>
      </div>
      <div>
        <Switch
          label="Check for updates"
          checked={form.get('update_check_enabled') === 'true'}
          onchange={(v) => form.set('update_check_enabled', v ? 'true' : 'false')}
        />
        <label class="up-int">
          <span class="dm-sub">Every</span>
          <input
            type="number"
            class="dm-input"
            min="15"
            max="10080"
            value={form.get('update_check_interval_minutes') || '120'}
            oninput={(e) => form.set('update_check_interval_minutes', e.currentTarget.value)}
          />
          <span class="dm-sub">minutes (15 to 10080)</span>
        </label>
      </div>
    </div>
  </div>

  <div class="set-bar">
    <button type="button" class="dm-btn" onclick={form.discard} disabled={!form.dirty || form.busy}>Discard</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={form.save} disabled={!form.dirty || form.busy}>
      {form.busy ? 'Saving…' : 'Save'}
    </button>
  </div>
</div>

<style>
  .up-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .up-err { color: var(--bad); font-size: var(--fs-sm); }
  .up-box {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    border: 1px solid var(--accent);
    background: var(--accent-soft);
    border-radius: var(--r);
    width: min(620px, 100%);
  }
  .up-cmd { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .up-cmd code {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    background: var(--band);
    border: 1px solid var(--border);
    border-radius: var(--r-sm);
    padding: 4px 8px;
  }
  .up-int { display: flex; align-items: center; gap: 8px; }
  .up-int input { width: 90px; }
  .set-bar { display: flex; justify-content: flex-end; gap: 8px; border-top: 1px solid var(--border); padding-top: 14px; }
  :global(.up-spin) { animation: up-spin 0.9s linear infinite; }
  @keyframes up-spin { to { transform: rotate(360deg); } }
</style>
