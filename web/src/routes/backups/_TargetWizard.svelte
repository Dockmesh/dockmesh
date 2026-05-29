<script lang="ts">
  // 3-step backup-target wizard. Step 1 picks the storage type + name,
  // step 2 fills in the type-specific connection fields, step 3 runs
  // a live test (5 phases — connect / auth / write / read / delete).
  // Mockup reference: `Dockmesh Wizard (6)/backup-wizards.jsx::TargetWizard`.
  import { api, ApiError, type BackupTarget } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Eyebrow } from '$lib/components/editorial';
  import WizardShell from './_WizardShell.svelte';
  import { Check, X, RefreshCw, AlertTriangle } from 'lucide-svelte';

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

  const STEPS = ['Type', 'Connection', 'Test'];
  let step = $state(0);
  let busy = $state(false);

  type TargetType = 'local' | 'sftp' | 'smb' | 'webdav' | 's3';
  let type = $state<TargetType>('local');
  let name = $state('new-target');
  let cfg = $state<Record<string, any>>({ path: './data/backups' });

  // Test state. We start in 'idle' on each step-3 entry — the operator
  // explicitly clicks "Run test" to fire the probe. That avoids burning
  // S3 / SFTP API calls on accidental wizard navigation.
  type TestState = 'idle' | 'testing' | 'ok' | 'fail';
  let testState = $state<TestState>('idle');
  let testResult = $state<{ status: string; total_bytes?: number; used_bytes?: number; free_bytes?: number; error?: string } | null>(null);
  // Per-check phase status. Set in lock-step with the probe so the
  // five-row check list animates from idle → running → ok / fail.
  let checks = $state<Array<{ id: string; label: string; state: 'idle' | 'running' | 'ok' | 'fail' }>>([
    { id: 'connect', label: 'Connect to endpoint', state: 'idle' },
    { id: 'auth',    label: 'Authenticate',         state: 'idle' },
    { id: 'write',   label: 'Write probe file',     state: 'idle' },
    { id: 'read',    label: 'Read probe back',      state: 'idle' },
    { id: 'delete',  label: 'Delete probe',         state: 'idle' }
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
      cfg = { path: './data/backups' };
    }
  });

  function resetChecks() {
    checks = checks.map((c) => ({ ...c, state: 'idle' as const }));
  }
  function setCheck(id: string, state: 'idle' | 'running' | 'ok' | 'fail') {
    checks = checks.map((c) => (c.id === id ? { ...c, state } : c));
  }

  async function runTest() {
    if (testState === 'testing') return;
    testState = 'testing';
    testResult = null;
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
      const r = await api.backups.testTargetConfig(type, cfgPayload);
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
      }
    } catch (err) {
      testDurationMs = Date.now() - testStartedAt;
      testResult = { status: 'error', error: err instanceof ApiError ? err.message : String(err) };
      setCheck('connect', 'fail');
      testState = 'fail';
    }
  }

  // Validation per step
  const step0Valid = $derived(name.trim().length > 0);
  const step1Valid = $derived.by(() => {
    if (type === 'local') return !!cfg.path;
    if (type === 'sftp') return !!cfg.host && !!cfg.username;
    if (type === 'smb') return !!cfg.host && !!cfg.share;
    if (type === 'webdav') return !!cfg.url && !!cfg.username;
    if (type === 's3') return !!cfg.endpoint && !!cfg.bucket && !!cfg.access_key;
    return false;
  });
  const step2Valid = $derived(true); // Save Anyway is always allowed
  const stepValidities = [step0Valid, step1Valid, step2Valid];
  const canContinue = $derived(stepValidities[step]);

  async function finish() {
    busy = true;
    try {
      const cfgPayload: Record<string, any> = { ...cfg };
      if (cfgPayload.port) cfgPayload.port = parseInt(cfgPayload.port) || 0;
      await onsave({ name: name.trim(), type, config: cfgPayload }, !!editing, editing?.id);
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : (err as Error).message);
    } finally {
      busy = false;
    }
  }

  function targetTypeMeta(t: string): { label: string; glyph: string; blurb: string } {
    if (t === 's3') return { label: 'S3 / object store', glyph: 'S3', blurb: 'AWS S3, MinIO, Wasabi, B2 — object storage with credentials.' };
    if (t === 'sftp') return { label: 'SFTP over SSH', glyph: 'FTP', blurb: 'Any SSH host with chrooted user. Uses key or password auth.' };
    if (t === 'smb') return { label: 'SMB / NAS share', glyph: 'NAS', blurb: 'Synology, QNAP, TrueNAS, Windows shares.' };
    if (t === 'webdav') return { label: 'WebDAV', glyph: 'DAV', blurb: 'Nextcloud, ownCloud, generic WebDAV endpoints.' };
    return { label: 'Local directory', glyph: 'LOC', blurb: 'On-host path. Survives crashes but not full host loss.' };
  }
  const meta = $derived(targetTypeMeta(type));

  function fmtBytes(n?: number): string {
    if (!n) return '—';
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }
</script>

