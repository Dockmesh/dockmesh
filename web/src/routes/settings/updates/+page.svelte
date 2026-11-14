<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { api, ApiError, type UpdateInstallState, type UpdateStatus } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { releaseLink } from '$lib/links';
  import { Segmented, StatusChip, Switch } from '$lib/components/ui';
  import { Copy, RefreshCw } from 'lucide-svelte';
  import { createSettingsForm } from '../settingsForm.svelte';
  import { m } from '$lib/paraglide/messages.js';
  import UpdateDialog from './UpdateDialog.svelte';

  const form = createSettingsForm();
  let status = $state<UpdateStatus | null>(null);
  let checking = $state(false);
  const releaseHref = $derived(releaseLink(status?.release_url));
  const stableOneLiner = 'curl -fsSL https://get.dockmesh.dev | sudo bash';
  // get.dockmesh.dev serves the latest stable installer, so a candidate is
  // installed with the installer published next to it.
  const tagPattern = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
  const prereleaseTag = $derived(status?.latest_prerelease ? (status.latest_version ?? '') : '');
  const upgradeOneLiner = $derived(
    prereleaseTag
      ? `curl -fsSL https://github.com/Dockmesh/dockmesh/releases/download/${prereleaseTag}/install.sh | sudo bash -s -- --version ${prereleaseTag}`
      : stableOneLiner
  );
  const showCommand = $derived(!prereleaseTag || tagPattern.test(prereleaseTag));
  const canChangeChannel = $derived(allowed('system.upgrade'));
  let savedChannel = $state('stable');
  const channel = $derived(form.get('update_channel') || 'stable');

  let install = $state<UpdateInstallState | null>(null);
  let dialogOpen = $state(false);
  let resume = $state<UpdateInstallState | null>(null);
  const securityRelease = $derived(status?.severity === 'security');
  const canInstall = $derived(
    allowed('system.upgrade') && !!install && !install.unavailable && !!status?.update_available && !status.is_dev_build && showCommand
  );

  // Picks up an install that was started before the page was opened.
  async function loadInstall() {
    if (!allowed('system.upgrade')) return;
    try {
      install = await api.system.updateInstallState();
    } catch {
      return;
    }
    const running = install.phase === 'downloading' || install.phase === 'waiting_for_stacks' || install.phase === 'applying' || install.pending;
    if (running && install.version) {
      resume = install;
      dialogOpen = true;
    }
  }

  async function installDone() {
    try {
      status = await api.system.updateStatus();
    } catch {
      /* the dialog shows the result either way */
    }
    loadInstall();
  }

  onMount(() => {
    if (!allowed('system.update')) {
      goto('/', { replace: true });
      return;
    }
    form.load().then(() => (savedChannel = form.get('update_channel') || 'stable'));
    api.system
      .updateStatus()
      .then((s) => (status = s))
      .catch(() => {});
    loadInstall();
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

  // A new channel changes which release counts as latest, so look again
  // right away instead of at the next poll.
  async function save() {
    await form.save();
    if (form.dirty || channel === savedChannel) return;
    savedChannel = channel;
    await checkNow();
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
          <div class="up-box" class:up-box-sec={securityRelease} data-testid="update-box">
            <div class="up-head">
              <b>{status.is_dev_build ? m.settings_release_available({ version: status.latest_version ?? '' }) : m.settings_update_to({ version: status.latest_version ?? '' })}</b>
              {#if securityRelease}
                <StatusChip tone="bad" label={m.settings_security_release()} />
              {/if}
            </div>
            {#if securityRelease && status.security?.length}
              <ul class="up-sec">
                {#each status.security as s (s)}<li>{s}</li>{/each}
              </ul>
            {/if}
            {#if releaseHref}
              <a href={releaseHref} target="_blank" rel="noopener">{m.settings_release_notes()}</a>
            {/if}
            {#if canInstall}
              <div>
                <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" data-testid="update-now" onclick={() => { resume = null; dialogOpen = true; }}>
                  {m.settings_update_now()}
                </button>
              </div>
            {:else if install?.unavailable === 'container'}
              <span class="dm-sub" data-testid="update-container">{m.settings_update_container()}</span>
            {:else if install?.unavailable === 'no_updater'}
              <span class="dm-sub" data-testid="update-no-updater">{m.settings_update_no_updater()}</span>
            {/if}
            {#if showCommand && install?.unavailable !== 'container'}
              <span class="dm-sub">{status.latest_prerelease ? m.settings_run_prerelease({ version: status.latest_version }) : canInstall ? m.settings_run_on_host_alt() : m.settings_run_on_host()}</span>
              <div class="up-cmd">
                <code class="dm-mono">{upgradeOneLiner}</code>
                <button type="button" class="dm-btn dm-btn-sm" onclick={() => copyWithToast(upgradeOneLiner, m.settings_command_copied())}>
                  <Copy size={12} /> {m.common_copy()}
                </button>
              </div>
            {/if}
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

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_update_channel()}</h4>
        <p>{m.settings_update_channel_hint()}</p>
      </div>
      <div class="up-chan">
        {#if canChangeChannel}
          <Segmented
            label={m.settings_update_channel()}
            value={channel}
            options={[
              { key: 'stable', label: m.settings_channel_stable() },
              { key: 'prerelease', label: m.settings_channel_prerelease() }
            ]}
            onchange={(k) => form.set('update_channel', k)}
          />
        {:else}
          <span>{channel === 'prerelease' ? m.settings_channel_prerelease() : m.settings_channel_stable()}</span>
          <span class="dm-sub">{m.settings_channel_needs_upgrade()}</span>
        {/if}
      </div>
    </div>
  </div>

  {#if status?.latest_version}
    <UpdateDialog
      bind:open={dialogOpen}
      version={resume?.version ?? status.latest_version}
      security={securityRelease ? (status.security ?? []) : []}
      {resume}
      ondone={installDone}
    />
  {/if}

  <div class="set-bar">
    <button type="button" class="dm-btn" onclick={form.discard} disabled={!form.dirty || form.busy}>{m.settings_discard()}</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={save} disabled={!form.dirty || form.busy}>
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
  .up-box-sec { border-color: var(--bad); }
  .up-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .up-sec { margin: 0; padding-left: 18px; font-size: var(--fs-sm); }
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
  .up-chan { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
  .up-int input { width: 90px; }
  .set-bar { display: flex; justify-content: flex-end; gap: 8px; border-top: 1px solid var(--border); padding-top: 14px; }
  :global(.up-spin) { animation: up-spin 0.9s linear infinite; }
  @keyframes up-spin { to { transform: rotate(360deg); } }
</style>
