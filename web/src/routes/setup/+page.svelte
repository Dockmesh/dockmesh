<script lang="ts">
  import AuthShell from '$lib/components/auth/AuthShell.svelte';
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
  import { m } from '$lib/paraglide/messages.js';

  type Step = 1 | 2 | 3 | 4 | 5 | 6 | 7;

  const STEPS: Array<{ n: Step; short: string }> = [
    { n: 1, short: m.setup_step_welcome() },
    { n: 2, short: m.setup_step_data() },
    { n: 3, short: m.setup_step_service_user() },
    { n: 4, short: m.setup_step_admin() },
    { n: 5, short: m.setup_step_url() },
    { n: 6, short: m.setup_step_review() },
    { n: 7, short: m.setup_step_done() }
  ];

  const HEADERS: Record<Step, { title: string; sub: string }> = {
    1: {
      title: m.setup_h1_title(),
      sub: m.setup_h1_sub()
    },
    2: {
      title: m.setup_h2_title(),
      sub: m.setup_h2_sub()
    },
    3: {
      title: m.setup_h3_title(),
      sub: m.setup_h3_sub()
    },
    4: {
      title: m.setup_h4_title(),
      sub: m.setup_h4_sub()
    },
    5: {
      title: m.setup_h5_title(),
      sub: m.setup_h5_sub()
    },
    6: {
      title: m.setup_h6_title(),
      sub: m.setup_h6_sub()
    },
    7: {
      title: m.setup_h7_title(),
      sub: m.setup_h7_sub()
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
        restoreError = res.error ?? res.sanity?.summary ?? m.setup_restore_checks_failed();
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
    if (installFailed) return { title: m.setup_failed_title(), sub: m.setup_failed_sub() };
    if (credsRevealed) return { title: m.setup_installed_title(), sub: m.setup_installed_sub() };
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
      // The session ends with the code, or when the code is entered in
      // another browser.
      if (r.status === 401 && !installing) {
        await refreshGate();
        return;
      }
      if (!r.ok) return;
      serverInfo = await r.json();
    } catch { /* keep the last value */ }
  }

  // ----- Step 2: data dir, validated 250 ms after typing stops -------------
  let dataDirStatus = $state<{ kind: 'ok' | 'warn' | 'fail'; text: string }>({
    kind: 'fail',
    text: m.setup_path_required()
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
        dataDirStatus = { kind: 'fail', text: m.setup_validation_failed_http({ status: r.status }) };
        return;
      }
      const d = await r.json();
      dataDirStatus = { kind: d.status, text: d.message };
    } catch (e) {
      dataDirStatus = {
        kind: 'fail',
        text: m.setup_validation_failed({ error: e instanceof Error ? e.message : String(e) })
      };
    }
  }

  $effect(() => {
    const path = dataDir;
    if (dataDirDebounce) clearTimeout(dataDirDebounce);
    if (!path || path.trim() === '') {
      dataDirStatus = { kind: 'fail', text: m.setup_path_required() };
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
        svcCheck = { kind: 'fail', text: m.setup_validation_failed_http({ status: r.status }) };
        return;
      }
      const d = await r.json();
      svcCheck = { kind: d.status, text: d.message };
    } catch (e) {
      svcCheck = { kind: 'fail', text: m.setup_validation_failed({ error: e instanceof Error ? e.message : String(e) }) };
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
    return { score: s, label: ['—', m.account_strength_weak(), m.setup_strength_ok(), m.account_strength_good(), m.account_strength_strong()][s] };
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
      else urlTest = { state: 'fail', reason: d.message ?? m.setup_unreachable() };
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
    installMsg = m.setup_submitting();
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
      // Setup ends with the install, and the wizard endpoints with it.
      if (serverInfoTimer) clearInterval(serverInfoTimer);
      serverInfoTimer = null;
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
        installFailed = m.setup_stream_lost();
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
      ? m.setup_svc_recommended({ user: RECOMMENDED_USER })
      : svcMode === 'new'
        ? (svcAddToDocker ? m.setup_svc_new_docker({ user: svcNewUser }) : m.setup_svc_new({ user: svcNewUser }))
        : svcExistingUser || '—'
  );

  // ----- Setup code: nothing of the wizard loads before it ----------------
  type CodeState = 'no_code' | 'ready' | 'expired' | 'locked';
  let gate = $state<'checking' | 'code' | 'open'>('checking');
  let codeState = $state<CodeState>('ready');
  let sessionReplaced = $state(false);
  let codeInput = $state('');
  let codeFromLink = $state(false);
  let codeError = $state<string | null>(null);
  let codeBusy = $state(false);
  let codeHelpOpen = $state(false);
  let wizardStarted = false;

  // Returns true when this browser holds a setup session. Setup may also
  // have finished meanwhile (restart, bookmark): then go to the dashboard.
  async function refreshGate(): Promise<boolean> {
    let st: { active?: boolean; code?: CodeState; session?: string } | null = null;
    try {
      const r = await fetch('/api/v1/setup/status', { headers: { Accept: 'application/json' } });
      if (r.ok) st = await r.json();
    } catch {
      /* show the code form; entering the code reports the real state */
    }
    if (st && !st.active) {
      window.location.replace('/');
      return false;
    }
    if (st?.session === 'valid' || st?.session === 'read_only') {
      gate = 'open';
      return true;
    }
    codeState = st?.code ?? 'ready';
    sessionReplaced = st?.session === 'replaced';
    gate = 'code';
    return false;
  }

  async function submitCode() {
    codeBusy = true;
    codeError = null;
    try {
      const r = await fetch('/api/v1/setup/code', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Dockmesh-Setup': '1' },
        body: JSON.stringify({ code: codeInput })
      });
      if (r.status === 204) {
        codeInput = '';
        sessionReplaced = false;
        gate = 'open';
        startWizard();
        return;
      }
      const b = await r.json().catch(() => ({}));
      if (b.code_state) codeState = b.code_state;
      if (r.status === 429) {
        codeError = m.setup_code_rate_limited({ minutes: Math.max(1, Math.ceil((b.retry_after_secs ?? 60) / 60)) });
      } else if (r.status === 401) {
        codeError = m.setup_code_wrong({ n: b.attempts_left ?? 0 });
      } else if (r.status === 410 && !b.code_state) {
        window.location.replace('/');
      } else if (r.status === 400 || r.status === 403) {
        codeError = m.setup_code_format();
      }
    } catch (e) {
      codeError = e instanceof Error ? e.message : String(e);
    } finally {
      codeBusy = false;
    }
  }

  function retryCode() {
    codeError = null;
    codeState = 'ready';
    sessionReplaced = false;
  }

  onMount(async () => {
    // The link the server prints carries the code in the fragment, which
    // never reaches the server. Fill the field, drop it from the address
    // bar, and wait for Continue: opening the link again must not take
    // over a running setup.
    const fragment = window.location.hash.slice(1);
    if (fragment) {
      codeInput = decodeURIComponent(fragment);
      codeFromLink = true;
      history.replaceState(history.state, '', window.location.pathname + window.location.search);
    }
    if (await refreshGate()) startWizard();
  });

  function startWizard() {
    if (wizardStarted) return;
    wizardStarted = true;
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
  }
  onDestroy(() => {
    if (serverInfoTimer) clearInterval(serverInfoTimer);
  });
