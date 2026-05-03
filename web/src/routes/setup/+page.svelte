<script lang="ts">
  // Dockmesh install wizard — editorial 7-step flow.
  //
  // P.14.3 ships this with mock data only — every "Continue" advances
  // local state, every form input lives in a $state variable, the
  // terminal in step 7 prints a hard-coded canned sequence on a timer.
  // P.14.4 wires this up to the real /api/v1/setup/* endpoints.
  //
  // The visual language is a 1:1 port of the claude.ai mockup — dot
  // mesh, watermark step number, eyebrow with typed text + cursor,
  // subway-rail on the left edge, italic accent words in titles, and a
  // terminal-output triumph for step 7. CSS lives in a scoped <style>
  // block so this file is fully self-contained.
  import { onMount, onDestroy } from 'svelte';

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

  // Editorial-layout headers — eyebrow + italic-accent title + subtitle per step.
  // Italic accent uses a span class, applied via {@html} below for the title.
  const ED_HEADERS: Record<Step, { eyebrow: string; title: string; sub: string }> = {
    1: {
      eyebrow: 'Welcome',
      title: `Let's get <span class="ed-title-accent">Dockmesh</span><br/>up and running.`,
      sub: `Seven small steps. About a minute. We'll check this server, set a couple of things up, and hand you the keys.`
    },
    2: {
      eyebrow: 'Storage',
      title: `Where should <span class="ed-title-accent">data</span> live?`,
      sub: `Pick a directory for the database, secrets, and stack definitions. It should be on a volume you can grow.`
    },
    3: {
      eyebrow: 'Identity',
      title: `Run as which <span class="ed-title-accent">user</span>?`,
      sub: `The OS account Dockmesh runs under. We recommend a dedicated user with access to the Docker socket.`
    },
    4: {
      eyebrow: 'Authentication',
      title: `Your <span class="ed-title-accent">first key</span> in.`,
      sub: `An admin login for the dashboard. You can wire up OIDC and rotate this later.`
    },
    5: {
      eyebrow: 'Networking',
      title: `How do agents <span class="ed-title-accent">find home</span>?`,
      sub: `Remote agents dial back to this URL. Embedded in OIDC redirects and notification emails too.`
    },
    6: {
      eyebrow: 'Confirm',
      title: `Ready to <span class="ed-title-accent">commit</span>?`,
      sub: `Nothing has been written yet. Review and we'll create the user, write the env file, and start the service.`
    },
    7: {
      eyebrow: 'Installed',
      title: `Dockmesh is <span class="ed-title-accent">live</span>.`,
      sub: `Service is running. Below are your credentials — copy them somewhere safe before you close this tab.`
    }
  };

  // Wizard-wide state machine.
  let step = $state<Step>(1);
  let direction = $state<'forward' | 'back'>('forward');
  let completed = $state<Set<Step>>(new Set());

  // Per-step form state.
  let dataDir = $state('/data');
  // Three modes so the recommended path doesn't conflict with the
  // "I want to pick my own user" paths:
  //   - recommended: use the `dockmesh` user that install.sh has
  //     already set up (no input field — informational).
  //   - existing: a different system user the operator chose
  //     (e.g. "ops" / "deploy") — text input, validates against
  //     /etc/passwd via /api/v1/setup/validate-user.
  //   - new: create a fresh system user under a different name
  //     (e.g. "dockermaster") — text input, validates that the
  //     name is free.
  // Defaults: recommended, since install.sh runs before the wizard
  // and the dockmesh user is the cleanest happy path.
  let svcMode = $state<'recommended' | 'existing' | 'new'>('recommended');
  let svcExistingUser = $state('');
  let svcNewUser = $state('');
  let svcAddToDocker = $state(true);
  // Recommended path uses the install-script-created user verbatim.
  // Hardcoded so the operator can't typo it into mismatching install.sh.
  const RECOMMENDED_USER = 'dockmesh';

  let adminUsername = $state('admin');
  let adminPassword = $state('');
  let adminEmail = $state('');
  let adminShowPassword = $state(false);

  // Default URL prefilled from window.location.origin on mount —
  // that's the URL the operator typed into their browser to reach the
  // wizard, so testing it always succeeds (server is by definition
  // reachable from itself). The hardcoded fallback only matters during
  // SSR where window doesn't exist.
  let publicUrl = $state('http://localhost:8080');

  // P3 mock: clicking Install on step 6 fires runInstall() which
  // walks through canned phase messages then jumps to step 7. P4
  // replaces this with a real /setup/commit + SSE stream.
  let installing = $state(false);
  let installMsg = $state('');
  let meshAfterglow = $state(false);

  // ----- Step 1 — preflight (live from backend) ----------------------------
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

  // ----- Top-bar live server info -----------------------------------------
  let serverInfo = $state<{ version: string; os: string; ip: string; docker_version: string; uptime_secs: number } | null>(null);
  let serverInfoTimer: ReturnType<typeof setInterval> | null = null;

  async function loadServerInfo() {
    try {
      const r = await fetch('/api/v1/setup/server-info');
      if (!r.ok) return;
      serverInfo = await r.json();
    } catch { /* network blip — keep last value */ }
  }

  // ----- Step 2 — data dir live-validate (debounced 250ms) -----------------
  let dataDirStatus = $state<{ kind: 'ok' | 'warn' | 'fail'; text: string }>({
    kind: 'fail',
    text: 'path required'
  });
  let dataDirDebounce: ReturnType<typeof setTimeout> | null = null;

  async function validateDataDirRemote(path: string) {
    try {
      const r = await fetch('/api/v1/setup/validate-data-dir', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
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

  // ----- Step 3 — service user live-validate (debounced 250ms) -------------
  let svcCheck = $state<{ kind: 'ok' | 'warn' | 'fail' | 'idle'; text: string }>({
    kind: 'idle',
    text: ''
  });
  let svcDebounce: ReturnType<typeof setTimeout> | null = null;

  async function validateUserRemote(mode: 'existing' | 'new', username: string) {
    try {
      const r = await fetch('/api/v1/setup/validate-user', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
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

  // Step 4 — password strength meter.
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

  // Step 5 — URL validity + connection-test mock.
  let urlTest = $state<{ state: 'idle' | 'loading' | 'ok' | 'fail'; ms?: number; reason?: string }>({ state: 'idle' });
  const urlBlockNext = $derived.by(() => {
    try { new URL(publicUrl); return false; } catch { return true; }
  });
  async function runUrlTest() {
    urlTest = { state: 'loading' };
    try {
      const r = await fetch('/api/v1/setup/test-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
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

  // ----- Navigation --------------------------------------------------------

  function goNext() {
    if (!completed.has(step)) completed = new Set([...completed, step]);
    direction = 'forward';
    if (step < 7) step = (step + 1) as Step;
    window.scrollTo({ top: 0, behavior: 'instant' });
  }
  function goBack() {
    direction = 'back';
    if (step > 1) step = (step - 1) as Step;
    window.scrollTo({ top: 0, behavior: 'instant' });
  }
  function jumpTo(n: Step) {
    direction = n < step ? 'back' : 'forward';
    step = n;
    completed = new Set(Array.from(completed).filter((x) => x < n));
    window.scrollTo({ top: 0, behavior: 'instant' });
  }

  // Sync ?step=N into URL so refresh keeps the operator at the same place.
  $effect(() => {
    const u = new URL(window.location.href);
    u.searchParams.set('step', String(step));
    history.replaceState(null, '', u.toString());
  });

  // ----- Step 6 → install runner (real backend, SSE-streamed) --------------
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
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          data_dir: dataDir,
          service_user: {
            mode: svcMode === 'new' ? 'create' : 'existing',
            username:
              svcMode === 'recommended' ? RECOMMENDED_USER
              : svcMode === 'new' ? svcNewUser.trim()
              : svcExistingUser.trim(),
            add_to_docker_group: svcMode !== 'recommended' && svcAddToDocker
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
      // Move to Step 7 immediately so the operator watches the stream.
      completed = new Set([1, 2, 3, 4, 5, 6]);
      step = 7;
      meshAfterglow = true;
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
      meshAfterglow = false;
      // Reveal credentials only on a clean run — failed installs show
      // the error banner instead.
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

  // ----- Step 7 — terminal lines (live SSE) --------------------------------
  // streamLines is appended to as `event: line` SSE frames arrive from
  // /api/v1/setup/stream/{run_id}. credsRevealed gets flipped by the
  // SSE `end` handler when the install finishes without a fail event.
  // Mapping from server status strings to the kind the renderer uses:
  //   "info" → grey bullet, "ok" → green check, "warn" → amber !,
  //   "fail" → red ✗, "done" → blue arrow (final).
  function statusToMark(status: string): string {
    if (status === 'ok') return '✓';
    if (status === 'warn') return '!';
    if (status === 'fail') return '✗';
    if (status === 'done') return '→';
    return '•'; // info, start, anything else
  }
  let credsRevealed = $state(false);

  // ----- Step 7 — credentials display + copy -------------------------------
  // Mock password — P4 replaces this with the real one returned by /commit
  // (or the one the operator typed). Here we render whatever's in admin.password
  // or fall back to a fake one so the credentials block always has content.
  const finalPassword = $derived(adminPassword || 'ZNb7-pq4Kx-2VwLm');
  let copiedKey = $state<string | null>(null);
  let credShowPw = $state(false);
  function copyToClipboard(key: string, value: string) {
    navigator.clipboard?.writeText(value).catch(() => {});
    copiedKey = key;
    setTimeout(() => { if (copiedKey === key) copiedKey = null; }, 1400);
  }

  // ----- Eyebrow typed-text effect -----------------------------------------
  let eyebrowShown = $state('');
  let eyebrowTimer: ReturnType<typeof setInterval> | null = null;
  let prevEyebrowText = '';
  $effect(() => {
    const target = ED_HEADERS[step].eyebrow;
    if (target === prevEyebrowText) return;
    const old = prevEyebrowText;
    prevEyebrowText = target;
    if (eyebrowTimer) clearInterval(eyebrowTimer);
    if (direction === 'back' && old) {
      let i = old.length;
      eyebrowShown = old;
      eyebrowTimer = setInterval(() => {
        i -= 1;
        if (i <= 0) {
          if (eyebrowTimer) clearInterval(eyebrowTimer);
          let j = 0;
          eyebrowTimer = setInterval(() => {
            j += 1;
            eyebrowShown = target.slice(0, j);
            if (j >= target.length && eyebrowTimer) {
              clearInterval(eyebrowTimer);
              eyebrowTimer = null;
            }
          }, 55);
        } else {
          eyebrowShown = old.slice(0, i);
        }
      }, 32);
    } else {
      let i = 0;
      eyebrowShown = '';
      eyebrowTimer = setInterval(() => {
        i += 1;
        eyebrowShown = target.slice(0, i);
        if (i >= target.length && eyebrowTimer) {
          clearInterval(eyebrowTimer);
          eyebrowTimer = null;
        }
      }, 55);
    }
  });
  onDestroy(() => { if (eyebrowTimer) clearInterval(eyebrowTimer); });

  // Restore step from URL on mount + kick off live data fetches.
  onMount(() => {
    const param = new URLSearchParams(window.location.search).get('step');
    const n = parseInt(param || '1', 10);
    if (Number.isFinite(n) && n >= 1 && n <= 7) {
      step = n as Step;
      const arr: Step[] = [];
      for (let i = 1; i < n; i++) arr.push(i as Step);
      completed = new Set(arr);
    }
    // Default the public URL field to the URL the operator is currently
    // browsing — the wizard reaches itself, so the test-connect button
    // succeeds out of the box. Operator can still change it.
    if (typeof window !== 'undefined' && window.location?.origin) {
      publicUrl = window.location.origin;
    }
    // Live server info for the top bar — updates uptime + IP every 10s.
    loadServerInfo();
    serverInfoTimer = setInterval(loadServerInfo, 10_000);
    // Preflight loads once on mount; the operator can revisit step 1
    // after fixing something on the host but the values are stale until
    // they hit "Continue" again. A future polish slice could add a
    // refresh button.
    loadPreflight();
  });
  onDestroy(() => {
    if (serverInfoTimer) clearInterval(serverInfoTimer);
  });
</script>

<svelte:head>
  <title>Dockmesh — Install Wizard</title>
  <link rel="preconnect" href="https://fonts.googleapis.com" />
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin="anonymous" />
  <link
    rel="stylesheet"
    href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap"
  />
</svelte:head>

<div class="ed-shell" data-screen-label={`Step ${step}`}>
  <div class="ed-mesh" class:ed-mesh--live={meshAfterglow || installing} aria-hidden="true"></div>

  <div class="ed-watermark" aria-hidden="true">
    <svg width="520" height="520" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="0.6">
      <rect x="3.5" y="3.5" width="11" height="11" rx="0.4" />
      <rect x="9.5" y="9.5" width="11" height="11" rx="0.4" />
    </svg>
  </div>

  <!-- Vertical step rail (subway-line, left edge) -->
  <nav class="ed-rail" aria-label="Setup progress">
    {#each STEPS as s (s.n)}
      {@const done = completed.has(s.n)}
      {@const cur = step === s.n}
      {@const reachable = done || cur}
      <button
        type="button"
        class="ed-rail-item"
        class:ed-rail-item--current={cur}
        class:ed-rail-item--done={done && !cur}
        aria-disabled={!reachable}
        aria-current={cur ? 'step' : undefined}
        onclick={() => reachable && jumpTo(s.n)}
      >
        <span class="ed-rail-num">{String(s.n).padStart(2, '0')}</span>
        <span class="ed-rail-label">{s.short}</span>
      </button>
    {/each}
  </nav>

  <!-- Top bar: dockmesh / install · v · ip · online -->
  <header class="ed-topbar">
    <div class="ed-brand">
      <img src="/logo-mark.svg" alt="Dockmesh" class="ed-brand-logo" />
      dockmesh
      <span style="color: var(--border-strong); margin: 0 4px;">/</span>
      <span style="color: var(--fg-muted); font-weight: 400;">install</span>
    </div>
    <div class="ed-meta">
      <span>v<b>{serverInfo?.version ?? '—'}</b></span>
      <span style="color: var(--border-strong);">·</span>
      <span><b>{serverInfo?.ip ?? '—'}</b></span>
      <span style="color: var(--border-strong);">·</span>
      <span><b>docker {serverInfo?.docker_version ?? '—'}</b></span>
      <span style="color: var(--border-strong);">·</span>
      <span style="display: inline-flex; align-items: center; gap: 6px;">
        <span style="width: 6px; height: 6px; border-radius: 9999px; background: var(--color-success-400); box-shadow: 0 0 8px var(--color-success-400);"></span>
        <b>online</b>
      </span>
    </div>
  </header>

  <main class="ed-stage" class:ed-hero={step === 1}>
    <div class="ed-canvas">
      <div style="position: relative; z-index: 1;">
        <div class="ed-eyebrow">
          <span style="min-width: {Math.max(eyebrowShown.length, ED_HEADERS[step].eyebrow.length)}ch; display: inline-block;">{eyebrowShown}</span>
          <span class="ed-cursor"></span>
        </div>
        <h1 class="ed-title">{@html ED_HEADERS[step].title}</h1>
        <p class="ed-subtitle">{ED_HEADERS[step].sub}</p>
      </div>

      <div class="ed-content" class:ed-content--open={step === 1 || step === 7}>
        {#if step === 1}
          <!-- Step 1 — preflight manifest (live from /setup/preflight) -->
          <div class="ed-hero-body">
            {#if preflightLoading}
              <div class="ed-manifest-note" style="padding-left: 0; margin-top: 0;">
                Probing the host…
              </div>
            {:else if preflightError}
              <div class="ed-manifest-note" style="padding-left: 0; margin-top: 0; color: var(--color-danger-400);">
                Preflight failed: {preflightError}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" style="margin-left: 8px;" onclick={loadPreflight}>Retry</button>
              </div>
            {:else}
              <ul class="ed-manifest">
                {#each preflightChecks as c (c.key)}
                  <li class="ed-manifest-row ed-manifest-row--{c.status}">
                    <span class="ed-manifest-marker" aria-hidden="true">
                      {c.status === 'ok' ? '✓' : c.status === 'warn' ? '!' : '✗'}
                    </span>
                    <span class="ed-manifest-label">{c.label}</span>
                    <span class="ed-manifest-value">{c.value}</span>
                  </li>
                {/each}
              </ul>
              {#each preflightWarnings as w}
                {#if w.message || w.hint}
                  <div class="ed-manifest-note">
                    <span style="color: var(--color-warning-400); margin-right: 4px;">{w.label}:</span>
                    {w.message ?? ''}
                    {#if w.hint}
                      <a href={w.hint} style="color: var(--color-brand-300);" class="underline-offset-2 hover:underline" target="_blank" rel="noopener">{w.hint}</a>
                    {/if}
                  </div>
                {/if}
              {/each}
            {/if}
            <div class="ed-hero-cta">
              <button type="button" class="dm-btn dm-btn-primary dm-btn-lg" onclick={goNext} disabled={preflightHasFail || preflightLoading}>
                Begin setup
                <svg class="w-4 h-4" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
                  <path d="M8 5l5 5-5 5" /><path d="M13 10H4" />
                </svg>
              </button>
              <span class="ed-hero-cta-meta">7 steps · about 60 seconds · no internet required</span>
            </div>
          </div>

        {:else if step === 2}
          <!-- Step 2 — data dir input + live status -->
          <label for="data-dir" class="block text-xs font-medium mb-2" style="color: var(--fg-muted);">Data directory</label>
          <input
            id="data-dir"
            class="dm-input dm-input-mono"
            bind:value={dataDir}
            spellcheck="false"
            autocomplete="off"
          />
          <div class="mt-2.5 flex items-center gap-2 text-xs"
            style:color={dataDirStatus.kind === 'ok' ? 'var(--color-success-400)' : dataDirStatus.kind === 'warn' ? 'var(--color-warning-400)' : 'var(--color-danger-400)'}>
            <span aria-hidden="true">
              {dataDirStatus.kind === 'ok' ? '✓' : dataDirStatus.kind === 'warn' ? '!' : '✗'}
            </span>
            <span style:color={dataDirStatus.kind === 'ok' ? 'var(--fg)' : 'inherit'}>{dataDirStatus.text}</span>
          </div>
          <p class="text-xs mt-5 leading-relaxed" style="color: var(--fg-muted);">
            Dockmesh stores its database, secrets, CA certificates, and stack compose files here.
            About 50 MB on a fresh install; grows with deploy history and stack count.
          </p>
          <div class="mt-5 pt-4 text-xs" style="border-top: 1px solid var(--border-subtle); color: var(--fg-subtle);">
            <div class="mb-2">Platform defaults</div>
            <div class="grid gap-2" style="grid-template-columns: 1fr 1fr;">
              <button type="button" class="text-left font-mono" style="color: var(--fg-muted);" onclick={() => (dataDir = '/var/lib/dockmesh')}>
                <div style="color: var(--fg);">/var/lib/dockmesh</div>
                <div style="color: var(--fg-subtle); font-size: 10.5px; margin-top: 2px;"># Linux · follows FHS · used by postgresql, mysql</div>
              </button>
              <button type="button" class="text-left font-mono" style="color: var(--fg-muted);" onclick={() => (dataDir = '/usr/local/var/dockmesh')}>
                <div style="color: var(--fg);">/usr/local/var/dockmesh</div>
                <div style="color: var(--fg-subtle); font-size: 10.5px; margin-top: 2px;"># macOS · homebrew convention</div>
              </button>
            </div>
          </div>
          <div class="dm-divider mt-8 mb-5"></div>
          <div class="flex items-center justify-between">
            <button type="button" class="dm-btn dm-btn-ghost" onclick={goBack}>← Back</button>
            <button type="button" class="dm-btn dm-btn-primary" onclick={goNext} disabled={dataDirStatus.kind === 'fail'}>Continue →</button>
          </div>

        {:else if step === 3}
          <!-- Step 3 — service user. Three modes:
                 1. recommended (default): use the dockmesh user the
                    install script already created. No input needed.
                 2. existing: pick a different existing system user.
                 3. new: create a new user under a custom name. -->
          <div class="flex flex-col gap-3">

            <!-- Recommended: use dockmesh -->
            <label class="flex items-start gap-3 p-4 rounded-lg cursor-pointer"
              style:border={'1px solid'}
              style:border-color={svcMode === 'recommended' ? 'var(--color-brand-500)' : 'var(--border)'}
              style:background={svcMode === 'recommended' ? 'var(--accent-bg)' : 'transparent'}>
              <input type="radio" name="svc-mode" class="dm-radio mt-1" checked={svcMode === 'recommended'} onchange={() => (svcMode = 'recommended')} />
              <div class="flex-1 min-w-0">
                <div class="flex items-baseline gap-2">
                  <div class="text-sm font-medium" style="color: var(--fg);">Use the dockmesh service user</div>
                  <span class="text-[10px] uppercase tracking-[0.1em]" style="color: var(--accent-fg);">Recommended</span>
                </div>
                <div class="text-xs mt-1" style="color: var(--fg-muted);">
                  Created automatically by the install script. Already in the <span class="font-mono">docker</span> group, dedicated to dockmesh, no shell login.
                </div>
                {#if svcMode === 'recommended' && svcCheck.kind !== 'idle' && svcCheck.text}
                  <div class="mt-3 text-xs flex items-center gap-1.5"
                    style:color={svcCheck.kind === 'ok' ? 'var(--color-success-400)' : svcCheck.kind === 'warn' ? 'var(--color-warning-400)' : 'var(--color-danger-400)'}>
                    {svcCheck.kind === 'ok' ? '✓' : svcCheck.kind === 'warn' ? '!' : '✗'}
                    {svcCheck.text}
                  </div>
                {/if}
              </div>
            </label>

            <!-- Use a different existing user -->
            <label class="flex items-start gap-3 p-4 rounded-lg cursor-pointer"
              style:border={'1px solid'}
              style:border-color={svcMode === 'existing' ? 'var(--color-brand-500)' : 'var(--border)'}
              style:background={svcMode === 'existing' ? 'var(--accent-bg)' : 'transparent'}>
              <input type="radio" name="svc-mode" class="dm-radio mt-1" checked={svcMode === 'existing'} onchange={() => (svcMode = 'existing')} />
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium" style="color: var(--fg);">Use a different existing user</div>
                <div class="text-xs mt-1" style="color: var(--fg-muted);">
                  For ops who already have a service account they prefer (e.g. <span class="font-mono">ops</span>, <span class="font-mono">deploy</span>).
                </div>
                {#if svcMode === 'existing'}
                  <div class="mt-3">
                    <label for="existing-user" class="block text-xs mb-1.5" style="color: var(--fg-muted);">Username</label>
                    <input id="existing-user" class="dm-input dm-input-mono" placeholder="ops" bind:value={svcExistingUser} aria-invalid={svcCheck.kind === 'fail'} spellcheck="false" autocomplete="off" />
                    {#if svcCheck.kind !== 'idle' && svcCheck.text}
                      <div class="mt-2 text-xs flex items-center gap-1.5"
                        style:color={svcCheck.kind === 'ok' ? 'var(--color-success-400)' : svcCheck.kind === 'warn' ? 'var(--color-warning-400)' : 'var(--color-danger-400)'}>
                        {svcCheck.kind === 'ok' ? '✓' : svcCheck.kind === 'warn' ? '!' : '✗'}
                        {svcCheck.text}
                      </div>
                    {/if}
                    <label class="mt-3 flex items-start gap-2.5 cursor-pointer">
                      <input type="checkbox" class="dm-check mt-0.5" bind:checked={svcAddToDocker} />
                      <span class="text-xs" style="color: var(--fg);">
                        Add to <span class="font-mono">docker</span> group if not already a member
                      </span>
                    </label>
                  </div>
                {/if}
              </div>
            </label>

            <!-- Create a new custom-named user -->
            <label class="flex items-start gap-3 p-4 rounded-lg cursor-pointer"
              style:border={'1px solid'}
              style:border-color={svcMode === 'new' ? 'var(--color-brand-500)' : 'var(--border)'}
              style:background={svcMode === 'new' ? 'var(--accent-bg)' : 'transparent'}>
              <input type="radio" name="svc-mode" class="dm-radio mt-1" checked={svcMode === 'new'} onchange={() => (svcMode = 'new')} />
              <div class="flex-1 min-w-0">
                <div class="text-sm font-medium" style="color: var(--fg);">Create a new user under a different name</div>
                <div class="text-xs mt-1" style="color: var(--fg-muted);">
                  Pick your own name (e.g. <span class="font-mono">dockermaster</span>) — the wizard creates it on commit.
                </div>
                {#if svcMode === 'new'}
                  <div class="mt-3 flex flex-col gap-3">
                    <div>
                      <label for="new-user" class="block text-xs mb-1.5" style="color: var(--fg-muted);">Username</label>
                      <input id="new-user" class="dm-input dm-input-mono" placeholder="dockermaster" bind:value={svcNewUser} aria-invalid={svcCheck.kind === 'fail'} spellcheck="false" autocomplete="off" />
                      {#if svcCheck.kind !== 'idle' && svcCheck.text}
                        <div class="mt-2 text-xs flex items-center gap-1.5"
                          style:color={svcCheck.kind === 'ok' ? 'var(--color-success-400)' : svcCheck.kind === 'warn' ? 'var(--color-warning-400)' : 'var(--color-danger-400)'}>
                          {svcCheck.kind === 'ok' ? '✓' : svcCheck.kind === 'warn' ? '!' : '✗'}
                          {svcCheck.text}
                        </div>
                      {/if}
                      <div class="mt-1.5 font-mono text-[10.5px]" style="color: var(--fg-subtle);">
                        # uid auto-assigned · /usr/sbin/nologin · won't replace the dockmesh user that already exists
                      </div>
                    </div>
                    <label class="flex items-start gap-2.5 cursor-pointer">
                      <input type="checkbox" class="dm-check mt-0.5" bind:checked={svcAddToDocker} />
                      <span class="text-xs" style="color: var(--fg);">
                        Add to <span class="font-mono">docker</span> group
                        <span class="block text-[11px] mt-0.5" style="color: var(--fg-subtle);">
                          Required so the new user can talk to the Docker daemon.
                        </span>
                      </span>
                    </label>
                  </div>
                {/if}
              </div>
            </label>
          </div>
          <div class="dm-divider mt-8 mb-5"></div>
          <div class="flex items-center justify-between">
            <button type="button" class="dm-btn dm-btn-ghost" onclick={goBack}>← Back</button>
            <button type="button" class="dm-btn dm-btn-primary" onclick={goNext} disabled={svcBlockNext}>Continue →</button>
          </div>

        {:else if step === 4}
          <!-- Step 4 — admin -->
          <div class="flex flex-col gap-5">
            <div>
              <label for="admin-user" class="block text-xs font-medium mb-2" style="color: var(--fg-muted);">Username</label>
              <input id="admin-user" class="dm-input dm-input-mono" bind:value={adminUsername} spellcheck="false" autocomplete="off" />
              <div class="mt-1.5 font-mono text-[10.5px]" style="color: var(--fg-subtle);">
                # role=superadmin · auth=local · can be disabled later if you wire OIDC
              </div>
            </div>
            <div>
              <div class="flex items-baseline justify-between mb-2">
                <label for="admin-pw" class="block text-xs font-medium" style="color: var(--fg-muted);">Password</label>
                <button type="button" class="text-xs" style="color: var(--accent-fg);" onclick={generatePassword}>
                  Generate strong password
                </button>
              </div>
              <div class="relative">
                {#if adminShowPassword}
                  <input id="admin-pw" type="text" class="dm-input dm-input-mono pr-20" bind:value={adminPassword} autocomplete="new-password" />
                {:else}
                  <input id="admin-pw" type="password" class="dm-input dm-input-mono pr-20" bind:value={adminPassword} autocomplete="new-password" />
                {/if}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs absolute right-1.5 top-1/2 -translate-y-1/2" onclick={() => (adminShowPassword = !adminShowPassword)}>
                  {adminShowPassword ? 'Hide' : 'Show'}
                </button>
              </div>
              <div class="mt-2.5 flex items-center gap-2">
                <div class="flex gap-1 flex-1">
                  {#each [0, 1, 2, 3] as i}
                    <div class="dm-strength-seg" style:background={
                      strength.score > i
                        ? (strength.score === 1 ? 'var(--color-danger-400)'
                          : strength.score === 2 ? 'var(--color-warning-400)'
                          : strength.score === 3 ? 'var(--color-success-400)'
                          : 'var(--color-success-500)')
                        : 'var(--border)'
                    }></div>
                  {/each}
                </div>
                <span class="text-[11px] tabular-nums w-12 text-right" style="color: var(--fg-subtle);">{strength.label}</span>
              </div>
            </div>
            <div>
              <label for="admin-email" class="block text-xs font-medium mb-2" style="color: var(--fg-muted);">
                Email <span style="color: var(--fg-subtle);">(optional)</span>
              </label>
              <input id="admin-email" type="email" class="dm-input" placeholder="admin@example.com" bind:value={adminEmail} spellcheck="false" autocomplete="off" />
            </div>
          </div>
          <div class="dm-divider mt-8 mb-5"></div>
          <div class="flex items-center justify-between">
            <button type="button" class="dm-btn dm-btn-ghost" onclick={goBack}>← Back</button>
            <button type="button" class="dm-btn dm-btn-primary" onclick={goNext} disabled={adminBlockNext}>Continue →</button>
          </div>

        {:else if step === 5}
          <!-- Step 5 — public URL -->
          <label for="pub-url" class="block text-xs font-medium mb-2" style="color: var(--fg-muted);">Public URL</label>
          <div class="flex items-stretch gap-2">
            <input id="pub-url" class="dm-input dm-input-mono flex-1" bind:value={publicUrl} spellcheck="false" autocomplete="off" />
            <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm whitespace-nowrap" onclick={runUrlTest} disabled={urlTest.state === 'loading'}>
              {urlTest.state === 'loading' ? 'Testing…' : 'Test connection'}
            </button>
          </div>
          <div class="mt-1.5 font-mono text-[10.5px]" style="color: var(--fg-subtle);">
            # written to DOCKMESH_BASE_URL · embedded in agent registration & OIDC redirect_uri
          </div>
          {#if urlTest.state === 'ok'}
            <div class="mt-2.5 flex items-center gap-2 text-xs" style="color: var(--color-success-400);">
              ✓ reached this server in <span class="font-mono">{urlTest.ms}ms</span>
            </div>
          {:else if urlTest.state === 'fail'}
            <div class="mt-2.5 flex items-center gap-2 text-xs" style="color: var(--color-danger-400);">
              ✗ couldn't reach ({urlTest.reason})
            </div>
          {/if}
          <p class="text-xs mt-5 leading-relaxed" style="color: var(--fg-muted);">
            For now, the LAN IP works fine. Once you've set up a domain + reverse proxy or DNS,
            change this in <span style="color: var(--fg);">Settings → System</span> without restarting the server.
          </p>
          <div class="dm-divider mt-8 mb-5"></div>
          <div class="flex items-center justify-between">
            <button type="button" class="dm-btn dm-btn-ghost" onclick={goBack}>← Back</button>
            <button type="button" class="dm-btn dm-btn-primary" onclick={goNext} disabled={urlBlockNext}>Continue →</button>
          </div>

        {:else if step === 6}
          <!-- Step 6 — review -->
          <ul class="flex flex-col" style="border-top: 1px solid var(--border-subtle);">
            {#each [
              { label: 'Data directory', value: dataDir, jumpTo: 2 as Step, dim: false },
              { label: 'Service user', value: svcMode === 'recommended' ? `${RECOMMENDED_USER} (recommended)` : svcMode === 'new' ? `${svcNewUser} (new${svcAddToDocker ? ', in docker group' : ''})` : (svcExistingUser ? `${svcExistingUser}${svcAddToDocker ? ' (in docker group)' : ''}` : '—'), jumpTo: 3 as Step, dim: false },
              { label: 'Admin user', value: `${adminUsername} / ${'•'.repeat(Math.max(8, adminPassword.length || 8))}`, jumpTo: 4 as Step, dim: false },
              { label: 'Admin email', value: adminEmail || '—', jumpTo: 4 as Step, dim: !adminEmail },
              { label: 'Public URL', value: publicUrl, jumpTo: 5 as Step, dim: false }
            ] as r}
              <li class="grid items-baseline py-3 gap-3" style="grid-template-columns: 9.5rem 1fr auto; border-bottom: 1px solid var(--border-subtle);">
                <span class="text-xs" style="color: var(--fg-muted);">{r.label}</span>
                <span class="text-sm font-mono truncate" style:color={r.dim ? 'var(--fg-subtle)' : 'var(--fg)'} title={r.value}>{r.value}</span>
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => jumpTo(r.jumpTo)} disabled={installing}>Edit</button>
              </li>
            {/each}
          </ul>
          <p class="text-xs mt-5 leading-relaxed" style="color: var(--fg-muted);">
            On confirm, Dockmesh writes the env file, creates the user (if needed), seeds the
            database, and restarts the service. About 3 seconds.
          </p>
          <div class="dm-divider mt-8 mb-5"></div>
          <div class="flex items-center justify-between">
            <button type="button" class="dm-btn dm-btn-ghost" onclick={goBack} disabled={installing}>← Back</button>
            <div class="flex items-center gap-3">
              {#if installing}
                <span class="text-xs flex items-center gap-2" style="color: var(--fg-muted);">{installMsg}</span>
              {/if}
              <button type="button" class="dm-btn dm-btn-primary" onclick={runInstall} disabled={installing}>
                {installing ? 'Installing…' : 'Install'}
              </button>
            </div>
          </div>

        {:else if step === 7}
          <!-- Step 7 — terminal triumph -->
          <div class="ed-term">
            <div class="ed-term-chrome">
              <div class="ed-term-dots" aria-hidden="true">
                <span></span><span></span><span></span>
              </div>
              <div class="ed-term-title">dockmesh@host · ~</div>
              <div class="ed-term-spacer"></div>
            </div>
            <div class="ed-term-body" role="log" aria-live="polite">
              {#each streamLines as l (l.ts + l.step)}
                <div class="ed-term-line ed-term-line--{l.status}">
                  <span class="ed-term-mark ed-term-mark--{l.status}" aria-hidden="true">{statusToMark(l.status)}</span>
                  <span class="ed-term-text">{l.message}</span>
                </div>
              {/each}
              {#if installing}
                <div class="ed-term-line ed-term-line--cursor">
                  <span class="ed-cursor"></span>
                </div>
              {/if}
              {#if installFailed}
                <div class="ed-term-line ed-term-line--fail" style="margin-top: 8px;">
                  <span class="ed-term-mark ed-term-mark--fail" aria-hidden="true">✗</span>
                  <span class="ed-term-text">install failed: {installFailed}</span>
                </div>
              {/if}
              {#if credsRevealed}
                <div class="ed-term-creds">
                  <div class="ed-term-creds-label">credentials</div>
                  <div class="ed-term-creds-grid">
                    <span class="ed-term-creds-key">login_url</span>
                    <span class="ed-term-creds-eq">=</span>
                    <span class="ed-term-creds-val">{publicUrl}</span>
                    <button type="button" class="ed-term-creds-copy" onclick={() => copyToClipboard('url', publicUrl)}>
                      {copiedKey === 'url' ? '✓ copied' : 'copy'}
                    </button>

                    <span class="ed-term-creds-key">username</span>
                    <span class="ed-term-creds-eq">=</span>
                    <span class="ed-term-creds-val">{adminUsername}</span>
                    <button type="button" class="ed-term-creds-copy" onclick={() => copyToClipboard('user', adminUsername)}>
                      {copiedKey === 'user' ? '✓ copied' : 'copy'}
                    </button>

                    <span class="ed-term-creds-key">password</span>
                    <span class="ed-term-creds-eq">=</span>
                    <span class="ed-term-creds-val" style="display: inline-flex; align-items: center; gap: 8px;">
                      <span>{credShowPw ? finalPassword : '•'.repeat(Math.max(10, finalPassword.length))}</span>
                      <button type="button" onclick={() => (credShowPw = !credShowPw)}
                        style="background: none; border: 0; cursor: pointer; color: var(--fg-subtle); padding: 0; font-size: 11px; font-family: var(--font-mono);">
                        {credShowPw ? '[hide]' : '[show]'}
                      </button>
                    </span>
                    <button type="button" class="ed-term-creds-copy" onclick={() => copyToClipboard('pw', finalPassword)}>
                      {copiedKey === 'pw' ? '✓ copied' : 'copy'}
                    </button>
                  </div>
                </div>
              {/if}
            </div>
            {#if credsRevealed}
              <div class="ed-term-cta">
                <a href="/" data-sveltekit-reload class="dm-btn dm-btn-primary dm-btn-lg">
                  Open dashboard
                  <svg class="w-4 h-4" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M8 5l5 5-5 5" /><path d="M13 10H4" />
                  </svg>
                </a>
                <span class="ed-term-cta-meta">
                  forgot the password? run
                  <code class="font-mono px-1.5 py-0.5 rounded" style="background: var(--bg); color: var(--fg-muted); border: 1px solid var(--border);">
                    sudo dockmesh admin reset-password
                  </code>
                </span>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  </main>
</div>

<style>
  /* Editorial layout. Self-contained — pulls only the CSS variables
     defined in app.css's @theme block. No deps on Tailwind utility
     classes outside what app.css already defines (.dm-btn, .dm-input). */

  :global(body) {
    background: var(--bg-elevated);
    color: var(--fg);
  }

  /* Mono-input variant */
  :global(.dm-input-mono) {
    font-family: var(--font-mono);
    font-size: 0.8125rem;
  }
  :global(.dm-btn-lg) { padding: 0.75rem 1.4rem; font-size: 0.95rem; border-radius: 10px; }

  /* Radio / check (mockup style — minimal, monochrome) */
  :global(.dm-radio), :global(.dm-check) {
    appearance: none;
    width: 1rem; height: 1rem;
    border: 1px solid var(--border-strong);
    background: var(--bg);
    flex-shrink: 0;
    cursor: pointer;
    transition: border-color 0.15s ease, background-color 0.15s ease;
  }
  :global(.dm-radio) { border-radius: 9999px; }
  :global(.dm-check) { border-radius: 4px; }
  :global(.dm-radio:checked), :global(.dm-check:checked) {
    border-color: var(--color-brand-500);
    background: var(--color-brand-500);
  }
  :global(.dm-radio:checked) { box-shadow: inset 0 0 0 3px var(--surface); }
  :global(.dm-check:checked) {
    background-image: url("data:image/svg+xml,%3Csvg viewBox='0 0 12 12' fill='none' xmlns='http://www.w3.org/2000/svg'%3E%3Cpath d='M2.5 6.2L4.8 8.5L9.5 3.8' stroke='white' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
    background-size: 100% 100%;
  }

  /* Strength bar */
  :global(.dm-strength-seg) {
    height: 4px; flex: 1;
    background: var(--border);
    border-radius: 2px;
    transition: background-color 0.15s ease;
  }

  /* Divider */
  :global(.dm-divider) { height: 1px; background: var(--border); }

  /* ───── Editorial shell ───── */
  .ed-shell {
    min-height: 100vh;
    background: var(--bg);
    color: var(--fg);
    position: relative;
    overflow: hidden;
    isolation: isolate;
    font-family: 'Inter', system-ui, sans-serif;
  }

  .ed-mesh {
    position: absolute; inset: 0; z-index: 0;
    background-image: radial-gradient(circle at 1px 1px, rgba(148, 163, 184, 0.10) 1px, transparent 0);
    background-size: 24px 24px;
    mask-image: radial-gradient(ellipse 70% 60% at 50% 40%, #000 30%, transparent 80%);
    -webkit-mask-image: radial-gradient(ellipse 70% 60% at 50% 40%, #000 30%, transparent 80%);
    pointer-events: none;
  }
  .ed-mesh--live {
    background-image:
      radial-gradient(circle at 1px 1px, rgba(56, 189, 248, 0.55) 1.5px, transparent 0),
      radial-gradient(circle at 13px 13px, rgba(56, 189, 248, 0.18) 1px, transparent 0);
    background-size: 24px 24px, 24px 24px;
  }

  .ed-watermark {
    position: absolute;
    right: -120px; top: 50%; transform: translateY(-50%);
    z-index: 0;
    color: var(--color-brand-500);
    opacity: 0.025;
    pointer-events: none;
  }

  /* Vertical step rail */
  .ed-rail {
    position: fixed;
    left: 32px; top: 50%; transform: translateY(-50%);
    z-index: 5;
    display: flex; flex-direction: column;
    padding: 8px 0;
  }
  .ed-rail::before {
    content: "";
    position: absolute;
    left: 11px;
    top: 22px; bottom: 22px;
    width: 1px;
    background: var(--border);
  }
  .ed-rail-item {
    position: relative;
    display: flex; align-items: center; gap: 14px;
    padding: 8px 0 8px 32px;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    cursor: pointer;
    transition: color 0.18s ease, transform 0.18s ease;
    background: none; border: 0;
    text-align: left;
  }
  .ed-rail-item::before {
    content: "";
    position: absolute;
    left: 7px; top: 50%; transform: translateY(-50%);
    width: 9px; height: 9px;
    border-radius: 9999px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    transition: all 0.22s cubic-bezier(0.2, 0.7, 0.2, 1);
    z-index: 1;
  }
  .ed-rail-item[aria-disabled="true"] { cursor: not-allowed; }
  .ed-rail-item:hover:not([aria-disabled="true"]) { color: var(--fg); }
  .ed-rail-item:hover:not([aria-disabled="true"])::before { border-color: var(--fg-muted); }
  .ed-rail-item .ed-rail-num { width: 18px; }
  .ed-rail-item .ed-rail-label {
    opacity: 0; transform: translateX(-4px);
    transition: opacity 0.18s ease, transform 0.18s ease;
    text-transform: none;
    font-family: 'Inter', system-ui, sans-serif;
    font-size: 12px;
    letter-spacing: 0;
    color: var(--fg-muted);
    white-space: nowrap;
  }
  .ed-rail:hover .ed-rail-label { opacity: 1; transform: translateX(0); }
  .ed-rail-item--current {
    color: var(--color-brand-300);
    padding-top: 14px; padding-bottom: 14px;
  }
  .ed-rail-item--current::before {
    width: 13px; height: 13px;
    left: 5px;
    background: var(--color-brand-400);
    border-color: var(--color-brand-300);
    box-shadow: 0 0 0 4px color-mix(in srgb, var(--color-brand-500) 18%, transparent),
                0 0 12px color-mix(in srgb, var(--color-brand-400) 50%, transparent);
  }
  .ed-rail-item--current .ed-rail-label { color: var(--fg); opacity: 1; transform: translateX(0); font-weight: 500; }
  .ed-rail-item--done { color: var(--fg-muted); }
  .ed-rail-item--done .ed-rail-num { color: var(--color-brand-700); }
  .ed-rail-item--done::before {
    background: color-mix(in srgb, var(--color-brand-500) 65%, transparent);
    border-color: color-mix(in srgb, var(--color-brand-400) 70%, transparent);
  }

  /* Top bar */
  .ed-topbar {
    position: relative; z-index: 4;
    display: flex; align-items: center; justify-content: space-between;
    padding: 22px 32px 22px 92px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.05em;
  }
  .ed-brand {
    display: flex; align-items: center; gap: 10px;
    color: var(--fg);
    font-family: 'Inter', system-ui, sans-serif;
    font-size: 13px; font-weight: 500;
    letter-spacing: -0.01em;
  }
  .ed-brand-mark { color: var(--color-brand-400); }
  .ed-brand-logo { width: 32px; height: 32px; display: block; flex-shrink: 0; }
  .ed-meta { display: flex; align-items: center; gap: 14px; }
  .ed-meta b { color: var(--fg-muted); font-weight: 500; }

  /* Stage + canvas */
  .ed-stage {
    position: relative; z-index: 2;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    padding: 24px 60px 60px 92px;
  }
  .ed-canvas {
    max-width: 880px;
    margin: 0 auto;
    width: 100%;
    position: relative;
  }
  .ed-hero { margin-top: 16px; }
  .ed-hero :global(.ed-title) { font-size: 64px; }
  .ed-hero .ed-content { padding: 40px 44px 44px; }

  /* Eyebrow */
  .ed-eyebrow {
    display: inline-flex; align-items: center; gap: 10px;
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.18em;
    text-transform: uppercase;
    color: var(--color-brand-300);
    margin-bottom: 22px;
  }
  .ed-eyebrow::before {
    content: "";
    width: 24px; height: 1px;
    background: currentColor;
  }
  .ed-cursor {
    display: inline-block;
    width: 8px; height: 1.05em;
    background: var(--color-brand-400);
    margin-left: 4px;
    transform: translateY(2px);
    animation: ed-blink 1.05s steps(2) infinite;
  }
  @keyframes ed-blink { 0%, 50% { opacity: 1; } 50.01%, 100% { opacity: 0; } }

  /* Editorial title */
  .ed-title {
    font-size: 48px;
    line-height: 1.02;
    letter-spacing: -0.025em;
    font-weight: 600;
    color: var(--fg);
    text-wrap: balance;
    max-width: 18ch;
  }
  :global(.ed-title-accent) {
    color: var(--color-brand-300);
    font-style: italic;
    font-weight: 500;
  }
  .ed-subtitle {
    font-size: 16px;
    line-height: 1.55;
    color: var(--fg-muted);
    margin-top: 20px;
    max-width: 56ch;
    text-wrap: pretty;
  }

  /* Content frame */
  .ed-content {
    position: relative;
    margin-top: 48px;
    padding: 36px 40px 40px;
    background: linear-gradient(
      to bottom,
      color-mix(in srgb, var(--surface) 85%, transparent) 0%,
      var(--surface) 100%);
    border: 1px solid var(--border);
    border-radius: 14px;
    box-shadow:
      inset 0 1px 0 rgba(255,255,255,0.025),
      0 30px 60px -30px rgba(0, 0, 0, 0.6);
  }
  .ed-content::before {
    content: "";
    position: absolute;
    top: -1px; left: 24px; right: 24px;
    height: 1px;
    background: linear-gradient(90deg, transparent, var(--color-brand-500), transparent);
    opacity: 0.5;
  }
  .ed-content--open {
    background: none !important;
    border: 0 !important;
    box-shadow: none !important;
    padding: 0 !important;
    margin-top: 56px;
  }
  .ed-content--open::before { display: none !important; }

  /* Step 1 hero body */
  .ed-hero-body {
    display: flex;
    flex-direction: column;
    gap: 28px;
  }
  .ed-manifest {
    display: flex;
    flex-direction: column;
    border-top: 1px solid var(--border-subtle);
    padding-left: 0;
    list-style: none;
  }
  .ed-manifest-row {
    display: grid;
    grid-template-columns: 28px 1fr auto;
    align-items: baseline;
    gap: 16px;
    padding: 14px 0;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 14px;
  }
  .ed-manifest-marker {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--color-success-400);
    text-align: center;
    font-weight: 600;
  }
  .ed-manifest-row--warn .ed-manifest-marker { color: var(--color-warning-400); }
  .ed-manifest-row--fail .ed-manifest-marker { color: var(--color-danger-400); }
  .ed-manifest-label { color: var(--fg); }
  .ed-manifest-value {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-muted);
    text-align: right;
  }
  .ed-manifest-note {
    font-size: 12px;
    color: var(--fg-muted);
    padding-left: 44px;
    margin-top: -16px;
  }
  .ed-hero-cta {
    display: flex;
    align-items: center;
    gap: 20px;
    margin-top: 8px;
    flex-wrap: wrap;
  }
  .ed-hero-cta-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }

  /* Step 7 terminal. Chrome + body must hug (no gap) so the window
     reads as one panel with three traffic-light dots above the
     content. Spacing under the panel is added via margin on the CTA
     row below, not via flex-gap on this parent. */
  .ed-term {
    display: flex;
    flex-direction: column;
    max-width: 720px;
    margin: 0 auto;
  }
  .ed-term-chrome {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    background: #0b0d10;
    border: 1px solid #1f242b;
    border-bottom: 0;
    border-radius: 8px 8px 0 0;
  }
  .ed-term-dots {
    display: inline-flex; gap: 6px;
  }
  .ed-term-dots span {
    width: 11px; height: 11px;
    border-radius: 999px;
    background: #2a3038;
  }
  .ed-term-dots span:nth-child(1) { background: #ff5f56; opacity: 0.6; }
  .ed-term-dots span:nth-child(2) { background: #ffbd2e; opacity: 0.5; }
  .ed-term-dots span:nth-child(3) { background: #27c93f; opacity: 0.55; }
  .ed-term-title {
    flex: 1;
    text-align: center;
    font-family: var(--font-mono);
    font-size: 11px;
    color: #6b7280;
    letter-spacing: 0.04em;
  }
  .ed-term-spacer { width: 33px; }
  .ed-term-body {
    background: #0b0d10;
    border: 1px solid #1f242b;
    border-radius: 0 0 8px 8px;
    padding: 18px 20px 22px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.7;
    color: #c9d1d9;
    min-height: 280px;
  }
  /* Marker + content layout. The marker is absolutely positioned in
     a 22px gutter so the text is a normal block — wraps cleanly on
     long messages, copies as a single line per row. Previous flex
     layout broke when long warn lines wrapped (mark stayed at top
     while text wrapped under it) and copied as alternating
     mark/text fragments. */
  .ed-term-line {
    position: relative;
    padding-left: 22px;
    line-height: 1.7;
    animation: ed-term-line-in 0.2s cubic-bezier(0.2, 0.7, 0.2, 1);
    word-break: break-word;
  }
  @keyframes ed-term-line-in {
    from { opacity: 0; transform: translateY(-2px); }
    to   { opacity: 1; transform: translateY(0); }
  }
  .ed-term-mark {
    position: absolute;
    left: 0;
    top: 0;
    color: var(--color-success-400);
    font-weight: 600;
    width: 14px;
    text-align: center;
  }
  .ed-term-mark--info { color: #6b7280; }
  .ed-term-mark--done { color: var(--color-brand-400); }
  .ed-term-mark--warn { color: var(--color-warning-400); }
  .ed-term-mark--fail { color: var(--color-danger-400); }
  .ed-term-mark--start { color: #6b7280; }
  .ed-term-line--cmd .ed-term-text { color: #f0f6fc; }
  .ed-term-line--ok .ed-term-text { color: #c9d1d9; }
  .ed-term-line--info .ed-term-text { color: #8b949e; }
  .ed-term-line--start .ed-term-text { color: #8b949e; }
  .ed-term-line--warn .ed-term-text { color: var(--color-warning-400); }
  .ed-term-line--fail .ed-term-text { color: var(--color-danger-400); }
  .ed-term-line--done .ed-term-text { color: var(--color-brand-400); font-weight: 600; }
  .ed-term-line--cursor { padding-left: 22px; }

  .ed-term-creds {
    margin-top: 22px;
    padding-top: 20px;
    border-top: 1px dashed #2a3038;
    animation: ed-term-creds-in 0.5s cubic-bezier(0.2, 0.7, 0.2, 1);
  }
  @keyframes ed-term-creds-in {
    from { opacity: 0; transform: translateY(4px); }
    to   { opacity: 1; transform: translateY(0); }
  }
  .ed-term-creds-label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: #6b7280;
    margin-bottom: 10px;
  }
  .ed-term-creds-grid {
    display: grid;
    grid-template-columns: max-content max-content 1fr max-content;
    gap: 8px 14px;
    align-items: center;
  }
  .ed-term-creds-key { color: var(--color-brand-400); font-size: 12.5px; }
  .ed-term-creds-eq { color: #6b7280; }
  .ed-term-creds-val { color: #f0f6fc; font-size: 12.5px; word-break: break-all; }
  .ed-term-creds-copy {
    background: transparent;
    border: 1px solid #2a3038;
    color: #8b949e;
    font-family: var(--font-mono);
    font-size: 11px;
    padding: 3px 9px;
    border-radius: 4px;
    cursor: pointer;
    transition: all 0.15s ease;
  }
  .ed-term-creds-copy:hover {
    border-color: var(--color-brand-400);
    color: var(--color-brand-400);
  }
  .ed-term-cta {
    display: flex; align-items: center; gap: 20px; flex-wrap: wrap;
    margin-top: 24px;
    animation: ed-term-creds-in 0.5s cubic-bezier(0.2, 0.7, 0.2, 1) 0.2s both;
  }
  .ed-term-cta-meta {
    font-size: 12px;
    color: var(--fg-subtle);
    text-wrap: pretty;
    max-width: 38ch;
  }

  /* Mobile */
  @media (max-width: 800px) {
    .ed-rail { display: none; }
    .ed-topbar { padding-left: 24px; }
    .ed-stage { padding: 16px 24px 60px; }
    .ed-title { font-size: 32px; }
    .ed-hero :global(.ed-title) { font-size: 38px; }
    .ed-content { padding: 24px; }
  }

  /* Reduced motion */
  @media (prefers-reduced-motion: reduce) {
    .ed-cursor { animation: none; }
    .ed-term-line { animation: none; }
    .ed-term-creds, .ed-term-cta { animation: none; }
  }
</style>
