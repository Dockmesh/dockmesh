<script lang="ts">
  // Install wizard: one question per step, a review of every answer before
  // anything is written, then the live install log and the credentials.
  import { onMount, onDestroy } from 'svelte';
  import { Check, X, TriangleAlert, Copy, Eye, EyeOff, ArrowRight, RotateCcw } from 'lucide-svelte';
  import { copyToClipboard as clipboardCopy } from '$lib/clipboard';
  import { uploadSystemBackup } from '$lib/api';
  import { waitForRestart } from '$lib/restart';
  import { fmtVersion } from '$lib/version';
  import Button from '$lib/components/ui/Button.svelte';
  import StatusChip from '$lib/components/ui/StatusChip.svelte';

  type Step = 1 | 2 | 3 | 4 | 5 | 6 | 7;

  const STEPS: Array<{ n: Step; short: string }> = [
    { n: 1, short: 'Welcome' },
    { n: 2, short: 'Data' },
    { n: 3, short: 'Service user' },
    { n: 4, short: 'Admin' },
    { n: 5, short: 'URL' },
    { n: 6, short: 'Review' },
    { n: 7, short: 'Done' }
  ];

  const HEADERS: Record<Step, { title: string; sub: string }> = {
    1: {
      title: 'Set up Dockmesh',
      sub: 'Seven short steps, about a minute. Nothing is written until you confirm on the review step.'
    },
    2: {
      title: 'Where should Dockmesh keep its data?',
      sub: 'The database, secrets, CA certificates and stack files live in this directory. Pick a volume you can grow.'
    },
    3: {
      title: 'Which user should Dockmesh run as?',
      sub: 'The system account of the Dockmesh service. It needs access to the Docker socket.'
    },
    4: {
      title: 'Create the first admin',
      sub: 'The login for the dashboard. You can add single sign-on and more users later.'
    },
    5: {
      title: 'Under which address is this server reachable?',
      sub: 'Agents on other hosts connect back to this URL. Sign-in redirects and notification links use it too.'
    },
    6: {
      title: 'Review and install',
      sub: 'Nothing has been written yet. Install creates the user if needed, writes the env file, seeds the database and starts the service.'
    },
    7: {
      title: 'Installing Dockmesh',
      sub: 'Keep this tab open until the credentials appear and copy them somewhere safe.'
    }
  };

  // "Restore from backup" path: instead of a new install, bring back a
  // system backup (with the key file exported from the original install
  // if the backup is encrypted). The server restarts into it.
  let restoreOpen = $state(false);
  let restoreFile = $state<File | null>(null);
  let restoreKey = $state<File | null>(null);
  let restorePhase = $state<'idle' | 'uploading' | 'restarting' | 'stuck'>('idle');
  let restoreError = $state('');
  let restoreChecks = $state<{ name: string; status: string; message?: string }[]>([]);

  async function restoreFromBackup() {
    if (!restoreFile) return;
    restorePhase = 'uploading';
    restoreError = '';
    restoreChecks = [];
    try {
      const res = await uploadSystemBackup('/api/v1/setup/restore', restoreFile, restoreKey, null, { 'X-Dockmesh-Setup': '1' });
      if (!res.restarting) {
        restoreError = res.error ?? res.sanity?.summary ?? 'the backup failed the restore checks';
        restoreChecks = res.sanity?.checks ?? [];
        restorePhase = 'idle';
        return;
      }
    } catch (err) {
      restoreError = err instanceof Error ? err.message : String(err);
      restorePhase = 'idle';
      return;
    }
    restorePhase = 'restarting';
    if (await waitForRestart()) {
      window.location.href = '/login';
    } else {
      restorePhase = 'stuck';
    }
  }

  let step = $state<Step>(1);
  const head = $derived.by(() => {
    if (step !== 7) return HEADERS[step];
    if (installFailed) return { title: 'The install stopped', sub: 'The log shows the step that failed. Fix the cause and run the installer again.' };
    if (credsRevealed) return { title: 'Dockmesh is installed', sub: 'Copy the sign-in below somewhere safe, then open Dockmesh.' };
    return HEADERS[7];
  });
  let completed = $state<Set<Step>>(new Set());

  // Where install.sh puts the env file and the systemd unit points.
  let dataDir = $state('/var/lib/dockmesh');
  // recommended: the `dockmesh` user install.sh created (no input);
  // existing: another system user, checked against /etc/passwd;
  // new: a user the commit creates under a free name.
  let svcMode = $state<'recommended' | 'existing' | 'new'>('recommended');
  let svcExistingUser = $state('');
  let svcNewUser = $state('');
  let svcAddToDocker = $state(true);
  // Fixed so it always matches the user install.sh created.
  const RECOMMENDED_USER = 'dockmesh';

  let adminUsername = $state('admin');
  let adminPassword = $state('');
  let adminEmail = $state('');
  let adminShowPassword = $state(false);

  // Replaced on mount by the origin the browser used to reach the wizard,
  // which the connection test can always reach.
  let publicUrl = $state('http://localhost:8080');

  let installing = $state(false);
  let installMsg = $state('');

  // ----- Step 1: preflight --------------------------------------------------
  type PreflightCheck = {
    key: string;
    label: string;
    value: string;
    status: 'ok' | 'warn' | 'fail';
    message?: string;
    hint?: string;
  };
  let preflightChecks = $state<PreflightCheck[]>([]);
  let preflightLoading = $state(true);
  let preflightError = $state<string | null>(null);
  const preflightHasFail = $derived(preflightChecks.some((c) => c.status === 'fail'));
  const preflightWarnings = $derived(preflightChecks.filter((c) => c.status === 'warn'));

  async function loadPreflight() {
    preflightLoading = true;
    preflightError = null;
    try {
      const r = await fetch('/api/v1/setup/preflight');
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const data = await r.json();
      preflightChecks = data.checks ?? [];
    } catch (e) {
      preflightError = e instanceof Error ? e.message : String(e);
    } finally {
      preflightLoading = false;
    }
  }

  // ----- Header: live server info -------------------------------------------
  let serverInfo = $state<{ version: string; os: string; ip: string; docker_version: string; uptime_secs: number } | null>(null);
  let serverInfoTimer: ReturnType<typeof setInterval> | null = null;

  async function loadServerInfo() {
    try {
      const r = await fetch('/api/v1/setup/server-info');
      if (!r.ok) return;
      serverInfo = await r.json();
    } catch { /* keep the last value */ }
  }

  // ----- Step 2: data dir, validated 250 ms after typing stops -------------
  let dataDirStatus = $state<{ kind: 'ok' | 'warn' | 'fail'; text: string }>({
    kind: 'fail',
    text: 'path required'
  });
  let dataDirDebounce: ReturnType<typeof setTimeout> | null = null;

  async function validateDataDirRemote(path: string) {
    try {
      const r = await fetch('/api/v1/setup/validate-data-dir', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Dockmesh-Setup': '1' },
        body: JSON.stringify({ path })
      });
      if (!r.ok) {
        dataDirStatus = { kind: 'fail', text: 'validation failed (HTTP ' + r.status + ')' };
        return;
      }
      const d = await r.json();
      dataDirStatus = { kind: d.status, text: d.message };
    } catch (e) {
      dataDirStatus = {
        kind: 'fail',
        text: 'validation failed: ' + (e instanceof Error ? e.message : String(e))
      };
    }
  }

  $effect(() => {
    const path = dataDir;
    if (dataDirDebounce) clearTimeout(dataDirDebounce);
    if (!path || path.trim() === '') {
      dataDirStatus = { kind: 'fail', text: 'path required' };
      return;
    }
    dataDirDebounce = setTimeout(() => validateDataDirRemote(path), 250);
  });

  // ----- Step 3: service user, validated 250 ms after typing stops ---------
  let svcCheck = $state<{ kind: 'ok' | 'warn' | 'fail' | 'idle'; text: string }>({
    kind: 'idle',
    text: ''
  });
  let svcDebounce: ReturnType<typeof setTimeout> | null = null;

  async function validateUserRemote(mode: 'existing' | 'new', username: string) {
    try {
      const r = await fetch('/api/v1/setup/validate-user', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Dockmesh-Setup': '1' },
        body: JSON.stringify({ mode: mode === 'new' ? 'create' : 'existing', username })
      });
      if (!r.ok) {
        svcCheck = { kind: 'fail', text: 'validation failed (HTTP ' + r.status + ')' };
        return;
      }
      const d = await r.json();
      svcCheck = { kind: d.status, text: d.message };
    } catch (e) {
      svcCheck = { kind: 'fail', text: 'validation failed: ' + (e instanceof Error ? e.message : String(e)) };
    }
  }

  $effect(() => {
    const mode = svcMode;
    let name: string;
    let backendMode: 'existing' | 'new';
    if (mode === 'recommended') {
      name = RECOMMENDED_USER;
      backendMode = 'existing';
    } else if (mode === 'existing') {
      name = svcExistingUser.trim();
      backendMode = 'existing';
    } else {
      name = svcNewUser.trim();
      backendMode = 'new';
    }
    if (svcDebounce) clearTimeout(svcDebounce);
    if (!name) {
      svcCheck = { kind: 'idle', text: '' };
      return;
    }
    svcDebounce = setTimeout(() => validateUserRemote(backendMode, name), 250);
  });

  const svcBlockNext = $derived.by(() => {
    if (svcMode === 'recommended') return svcCheck.kind === 'fail' || svcCheck.kind === 'idle';
    if (svcMode === 'existing') return svcExistingUser.trim().length === 0 || svcCheck.kind === 'fail' || svcCheck.kind === 'idle';
    return svcNewUser.trim().length === 0 || svcCheck.kind === 'fail' || svcCheck.kind === 'idle';
  });

  // ----- Step 4: admin --------------------------------------------------------
  function scorePassword(pw: string): { score: number; label: string } {
    if (!pw) return { score: 0, label: '—' };
    let s = 0;
    if (pw.length >= 8) s++;
    if (pw.length >= 14) s++;
    if (/[A-Z]/.test(pw) && /[a-z]/.test(pw) && /\d/.test(pw)) s++;
    if (/[^A-Za-z0-9]/.test(pw) && pw.length >= 12) s++;
    s = Math.min(4, s);
    return { score: s, label: ['—', 'weak', 'ok', 'good', 'strong'][s] };
  }
  const strength = $derived(scorePassword(adminPassword));
  const adminBlockNext = $derived(adminUsername.trim().length === 0 || adminPassword.length < 8);

  function generatePassword() {
    const chars = 'abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%^&*';
    const arr = new Uint32Array(18);
    crypto.getRandomValues(arr);
    let out = '';
    for (let i = 0; i < 18; i++) out += chars[arr[i] % chars.length];
    adminPassword = out;
    adminShowPassword = true;
  }

  // ----- Step 5: public URL ---------------------------------------------------
  let urlTest = $state<{ state: 'idle' | 'loading' | 'ok' | 'fail'; ms?: number; reason?: string }>({ state: 'idle' });
  const urlBlockNext = $derived.by(() => {
    try { new URL(publicUrl); return false; } catch { return true; }
  });
  async function runUrlTest() {
    urlTest = { state: 'loading' };
    try {
      const r = await fetch('/api/v1/setup/test-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Dockmesh-Setup': '1' },
        body: JSON.stringify({ url: publicUrl, expect_health: true })
      });
      if (!r.ok) {
        urlTest = { state: 'fail', reason: 'HTTP ' + r.status };
        return;
      }
      const d = await r.json();
      if (d.status === 'ok') urlTest = { state: 'ok', ms: d.latency_ms ?? 0 };
      else urlTest = { state: 'fail', reason: d.message ?? 'unreachable' };
    } catch (e) {
      urlTest = { state: 'fail', reason: e instanceof Error ? e.message : String(e) };
    }
  }
  $effect(() => { if (publicUrl) urlTest = { state: 'idle' }; });

  // ----- Navigation -----------------------------------------------------------
  function goNext() {
    if (!completed.has(step)) completed = new Set([...completed, step]);
    if (step < 7) step = (step + 1) as Step;
    window.scrollTo({ top: 0, behavior: 'instant' });
  }
  function goBack() {
    if (step > 1) step = (step - 1) as Step;
    window.scrollTo({ top: 0, behavior: 'instant' });
  }
  function jumpTo(n: Step) {
    step = n;
    completed = new Set(Array.from(completed).filter((x) => x < n));
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  // ?step=N keeps the operator on the same step across a reload.
  $effect(() => {
    const u = new URL(window.location.href);
    u.searchParams.set('step', String(step));
    history.replaceState(null, '', u.toString());
  });

  // ----- Step 6 → install, streamed over SSE ---------------------------------
  type StreamEvent = { ts: string; step: string; message: string; status: string };
  let streamLines = $state<StreamEvent[]>([]);
  let installFailed = $state<string | null>(null);
  let eventSource: EventSource | null = null;

  async function runInstall() {
    installing = true;
    installMsg = 'Submitting…';
    streamLines = [];
    installFailed = null;
    try {
      const r = await fetch('/api/v1/setup/commit', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Dockmesh-Setup': '1' },
        body: JSON.stringify({
          data_dir: dataDir,
          service_user: {
            mode: svcMode === 'new' ? 'create' : 'existing',
            username:
              svcMode === 'recommended' ? RECOMMENDED_USER
              : svcMode === 'new' ? svcNewUser.trim()
              : svcExistingUser.trim(),
            add_to_docker_group: svcMode === 'new' && svcAddToDocker
          },
          admin: {
            username: adminUsername,
            password: adminPassword,
            email: adminEmail
          },
          public_url: publicUrl
        })
      });
      if (!r.ok) {
        const body = await r.json().catch(() => ({}));
        installing = false;
        installMsg = '';
        installFailed = body?.error ?? `HTTP ${r.status}`;
        return;
      }
      const { run_id, stream_url } = await r.json();
      completed = new Set([1, 2, 3, 4, 5, 6]);
      step = 7;
      window.scrollTo({ top: 0, behavior: 'instant' });
      subscribeToStream(stream_url || `/api/v1/setup/stream/${run_id}`);
    } catch (e) {
      installing = false;
      installMsg = '';
      installFailed = e instanceof Error ? e.message : String(e);
    }
  }

  function subscribeToStream(url: string) {
    if (eventSource) eventSource.close();
    eventSource = new EventSource(url);
    eventSource.addEventListener('line', (ev) => {
      try {
        const data: StreamEvent = JSON.parse((ev as MessageEvent).data);
        streamLines = [...streamLines, data];
        if (data.status === 'fail') installFailed = data.message;
      } catch { /* ignore malformed frame */ }
    });
    eventSource.addEventListener('end', () => {
      eventSource?.close();
      eventSource = null;
      installing = false;
      installMsg = '';
      // A failed install shows its error instead of the credentials.
      if (!installFailed) credsRevealed = true;
    });
    eventSource.onerror = () => {
      eventSource?.close();
      eventSource = null;
      installing = false;
      installMsg = '';
      if (!installFailed && streamLines.length === 0) {
        installFailed = 'connection to install stream lost';
      }
    };
  }
  onDestroy(() => { if (eventSource) eventSource.close(); });

  // ----- Step 7: credentials --------------------------------------------------
  let credsRevealed = $state(false);
  let copiedKey = $state<string | null>(null);
  let credShowPw = $state(false);
  function copyToClipboard(key: string, value: string) {
    void clipboardCopy(value);
    copiedKey = key;
    setTimeout(() => { if (copiedKey === key) copiedKey = null; }, 1400);
  }

  const svcSummary = $derived(
    svcMode === 'recommended'
      ? `${RECOMMENDED_USER} (recommended)`
      : svcMode === 'new'
        ? `${svcNewUser} (new${svcAddToDocker ? ', in docker group' : ''})`
        : svcExistingUser || '—'
  );

  onMount(async () => {
    // Setup may have finished meanwhile (restart, bookmark); a commit
    // would only return 410, so go to the dashboard instead.
    try {
      const r = await fetch('/api/v1/setup/status', { headers: { Accept: 'application/json' } });
      if (r.ok) {
        const s = await r.json();
        if (!s.active) {
          window.location.replace('/');
          return;
        }
      }
    } catch {
      // The commit endpoint rejects if setup really ended.
    }
    const param = new URLSearchParams(window.location.search).get('step');
    const n = parseInt(param || '1', 10);
    if (Number.isFinite(n) && n >= 1 && n <= 7) {
      step = n as Step;
      const arr: Step[] = [];
      for (let i = 1; i < n; i++) arr.push(i as Step);
      completed = new Set(arr);
    }
    if (typeof window !== 'undefined' && window.location?.origin) {
      publicUrl = window.location.origin;
    }
    loadServerInfo();
    serverInfoTimer = setInterval(loadServerInfo, 10_000);
    loadPreflight();
  });
  onDestroy(() => {
    if (serverInfoTimer) clearInterval(serverInfoTimer);
  });
