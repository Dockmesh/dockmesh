<script lang="ts">
  // 3-step backup-target wizard. Step 1 picks the storage type + name,
  // step 2 fills in the type-specific connection fields, step 3 runs
  // a live test (5 phases — connect / auth / write / read / delete).
  // Mockup reference: `Dockmesh Wizard (6)/backup-wizards.jsx::TargetWizard`.
  import { fmtBytes } from '$lib/format';
  import { api, ApiError, targetUsage, type BackupTarget } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { stepValid, type TargetType } from '$lib/backupTargetForm';
  import WizardShell from './_WizardShell.svelte';
  import { Check, X, RefreshCw, AlertTriangle } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  interface Props {
    open: boolean;
    editing: BackupTarget | null;
    onclose: () => void;
    onsave: (
      payload: { name: string; type: string; config: Record<string, any> },
      isEdit: boolean,
      originalId?: number
    ) => Promise<void>;
  }
  let { open, editing, onclose, onsave }: Props = $props();

  const STEPS = [m.common_type(), m.backups_connection(), m.common_test()];
  let step = $state(0);
  let busy = $state(false);

  let type = $state<TargetType>('local');
  let name = $state('new-target');
  let cfg = $state<Record<string, any>>({ path: '' });
  let defaultPath = $state('');

  // Test state. We start in 'idle' on each step-3 entry — the operator
  // explicitly clicks "Run test" to fire the probe. That avoids burning
  // S3 / SFTP API calls on accidental wizard navigation.
  type TestState = 'idle' | 'testing' | 'ok' | 'fail';
  let testState = $state<TestState>('idle');
  let testResult = $state<{ status: string; total_bytes?: number; used_bytes?: number; free_bytes?: number; error?: string } | null>(null);
  // Per-check phase status. Set in lock-step with the probe so the
  // five-row check list animates from idle → running → ok / fail.
  let checks = $state<Array<{ id: string; label: string; state: 'idle' | 'running' | 'ok' | 'fail' }>>([
    { id: 'connect', label: m.backups_twiz_check_connect(), state: 'idle' },
    { id: 'auth',    label: m.backups_twiz_check_auth(),    state: 'idle' },
    { id: 'write',   label: m.backups_twiz_check_write(),   state: 'idle' },
    { id: 'read',    label: m.backups_twiz_check_read(),    state: 'idle' },
    { id: 'delete',  label: m.backups_twiz_check_delete(),  state: 'idle' }
  ]);
  let testStartedAt = $state(0);
  let testDurationMs = $state(0);

  let initialised = $state(false);
  $effect(() => {
    if (!open) { initialised = false; return; }
    if (initialised) return;
    initialised = true;
    step = 0;
    testState = 'idle';
    testResult = null;
    resetChecks();
    if (editing) {
      type = editing.type as TargetType;
      name = editing.name;
      cfg = { ...(editing.config ?? {}) };
    } else {
      type = 'local';
      name = 'new-target';
      cfg = { path: defaultPath };
      void api.backups.localPath().then((p) => {
        defaultPath = p;
        if (!editing && !cfg.path) cfg = { ...cfg, path: p };
      });
    }
  });

  function resetChecks() {
    checks = checks.map((c) => ({ ...c, state: 'idle' as const }));
  }
  function setCheck(id: string, state: 'idle' | 'running' | 'ok' | 'fail') {
    checks = checks.map((c) => (c.id === id ? { ...c, state } : c));
  }

  let hostKeyChanged = $state(false);
  let acceptingKey = $state(false);

  async function acceptHostKey() {
    if (!editing) return;
    acceptingKey = true;
    try {
      await api.backups.acceptTargetHostKey(editing.id);
      toast.success(m.backups_host_key_accepted());
      await runTest();
    } catch (err) {
      toast.error(m.backups_accept_host_key_failed(), err instanceof ApiError ? err.message : (err as Error).message);
    } finally {
      acceptingKey = false;
    }
  }

  async function runTest() {
    if (testState === 'testing') return;
    testState = 'testing';
    testResult = null;
    hostKeyChanged = false;
    resetChecks();
    testStartedAt = Date.now();
    // Walk the checklist visually. Real test happens in one round-trip;
    // the per-row animation is presentation. If the round-trip succeeds
    // all five flip to ok; if it fails we mark connect+auth ok and
    // pin the failure on whichever phase the error mentions, falling
    // back to 'write' which is the most common late-stage failure.
    setCheck('connect', 'running');
    const cfgPayload: Record<string, any> = { ...cfg };
    if (cfgPayload.port) cfgPayload.port = parseInt(cfgPayload.port) || 0;
    try {
      const r = await api.backups.testTargetConfig(type, cfgPayload, editing?.id);
      testResult = r;
      testDurationMs = Date.now() - testStartedAt;
      if (r.status === 'connected' || r.status === 'ok') {
        for (const c of checks) setCheck(c.id, 'ok');
        testState = 'ok';
      } else {
        setCheck('connect', 'ok');
        setCheck('auth', 'ok');
        const errLower = (r.error ?? '').toLowerCase();
        const failOn = errLower.includes('auth') || errLower.includes('credentials') ? 'auth'
                     : errLower.includes('read') ? 'read'
                     : errLower.includes('delete') ? 'delete'
                     : 'write';
        if (failOn === 'auth') setCheck('auth', 'fail');
        else {
          setCheck(failOn, 'fail');
          // Phases after the failure stay idle.
        }
        testState = 'fail';
        if (editing && type === 'sftp' && errLower.includes('host key changed')) {
          // The unsaved-config test cannot record the new key; the stored
          // target's own test does, which is what Accept works on.
          await api.backups.testTarget(editing.id).catch(() => null);
          hostKeyChanged = true;
        }
      }
    } catch (err) {
      testDurationMs = Date.now() - testStartedAt;
      testResult = { status: 'error', error: err instanceof ApiError ? err.message : String(err) };
      setCheck('connect', 'fail');
      testState = 'fail';
    }
  }

  const canContinue = $derived(stepValid(step, name, type, cfg));

  async function finish() {
    busy = true;
    try {
      const cfgPayload: Record<string, any> = { ...cfg };
      if (cfgPayload.port) cfgPayload.port = parseInt(cfgPayload.port) || 0;
      await onsave({ name: name.trim(), type, config: cfgPayload }, !!editing, editing?.id);
    } catch (err) {
      toast.error(m.backups_save_failed(), err instanceof ApiError ? err.message : (err as Error).message);
    } finally {
      busy = false;
    }
  }

  function targetTypeMeta(t: string): { label: string; glyph: string; blurb: string } {
    if (t === 's3') return { label: m.backups_ttype_s3_label(), glyph: 'S3', blurb: m.backups_ttype_s3_blurb() };
    if (t === 'sftp') return { label: m.backups_ttype_sftp_label(), glyph: 'FTP', blurb: m.backups_ttype_sftp_blurb() };
    if (t === 'smb') return { label: m.backups_ttype_smb_label(), glyph: 'NAS', blurb: m.backups_ttype_smb_blurb() };
    if (t === 'webdav') return { label: 'WebDAV', glyph: 'DAV', blurb: m.backups_ttype_webdav_blurb() };
    return { label: m.backups_ttype_local_label(), glyph: 'LOC', blurb: m.backups_ttype_local_blurb() };
  }
  const meta = $derived(targetTypeMeta(type));

