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
  import { m } from '$lib/paraglide/messages.js';

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
      toast.success(m.settings_check_complete(), status?.update_available ? m.settings_update_available_v({ version: status.latest_version ?? '' }) : m.settings_up_to_date_toast());
    } catch (err) {
      toast.error(m.settings_check_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      checking = false;
    }
  }

  function fmtRelative(iso: string | undefined): string {
    if (!iso) return m.common_never();
    const secs = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
    if (secs < 60) return m.time_just_now();
    if (secs < 3600) return m.time_min_ago({ n: Math.floor(secs / 60) });
    if (secs < 86400) return m.time_h_ago({ n: Math.floor(secs / 3600) });
    return m.time_d_ago({ n: Math.floor(secs / 86400) });
  }

  const verdict = $derived.by<{ tone: 'ok' | 'warn' | 'bad' | 'off' | 'acc'; label: string }>(() => {
    if (!status) return { tone: 'off', label: '—' };
    if (status.error) return { tone: 'bad', label: m.settings_verdict_check_failed() };
    if (status.is_dev_build && status.latest_version) return { tone: 'warn', label: m.settings_verdict_dev_build() };
    if (status.update_available) return { tone: 'acc', label: m.settings_verdict_update_available() };
    if (!status.enabled) return { tone: 'off', label: m.settings_verdict_checks_off() };
    return { tone: 'ok', label: m.settings_verdict_up_to_date() };
  });
</script>

<div class="dm-page">
  <div class="dm-ph">
    <div>
      <h1>{m.settings_updates_title()}</h1>
      <div class="dm-sub">{m.settings_updates_sub()}</div>
    </div>
  </div>

  <div>
    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_dockmesh_version()}</h4>
        <p>{m.settings_running_version({ version: status?.current_version ?? '…' })}</p>
      </div>
      <div>
        <div class="up-row">
          <StatusChip tone={verdict.tone} label={verdict.label} />
          <span class="dm-sub">{m.settings_latest_checked({ version: status?.latest_version || '—', ago: fmtRelative(status?.checked_at) })}</span>
          <button type="button" class="dm-btn dm-btn-sm" onclick={checkNow} disabled={checking}>
            <RefreshCw size={12} class={checking ? 'up-spin' : ''} /> {checking ? m.settings_checking() : m.settings_check_now()}
          </button>
        </div>
        {#if status?.error}
          <span class="up-err" role="alert">{status.error}</span>
        {/if}
        {#if status?.update_available}
          <div class="up-box">
            <b>{status.is_dev_build ? m.settings_release_available({ version: status.latest_version ?? '' }) : m.settings_update_to({ version: status.latest_version ?? '' })}</b>
            {#if status.release_url}
              <a href={status.release_url} target="_blank" rel="noopener">{m.settings_release_notes()}</a>
            {/if}
            <span class="dm-sub">{m.settings_run_on_host()}</span>
            <div class="up-cmd">
              <code class="dm-mono">{upgradeOneLiner}</code>
              <button type="button" class="dm-btn dm-btn-sm" onclick={() => copyWithToast(upgradeOneLiner, m.settings_command_copied())}>
                <Copy size={12} /> {m.common_copy()}
              </button>
            </div>
          </div>
        {/if}
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_auto_checks()}</h4>
        <p>{m.settings_auto_checks_hint()}</p>
      </div>
      <div>
        <Switch
          label={m.settings_check_for_updates()}
          checked={form.get('update_check_enabled') === 'true'}
          onchange={(v) => form.set('update_check_enabled', v ? 'true' : 'false')}
        />
        <label class="up-int">
          <span class="dm-sub">{m.settings_every()}</span>
          <input
            type="number"
            class="dm-input"
            min="15"
            max="10080"
            value={form.get('update_check_interval_minutes') || '120'}
            oninput={(e) => form.set('update_check_interval_minutes', e.currentTarget.value)}
          />
          <span class="dm-sub">{m.settings_minutes_range()}</span>
        </label>
      </div>
    </div>
  </div>

  <div class="set-bar">
    <button type="button" class="dm-btn" onclick={form.discard} disabled={!form.dirty || form.busy}>{m.settings_discard()}</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={form.save} disabled={!form.dirty || form.busy}>
      {form.busy ? m.common_saving() : m.common_save()}
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