</script>

<svelte:head>
  <title>Set up Dockmesh</title>
</svelte:head>

{#snippet mark(kind: string)}
  <span class="mk {kind}" aria-hidden="true">
    {#if kind === 'ok'}<Check size={13} strokeWidth={2.4} />
    {:else if kind === 'warn'}<TriangleAlert size={13} strokeWidth={2.2} />
    {:else if kind === 'fail'}<X size={13} strokeWidth={2.4} />
    {:else if kind === 'done'}<ArrowRight size={13} strokeWidth={2.2} />
    {:else}<span class="bullet"></span>{/if}
  </span>
{/snippet}

{#snippet checkLine(kind: string, text: string)}
  <div class="check {kind}" role="status">{@render mark(kind)}<span>{text}</span></div>
{/snippet}

<div class="sw">
  <header class="top">
    <div class="brand">
      <img src="/logo-mark.svg" alt="" width="22" height="22" />
      <b>Dockmesh</b>
      <span class="dim">/ setup</span>
    </div>
    <div class="meta dm-num" aria-label="This server">
      <span>{serverInfo ? fmtVersion(serverInfo.version) : '—'}</span>
      <span class="sep">·</span>
      <span>{serverInfo?.ip ?? '—'}</span>
      <span class="sep">·</span>
      <span>docker {serverInfo?.docker_version ?? '—'}</span>
    </div>
  </header>

  <main class="col">
    <nav aria-label="Setup steps">
      <ol class="stepper">
        {#each STEPS as s (s.n)}
          {@const done = completed.has(s.n) && step !== s.n}
          {@const cur = step === s.n}
          {@const reachable = (done || cur) && step !== 7 && !installing}
          <li class:done class:on={cur}>
            <button type="button" disabled={!reachable} aria-current={cur ? 'step' : undefined} onclick={() => reachable && jumpTo(s.n)}>
              <span class="n">{#if done}<Check size={11} strokeWidth={3} />{:else}{s.n}{/if}</span>
              <span class="l">{s.short}</span>
            </button>
          </li>
        {/each}
      </ol>
    </nav>

    <div class="head">
      <h1>{head.title}</h1>
      <p>{head.sub}</p>
    </div>

    <section class="panel">
      {#if step === 1}
        <h2 class="dm-h2">This server</h2>
        {#if preflightLoading}
          <div class="dm-empty">Checking this server…</div>
        {:else if preflightError}
          <div class="callout bad">
            <span>The server check failed: {preflightError}</span>
            <Button size="sm" variant="secondary" onclick={loadPreflight}><RotateCcw size={13} /> Check again</Button>
          </div>
        {:else}
          <div class="dm-tbl">
            {#each preflightChecks as c (c.key)}
              <div class="dm-tr pf">
                {@render mark(c.status)}
                <span>{c.label}</span>
                <span class="dm-num" class:warn={c.status === 'warn'} class:bad={c.status === 'fail'} title={c.value}>{c.value}</span>
              </div>
            {/each}
          </div>
          {#each preflightWarnings as w (w.key)}
            {#if w.message || w.hint}
              <p class="note">
                <b class="warn-t">{w.label}:</b>
                {w.message ?? ''}
                {#if w.hint}<a href={w.hint} target="_blank" rel="noopener">{w.hint}</a>{/if}
              </p>
            {/if}
          {/each}
          {#if preflightHasFail}
            <p class="note bad-t">Fix the failed checks on this server, then check again.</p>
          {/if}
        {/if}

        <div class="foot">
          <Button variant="ghost" size="sm" onclick={() => (restoreOpen = !restoreOpen)} aria-expanded={restoreOpen}>
            Restore from a backup instead
          </Button>
          <span class="dm-sp"></span>
          {#if !preflightLoading && !preflightError}
            <Button variant="ghost" size="sm" onclick={loadPreflight}><RotateCcw size={13} /> Check again</Button>
          {/if}
          <Button variant="primary" onclick={goNext} disabled={preflightHasFail || preflightLoading}>
            Begin setup <ArrowRight size={14} />
          </Button>
        </div>

        {#if restoreOpen}
          <div class="restore">
            <h2 class="dm-h2">Restore from a backup</h2>
            <p class="hint">
              Brings back a Dockmesh system backup: users, settings, keys and stacks. Volumes are restored
              afterwards, from the Backups page.
            </p>
            <div class="dm-field">
              <label for="rs-file">System backup (.tar.gz or .tar.gz.age)</label>
              <input id="rs-file" class="file" type="file" accept=".gz,.age,.tar" onchange={(e) => (restoreFile = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
            </div>
            <div class="dm-field">
              <label for="rs-key">Backup key file, if the backup is encrypted</label>
              <input id="rs-key" class="file" type="file" accept=".txt,.key" onchange={(e) => (restoreKey = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
              <span class="hint">On the old server: Settings → Encryption key → Export key.</span>
            </div>
            {#if restoreError}
              <div class="callout bad" role="alert">
                <div>
                  <div>{restoreError}</div>
                  {#each restoreChecks.filter((c) => c.status === 'fail') as c (c.name)}
                    <div>{c.name}: {c.message ?? ''}</div>
                  {/each}
                </div>
              </div>
            {/if}
            {#if restorePhase === 'restarting'}
              <p class="note">Restoring and restarting Dockmesh… You'll be sent to the login page; sign in with an account of the restored install.</p>
            {:else if restorePhase === 'stuck'}
              <div class="callout bad" role="alert">Dockmesh hasn't come back yet. If it isn't run by systemd or Docker, start it again by hand; the restore finishes on the next start.</div>
            {:else}
              <div>
                <Button variant="primary" size="sm" onclick={restoreFromBackup} disabled={!restoreFile} loading={restorePhase === 'uploading'}>
                  {restorePhase === 'uploading' ? 'Checking the backup…' : 'Restore'}
                </Button>
              </div>
            {/if}
          </div>
        {/if}

      {:else if step === 2}
        <div class="dm-field">
          <label for="data-dir">Data directory</label>
          <input id="data-dir" class="dm-input dm-mono" bind:value={dataDir} spellcheck="false" autocomplete="off" aria-invalid={dataDirStatus.kind === 'fail'} />
          {@render checkLine(dataDirStatus.kind, dataDirStatus.text)}
          <span class="hint">About 50 MB on a fresh install; grows with deploy history and the number of stacks.</span>
        </div>
        <div class="dm-field">
          <span class="lbl">Common locations</span>
          <div class="presets">
            <button type="button" class="preset" class:on={dataDir === '/var/lib/dockmesh'} onclick={() => (dataDir = '/var/lib/dockmesh')}>
              <span class="dm-mono">/var/lib/dockmesh</span><small>Linux, where services keep state</small>
            </button>
            <button type="button" class="preset" class:on={dataDir === '/usr/local/var/dockmesh'} onclick={() => (dataDir = '/usr/local/var/dockmesh')}>
              <span class="dm-mono">/usr/local/var/dockmesh</span><small>macOS, Homebrew layout</small>
            </button>
          </div>
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>Back</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={dataDirStatus.kind === 'fail'}>Continue</Button>
        </div>

      {:else if step === 3}
        <div class="choices" role="radiogroup" aria-label="Service user">
          <label class="radio-card" class:on={svcMode === 'recommended'}>
            <input type="radio" name="svc-mode" checked={svcMode === 'recommended'} onchange={() => (svcMode = 'recommended')} />
            <div class="rc-b">
              <div class="rc-t">Use the <span class="dm-mono">dockmesh</span> service user <StatusChip tone="acc" label="recommended" /></div>
              <div class="hint">Created by the install script, already in the <span class="dm-mono">docker</span> group, no shell login.</div>
              {#if svcMode === 'recommended' && svcCheck.kind !== 'idle' && svcCheck.text}
                {@render checkLine(svcCheck.kind, svcCheck.text)}
              {/if}
            </div>
          </label>

          <label class="radio-card" class:on={svcMode === 'existing'}>
            <input type="radio" name="svc-mode" checked={svcMode === 'existing'} onchange={() => (svcMode = 'existing')} />
            <div class="rc-b">
              <div class="rc-t">Use another existing user</div>
              <div class="hint">For a service account you already have, such as <span class="dm-mono">ops</span> or <span class="dm-mono">deploy</span>.</div>
              {#if svcMode === 'existing'}
                <div class="dm-field sub">
                  <label for="existing-user">Username</label>
                  <input id="existing-user" class="dm-input dm-mono" placeholder="ops" bind:value={svcExistingUser} aria-invalid={svcCheck.kind === 'fail'} spellcheck="false" autocomplete="off" />
                  {#if svcCheck.kind !== 'idle' && svcCheck.text}{@render checkLine(svcCheck.kind, svcCheck.text)}{/if}
                  <span class="hint">
                    Setup does not change the groups of an existing account. If it is not in the
                    <span class="dm-mono">docker</span> group yet, run
                    <span class="dm-mono">sudo usermod -aG docker {svcExistingUser.trim() || '<user>'}</span>.
                  </span>
                </div>
              {/if}
            </div>
          </label>

          <label class="radio-card" class:on={svcMode === 'new'}>
            <input type="radio" name="svc-mode" checked={svcMode === 'new'} onchange={() => (svcMode = 'new')} />
            <div class="rc-b">
              <div class="rc-t">Create a new user</div>
              <div class="hint">Under a name you choose, created when you install. The <span class="dm-mono">dockmesh</span> user stays as it is.</div>
              {#if svcMode === 'new'}
                <div class="dm-field sub">
                  <label for="new-user">Username</label>
                  <input id="new-user" class="dm-input dm-mono" placeholder="dockermaster" bind:value={svcNewUser} aria-invalid={svcCheck.kind === 'fail'} spellcheck="false" autocomplete="off" />
                  {#if svcCheck.kind !== 'idle' && svcCheck.text}{@render checkLine(svcCheck.kind, svcCheck.text)}{/if}
                  <span class="hint">Gets the next free uid and no login shell.</span>
                </div>
                <label class="cb">
                  <input type="checkbox" bind:checked={svcAddToDocker} />
                  <span>Add to the <span class="dm-mono">docker</span> group <span class="hint">needed to talk to the Docker daemon</span></span>
                </label>
              {/if}
            </div>
          </label>
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>Back</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={svcBlockNext}>Continue</Button>
        </div>

      {:else if step === 4}
        <div class="dm-field">
          <label for="admin-user">Username</label>
          <input id="admin-user" class="dm-input dm-mono" bind:value={adminUsername} spellcheck="false" autocomplete="off" />
          <span class="hint">A local admin with full rights. You can disable it later once single sign-on works.</span>
        </div>
        <div class="dm-field">
          <div class="lbl-row">
            <label for="admin-pw">Password</label>
            <button type="button" class="link" onclick={generatePassword}>Generate a strong password</button>
          </div>
          <div class="pw">
            <input id="admin-pw" type={adminShowPassword ? 'text' : 'password'} class="dm-input dm-mono" bind:value={adminPassword} autocomplete="new-password" aria-describedby="pw-strength" />
            <button type="button" class="eye" onclick={() => (adminShowPassword = !adminShowPassword)} aria-label={adminShowPassword ? 'Hide password' : 'Show password'}>
              {#if adminShowPassword}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
            </button>
          </div>
          <div class="strength" id="pw-strength">
            {#each [0, 1, 2, 3] as i (i)}
              <i class:on={strength.score > i} class="s{strength.score}"></i>
            {/each}
            <span class="dm-num dm-dim">{strength.label}</span>
          </div>
          <span class="hint">At least 8 characters.</span>
        </div>
        <div class="dm-field">
          <label for="admin-email">Email <span class="dm-dim">(optional)</span></label>
          <input id="admin-email" type="email" class="dm-input" placeholder="admin@example.com" bind:value={adminEmail} spellcheck="false" autocomplete="off" />
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>Back</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={adminBlockNext}>Continue</Button>
        </div>

      {:else if step === 5}
        <div class="dm-field">
          <label for="pub-url">Public URL</label>
          <div class="row">
            <input id="pub-url" class="dm-input dm-mono" bind:value={publicUrl} spellcheck="false" autocomplete="off" aria-invalid={urlBlockNext} />
            <Button variant="secondary" onclick={runUrlTest} loading={urlTest.state === 'loading'} disabled={urlBlockNext}>
              {urlTest.state === 'loading' ? 'Testing…' : 'Test connection'}
            </Button>
          </div>
          {#if urlTest.state === 'ok'}
            {@render checkLine('ok', `Reached this server in ${urlTest.ms} ms`)}
          {:else if urlTest.state === 'fail'}
            {@render checkLine('fail', `Could not reach it (${urlTest.reason})`)}
          {:else if urlBlockNext}
            {@render checkLine('fail', 'Not a valid URL')}
          {/if}
          <span class="hint">
            Saved as <span class="dm-mono">DOCKMESH_BASE_URL</span>. The LAN address is fine for now; once you have a
            domain, change it under Settings without restarting the server.
          </span>
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>Back</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={urlBlockNext}>Continue</Button>
        </div>

      {:else if step === 6}
        <div class="dm-tbl">
          {#each [
            { label: 'Data directory', value: dataDir, to: 2 as Step, dim: false },
            { label: 'Service user', value: svcSummary, to: 3 as Step, dim: false },
            { label: 'Admin user', value: `${adminUsername} / ${'•'.repeat(Math.max(8, adminPassword.length || 8))}`, to: 4 as Step, dim: false },
            { label: 'Admin email', value: adminEmail || '—', to: 4 as Step, dim: !adminEmail },
            { label: 'Public URL', value: publicUrl, to: 5 as Step, dim: false }
          ] as r (r.label)}
            <div class="dm-tr rv">
              <span class="dm-dim">{r.label}</span>
              <span class="dm-mono" class:dm-dim={r.dim} title={r.value}>{r.value}</span>
              <span class="dm-tr-menu"><Button variant="ghost" size="xs" onclick={() => jumpTo(r.to)} disabled={installing}>Edit</Button></span>
            </div>
          {/each}
        </div>
        {#if installFailed}
          <div class="callout bad" role="alert">Install failed: {installFailed}</div>
        {/if}
        <div class="foot">
          <Button variant="ghost" onclick={goBack} disabled={installing}>Back</Button>
          <span class="dm-sp"></span>
          {#if installing}<span class="dm-sub">{installMsg}</span>{/if}
          <Button variant="primary" onclick={runInstall} loading={installing}>
            {installing ? 'Installing…' : 'Install'}
          </Button>
        </div>

      {:else if step === 7}
        <div class="status">
          {#if installFailed}
            <StatusChip tone="bad" label="failed" /><span>The install stopped. The log shows the step that failed.</span>
          {:else if credsRevealed}
            <StatusChip tone="ok" label="installed" /><span>Dockmesh is running.</span>
          {:else}
            <StatusChip tone="acc" label="installing" /><span>{installMsg || 'Working through the install steps…'}</span>
          {/if}
        </div>
        <h2 class="dm-h2">Log</h2>
        <div class="log dm-mono" role="log" aria-live="polite">
          {#each streamLines as l (l.ts + l.step)}
            <div class="ln {l.status}">{@render mark(l.status)}<span>{l.message}</span></div>
          {:else}
            <div class="dm-dim">Waiting for output…</div>
          {/each}
          {#if installFailed}
            <div class="ln fail">{@render mark('fail')}<span>install failed: {installFailed}</span></div>
          {/if}
        </div>

        {#if credsRevealed}
          <h2 class="dm-h2 creds-h">Your sign-in</h2>
          <dl class="dm-kv creds">
            <dt>Login URL</dt>
            <dd>
              <span class="dm-mono">{publicUrl}</span>
              <button type="button" class="copy" onclick={() => copyToClipboard('url', publicUrl)} aria-label="Copy login URL">
                {#if copiedKey === 'url'}<Check size={13} /> copied{:else}<Copy size={13} /> copy{/if}
              </button>
            </dd>
            <dt>Username</dt>
            <dd>
              <span class="dm-mono">{adminUsername}</span>
              <button type="button" class="copy" onclick={() => copyToClipboard('user', adminUsername)} aria-label="Copy username">
                {#if copiedKey === 'user'}<Check size={13} /> copied{:else}<Copy size={13} /> copy{/if}
              </button>
            </dd>
            <dt>Password</dt>
            <dd>
              {#if adminPassword}
                <span class="dm-mono">{credShowPw ? adminPassword : '•'.repeat(Math.max(10, adminPassword.length))}</span>
                <button type="button" class="copy" onclick={() => (credShowPw = !credShowPw)} aria-label={credShowPw ? 'Hide password' : 'Show password'}>
                  {#if credShowPw}<EyeOff size={13} /> hide{:else}<Eye size={13} /> show{/if}
                </button>
                <button type="button" class="copy" onclick={() => copyToClipboard('pw', adminPassword)} aria-label="Copy password">
                  {#if copiedKey === 'pw'}<Check size={13} /> copied{:else}<Copy size={13} /> copy{/if}
                </button>
              {:else}
                <span class="dm-dim">the password you entered</span>
              {/if}
            </dd>
          </dl>
          <div class="foot">
            <span class="dm-sub">Forgot the password? Run <span class="dm-mono">sudo dockmesh admin reset-password</span> on the server.</span>
            <span class="dm-sp"></span>
            <a href="/" data-sveltekit-reload class="dm-btn dm-btn-primary">Open Dockmesh <ArrowRight size={14} /></a>
          </div>
        {/if}
      {/if}
    </section>
  </main>
</div>

<style>
  :global(body) { background: var(--bg); color: var(--fg); }

  .sw { min-height: 100vh; display: flex; flex-direction: column; }
  .top {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 14px 28px;
    border-bottom: 1px solid var(--border);
  }
  .brand { display: flex; align-items: center; gap: 8px; font-size: var(--fs-md); }
  .brand .dim { color: var(--subtle); }
  .meta { margin-left: auto; color: var(--subtle); display: flex; gap: 6px; flex-wrap: wrap; }
  .meta .sep { color: var(--border); }

  .col { width: min(720px, 100% - 32px); margin: 0 auto; padding: 28px 0 56px; display: flex; flex-direction: column; gap: 22px; }

  .stepper { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 4px 6px; }
  .stepper li { display: flex; align-items: center; }
  .stepper li + li::before { content: ''; width: 14px; height: 1px; background: var(--border); margin-right: 6px; }
  .stepper button {
    all: unset;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: var(--fs-sm);
    color: var(--subtle);
    cursor: default;
    border-radius: var(--r-sm);
    padding: 2px 4px;
  }
  .stepper button:not(:disabled) { cursor: pointer; }
  .stepper button:not(:disabled):hover .l { color: var(--fg); text-decoration: underline; }
  .stepper button:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
  .stepper .n {
    width: 20px;
    height: 20px;
    border-radius: 10px;
    border: 1px solid var(--border);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    font-family: var(--font-mono);
  }
  .stepper .on button { color: var(--fg); font-weight: 600; }
  .stepper .on .n { border-color: var(--accent); color: var(--accent); }
  .stepper .done .n { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }

  .head h1 { margin: 0; font-size: var(--fs-xl); font-weight: 700; letter-spacing: -0.02em; line-height: 1.25; }
  .head p { margin: 6px 0 0; color: var(--muted); max-width: 62ch; }

  .panel {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 22px 24px;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .panel :global(.dm-h2) { margin: 0; }
  .foot {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    border-top: 1px solid var(--border);
    padding-top: 16px;
    margin-top: 4px;
  }

  .dm-field .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
  .dm-field.sub { margin-top: 10px; }
  .hint { color: var(--subtle); font-size: var(--fs-xs); }
  .note { margin: 0; font-size: var(--fs-sm); color: var(--muted); }
  .note a { color: var(--accent); }
  .warn-t { color: var(--warn); font-weight: 600; }
  .bad-t { color: var(--bad); }
  .row { display: flex; gap: 8px; align-items: stretch; }
  .row input { flex: 1; min-width: 0; }
  .lbl-row { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
  .lbl-row label { font-size: 12px; color: var(--muted); font-weight: 500; }
  .link { all: unset; cursor: pointer; color: var(--accent); font-size: var(--fs-xs); }
  .link:hover { text-decoration: underline; }
  .link:focus-visible { outline: 2px solid var(--accent); }

  .mk { width: 16px; height: 16px; display: inline-flex; align-items: center; justify-content: center; flex: none; color: var(--subtle); }
  .mk.ok { color: var(--ok); }
  .mk.warn { color: var(--warn); }
  .mk.fail { color: var(--bad); }
  .mk.done { color: var(--accent); }
  .mk .bullet { width: 5px; height: 5px; background: currentColor; }
  .check { display: flex; align-items: center; gap: 6px; font-size: var(--fs-sm); }
  .check.ok { color: var(--fg); }
  .check.warn { color: var(--warn); }
  .check.fail { color: var(--bad); }

  .pf { grid-template-columns: 16px minmax(140px, 1fr) minmax(0, 1.4fr); }
  .rv { grid-template-columns: 9.5rem minmax(0, 1fr) auto; }

  .callout {
    display: flex;
    align-items: center;
    gap: 12px;
    border-radius: var(--r);
    padding: 10px 12px;
    font-size: var(--fs-sm);
  }
  .callout.bad { border: 1px solid color-mix(in srgb, var(--bad) 45%, transparent); background: color-mix(in srgb, var(--bad) 8%, transparent); color: var(--fg); }

  .restore { border-top: 1px solid var(--border); padding-top: 16px; display: flex; flex-direction: column; gap: 14px; }
  .restore .hint { margin: -8px 0 0; font-size: var(--fs-sm); }
  .file { font: inherit; font-size: var(--fs-sm); color: var(--muted); }

  .presets { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .preset {
    all: unset;
    cursor: pointer;
    border: 1px solid var(--border);
    border-radius: var(--r);
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: var(--fs-sm);
  }
  .preset small { color: var(--subtle); font-size: var(--fs-xs); }
  .preset:hover { border-color: var(--subtle); }
  .preset.on { border-color: var(--accent); background: var(--accent-soft); }
  .preset:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }

  .choices { display: flex; flex-direction: column; gap: 8px; }
  .radio-card {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 9px;
    cursor: pointer;
    background: var(--surface);
  }
  .radio-card.on { border-color: var(--accent); background: var(--accent-soft); }
  .radio-card input[type='radio'] { margin-top: 3px; accent-color: var(--accent); }
  .rc-b { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .rc-t { font-weight: 600; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .cb { display: flex; gap: 8px; align-items: flex-start; margin-top: 10px; font-size: var(--fs-sm); cursor: pointer; }
  .cb input { margin-top: 3px; accent-color: var(--accent); }
  .cb .hint { display: block; }

  .pw { position: relative; }
  .pw input { padding-right: 40px; }
  .eye {
    all: unset;
    position: absolute;
    right: 6px;
    top: 50%;
    transform: translateY(-50%);
    width: 28px;
    height: 26px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--muted);
    cursor: pointer;
    border-radius: var(--r-sm);
  }
  .eye:hover { color: var(--fg); }
  .eye:focus-visible { outline: 2px solid var(--accent); }
  .strength { display: flex; align-items: center; gap: 4px; margin-top: 2px; }
  .strength i { flex: 1; height: 4px; background: var(--field); }
  .strength i.on.s1 { background: var(--bad); }
  .strength i.on.s2 { background: var(--warn); }
  .strength i.on.s3, .strength i.on.s4 { background: var(--ok); }
  .strength span { width: 48px; text-align: right; }

  .status { display: flex; align-items: center; gap: 10px; font-size: var(--fs-sm); }
  .log {
    background: var(--code, var(--field));
    border-radius: var(--r-sm);
    padding: 10px 12px;
    font-size: 12px;
    line-height: 1.6;
    max-height: 340px;
    overflow: auto;
    margin-top: -8px;
  }
  .ln { display: flex; gap: 8px; align-items: flex-start; }
  .ln > span:last-child { white-space: pre-wrap; overflow-wrap: anywhere; }
  .ln.warn > span:last-child { color: var(--warn); }
  .ln.fail > span:last-child { color: var(--bad); }
  .ln .mk { margin-top: 2px; }

  .creds-h { margin-top: 4px !important; }
  .creds { margin-top: -8px; }
  .creds dd { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .copy {
    all: unset;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--fs-xs);
    color: var(--muted);
    border: 1px solid var(--border);
    border-radius: var(--r-sm);
    padding: 1px 6px;
  }
  .copy:hover { color: var(--fg); border-color: var(--subtle); }
  .copy:focus-visible { outline: 2px solid var(--accent); }

  @media (max-width: 640px) {
    .top { padding: 12px 16px; }
    .panel { padding: 18px 16px; }
    .presets { grid-template-columns: 1fr; }
    .rv { grid-template-columns: 7rem minmax(0, 1fr) auto; }
  }
</style>
