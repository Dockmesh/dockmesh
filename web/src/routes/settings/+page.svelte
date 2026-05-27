<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { onMount } from 'svelte';
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { EditorialPage, Eyebrow, Field } from '$lib/components/editorial';
  import { Skeleton } from '$lib/components/ui';
  import {
    KeyRound, ShieldCheck, AlertCircle, Copy, RefreshCw, Download,
  } from 'lucide-svelte';

  onMount(() => {
    if (!allowed('system.update')) { goto('/'); return; }
    const search = new URLSearchParams($page.url.search);
    const tab = search.get('tab');
    const redirectMap: Record<string, string> = {
      account:    '/account',
      users:      '/users',
      audit:      '/audit',
      sso:        '/authentication',
      roles:      '/users',
      api_tokens: '/tokens',
      registries: '/registries',
      data:       '/backups',
    };
    if (tab && tab in redirectMap) {
      goto(redirectMap[tab], { replaceState: true });
      return;
    }
    const subParam = search.get('sub');
    if (subParam === 'data') {
      goto('/backups', { replaceState: true });
      return;
    }
    loadAll();
  });

  let systemInfo = $state<{ version: string; commit: string; build_date: string; go_version: string; os: string; arch: string; uptime_seconds: number } | null>(null);
  let sysSettings = $state<Map<string, string>>(new Map());
  let settingsBusy = $state(false);

  let updateStatus = $state<import('$lib/api').UpdateStatus | null>(null);
  let updateCheckBusy = $state(false);

  let secretsRotateBusy = $state(false);
  let secretsRotateResult = $state<{ reencrypted: number; old_recipient: string; new_recipient: string } | null>(null);

  const upgradeOneLiner = 'curl -fsSL https://get.dockmesh.dev | sudo bash && sudo systemctl restart dockmesh';

  async function loadAll() {
    await Promise.all([loadSystemInfo(), loadUpdateStatus()]);
  }

  async function loadSystemInfo() {
    try {
      const [info, settingsArr] = await Promise.all([
        api.system.info(),
        api.system.settings().catch(() => [] as Array<{ key: string; value: string }>),
      ]);
      systemInfo = info;
      sysSettings = new Map(settingsArr.map((e) => [e.key, e.value]));
    } catch (err) {
      toast.error('Failed to load system info', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function loadUpdateStatus() {
    try { updateStatus = await api.system.updateStatus(); }
    catch { /* updater may be disabled; quietly */ }
  }

  async function recheckUpdate() {
    updateCheckBusy = true;
    try {
      updateStatus = await api.system.checkUpdateNow();
      toast.success('Check complete', updateStatus?.update_available ? `Update available: ${updateStatus.latest_version}` : 'Up to date');
    } catch (err) {
      toast.error('Check failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      updateCheckBusy = false;
    }
  }

  async function rotateEncryptionKey() {
    const ok = await confirm.ask({
      title: 'Rotate encryption key?',
      message: 'Generates a new age key and re-encrypts every stack .env.age.',
      body: 'External backups encrypted with the old key must be re-encrypted or re-created separately — Dockmesh does not track them yet.',
      confirmLabel: 'Rotate',
      danger: true,
    });
    if (!ok) return;
    secretsRotateBusy = true;
    try {
      const res = await api.secrets.rotate();
      secretsRotateResult = res;
      toast.success('Key rotated', `${res.reencrypted} .env.age re-encrypted`);
    } catch (err) {
      toast.error('Rotate failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      secretsRotateBusy = false;
    }
  }

  function getSetting(key: string): string {
    return sysSettings.get(key) ?? '';
  }
  function setSetting(key: string, value: string) {
    sysSettings.set(key, value);
    sysSettings = new Map(sysSettings);
  }
  async function saveSettings() {
    settingsBusy = true;
    try {
      const entries = [...sysSettings.entries()].map(([key, value]) => ({ key, value }));
      await api.system.updateSettings(entries);
      toast.success('Settings saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      settingsBusy = false;
    }
  }

  async function copyUpgradeCmd(cmd: string) {
    try {
      if (typeof navigator !== 'undefined' && navigator.clipboard) {
        await navigator.clipboard.writeText(cmd);
        toast.success('Command copied');
      }
    } catch {
      toast.error('Copy failed', 'Select the command manually and Ctrl+C');
    }
  }

  function fmtUptime(secs?: number): string {
    if (!secs) return '—';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }
  function fmtRelative(iso: string | undefined): string {
    if (!iso) return 'never';
    const secs = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }
</script>

<EditorialPage>
  <section class="set">
    <header class="set-header">
      <div class="set-header-text">
        <h1 class="ed-title set-title">Settings</h1>
        <p class="ed-subtitle set-subtitle">
          {#if systemInfo}
            Dockmesh {systemInfo.version} · {systemInfo.os}/{systemInfo.arch} · uptime {fmtUptime(systemInfo.uptime_seconds)}
          {:else}
            Loading instance info…
          {/if}
        </p>
      </div>
    </header>

    <section class="set-section">
      <Eyebrow>01 · Instance</Eyebrow>

      {#if systemInfo}
        <div class="dm-card set-info-card">
          <div class="set-info-grid">
            <div class="set-info-item">
              <span class="set-info-label">Version</span>
              <span class="set-info-value">{systemInfo.version}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Commit</span>
              <span class="set-info-value">{systemInfo.commit.slice(0, 7)}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Built</span>
              <span class="set-info-value">{systemInfo.build_date.slice(0, 10)}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Uptime</span>
              <span class="set-info-value">{fmtUptime(systemInfo.uptime_seconds)}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Go</span>
              <span class="set-info-value">{systemInfo.go_version}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Platform</span>
              <span class="set-info-value">{systemInfo.os}/{systemInfo.arch}</span>
            </div>
          </div>
        </div>
      {:else}
        <Skeleton width="100%" height="6rem" />
      {/if}
    </section>

    <section class="set-section">
      <Eyebrow>02 · Updates</Eyebrow>

      <div class="dm-card set-update-card">
        <div class="set-update-head">
          <div class="set-info-grid set-info-grid-2">
            <div class="set-info-item">
              <span class="set-info-label">Current</span>
              <span class="set-info-value">{updateStatus?.current_version ?? '—'}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Latest</span>
              <span class="set-info-value" class:set-update-newer={updateStatus?.update_available}>
                {updateStatus?.latest_version ?? '—'}
              </span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Last checked</span>
              <span class="set-info-value">{fmtRelative(updateStatus?.checked_at)}</span>
            </div>
            <div class="set-info-item">
              <span class="set-info-label">Status</span>
              <span class="set-info-value">
                {#if updateStatus?.error}
                  <span class="set-status-err">error</span>
                {:else if updateStatus?.is_dev_build && updateStatus.latest_version}
                  <span class="set-status-warn">dev build</span>
                {:else if updateStatus?.update_available}
                  <span class="set-status-update">update available</span>
                {:else if updateStatus && !updateStatus.enabled}
                  <span class="set-status-muted">disabled</span>
                {:else if updateStatus}
                  <span class="set-status-ok">up to date</span>
                {:else}
                  <span class="set-status-muted">—</span>
                {/if}
              </span>
            </div>
          </div>
          <button
            type="button"
            class="dm-btn dm-btn-secondary dm-btn-sm"
            onclick={recheckUpdate}
            disabled={updateCheckBusy}
          >
            <RefreshCw size={12} strokeWidth={1.5} class={updateCheckBusy ? 'set-spin' : ''} />
            {updateCheckBusy ? 'Checking…' : 'Check now'}
          </button>
        </div>

        {#if updateStatus?.error}
          <div class="set-error-banner" role="alert">
            <AlertCircle size={12} strokeWidth={1.6} />
            <span>{updateStatus.error}</span>
          </div>
        {/if}

        {#if updateStatus?.update_available}
          <div class="set-upgrade-banner">
            <div class="set-upgrade-head">
              <div>
                <div class="set-upgrade-title">
                  {updateStatus.is_dev_build
                    ? `Release ${updateStatus.latest_version} available`
                    : `Upgrade to ${updateStatus.latest_version}`}
                </div>
                {#if updateStatus.release_url}
                  <a
                    href={updateStatus.release_url}
                    target="_blank"
                    rel="noopener"
                    class="set-upgrade-notes-link"
                  >
                    View release notes →
                  </a>
                {/if}
              </div>
            </div>
            <div class="set-upgrade-cmd-label">Run on this host:</div>
            <div class="set-upgrade-cmd-row">
              <code class="set-upgrade-cmd">{upgradeOneLiner}</code>
              <button
                type="button"
                class="dm-btn dm-btn-secondary dm-btn-xs"
                onclick={() => copyUpgradeCmd(upgradeOneLiner)}
              >
                <Copy size={11} strokeWidth={1.5} /> copy
              </button>
            </div>
            <p class="set-upgrade-blurb">
              Keeps your data and stacks. The installer swaps the binary and the service restart
              does the rest — typical downtime &lt; 3s.
            </p>
          </div>
        {/if}

        <div class="set-update-controls">
          <label class="set-toggle">
            <input
              type="checkbox"
              checked={getSetting('update_check_enabled') === 'true'}
              onchange={(e) => setSetting('update_check_enabled', (e.target as HTMLInputElement).checked ? 'true' : 'false')}
            />
            <span class="set-toggle-track"><span class="set-toggle-knob"></span></span>
            <div class="set-toggle-text">
              <span class="set-toggle-label">Automatic update checks</span>
              <span class="set-toggle-hint">Turn off for air-gapped installs.</span>
            </div>
          </label>

          <Field label="Check interval (minutes)" hint="Min 15, max 10080 (1 week). Default 120 (2h).">
            <input
              type="number"
              class="dm-input set-input set-input-narrow"
              min="15"
              max="10080"
              value={getSetting('update_check_interval_minutes') || '120'}
              onchange={(e) => setSetting('update_check_interval_minutes', (e.target as HTMLInputElement).value)}
            />
          </Field>
        </div>
      </div>
    </section>

    <section class="set-section">
      <Eyebrow>03 · Configuration</Eyebrow>

      <div class="dm-card set-config-card">
        <label class="set-toggle">
          <input
            type="checkbox"
            checked={getSetting('scanner_enabled') === 'true'}
            onchange={(e) => setSetting('scanner_enabled', (e.target as HTMLInputElement).checked ? 'true' : 'false')}
          />
          <span class="set-toggle-track"><span class="set-toggle-knob"></span></span>
          <div class="set-toggle-text">
            <span class="set-toggle-label">Vulnerability scanner (Grype)</span>
            <span class="set-toggle-hint">Enable CVE scanning for Docker images.</span>
          </div>
        </label>

        <div class="set-divider"></div>

        <Field label="Base URL" hint="Used for OIDC callbacks and agent enrollment links.">
          <input
            type="text"
            class="dm-input set-input"
            placeholder="https://dockmesh.example.com"
            value={getSetting('base_url')}
            onchange={(e) => setSetting('base_url', (e.target as HTMLInputElement).value)}
          />
        </Field>

        <Field label="Agent public URL" hint="The wss:// URL agents use to connect. Leave empty to auto-derive from Base URL.">
          <input
            type="text"
            class="dm-input set-input"
            placeholder="wss://dockmesh.example.com:8443/connect"
            value={getSetting('agent_public_url')}
            onchange={(e) => setSetting('agent_public_url', (e.target as HTMLInputElement).value)}
          />
        </Field>

        <div class="set-config-actions">
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            onclick={saveSettings}
            disabled={settingsBusy}
          >
            {settingsBusy ? 'Saving…' : 'Save settings'}
          </button>
        </div>
      </div>
    </section>

    <section class="set-section">
      <Eyebrow>04 · Encryption key</Eyebrow>

      <div class="dm-card set-key-card">
        <div class="set-key-row">
          <span class="set-key-icon">
            <KeyRound size={18} strokeWidth={1.5} />
          </span>
          <div class="set-key-actions">
            <a
              class="dm-btn dm-btn-secondary dm-btn-sm"
              href="/api/v1/system/backup-key/export"
              download="dockmesh-backup-key.txt"
            >
              <Download size={12} strokeWidth={1.5} /> Export key
            </a>
            <button
              type="button"
              class="dm-btn dm-btn-secondary dm-btn-sm"
              onclick={rotateEncryptionKey}
              disabled={secretsRotateBusy}
            >
              <RefreshCw size={12} strokeWidth={1.5} class={secretsRotateBusy ? 'set-spin' : ''} />
              {secretsRotateBusy ? 'Rotating…' : 'Rotate key'}
            </button>
          </div>
        </div>

        <p class="set-key-dr-hint">
          DR scenario: if Dockmesh itself is destroyed, import this key on the new host
          before running <code class="set-inline-code">dockmesh restore</code> —
          <a href="https://dockmesh.dev/docs/operations/disaster-recovery/" target="_blank" rel="noopener">
            recovery playbook →
          </a>
        </p>

        {#if secretsRotateResult}
          <div class="set-key-result">
            <div class="set-key-result-title">
              <ShieldCheck size={12} strokeWidth={1.6} />
              Rotation complete — {secretsRotateResult.reencrypted}
              <code class="set-inline-code">.env.age</code> re-encrypted.
            </div>
            <div class="set-key-recipient">
              <span class="set-key-recipient-label">old:</span>
              <code class="set-inline-code set-key-recipient-value">{secretsRotateResult.old_recipient}</code>
            </div>
            <div class="set-key-recipient">
              <span class="set-key-recipient-label">new:</span>
              <code class="set-inline-code set-key-recipient-value">{secretsRotateResult.new_recipient}</code>
            </div>
          </div>
        {/if}
      </div>
    </section>
  </section>
</EditorialPage>

<style>
  .set {
    display: flex;
    flex-direction: column;
    gap: 22px;
    max-width: 880px;
  }

  .set-header { display: flex; align-items: flex-end; gap: 24px; flex-wrap: wrap; }
  .set-header-text { min-width: 0; max-width: 70ch; }
  .set-title {
    font-size: 28px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .set-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }
  .set-inline-code {
    font-family: var(--font-mono);
    font-size: 11.5px;
    background: var(--bg);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border-subtle);
    color: var(--fg);
  }

  .set-section {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .set-info-card {
    padding: 18px;
  }
  .set-info-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 14px 18px;
  }
  .set-info-grid-2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .set-info-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .set-info-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .set-info-value {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .set-update-card {
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .set-update-head {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 14px;
    align-items: start;
  }
  .set-update-newer {
    color: var(--accent-fg);
    font-weight: 500;
  }
  .set-status-ok { color: var(--color-success-400); }
  .set-status-warn { color: var(--color-warning-400); }
  .set-status-update { color: var(--accent-fg); font-weight: 500; }
  .set-status-err { color: var(--color-danger-400); }
  .set-status-muted { color: var(--fg-subtle); }

  .set-upgrade-banner {
    padding: 12px 14px;
    border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
    background: color-mix(in srgb, var(--accent) 5%, var(--surface));
    border-radius: 5px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .set-upgrade-title {
    font-size: 14px;
    font-weight: 500;
    color: var(--fg);
  }
  .set-upgrade-notes-link {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--accent-fg);
    text-decoration: none;
  }
  .set-upgrade-notes-link:hover { text-decoration: underline; }
  .set-upgrade-cmd-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .set-upgrade-cmd-row {
    display: flex;
    gap: 8px;
    align-items: stretch;
  }
  .set-upgrade-cmd {
    flex: 1;
    padding: 7px 10px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg);
    overflow-x: auto;
    white-space: nowrap;
  }
  .set-upgrade-blurb {
    font-size: 11.5px;
    color: var(--fg-muted);
    margin: 0;
  }

  .set-update-controls {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border-subtle);
  }

  .set-error-banner {
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    border-radius: 4px;
    color: var(--color-danger-400);
    font-family: var(--font-mono);
    font-size: 11.5px;
    display: flex;
    align-items: flex-start;
    gap: 8px;
    line-height: 1.5;
  }

  .set-config-card {
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .set-config-card :global(.dm-input) {
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .set-config-card :global(.ed-field) { gap: 6px; }
  .set-input {
    font-family: var(--font-mono);
  }
  .set-input-narrow {
    max-width: 140px;
  }
  .set-divider {
    height: 1px;
    background: var(--border-subtle);
  }
  .set-config-actions {
    display: flex;
    justify-content: flex-end;
    padding-top: 6px;
    border-top: 1px solid var(--border-subtle);
  }

  .set-toggle {
    display: flex;
    align-items: center;
    gap: 12px;
    cursor: pointer;
  }
  .set-toggle input {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  .set-toggle-track {
    width: 36px;
    height: 20px;
    background: var(--border-strong);
    border-radius: 999px;
    position: relative;
    flex-shrink: 0;
    transition: background 150ms;
  }
  .set-toggle-knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    background: white;
    border-radius: 999px;
    transition: left 150ms;
  }
  .set-toggle input:checked + .set-toggle-track {
    background: var(--color-brand-500);
  }
  .set-toggle input:checked + .set-toggle-track .set-toggle-knob {
    left: 18px;
  }
  .set-toggle-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .set-toggle-label {
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
  }
  .set-toggle-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  .set-key-card {
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .set-key-row {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .set-key-icon {
    width: 36px;
    height: 36px;
    border-radius: 6px;
    border: 1px solid var(--border-subtle);
    background: var(--bg);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--accent-fg);
    flex-shrink: 0;
  }
  .set-key-actions {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .set-key-dr-hint {
    margin: 0;
    font-size: 11.5px;
    color: var(--fg-muted);
    line-height: 1.5;
  }
  .set-key-dr-hint a {
    color: var(--accent-fg);
    text-decoration: none;
  }
  .set-key-dr-hint a:hover { text-decoration: underline; }

  .set-key-result {
    padding-top: 12px;
    border-top: 1px solid var(--border-subtle);
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .set-key-result-title {
    color: var(--color-success-400);
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 4px;
  }
  .set-key-recipient {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .set-key-recipient-label {
    color: var(--fg-subtle);
    width: 32px;
  }
  .set-key-recipient-value {
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    white-space: nowrap;
  }

  .set-spin { animation: set-spin 0.9s linear infinite; }
  @keyframes set-spin {
    from { transform: rotate(0deg); }
    to   { transform: rotate(360deg); }
  }

  @media (max-width: 720px) {
    .set-info-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .set-update-head { grid-template-columns: 1fr; }
  }
</style>