</script>

<svelte:head>
  <title>{m.setup_h1_title()}</title>
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

{#snippet serverMeta()}
  <span class="meta dm-num" aria-label={m.setup_aria_this_server()}>
    <span>{serverInfo ? fmtVersion(serverInfo.version) : '—'}</span>
    <span class="sep">·</span>
    <span>{serverInfo?.ip ?? '—'}</span>
    <span class="sep">·</span>
    <span>docker {serverInfo?.docker_version ?? '—'}</span>
  </span>
{/snippet}

<AuthShell width={760} footer={serverMeta}>
  {#if gate === 'checking'}
    <div class="col"><div class="dm-empty">{m.setup_checking_server()}</div></div>
  {:else if gate === 'code'}
    <div class="col">
      {#if sessionReplaced}
        <div class="head"><h1>{m.setup_code_replaced_title()}</h1><p>{m.setup_code_replaced_body()}</p></div>
      {:else if codeState === 'expired'}
        <div class="head"><h1>{m.setup_code_expired_title()}</h1><p>{m.setup_code_expired_body()}</p></div>
      {:else if codeState === 'locked'}
        <div class="head"><h1>{m.setup_code_locked_title()}</h1><p>{m.setup_code_locked_body()}</p></div>
      {:else if codeState === 'no_code'}
        <div class="head"><h1>{m.setup_code_none_title()}</h1><p>{m.setup_code_none_body()}</p></div>
      {:else}
        <div class="head"><h1>{m.setup_code_title()}</h1><p>{m.setup_code_sub()}</p></div>
      {/if}
      <section class="panel">
        {#if sessionReplaced || codeState !== 'ready'}
          <div class="cmd-line">
            <span class="dm-mono">sudo dockmesh setup new-code</span>
            <Button size="sm" variant="secondary" onclick={() => copyToClipboard('newcode', 'sudo dockmesh setup new-code')}>
              {#if copiedKey === 'newcode'}<Check size={13} />{:else}<Copy size={13} />{/if}
            </Button>
          </div>
          <div class="foot">
            <span class="dm-sp"></span>
            <Button variant="primary" onclick={retryCode}>{m.setup_code_have_new()}</Button>
          </div>
        {:else}
          <form class="dm-field" onsubmit={(e) => { e.preventDefault(); submitCode(); }}>
            <label for="setup-code">{m.setup_code_label()}</label>
            <input id="setup-code" class="dm-input dm-mono code-input" placeholder="XXXX-XXXX-XXXX" bind:value={codeInput}
              autocomplete="off" spellcheck="false" aria-invalid={codeError !== null} />
            {#if codeError}
              {@render checkLine('fail', codeError)}
            {:else if codeFromLink}
              <span class="hint">{m.setup_code_from_link()}</span>
            {/if}
            <div>
              <button type="button" class="link" onclick={() => (codeHelpOpen = !codeHelpOpen)} aria-expanded={codeHelpOpen}>{m.setup_code_lost()}</button>
              {#if codeHelpOpen}
                <p class="note">{m.setup_code_lost_body_before()} <span class="dm-mono">sudo dockmesh setup new-code</span> {m.setup_code_lost_body_after()}</p>
              {/if}
            </div>
            <div class="foot">
              <span class="dm-sp"></span>
              <Button variant="primary" type="submit" disabled={codeBusy || codeInput.trim() === ''}>{m.setup_continue()} <ArrowRight size={14} /></Button>
            </div>
          </form>
        {/if}
      </section>
    </div>
  {:else}
  <div class="col">
    <nav aria-label={m.setup_aria_steps()}>
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
        <h2 class="dm-h2">{m.setup_this_server()}</h2>
        {#if preflightLoading}
          <div class="dm-empty">{m.setup_checking_server()}</div>
        {:else if preflightError}
          <div class="callout bad">
            <span>{m.setup_check_failed({ error: preflightError })}</span>
            <Button size="sm" variant="secondary" onclick={loadPreflight}><RotateCcw size={13} /> {m.setup_check_again()}</Button>
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
            <p class="note bad-t">{m.setup_fix_failed()}</p>
          {/if}
        {/if}

        <div class="foot">
          <Button variant="ghost" size="sm" onclick={() => (restoreOpen = !restoreOpen)} aria-expanded={restoreOpen}>
            {m.setup_restore_instead()}
          </Button>
          <span class="dm-sp"></span>
          {#if !preflightLoading && !preflightError}
            <Button variant="ghost" size="sm" onclick={loadPreflight}><RotateCcw size={13} /> {m.setup_check_again()}</Button>
          {/if}
          <Button variant="primary" onclick={goNext} disabled={preflightHasFail || preflightLoading}>
            {m.setup_begin()} <ArrowRight size={14} />
          </Button>
        </div>

        {#if restoreOpen}
          <div class="restore">
            <h2 class="dm-h2">{m.setup_restore_title()}</h2>
            <p class="hint">
              {m.setup_restore_hint()}
            </p>
            <div class="dm-field">
              <label for="rs-file">{m.setup_restore_file()}</label>
              <input id="rs-file" class="file" type="file" accept=".gz,.age,.tar" onchange={(e) => (restoreFile = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
            </div>
            <div class="dm-field">
              <label for="rs-key">{m.setup_restore_key()}</label>
              <input id="rs-key" class="file" type="file" accept=".txt,.key" onchange={(e) => (restoreKey = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
              <span class="hint">{m.setup_restore_key_hint()}</span>
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
              <p class="note">{m.setup_restoring()}</p>
            {:else if restorePhase === 'stuck'}
              <div class="callout bad" role="alert">{m.setup_restore_stuck()}</div>
            {:else}
              <div>
                <Button variant="primary" size="sm" onclick={restoreFromBackup} disabled={!restoreFile} loading={restorePhase === 'uploading'}>
                  {restorePhase === 'uploading' ? m.setup_checking_backup() : m.setup_restore()}
                </Button>
              </div>
            {/if}
          </div>
        {/if}

      {:else if step === 2}
        <div class="dm-field">
          <label for="data-dir">{m.setup_data_dir()}</label>
          <input id="data-dir" class="dm-input dm-mono" bind:value={dataDir} spellcheck="false" autocomplete="off" aria-invalid={dataDirStatus.kind === 'fail'} />
          {@render checkLine(dataDirStatus.kind, dataDirStatus.text)}
          <span class="hint">{m.setup_data_dir_hint()}</span>
        </div>
        <div class="dm-field">
          <span class="lbl">{m.setup_common_locations()}</span>
          <div class="presets">
            <button type="button" class="preset" class:on={dataDir === '/var/lib/dockmesh'} onclick={() => (dataDir = '/var/lib/dockmesh')}>
              <span class="dm-mono">/var/lib/dockmesh</span><small>{m.setup_loc_linux()}</small>
            </button>
            <button type="button" class="preset" class:on={dataDir === '/usr/local/var/dockmesh'} onclick={() => (dataDir = '/usr/local/var/dockmesh')}>
              <span class="dm-mono">/usr/local/var/dockmesh</span><small>{m.setup_loc_macos()}</small>
            </button>
          </div>
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>{m.common_back()}</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={dataDirStatus.kind === 'fail'}>{m.setup_continue()}</Button>
        </div>

      {:else if step === 3}
        <div class="choices" role="radiogroup" aria-label={m.setup_aria_service_user()}>
          <label class="radio-card" class:on={svcMode === 'recommended'}>
            <input type="radio" name="svc-mode" checked={svcMode === 'recommended'} onchange={() => (svcMode = 'recommended')} />
            <div class="rc-b">
              <div class="rc-t">{m.setup_rec_before()} <span class="dm-mono">dockmesh</span> {m.setup_rec_after()} <StatusChip tone="acc" label={m.setup_recommended()} /></div>
              <div class="hint">{m.setup_rec_hint_before()} <span class="dm-mono">docker</span>{m.setup_rec_hint_after()}</div>
              {#if svcMode === 'recommended' && svcCheck.kind !== 'idle' && svcCheck.text}
                {@render checkLine(svcCheck.kind, svcCheck.text)}
              {/if}
            </div>
          </label>

          <label class="radio-card" class:on={svcMode === 'existing'}>
            <input type="radio" name="svc-mode" checked={svcMode === 'existing'} onchange={() => (svcMode = 'existing')} />
            <div class="rc-b">
              <div class="rc-t">{m.setup_existing_title()}</div>
              <div class="hint">{m.setup_existing_hint_before()} <span class="dm-mono">ops</span> {m.setup_existing_hint_or()} <span class="dm-mono">deploy</span>.</div>
              {#if svcMode === 'existing'}
                <div class="dm-field sub">
                  <label for="existing-user">{m.setup_username()}</label>
                  <input id="existing-user" class="dm-input dm-mono" placeholder="ops" bind:value={svcExistingUser} aria-invalid={svcCheck.kind === 'fail'} spellcheck="false" autocomplete="off" />
                  {#if svcCheck.kind !== 'idle' && svcCheck.text}{@render checkLine(svcCheck.kind, svcCheck.text)}{/if}
                  <span class="hint">
                    {m.setup_existing_groups_before()}
                    <span class="dm-mono">docker</span>{m.setup_existing_groups_after()}
                    <span class="dm-mono">sudo usermod -aG docker {svcExistingUser.trim() || '<user>'}</span>.
                  </span>
                </div>
              {/if}
            </div>
          </label>

          <label class="radio-card" class:on={svcMode === 'new'}>
            <input type="radio" name="svc-mode" checked={svcMode === 'new'} onchange={() => (svcMode = 'new')} />
            <div class="rc-b">
              <div class="rc-t">{m.setup_new_title()}</div>
              <div class="hint">{m.setup_new_hint_before()} <span class="dm-mono">dockmesh</span>{m.setup_new_hint_after()}</div>
              {#if svcMode === 'new'}
                <div class="dm-field sub">
                  <label for="new-user">{m.setup_username()}</label>
                  <input id="new-user" class="dm-input dm-mono" placeholder="dockermaster" bind:value={svcNewUser} aria-invalid={svcCheck.kind === 'fail'} spellcheck="false" autocomplete="off" />
                  {#if svcCheck.kind !== 'idle' && svcCheck.text}{@render checkLine(svcCheck.kind, svcCheck.text)}{/if}
                  <span class="hint">{m.setup_new_uid_hint()}</span>
                </div>
                <label class="cb">
                  <input type="checkbox" bind:checked={svcAddToDocker} />
                  <span>{m.setup_add_docker_before()} <span class="dm-mono">docker</span>{m.setup_add_docker_after()} <span class="hint">{m.setup_add_docker_hint()}</span></span>
                </label>
              {/if}
            </div>
          </label>
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>{m.common_back()}</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={svcBlockNext}>{m.setup_continue()}</Button>
        </div>

      {:else if step === 4}
        <div class="dm-field">
          <label for="admin-user">{m.setup_username()}</label>
          <input id="admin-user" class="dm-input dm-mono" bind:value={adminUsername} spellcheck="false" autocomplete="off" />
          <span class="hint">{m.setup_admin_hint()}</span>
        </div>
        <div class="dm-field">
          <div class="lbl-row">
            <label for="admin-pw">{m.setup_password()}</label>
            <button type="button" class="link" onclick={generatePassword}>{m.setup_generate_password()}</button>
          </div>
          <div class="pw">
            <input id="admin-pw" type={adminShowPassword ? 'text' : 'password'} class="dm-input dm-mono" bind:value={adminPassword} autocomplete="new-password" aria-describedby="pw-strength" />
            <button type="button" class="eye" onclick={() => (adminShowPassword = !adminShowPassword)} aria-label={adminShowPassword ? m.setup_hide_password() : m.setup_show_password()}>
              {#if adminShowPassword}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
            </button>
          </div>
          <div class="strength" id="pw-strength">
            {#each [0, 1, 2, 3] as i (i)}
              <i class:on={strength.score > i} class="s{strength.score}"></i>
            {/each}
            <span class="dm-num dm-dim">{strength.label}</span>
          </div>
          <span class="hint">{m.setup_min_8()}</span>
        </div>
        <div class="dm-field">
          <label for="admin-email">{m.setup_email()} <span class="dm-dim">({m.common_optional()})</span></label>
          <input id="admin-email" type="email" class="dm-input" placeholder="admin@example.com" bind:value={adminEmail} spellcheck="false" autocomplete="off" />
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>{m.common_back()}</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={adminBlockNext}>{m.setup_continue()}</Button>
        </div>

      {:else if step === 5}
        <div class="dm-field">
          <label for="pub-url">{m.setup_public_url()}</label>
          <div class="row">
            <input id="pub-url" class="dm-input dm-mono" bind:value={publicUrl} spellcheck="false" autocomplete="off" aria-invalid={urlBlockNext} />
            <Button variant="secondary" onclick={runUrlTest} loading={urlTest.state === 'loading'} disabled={urlBlockNext}>
              {urlTest.state === 'loading' ? m.setup_testing() : m.setup_test_connection()}
            </Button>
          </div>
          {#if urlTest.state === 'ok'}
            {@render checkLine('ok', m.setup_reached({ ms: urlTest.ms ?? 0 }))}
          {:else if urlTest.state === 'fail'}
            {@render checkLine('fail', m.setup_not_reached({ reason: urlTest.reason ?? '' }))}
          {:else if urlBlockNext}
            {@render checkLine('fail', m.setup_invalid_url())}
          {/if}
          <span class="hint">
            {m.setup_url_hint()}
          </span>
        </div>
        <div class="foot">
          <Button variant="ghost" onclick={goBack}>{m.common_back()}</Button>
          <span class="dm-sp"></span>
          <Button variant="primary" onclick={goNext} disabled={urlBlockNext}>{m.setup_continue()}</Button>
        </div>

      {:else if step === 6}
        <div class="dm-tbl">
          {#each [
            { label: m.setup_rv_data_dir(), value: dataDir, to: 2 as Step, dim: false },
            { label: m.setup_rv_service_user(), value: svcSummary, to: 3 as Step, dim: false },
            { label: m.setup_rv_admin_user(), value: `${adminUsername} / ${'•'.repeat(Math.max(8, adminPassword.length || 8))}`, to: 4 as Step, dim: false },
            { label: m.setup_rv_admin_email(), value: adminEmail || '—', to: 4 as Step, dim: !adminEmail },
            { label: m.setup_rv_public_url(), value: publicUrl, to: 5 as Step, dim: false }
          ] as r (r.label)}
            <div class="dm-tr rv">
              <span class="dm-dim">{r.label}</span>
              <span class="dm-mono" class:dm-dim={r.dim} title={r.value}>{r.value}</span>
              <span class="dm-tr-menu"><Button variant="ghost" size="xs" onclick={() => jumpTo(r.to)} disabled={installing}>{m.common_edit()}</Button></span>
            </div>
          {/each}
        </div>
        {#if installFailed}
          <div class="callout bad" role="alert">{m.setup_install_failed({ error: installFailed })}</div>
        {/if}
        <div class="foot">
          <Button variant="ghost" onclick={goBack} disabled={installing}>{m.common_back()}</Button>
          <span class="dm-sp"></span>
          {#if installing}<span class="dm-sub">{installMsg}</span>{/if}
          <Button variant="primary" onclick={runInstall} loading={installing}>
            {installing ? m.setup_installing() : m.setup_install()}
          </Button>
        </div>

      {:else if step === 7}
        <div class="status">
          {#if installFailed}
            <StatusChip tone="bad" label={m.setup_chip_failed()} /><span>{m.setup_status_failed()}</span>
          {:else if credsRevealed}
            <StatusChip tone="ok" label={m.setup_chip_installed()} /><span>{m.setup_running()}</span>
          {:else}
            <StatusChip tone="acc" label={m.setup_chip_installing()} /><span>{installMsg || m.setup_working()}</span>
          {/if}
        </div>
        <h2 class="dm-h2">{m.setup_log()}</h2>
        <div class="log dm-mono" role="log" aria-live="polite">
          {#each streamLines as l (l.ts + l.step)}
            <div class="ln {l.status}">{@render mark(l.status)}<span>{l.message}</span></div>
          {:else}
            <div class="dm-dim">{m.setup_waiting()}</div>
          {/each}
          {#if installFailed}
            <div class="ln fail">{@render mark('fail')}<span>{m.setup_log_install_failed({ error: installFailed })}</span></div>
          {/if}
        </div>

        {#if credsRevealed}
          <h2 class="dm-h2 creds-h">{m.setup_your_signin()}</h2>
          <dl class="dm-kv creds">
            <dt>{m.setup_login_url()}</dt>
            <dd>
              <span class="dm-mono">{publicUrl}</span>
              <button type="button" class="copy" onclick={() => copyToClipboard('url', publicUrl)} aria-label={m.setup_copy_login_url()}>
                {#if copiedKey === 'url'}<Check size={13} /> {m.setup_copied()}{:else}<Copy size={13} /> {m.setup_copy()}{/if}
              </button>
            </dd>
            <dt>{m.setup_username()}</dt>
            <dd>
              <span class="dm-mono">{adminUsername}</span>
              <button type="button" class="copy" onclick={() => copyToClipboard('user', adminUsername)} aria-label={m.setup_copy_username()}>
                {#if copiedKey === 'user'}<Check size={13} /> {m.setup_copied()}{:else}<Copy size={13} /> {m.setup_copy()}{/if}
              </button>
            </dd>
            <dt>{m.setup_password()}</dt>
            <dd>
              {#if adminPassword}
                <span class="dm-mono">{credShowPw ? adminPassword : '•'.repeat(Math.max(10, adminPassword.length))}</span>
                <button type="button" class="copy" onclick={() => (credShowPw = !credShowPw)} aria-label={credShowPw ? m.setup_hide_password() : m.setup_show_password()}>
                  {#if credShowPw}<EyeOff size={13} /> {m.setup_hide()}{:else}<Eye size={13} /> {m.setup_show()}{/if}
                </button>
                <button type="button" class="copy" onclick={() => copyToClipboard('pw', adminPassword)} aria-label={m.setup_copy_password()}>
                  {#if copiedKey === 'pw'}<Check size={13} /> {m.setup_copied()}{:else}<Copy size={13} /> {m.setup_copy()}{/if}
                </button>
              {:else}
                <span class="dm-dim">{m.setup_password_entered()}</span>
              {/if}
            </dd>
          </dl>
          <div class="foot">
            <span class="dm-sub">{m.setup_forgot_before()} <span class="dm-mono">sudo dockmesh admin reset-password --user {adminUsername}</span>{m.setup_forgot_after()}</span>
            <span class="dm-sp"></span>
            <a href="/" data-sveltekit-reload class="dm-btn dm-btn-primary">{m.setup_open()} <ArrowRight size={14} /></a>
          </div>
        {/if}
      {/if}
    </section>
  </div>
  {/if}
</AuthShell>

<style>
  .meta { display: inline-flex; justify-content: center; gap: 6px; flex-wrap: wrap; }
  .meta .sep { color: var(--border-strong); }

  .col { display: flex; flex-direction: column; gap: 22px; }

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
  }
  .stepper .on button { color: var(--fg); font-weight: 600; }
  .stepper .on .n { border-color: var(--accent); color: var(--accent); }
  .stepper .done .n { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }

  .head h1 { margin: 0; font-size: var(--fs-xl); font-weight: 700; letter-spacing: -0.02em; line-height: 1.25; }
  .head p { margin: 6px 0 0; color: var(--muted); max-width: 62ch; }

  .panel {
    border-top: 1px solid var(--border);
    padding-top: 22px;
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
  .code-input { font-size: 18px; letter-spacing: 0.08em; }
  .cmd-line { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--radius, 6px); }
  .link { background: none; border: 0; padding: 0; color: var(--accent); font-size: var(--fs-sm); cursor: pointer; }
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
    .presets { grid-template-columns: 1fr; }
    .rv { grid-template-columns: 7rem minmax(0, 1fr) auto; }
  }
</style>