</script>

<WizardShell
  {open}
  title={editing ? m.backups_twiz_edit_title() : m.backups_twiz_new_title()}
  subtitle={editing && editing.job_count != null
    ? targetUsage(editing.job_count)
    : m.backups_twiz_subtitle()}
  steps={STEPS}
  stepIdx={step}
  {busy}
  finishLabel={editing ? m.backups_save_changes() : (testState === 'ok' ? m.backups_twiz_save_target() : m.backups_save_anyway())}
  nextDisabled={!canContinue}
  {onclose}
  onstep={(i) => (step = i)}
  onback={() => (step = Math.max(0, step - 1))}
  onnext={() => (step = Math.min(STEPS.length - 1, step + 1))}
  onfinish={finish}
>
  <!-- ============================================================== -->
  <!--  Step 0 — Type + name                                           -->
  <!-- ============================================================== -->
  {#if step === 0}
    <div class="wiz-stack">
      <div class="wiz-field">
        <label class="wiz-field-label">{m.backups_twiz_type_label()}</label>
        <span class="wiz-field-hint">{m.backups_twiz_type_hint()}</span>
        <div class="wiz-radio-grid">
          {#each ['local', 'sftp', 'smb', 'webdav', 's3'] as t (t)}
            {@const tm = targetTypeMeta(t)}
            <button type="button" class="wiz-radio-card" class:active={type === t} onclick={() => (type = t as TargetType)}>
              <span class="wiz-target-glyph">{tm.glyph}</span>
              <span class="wiz-radio-card-label">{tm.label}</span>
              <span class="wiz-radio-card-meta">{tm.blurb}</span>
            </button>
          {/each}
        </div>
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label" for="t-name">{m.backups_twiz_name_label()}</label>
        <span class="wiz-field-hint">{m.backups_twiz_name_hint()}</span>
        <input id="t-name" class="wiz-input wiz-input-mono" bind:value={name} />
      </div>
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  Step 1 — Connection                                            -->
  <!-- ============================================================== -->
  {#if step === 1}
    <div class="wiz-stack">
      {#if type === 'local'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-path">{m.common_path()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_local_path_hint()}</span>
          <input id="t-path" class="wiz-input wiz-input-mono" placeholder={defaultPath || '/srv/backups'} value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
        </div>
      {:else if type === 'sftp'}
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-host">{m.common_host()}</label>
            <input id="t-host" class="wiz-input wiz-input-mono" placeholder="backup.example.com" value={cfg.host ?? ''} oninput={(e) => (cfg = { ...cfg, host: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-port">{m.common_port()}</label>
            <input id="t-port" class="wiz-input wiz-input-mono" placeholder="22" value={cfg.port ?? ''} oninput={(e) => (cfg = { ...cfg, port: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-user">{m.backups_username()}</label>
            <input id="t-user" class="wiz-input wiz-input-mono" value={cfg.username ?? ''} oninput={(e) => (cfg = { ...cfg, username: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-pass">{m.backups_cfg_password()}</label>
            <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={cfg.password ?? ''} oninput={(e) => (cfg = { ...cfg, password: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-key">{m.backups_twiz_private_key_label()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_private_key_hint()}{cfg.private_key_set && !cfg.private_key ? ` ${m.backups_twiz_private_key_saved()}` : ''}</span>
          <textarea id="t-key" class="wiz-input wiz-input-mono" rows="4" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" value={cfg.private_key ?? ''} oninput={(e) => (cfg = { ...cfg, private_key: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-rpath">{m.backups_twiz_remote_path()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_remote_path_hint()}</span>
          <input id="t-rpath" class="wiz-input wiz-input-mono" placeholder="/backups" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
        </div>
      {:else if type === 'smb'}
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-host">{m.backups_cfg_server()}</label>
            <input id="t-host" class="wiz-input wiz-input-mono" placeholder="192.168.1.100" value={cfg.host ?? ''} oninput={(e) => (cfg = { ...cfg, host: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-port">{m.common_port()}</label>
            <input id="t-port" class="wiz-input wiz-input-mono" placeholder="445" value={cfg.port ?? ''} oninput={(e) => (cfg = { ...cfg, port: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-share">{m.backups_cfg_share()}</label>
            <input id="t-share" class="wiz-input wiz-input-mono" placeholder="backups" value={cfg.share ?? ''} oninput={(e) => (cfg = { ...cfg, share: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-spath">{m.backups_twiz_share_path()}</label>
            <input id="t-spath" class="wiz-input wiz-input-mono" placeholder="dockmesh" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-user">{m.backups_username()}</label>
            <input id="t-user" class="wiz-input wiz-input-mono" value={cfg.username ?? ''} oninput={(e) => (cfg = { ...cfg, username: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-pass">{m.backups_cfg_password()}</label>
            <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={cfg.password ?? ''} oninput={(e) => (cfg = { ...cfg, password: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
      {:else if type === 'webdav'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-url">{m.backups_twiz_webdav_url()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_webdav_url_hint()}</span>
          <input id="t-url" class="wiz-input wiz-input-mono" placeholder="https://nextcloud.example.com/remote.php/dav/files/user/" value={cfg.url ?? ''} oninput={(e) => (cfg = { ...cfg, url: (e.currentTarget as HTMLInputElement).value })} />
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-user">{m.backups_username()}</label>
            <input id="t-user" class="wiz-input wiz-input-mono" value={cfg.username ?? ''} oninput={(e) => (cfg = { ...cfg, username: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-pass">{m.backups_twiz_app_password()}</label>
            <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={cfg.password ?? ''} oninput={(e) => (cfg = { ...cfg, password: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-path">{m.common_path()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_webdav_path_hint()}</span>
          <input id="t-path" class="wiz-input wiz-input-mono" placeholder="/backups" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-ca">{m.backups_twiz_ca_label()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_ca_hint_https()}</span>
          <textarea id="t-ca" class="wiz-input wiz-input-mono" rows="3" spellcheck="false" placeholder="-----BEGIN CERTIFICATE-----" value={cfg.ca_cert ?? ''} oninput={(e) => (cfg = { ...cfg, ca_cert: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
        </div>
      {:else if type === 's3'}
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-ep">{m.backups_cfg_endpoint()}</label>
            <input id="t-ep" class="wiz-input wiz-input-mono" placeholder="s3.amazonaws.com" value={cfg.endpoint ?? ''} oninput={(e) => (cfg = { ...cfg, endpoint: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-region">{m.backups_cfg_region()}</label>
            <input id="t-region" class="wiz-input wiz-input-mono" placeholder="us-east-1" value={cfg.region ?? ''} oninput={(e) => (cfg = { ...cfg, region: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-bucket">{m.backups_cfg_bucket()}</label>
          <input id="t-bucket" class="wiz-input wiz-input-mono" placeholder="my-backups" value={cfg.bucket ?? ''} oninput={(e) => (cfg = { ...cfg, bucket: (e.currentTarget as HTMLInputElement).value })} />
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-ak">{m.backups_cfg_access_key()}</label>
            <input id="t-ak" class="wiz-input wiz-input-mono" value={cfg.access_key ?? ''} oninput={(e) => (cfg = { ...cfg, access_key: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-sk">{m.backups_cfg_secret_key()}</label>
            <input id="t-sk" class="wiz-input wiz-input-mono" type="password" value={cfg.secret_key ?? ''} oninput={(e) => (cfg = { ...cfg, secret_key: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-prefix">{m.backups_twiz_prefix_label()}</label>
          <span class="wiz-field-hint">{m.backups_twiz_prefix_hint()}</span>
          <input id="t-prefix" class="wiz-input wiz-input-mono" placeholder="backups/dockmesh" value={cfg.prefix ?? ''} oninput={(e) => (cfg = { ...cfg, prefix: (e.currentTarget as HTMLInputElement).value })} />
        </div>
        <label class="wiz-check">
          <input type="checkbox" checked={!!cfg.use_ssl} onchange={(e) => (cfg = { ...cfg, use_ssl: (e.currentTarget as HTMLInputElement).checked })} />
          <span>{m.backups_twiz_use_tls()}</span>
        </label>
        {#if cfg.use_ssl}
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-ca">{m.backups_twiz_ca_label()}</label>
            <span class="wiz-field-hint">{m.backups_twiz_ca_hint()}</span>
            <textarea id="t-ca" class="wiz-input wiz-input-mono" rows="3" spellcheck="false" placeholder="-----BEGIN CERTIFICATE-----" value={cfg.ca_cert ?? ''} oninput={(e) => (cfg = { ...cfg, ca_cert: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
          </div>
        {/if}
      {/if}
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  Step 2 — Test                                                  -->
  <!-- ============================================================== -->
  {#if step === 2}
    <div class="wiz-stack">
      <div class="wiz-test-card">
        <div class="wiz-test-head">
          <span class="wiz-target-glyph wiz-target-glyph-lg">{meta.glyph}</span>
          <div class="wiz-test-head-text">
            <div class="font-mono wiz-test-name">{name}</div>
            <div class="font-mono wiz-test-meta">{meta.label}</div>
          </div>
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={runTest} disabled={testState === 'testing'}>
            <RefreshCw size={11} strokeWidth={1.5} class={testState === 'testing' ? 'ed-spin' : ''} />
            {testState === 'testing' ? m.backups_testing() : m.backups_twiz_run_test()}
          </button>
        </div>

        <div class="wiz-test-checks">
          {#each checks as c (c.id)}
            <div class="wiz-test-row" class:ok={c.state === 'ok'} class:fail={c.state === 'fail'} class:running={c.state === 'running'}>
              {#if c.state === 'ok'}
                <span class="wiz-test-mark wiz-test-mark-ok"><Check size={10} strokeWidth={2} /></span>
              {:else if c.state === 'fail'}
                <span class="wiz-test-mark wiz-test-mark-fail"><X size={10} strokeWidth={2} /></span>
              {:else if c.state === 'running'}
                <span class="wiz-test-mark wiz-test-mark-running"></span>
              {:else}
                <span class="wiz-test-mark wiz-test-mark-idle"></span>
              {/if}
              <span class="wiz-test-label">{c.label}</span>
              <span class="font-mono wiz-test-state">
                {#if c.state === 'ok'}{m.backups_check_ok()}
                {:else if c.state === 'fail'}{m.backups_run_failed()}
                {:else if c.state === 'running'}…
                {:else}—{/if}
              </span>
            </div>
          {/each}
        </div>

        {#if testState === 'ok' && testResult}
          <div class="wiz-info-box wiz-info-success" style="margin-top: 14px;">
            <Check size={12} strokeWidth={1.8} class="wiz-info-icon" />
            <span>
              {m.backups_twiz_roundtrip_before()} <span class="font-mono">{testDurationMs}ms</span>{m.backups_twiz_roundtrip_after()}
              {#if testResult.total_bytes && testResult.total_bytes > 0}
                {m.backups_twiz_storage()} <span class="font-mono">{fmtBytes(testResult.free_bytes, '—')}</span> {m.backups_twiz_free_of()} <span class="font-mono">{fmtBytes(testResult.total_bytes, '—')}</span>.
              {:else}
                {m.backups_twiz_ready()}
              {/if}
            </span>
          </div>
        {:else if testState === 'fail' && testResult}
          <div class="wiz-info-box wiz-info-fail" style="margin-top: 14px;">
            <AlertTriangle size={12} strokeWidth={1.5} class="wiz-info-icon" />
            <span>
              <strong>{m.backups_twiz_test_failed()}</strong>
              <span class="font-mono wiz-test-err">{testResult.error || m.backups_unknown_error()}</span>
            </span>
            {#if hostKeyChanged}
              <button type="button" class="dm-btn dm-btn-danger dm-btn-xs" onclick={acceptHostKey} disabled={acceptingKey} title={m.backups_twiz_trust_key_title()}>
                {m.backups_twiz_accept_new_host_key()}
              </button>
            {/if}
          </div>
        {:else if testState === 'idle'}
          <p class="wiz-test-hint font-mono">{m.backups_twiz_idle_hint()}</p>
        {/if}
      </div>
    </div>
  {/if}
</WizardShell>

<style>
  .wiz-check { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--fg-muted, inherit); }
  .wiz-stack { display: flex; flex-direction: column; gap: 18px; }
  .wiz-grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }

  .wiz-field { display: flex; flex-direction: column; gap: 4px; }
  .wiz-field-label {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .wiz-field-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-bottom: 4px;
    line-height: 1.5;
  }
  .wiz-input {
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-elevated);
    color: var(--fg);
    font-size: 13px;
    transition: border-color 120ms;
  }
  .wiz-input:focus { outline: none; border-color: var(--accent); }
  .wiz-input-mono { font-family: var(--font-mono); }

  .wiz-radio-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 10px;
  }
  .wiz-radio-card {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    cursor: pointer;
    text-align: left;
    color: var(--fg-muted);
    transition: border-color 120ms, background 120ms, color 120ms;
  }
  .wiz-radio-card:hover { border-color: var(--border-strong); color: var(--fg); }
  .wiz-radio-card.active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 8%, transparent);
    color: var(--accent-fg);
  }
  .wiz-radio-card-label { font-size: 13px; color: var(--fg); }
  .wiz-radio-card.active .wiz-radio-card-label { color: var(--accent-fg); }
  .wiz-radio-card-meta { font-size: 11px; color: var(--fg-subtle); line-height: 1.5; }

  .wiz-target-glyph {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
  }
  .wiz-target-glyph-lg { width: 40px; height: 40px; font-size: 12px; }

  /* Info-box (success / fail) */
  .wiz-info-box {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    color: var(--fg-muted);
    font-size: 12px;
    line-height: 1.55;
  }
  :global(.wiz-info-icon) { flex-shrink: 0; margin-top: 3px; }
  .wiz-info-box em { color: var(--fg); font-style: normal; }
  .wiz-info-success {
    border-color: color-mix(in srgb, var(--color-success-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 8%, transparent);
  }
  .wiz-info-success :global(.wiz-info-icon) { color: var(--color-success-400); }
  .wiz-info-fail {
    border-color: color-mix(in srgb, var(--color-danger-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    color: var(--color-danger-400);
  }
  .wiz-info-fail :global(.wiz-info-icon) { color: var(--color-danger-400); }
  .wiz-info-fail strong { color: var(--color-danger-400); }
  .wiz-test-err {
    word-break: break-all;
    margin-left: 4px;
    color: var(--fg);
  }

  /* Test card layout */
  .wiz-test-card {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-elevated);
    padding: 18px 20px 20px;
  }
  .wiz-test-head {
    display: flex;
    align-items: center;
    gap: 14px;
    padding-bottom: 14px;
    border-bottom: 1px solid var(--border-subtle);
    margin-bottom: 14px;
  }
  .wiz-test-head-text { flex: 1; min-width: 0; }
  .wiz-test-name { font-size: 14px; color: var(--fg); }
  .wiz-test-meta { font-size: 11px; color: var(--fg-subtle); margin-top: 2px; }

  .wiz-test-checks {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .wiz-test-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 12px;
    border-radius: 4px;
    background: var(--surface);
    transition: background 120ms;
  }
  .wiz-test-row.ok { background: color-mix(in srgb, var(--color-success-500) 6%, transparent); }
  .wiz-test-row.fail { background: color-mix(in srgb, var(--color-danger-500) 8%, transparent); }
  .wiz-test-row.running { background: color-mix(in srgb, var(--accent) 8%, transparent); }

  .wiz-test-mark {
    width: 16px;
    height: 16px;
    border-radius: 8px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .wiz-test-mark-idle {
    background: var(--bg-elevated);
    border: 1px dashed var(--border);
  }
  .wiz-test-mark-running {
    background: color-mix(in srgb, var(--accent) 22%, transparent);
    border: 1px solid var(--accent);
    animation: wiz-test-pulse 0.8s ease-in-out infinite;
  }
  @keyframes wiz-test-pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.5; }
  }
  .wiz-test-mark-ok {
    background: color-mix(in srgb, var(--color-success-500) 22%, transparent);
    color: var(--color-success-400);
  }
  .wiz-test-mark-fail {
    background: color-mix(in srgb, var(--color-danger-500) 22%, transparent);
    color: var(--color-danger-400);
  }
  .wiz-test-label { flex: 1; font-size: 12.5px; color: var(--fg); }
  .wiz-test-state {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-left: auto;
  }
  .wiz-test-row.ok .wiz-test-state { color: var(--color-success-400); }
  .wiz-test-row.fail .wiz-test-state { color: var(--color-danger-400); }

  .wiz-test-hint {
    margin: 14px 0 0;
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.55;
  }
</style>