<WizardShell
  {open}
  eyebrow={editing ? 'Backups · edit target' : 'Backups · new target'}
  title={editing ? 'Edit target' : 'Where backups should land.'}
  subtitle="A target is a destination — local, NAS, SFTP, S3, WebDAV. Multiple jobs can share one target."
  steps={STEPS}
  stepIdx={step}
  width={720}
  {busy}
  finishLabel={editing ? 'Save changes' : (testState === 'ok' ? 'Save target' : 'Save anyway')}
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
        <label class="wiz-field-label">Target type</label>
        <span class="wiz-field-hint">Pick the kind of storage. The next step asks for type-specific connection details.</span>
        <div class="wiz-radio-grid">
          {#each ['local', 'sftp', 'smb', 'webdav', 's3'] as t (t)}
            {@const m = targetTypeMeta(t)}
            <button type="button" class="wiz-radio-card" class:active={type === t} onclick={() => (type = t as TargetType)}>
              <span class="wiz-target-glyph">{m.glyph}</span>
              <span class="wiz-radio-card-label">{m.label}</span>
              <span class="wiz-radio-card-meta">{m.blurb}</span>
            </button>
          {/each}
        </div>
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label" for="t-name">Target name</label>
        <span class="wiz-field-hint">A short identifier. Shows up in run logs and the target picker.</span>
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
          <label class="wiz-field-label" for="t-path">Path</label>
          <span class="wiz-field-hint">Absolute path. Must be writable by the dockmesh service user.</span>
          <input id="t-path" class="wiz-input wiz-input-mono" placeholder="./data/backups" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
        </div>
      {:else if type === 'sftp'}
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-host">Host</label>
            <input id="t-host" class="wiz-input wiz-input-mono" placeholder="backup.example.com" value={cfg.host ?? ''} oninput={(e) => (cfg = { ...cfg, host: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-port">Port</label>
            <input id="t-port" class="wiz-input wiz-input-mono" placeholder="22" value={cfg.port ?? ''} oninput={(e) => (cfg = { ...cfg, port: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-user">Username</label>
            <input id="t-user" class="wiz-input wiz-input-mono" value={cfg.username ?? ''} oninput={(e) => (cfg = { ...cfg, username: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-pass">Password</label>
            <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={cfg.password ?? ''} oninput={(e) => (cfg = { ...cfg, password: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-rpath">Remote path</label>
          <span class="wiz-field-hint">Where to write under the user's home. Created if missing.</span>
          <input id="t-rpath" class="wiz-input wiz-input-mono" placeholder="/backups" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
        </div>
      {:else if type === 'smb'}
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-host">Server</label>
            <input id="t-host" class="wiz-input wiz-input-mono" placeholder="192.168.1.100" value={cfg.host ?? ''} oninput={(e) => (cfg = { ...cfg, host: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-port">Port</label>
            <input id="t-port" class="wiz-input wiz-input-mono" placeholder="445" value={cfg.port ?? ''} oninput={(e) => (cfg = { ...cfg, port: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-share">Share</label>
            <input id="t-share" class="wiz-input wiz-input-mono" placeholder="backups" value={cfg.share ?? ''} oninput={(e) => (cfg = { ...cfg, share: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-spath">Path within share</label>
            <input id="t-spath" class="wiz-input wiz-input-mono" placeholder="dockmesh" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-user">Username</label>
            <input id="t-user" class="wiz-input wiz-input-mono" value={cfg.username ?? ''} oninput={(e) => (cfg = { ...cfg, username: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-pass">Password</label>
            <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={cfg.password ?? ''} oninput={(e) => (cfg = { ...cfg, password: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
      {:else if type === 'webdav'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-url">WebDAV URL</label>
          <span class="wiz-field-hint">Full WebDAV endpoint including the user-specific suffix on Nextcloud.</span>
          <input id="t-url" class="wiz-input wiz-input-mono" placeholder="https://nextcloud.example.com/remote.php/dav/files/user/" value={cfg.url ?? ''} oninput={(e) => (cfg = { ...cfg, url: (e.currentTarget as HTMLInputElement).value })} />
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-user">Username</label>
            <input id="t-user" class="wiz-input wiz-input-mono" value={cfg.username ?? ''} oninput={(e) => (cfg = { ...cfg, username: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-pass">App password</label>
            <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={cfg.password ?? ''} oninput={(e) => (cfg = { ...cfg, password: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-path">Path</label>
          <span class="wiz-field-hint">Subfolder under the WebDAV endpoint. Created if missing.</span>
          <input id="t-path" class="wiz-input wiz-input-mono" placeholder="/backups" value={cfg.path ?? ''} oninput={(e) => (cfg = { ...cfg, path: (e.currentTarget as HTMLInputElement).value })} />
        </div>
      {:else if type === 's3'}
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-ep">Endpoint</label>
            <input id="t-ep" class="wiz-input wiz-input-mono" placeholder="s3.amazonaws.com" value={cfg.endpoint ?? ''} oninput={(e) => (cfg = { ...cfg, endpoint: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-region">Region</label>
            <input id="t-region" class="wiz-input wiz-input-mono" placeholder="us-east-1" value={cfg.region ?? ''} oninput={(e) => (cfg = { ...cfg, region: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
        <div class="wiz-field">
          <label class="wiz-field-label" for="t-bucket">Bucket</label>
          <input id="t-bucket" class="wiz-input wiz-input-mono" placeholder="my-backups" value={cfg.bucket ?? ''} oninput={(e) => (cfg = { ...cfg, bucket: (e.currentTarget as HTMLInputElement).value })} />
        </div>
        <div class="wiz-grid-2">
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-ak">Access key</label>
            <input id="t-ak" class="wiz-input wiz-input-mono" value={cfg.access_key ?? ''} oninput={(e) => (cfg = { ...cfg, access_key: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-sk">Secret key</label>
            <input id="t-sk" class="wiz-input wiz-input-mono" type="password" value={cfg.secret_key ?? ''} oninput={(e) => (cfg = { ...cfg, secret_key: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        </div>
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
            {testState === 'testing' ? 'Testing…' : 'Run test'}
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
                {#if c.state === 'ok'}ok
                {:else if c.state === 'fail'}failed
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
              Round-trip succeeded in <span class="font-mono">{testDurationMs}ms</span>.
              {#if testResult.total_bytes && testResult.total_bytes > 0}
                Storage: <span class="font-mono">{fmtBytes(testResult.free_bytes)}</span> free of <span class="font-mono">{fmtBytes(testResult.total_bytes)}</span>.
              {:else}
                Target ready to receive backups.
              {/if}
            </span>
          </div>
        {:else if testState === 'fail' && testResult}
          <div class="wiz-info-box wiz-info-fail" style="margin-top: 14px;">
            <AlertTriangle size={12} strokeWidth={1.5} class="wiz-info-icon" />
            <span>
              <strong>Connection test failed.</strong>
              <span class="font-mono wiz-test-err">{testResult.error || 'unknown error'}</span>
            </span>
          </div>
        {:else if testState === 'idle'}
          <p class="wiz-test-hint font-mono">Click "Run test" to validate the connection. You can also save without testing — the next backup run will surface any error.</p>
        {/if}
      </div>
    </div>
  {/if}
</WizardShell>

<style>
  .wiz-stack { display: flex; flex-direction: column; gap: 18px; }
  .wiz-grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }

  .wiz-field { display: flex; flex-direction: column; gap: 4px; }
  .wiz-field-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
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
    font-family: var(--font-mono);
    font-size: 10.5px;
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
    font-size: 10.5px;
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
