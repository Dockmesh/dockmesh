<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { Check, Copy } from 'lucide-svelte';
  import Logo from '$lib/components/shell/Logo.svelte';
  import DiskList, { type Disk } from '$lib/components/setup/DiskList.svelte';
  import { MIN_PASSWORD_LENGTH } from '$lib/password';
  import { copyToClipboard } from '$lib/clipboard';
  import { fmtVersion } from '$lib/version';
  import { fmtMem } from '$lib/format';
  import { m } from '$lib/paraglide/messages.js';
  import {
    DOCS,
    DOCS_BASE,
    agentURL,
    agentURLError,
    baseURLError,
    codeFormatOK,
    codeFromFragment,
    defaultTrustedProxies,
    emailError,
    fingerprintLines,
    generatePassword,
    httpAddr,
    isEarly,
    isLoopback,
    listenAddressError,
    normalizeCode,
    passwordError,
    passwordStrength,
    pathSteps,
    portError,
    railClickable,
    repeatError,
    stacksOutside,
    trustedProxiesError,
    usernameError,
    type SetupPath,
    type StepId,
    type TLSChoice
  } from '$lib/setupWizard';

  const SETUP_HEADER = { 'Content-Type': 'application/json', 'X-Dockmesh-Setup': '1' };
  const NEW_CODE_CMD = 'sudo dockmesh setup new-code';

  // ----- Theme: follows the system, or the choice stored by the app ---------
  onMount(() => {
    const stored = localStorage.getItem('dockmesh.theme');
    const theme = stored === 'light' || stored === 'dark'
      ? stored
      : window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
  });

  // ----- Gate: the code screen and the states without a session -------------
  type CodeState = 'no_code' | 'ready' | 'expired' | 'locked';
  type Gate = 'checking' | 'code' | 'expired' | 'locked' | 'no_code' | 'replaced' | 'wizard';
  let gate = $state<Gate>('checking');
  let codeInput = $state('');
  let codeFromLink = $state(false);
  let codeError = $state<string | null>(null);
  let codeBusy = $state(false);
  let codeHelpOpen = $state(false);
  let wizardStarted = false;

  async function refreshGate(): Promise<boolean> {
    let st: { active?: boolean; code?: CodeState; session?: string } | null = null;
    try {
      const r = await fetch('/api/v1/setup/status', { headers: { Accept: 'application/json' } });
      if (r.ok) st = await r.json();
    } catch {
      /* show the code form; entering the code reports the real state */
    }
    if (st && !st.active) {
      window.location.replace('/login');
      return false;
    }
    if (st?.session === 'valid' || st?.session === 'read_only') {
      gate = 'wizard';
      return true;
    }
    if (st?.session === 'replaced') gate = 'replaced';
    else if (st?.code === 'expired') gate = 'expired';
    else if (st?.code === 'locked') gate = 'locked';
    else if (st?.code === 'no_code') gate = 'no_code';
    else gate = 'code';
    return false;
  }

  /** A setup endpoint answered 401: the session ended, show why. */
  async function sessionLost() {
    if (applying) return;
    stopServerInfo();
    wizardStarted = false;
    await refreshGate();
  }

  function codeFieldError(): string | null {
    if (!codeFormatOK(codeInput)) return m.sw_code_err_format();
    return null;
  }

  async function submitCode() {
    codeError = codeFieldError();
    if (codeError) return;
    codeBusy = true;
    try {
      const r = await fetch('/api/v1/setup/code', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({ code: normalizeCode(codeInput) })
      });
      if (r.status === 204) {
        codeInput = '';
        codeFromLink = false;
        gate = 'wizard';
        startWizard();
        return;
      }
      const b = await r.json().catch(() => ({}));
      if (r.status === 429) {
        codeError = m.sw_code_err_rate({ minutes: Math.max(1, Math.ceil((b.retry_after_secs ?? 60) / 60)) });
      } else if (r.status === 401) {
        codeError = m.sw_code_err_wrong({ n: b.attempts_left ?? 0 });
      } else if (b.code_state === 'expired') {
        gate = 'expired';
      } else if (b.code_state === 'locked') {
        gate = 'locked';
      } else if (b.code_state === 'no_code') {
        gate = 'no_code';
      } else if (r.status === 410) {
        window.location.replace('/login');
      } else {
        codeError = m.sw_error_banner({ message: b.error ?? `HTTP ${r.status}` });
      }
    } catch (e) {
      codeError = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
    } finally {
      codeBusy = false;
    }
  }

  function backToCode() {
    codeError = null;
    gate = 'code';
  }

  onMount(async () => {
    // The installer link carries the code after `#`, which never reaches
    // the server. Take it into the field, drop it from the address bar at
    // once and wait for Continue: opening the link again must not take
    // over a running setup.
    takeFragment();
    window.addEventListener('hashchange', takeFragment);
    if (await refreshGate()) startWizard();
  });
  onDestroy(() => window.removeEventListener('hashchange', takeFragment));

  function takeFragment() {
    if (!window.location.hash) return;
    const code = codeFromFragment(window.location.hash);
    history.replaceState(history.state, '', window.location.pathname + window.location.search);
    if (code) {
      codeInput = code;
      codeFromLink = true;
      codeError = null;
    }
  }

  // ----- Server info: header line, fingerprint, session liveness -----------
  type ServerInfo = { version: string; os: string; arch?: string; ip: string; docker_version: string; tls_fingerprint?: string; fresh?: boolean };
  let serverInfo = $state<ServerInfo | null>(null);
  // A 0.5.x install whose setup never finished commits through the old
  // path: its data dir and listener stay as they are and it does not
  // restart.
  const legacy = $derived(serverInfo !== null && serverInfo.fresh === false);
  let serverInfoTimer: ReturnType<typeof setInterval> | null = null;

  async function loadServerInfo() {
    try {
      const r = await fetch('/api/v1/setup/server-info');
      if (r.status === 401) return sessionLost();
      if (r.ok) serverInfo = await r.json();
    } catch {
      /* keep the last value */
    }
  }
  function stopServerInfo() {
    if (serverInfoTimer) clearInterval(serverInfoTimer);
    serverInfoTimer = null;
  }
  onDestroy(stopServerInfo);

  function startWizard() {
    if (wizardStarted) return;
    wizardStarted = true;
    if (!baseURL) baseURL = window.location.origin;
    if (!resumeTo) webPort = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
    if (webPort === '443' || webPort === '80') webPort = '8080';
    if (resumeTo) {
      const to = resumeTo;
      resumeTo = null;
      if (to === 'N7') void install();
      else if (to === 'R3') void restore();
      else void go(to);
    } else if (step === 'W1') step = 'W2';
    loadServerInfo();
    serverInfoTimer = setInterval(loadServerInfo, 10_000);
  }

  // ----- Steps ----------------------------------------------------------------
  let step = $state<StepId>('W1');
  let path = $state<SetupPath | null>(null);
  let choice = $state<SetupPath>('new');
  let banner = $state<string | null>(null);

  const steps = $derived(pathSteps(path));
  const idx = $derived(steps.indexOf(step));
  const early = $derived(isEarly(step));
  const railSteps = $derived(early ? steps.slice(0, 2) : steps);

  function stepName(id: StepId): string {
    switch (id) {
      case 'W1': return m.sw_step_code();
      case 'W2': return m.sw_step_start();
      case 'N1': return m.sw_step_admin();
      case 'N2': case 'R1': return m.sw_step_data();
      case 'N3': return m.sw_step_address();
      case 'N4': return m.sw_step_proxy();
      case 'N5': return m.sw_step_notifications();
      case 'N6': return m.sw_step_review();
      case 'R2': return m.sw_step_backup();
      case 'N7': case 'R3': return m.sw_step_apply();
    }
  }

  async function go(id: StepId) {
    banner = null;
    step = id;
    await tick();
    window.scrollTo({ top: 0, behavior: 'instant' });
    document.getElementById('step-title')?.focus();
  }
  function next() {
    if (idx >= 0 && idx < steps.length - 1) go(steps[idx + 1]);
  }
  function back() {
    if (idx > 0) go(steps[idx - 1]);
  }

  function chooseAndContinue() {
    path = choice;
    next();
  }

  // ----- N1 admin -------------------------------------------------------------
  let username = $state('admin');
  let email = $state('');
  let password = $state('');
  let repeat = $state('');
  let showPassword = $state(false);
  let generated = $state<string | null>(null);
  let copied = $state<string | null>(null);
  let touched = $state<Record<string, boolean>>({});
  let triedNext = $state<Record<string, boolean>>({});

  const strength = $derived(passwordStrength(password));
  const strengthLabel = $derived(
    [m.sw_strength_none(), m.account_strength_weak(), m.setup_strength_ok(), m.account_strength_good(), m.account_strength_strong()][strength]
  );

  function adminErrors() {
    return {
      username: usernameError(username) ? m.sw_admin_err_username() : null,
      email: emailError(email.trim()) ? m.sw_admin_err_email() : null,
      password: (() => {
        const e = passwordError(password, username, MIN_PASSWORD_LENGTH);
        if (e === 'password_short') return m.sw_admin_err_password({ n: MIN_PASSWORD_LENGTH });
        if (e === 'password_is_username') return m.sw_admin_err_password_username();
        return null;
      })(),
      repeat: repeatError(password, repeat) ? m.sw_admin_err_repeat() : null
    };
  }
  const adminErr = $derived(adminErrors());
  const show = (k: string, s: string) => touched[k] || triedNext[s];

  function adminNext() {
    triedNext = { ...triedNext, N1: true };
    if (Object.values(adminErr).some(Boolean)) return;
    next();
  }

  function generate() {
    generated = generatePassword();
    password = generated;
    repeat = generated;
    showPassword = true;
  }

  function copy(key: string, value: string) {
    void copyToClipboard(value);
    copied = key;
    setTimeout(() => { if (copied === key) copied = null; }, 1400);
  }

  // ----- N2 data directory ----------------------------------------------------
  type DirCheck = {
    path: string;
    status: 'ok' | 'warn' | 'fail';
    issue?: string;
    message: string;
    will_create: boolean;
    empty: boolean;
    fs_type?: string;
    free_bytes: number;
  };
  let dataDir = $state('/var/lib/dockmesh');
  let dirCheck = $state<DirCheck | null>(null);
  let dirChecking = $state(false);
  let dirCheckFor = $state('');
  let disks = $state<Disk[]>([]);
  let disksLoaded = false;

  async function loadDisks() {
    if (disksLoaded) return;
    try {
      const r = await fetch('/api/v1/setup/disks');
      if (r.status === 401) return sessionLost();
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      disks = (await r.json()).disks ?? [];
      disksLoaded = true;
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
    }
  }

  async function checkDir(): Promise<boolean> {
    const p = dataDir.trim();
    if (dirCheckFor === p && dirCheck && dirCheck.status !== 'fail') return true;
    dirChecking = true;
    try {
      const r = await fetch('/api/v1/setup/validate-data-dir', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({ path: p })
      });
      if (r.status === 401) {
        await sessionLost();
        return false;
      }
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      dirCheck = await r.json();
      dirCheckFor = p;
      return dirCheck!.status !== 'fail';
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
      return false;
    } finally {
      dirChecking = false;
    }
  }

  function useDisk(p: string) {
    dataDir = p;
    void checkDir();
  }

  async function dataNext() {
    if (legacy) return next();
    if ((await checkDir()) && (await checkStacks())) next();
  }

  function dirIssueText(c: DirCheck): string {
    const p = c.path;
    switch (c.issue) {
      case 'not_absolute': return m.sw_dir_err_absolute();
      case 'bad_characters': return m.sw_dir_err_characters();
      case 'system_dir': return m.sw_dir_err_system({ path: p });
      case 'inside_docker': return m.sw_dir_err_docker();
      case 'existing_install': return m.sw_dir_err_existing({ path: p });
      case 'not_empty': return m.sw_dir_err_not_empty({ path: p });
      case 'network_share': return m.sw_dir_err_network({ path: p, fs: c.fs_type ?? '' });
      case 'no_unix_permissions': return m.sw_dir_err_no_unix({ path: p, fs: c.fs_type ?? '' });
      case 'too_small': return m.sw_dir_err_small({ n: Math.floor(c.free_bytes / 1048576), path: p });
      case 'low_space': return m.sw_dir_warn_low({ n: (c.free_bytes / 1073741824).toFixed(1) });
      case 'not_a_directory': return m.sw_dir_err_file({ path: p });
      case 'no_parent': return m.sw_dir_err_no_parent({ path: p.replace(/\/[^/]+$/, '') || '/' });
      case 'unsafe_parent': return m.sw_dir_err_unsafe_parent({ path: p.replace(/\/[^/]+$/, '') || '/' });
      case 'symlink': return m.sw_dir_err_symlink({ path: p });
      case 'foreign_owner': return m.sw_dir_err_foreign({ path: p });
      default: return c.message;
    }
  }
  const defaultStacks = $derived(dataDir.trim().replace(/\/+$/, '') + '/stacks');
  const dirCurrent = $derived(dirCheck && dirCheckFor === dataDir.trim() ? dirCheck : null);

  // The stacks dir: <data>/stacks unless changed. Outside the data dir it
  // follows the rules root applies before it creates one.
  type StacksCheck = { path: string; status: 'ok' | 'warn' | 'fail'; issue?: string; message: string; command?: string; will_create?: boolean; fs_type?: string; free_bytes?: number };
  let stacksOpen = $state(false);
  let stacksInput = $state('');
  let stacksCheck = $state<StacksCheck | null>(null);
  let stacksCheckFor = $state('');
  let stacksChecking = $state(false);
  const stacksDir = $derived(stacksOpen && stacksInput.trim() ? stacksInput.trim() : defaultStacks);
  const stacksCustom = $derived(stacksDir !== defaultStacks);
  const stacksCurrent = $derived(stacksCheck && stacksCheckFor === `${dataDir.trim()}\n${stacksDir}` ? stacksCheck : null);

  function openStacks() {
    stacksOpen = true;
    if (!stacksInput.trim()) stacksInput = defaultStacks;
  }

  function closeStacks() {
    stacksOpen = false;
    stacksInput = '';
  }

  async function checkStacks(): Promise<boolean> {
    if (!stacksCustom) return true;
    const key = `${dataDir.trim()}\n${stacksDir}`;
    // A refusal is asked again: the admin may have prepared the directory meanwhile.
    if (stacksCheckFor === key && stacksCheck && stacksCheck.status !== 'fail') return true;
    stacksChecking = true;
    try {
      const r = await fetch('/api/v1/setup/validate-stacks-dir', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({ data_dir: dataDir.trim(), path: stacksDir })
      });
      if (r.status === 401) {
        await sessionLost();
        return false;
      }
      if (r.status === 400) {
        // The data dir itself is wrong; its own check says why.
        stacksCheck = null;
        return false;
      }
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      stacksCheck = await r.json();
      stacksCheckFor = key;
      return stacksCheck!.status !== 'fail';
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
      return false;
    } finally {
      stacksChecking = false;
    }
  }

  function stacksIssueText(c: StacksCheck): string {
    switch (c.issue) {
      case 'new_dir_not_allowed': return m.sw_n2_stacks_refused_new({ path: c.path });
      case 'foreign_owner': return m.sw_n2_stacks_refused_owner({ path: c.path });
      case 'not_empty': return m.sw_n2_stacks_refused_not_empty({ path: c.path });
      case 'not_absolute': return m.sw_dir_err_absolute();
      case 'bad_characters': return m.sw_dir_err_characters();
      case 'symlink': return m.sw_dir_err_symlink({ path: c.path });
      case 'too_small': return m.sw_dir_err_small({ n: Math.floor((c.free_bytes ?? 0) / 1048576), path: c.path });
      case 'low_space': return m.sw_dir_warn_low({ n: ((c.free_bytes ?? 0) / 1073741824).toFixed(1) });
      default: return c.message;
    }
  }

  $effect(() => {
    if ((step === 'N2' || step === 'R1') && !legacy) {
      void loadDisks();
      if (!dirCurrent) void checkDir();
    }
  });

  // ----- N3 address and TLS ---------------------------------------------------
  let baseURL = $state('');
  let webPort = $state('8080');
  let tls = $state<TLSChoice>('internal');
  let certFile = $state('');
  let keyFile = $state('');
  let listen = $state('127.0.0.1');
  let agentPort = $state('8443');
  let agentURLOpen = $state(false);
  let agentURLInput = $state('');
  // Null follows the listen address until the field is edited.
  let trustedInput = $state<string | null>(null);
  let urlTest = $state<{ state: 'idle' | 'busy' | 'ok' | 'fail'; ms?: number; reason?: string }>({ state: 'idle' });

  $effect(() => {
    void baseURL;
    urlTest = { state: 'idle' };
  });

  $effect(() => {
    void webPort;
    void agentPort;
    portBusy = { web: null, agent: null };
  });

  function addressErrors() {
    const u = legacy ? (baseURLError(baseURL.trim(), 'internal') === 'invalid' ? 'invalid' : null) : baseURLError(baseURL.trim(), tls);
    const absolute = (f: string) => f.startsWith('/') && !/\s/.test(f);
    return {
      base: u === 'https_required' ? m.sw_addr_err_https() : u === 'proxy_url' ? m.sw_addr_err_proxy_url() : u ? m.sw_addr_err_url()
        : tls === 'caddy' && isIPHost(hostOf(baseURL.trim())) ? m.sw_addr_err_caddy_name()
        : tls === 'caddy' && dnsCheck && dnsCheck.host === hostOf(baseURL.trim()) && !dnsCheck.resolves ? m.sw_n3_err_caddy_dns({ name: dnsCheck.host }) : null,
      port: legacy ? null : portError(webPort) ? m.sw_addr_err_port() : webPort === agentPort ? m.sw_addr_err_same_ports() : busyText(portBusy.web),
      agentPort: legacy ? null : portError(agentPort) ? m.sw_addr_err_port() : webPort === agentPort ? m.sw_addr_err_same_ports() : busyText(portBusy.agent),
      agentURL: !legacy && agentURLOpen && agentURLError(agentURLInput.trim()) ? m.sw_addr_err_agent_url() : null,
      cert: tls === 'file' && !absolute(certFile.trim()) ? m.sw_addr_err_file() : certError(),
      key: tls === 'file' && !absolute(keyFile.trim()) ? m.sw_addr_err_file() : null,
      listen: tls === 'proxy' && listenAddressError(listen.trim()) ? m.sw_addr_err_listen() : null,
      trusted: tls === 'proxy' ? trustedText() : null
    };
  }

  function trustedText(): string | null {
    if (trustedInput === null) return null;
    const e = trustedProxiesError(trustedInput.trim());
    if (!e) return null;
    return e.kind === 'all' ? m.sw_addr_err_trusted_all({ net: e.net }) : m.sw_addr_err_trusted();
  }

  // Ports another program listens on, as the server saw them.
  let portBusy = $state<{ web: string | null; agent: string | null }>({ web: null, agent: null });
  let portsChecking = $state(false);

  function busyText(port: string | null): string | null {
    return port ? m.sw_addr_err_busy({ port }) : null;
  }

  async function checkPorts() {
    if (legacy || portError(webPort) || portError(agentPort)) return;
    const web = webPort, agent = agentPort;
    portsChecking = true;
    try {
      const r = await fetch('/api/v1/setup/check-ports', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({ web_port: Number(web), agent_port: Number(agent) })
      });
      if (r.status === 401) return sessionLost();
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = await r.json();
      portBusy = {
        web: d.web?.issue === 'busy' ? web : null,
        agent: d.agent?.issue === 'busy' ? agent : null
      };
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
    } finally {
      portsChecking = false;
    }
  }

  // The own certificate, checked by the server as the dockmesh user.
  type CertCheck = { status: 'ok' | 'warn' | 'fail'; issue?: string; file?: string; not_after?: string; days?: number; message: string };
  let certCheck = $state<CertCheck | null>(null);
  let certCheckFor = $state('');
  let certChecking = $state(false);
  const certKey = $derived(`${certFile.trim()}\n${keyFile.trim()}\n${hostOf(baseURL.trim())}`);
  const certCurrent = $derived(tls === 'file' && certCheck && certCheckFor === certKey ? certCheck : null);

  function hostOf(u: string): string {
    try {
      return new URL(u).hostname.replace(/^\[|\]$/g, '');
    } catch {
      return '';
    }
  }

  async function checkCert(): Promise<boolean> {
    const c = certFile.trim(), k = keyFile.trim();
    if (tls !== 'file' || !c.startsWith('/') || !k.startsWith('/')) return true;
    // A refusal is asked again: the admin may have fixed the files meanwhile.
    if (certCheckFor === certKey && certCheck && certCheck.status !== 'fail') return true;
    certChecking = true;
    try {
      const key = certKey;
      const r = await fetch('/api/v1/setup/check-cert', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({ cert_file: c, key_file: k, host: hostOf(baseURL.trim()) })
      });
      if (r.status === 401) {
        await sessionLost();
        return false;
      }
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      certCheck = await r.json();
      certCheckFor = key;
      return certCheck!.status !== 'fail';
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
      return false;
    } finally {
      certChecking = false;
    }
  }

  function certDate(c: CertCheck): string {
    return (c.not_after ?? '').slice(0, 10);
  }

  function certError(): string | null {
    const c = certCurrent;
    if (!c || c.status !== 'fail') return null;
    const file = c.file ?? '';
    switch (c.issue) {
      case 'unreadable': return m.sw_cert_err_unreadable({ file });
      case 'missing': return m.sw_cert_err_missing({ file });
      case 'invalid': return m.sw_cert_err_invalid({ file });
      case 'mismatch': return m.sw_cert_err_mismatch();
      case 'expired': return m.sw_cert_err_expired({ date: certDate(c) });
      default: return c.message;
    }
  }

  function certNote(c: CertCheck): string {
    switch (c.issue) {
      case 'hostname': return m.sw_cert_warn_host({ host: hostOf(baseURL.trim()) });
      case 'expiring': return m.sw_cert_warn_expiring({ date: certDate(c), n: c.days ?? 0 });
      default: return m.sw_cert_ok({ date: certDate(c) });
    }
  }
  const addrErr = $derived(addressErrors());
  const listenPublic = $derived(tls === 'proxy' && !addrErr.listen && !isLoopback(listen.trim()));
  // The server trusts the bind address alone, and nobody for a wildcard.
  const trustedDefault = $derived(defaultTrustedProxies(listen.trim()));
  const trustedProxies = $derived(trustedInput === null ? trustedDefault : trustedInput.trim());
  const agentURLValue = $derived(agentURLOpen ? agentURLInput.trim() : agentURL(baseURL.trim(), agentPort));

  function openAgentURL() {
    agentURLOpen = true;
    if (!agentURLInput.trim()) agentURLInput = agentURL(baseURL.trim(), agentPort);
  }

  async function testURL() {
    urlTest = { state: 'busy' };
    try {
      const r = await fetch('/api/v1/setup/test-url', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({ url: baseURL.trim(), expect_health: true })
      });
      if (r.status === 401) return sessionLost();
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const d = await r.json();
      urlTest = d.status === 'ok'
        ? { state: 'ok', ms: d.latency_ms ?? 0 }
        : { state: 'fail', reason: d.message ?? '' };
    } catch (e) {
      urlTest = { state: 'fail', reason: e instanceof Error ? e.message : String(e) };
    }
  }

  // TLS mode caddy: does the base URL's name resolve, and to this server?
  type DNSCheck = { host: string; resolves: boolean; addresses?: string[]; local: boolean };
  let dnsCheck = $state<DNSCheck | null>(null);
  const dnsNote = $derived(
    tls === 'caddy' && dnsCheck && dnsCheck.resolves && !dnsCheck.local && dnsCheck.host === hostOf(baseURL.trim())
      ? m.sw_n3_warn_caddy_nat({ name: dnsCheck.host, addr: (dnsCheck.addresses ?? []).join(', ') })
      : ''
  );

  function isIPHost(h: string): boolean {
    return /^[0-9.]+$/.test(h) || h.includes(':');
  }

  async function checkDNS() {
    const host = hostOf(baseURL.trim());
    if (tls !== 'caddy' || !host || isIPHost(host)) return;
    try {
      const r = await fetch('/api/v1/setup/check-proxy', { method: 'POST', headers: SETUP_HEADER, body: JSON.stringify({ host }) });
      if (r.status === 401) return sessionLost();
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      dnsCheck = (await r.json()).dns ?? null;
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
    }
  }

  async function addressNext() {
    triedNext = { ...triedNext, N3: true };
    if (Object.values(addrErr).some(Boolean)) return;
    await checkDNS();
    await checkPorts();
    if (!(await checkCert())) return;
    if (Object.values(addressErrors()).some(Boolean)) return;
    next();
  }

  // ----- N4 reverse proxy -----------------------------------------------------
  type ProxyCheck = { busy_ports: number[]; image: string; pulled: boolean; pull_error?: string };
  let proxyOn = $state(false);
  let acmeEmail = $state('');
  let proxyCheck = $state<ProxyCheck | null>(null);
  let proxyChecking = $state(false);
  const proxyWanted = $derived(proxyOn || tls === 'caddy');
  const proxyCheckOK = $derived(!!proxyCheck && proxyCheck.busy_ports.length === 0 && proxyCheck.pulled);
  const proxyErr = $derived.by(() => {
    const e = acmeEmail.trim();
    if (!e) return tls === 'caddy' ? m.sw_n4_err_email_required() : null;
    return emailError(e) ? m.sw_admin_err_email() : null;
  });

  $effect(() => {
    if (step === 'N4' && proxyWanted && !proxyCheck && !proxyChecking) void checkProxy();
  });

  async function checkProxy() {
    proxyChecking = true;
    try {
      const r = await fetch('/api/v1/setup/check-proxy', { method: 'POST', headers: SETUP_HEADER, body: JSON.stringify({ pull: true }) });
      if (r.status === 401) return sessionLost();
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      proxyCheck = await r.json();
    } catch (e) {
      banner = m.sw_error_banner({ message: e instanceof Error ? e.message : String(e) });
    } finally {
      proxyChecking = false;
    }
  }

  async function proxyNext() {
    triedNext = { ...triedNext, N4: true };
    if (!proxyWanted) return next();
    if (proxyErr) return;
    // A refusal is asked again: the port may have been freed meanwhile.
    if (!proxyCheckOK) await checkProxy();
    if (proxyCheckOK) next();
  }

  function skipProxy() {
    proxyOn = false;
    next();
  }

  // ----- N6 review ------------------------------------------------------------
  const tlsName = $derived(
    { internal: m.sw_tls_internal(), file: m.sw_tls_file(), caddy: m.sw_tls_caddy(), proxy: m.sw_tls_proxy() }[tls]
  );

  // ----- N7 apply -------------------------------------------------------------
  // The six rows of the progress list; root reports only the step that
  // failed, so rows before it are done.
  const PROGRESS_FAIL_ROW: Record<string, number> = {
    values: 0, decide: 0, lock: 0, data_dir: 0, stacks_dir: 0,
    materialize: 1, restore: 1,
    check_config: 2,
    config: 3, unit: 3,
    restart: 4, start: 4,
    health: 5
  };
  let applying = $state(false);
  let applyRow = $state(0);
  let applyFailRow = $state<number | null>(null);
  let applyDetail = $state<string | null>(null);
  let applyDone = $state(false);
  let redirectIn = $state(3);
  // Setup finished, but this browser could not reach the new address:
  // show it instead of redirecting there.
  let newOriginUnverified = $state(false);
  // S3: the server came back in setup mode, or did not come back at all.
  let rolledBack = $state(false);
  let notAnswering = $state(false);
  // Where to continue once the code is entered again after S3.
  let resumeTo = $state<StepId | null>(null);

  /** S3 buttons: the restart ended the setup session, so the code comes first. */
  function resumeAfterRollback(to: StepId) {
    rolledBack = false;
    applyFailRow = null;
    resumeTo = to;
    wizardStarted = false;
    gate = 'code';
  }

  async function checkAgain() {
    notAnswering = false;
    applyFailRow = null;
    applying = true;
    if ((await afterRestart()) && path !== 'restore') startRedirect();
  }
  let eventSource: EventSource | null = null;
  onDestroy(() => eventSource?.close());

  const targetURL = $derived(baseURL.trim().replace(/\/+$/, ''));

  async function install() {
    applying = true;
    applyRow = 0;
    applyFailRow = null;
    applyDetail = null;
    applyDone = false;
    await go('N7');
    try {
      const r = await fetch('/api/v1/setup/commit', {
        method: 'POST',
        headers: SETUP_HEADER,
        body: JSON.stringify({
          data_dir: dataDir.trim(),
          stacks_dir: legacy || !stacksCustom ? undefined : stacksDir,
          proxy: legacy || !proxyWanted ? undefined : { enabled: true, acme_email: acmeEmail.trim() || undefined },
          agent: legacy ? undefined : {
            listen: `:${agentPort}`,
            url: agentURLOpen ? agentURLInput.trim() : undefined
          },
          service_user: { mode: 'existing', username: 'dockmesh', add_to_docker_group: false },
          admin: { username, email: email.trim(), password },
          public_url: targetURL,
          tls: legacy ? undefined : {
            mode: tls,
            http_addr: httpAddr(tls, listen.trim(), webPort),
            cert_file: tls === 'file' ? certFile.trim() : undefined,
            key_file: tls === 'file' ? keyFile.trim() : undefined,
            trusted_proxies: tls === 'proxy' && trustedInput !== null ? trustedInput.trim() : undefined
          }
        })
      });
      if (r.status === 401) {
        applying = false;
        return sessionLost();
      }
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        return failApply(0, b.error ?? `HTTP ${r.status}`);
      }
      const { stream_url } = await r.json();
      follow(stream_url);
    } catch (e) {
      failApply(0, e instanceof Error ? e.message : String(e));
    }
  }

  function failApply(row: number, detail: string) {
    eventSource?.close();
    eventSource = null;
    applyRow = row;
    applyFailRow = row;
    applyDetail = detail;
    applying = false;
  }

  function follow(url: string) {
    eventSource?.close();
    let restarting = false;
    eventSource = new EventSource(url);
    eventSource.addEventListener('line', (ev) => {
      let d: { step: string; message: string; status: string };
      try {
        d = JSON.parse((ev as MessageEvent).data);
      } catch {
        return;
      }
      if (d.status === 'fail') return failApply(PROGRESS_FAIL_ROW[d.step] ?? 1, d.message);
      if (d.step === 'restart' && d.status === 'ok') restarting = true;
      else if (applyRow < 1) applyRow = 1;
    });
    eventSource.addEventListener('end', async () => {
      eventSource?.close();
      eventSource = null;
      if (applyFailRow !== null) return;
      if (!restarting && legacy) {
        applyRow = 6;
        applyDone = true;
        applying = false;
        return startRedirect();
      }
      if (!restarting) return failApply(4, m.sw_apply_no_restart());
      if ((await afterRestart()) && path !== 'restore') startRedirect();
    });
    eventSource.onerror = () => {
      if (eventSource?.readyState === EventSource.CLOSED && applyFailRow === null && !restarting) {
        failApply(applyRow, m.sw_apply_stream_lost());
      }
    };
  }

  /** Follows the restart to its end; true when the server is up. */
  async function afterRestart(): Promise<boolean> {
    stopServerInfo();
    applyRow = 4;
    const outcome = await waitForServer();
    if (outcome === 'timeout') {
      failApply(5, m.sw_apply_not_back());
      notAnswering = true;
      return false;
    }
    if (outcome === 'setup') {
      failApply(4, m.sw_apply_rolled_back());
      rolledBack = true;
      return false;
    }
    applyRow = 6;
    applyDone = true;
    applying = false;
    newOriginUnverified = outcome === 'unverified';
    return !newOriginUnverified;
  }

  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

  /** Setup mode is back on this origin: the new configuration was rolled back. */
  async function setupModeBack(): Promise<boolean> {
    return (await setupStatus()) === 'active';
  }

  /** Setup status on this origin, 'unknown' when it does not answer. */
  async function setupStatus(): Promise<'active' | 'done' | 'unknown'> {
    try {
      const r = await fetch('/api/v1/setup/status', { cache: 'no-store' });
      if (!r.ok) return 'unknown';
      return (await r.json()).active === true ? 'active' : 'done';
    } catch {
      return 'unknown';
    }
  }

  /**
   * Waits until the restarted server answers. A restart can be quicker
   * than one poll, so after FAST_RESTART_MS without seeing it go down the
   * restart counts as done. A new origin cannot be read across origins;
   * an opaque answer from it counts as "up".
   */
  const FAST_RESTART_MS = 15_000;
  async function waitForServer(): Promise<'up' | 'unverified' | 'setup' | 'timeout'> {
    const sameOrigin = targetURL === window.location.origin;
    const started = Date.now();
    let wentDown = false;
    while (Date.now() - started < 180_000) {
      await sleep(1000);
      if (Date.now() - started > 3000) applyRow = 5;
      const settled = wentDown || Date.now() - started > FAST_RESTART_MS;
      if (sameOrigin) {
        try {
          const r = await fetch('/api/v1/health', { cache: 'no-store' });
          if (!r.ok) {
            wentDown = true;
            continue;
          }
        } catch {
          wentDown = true;
          continue;
        }
        if (settled) return (await setupModeBack()) ? 'setup' : 'up';
        continue;
      }
      try {
        await fetch(`${targetURL}/api/v1/health`, { mode: 'no-cors', cache: 'no-store' });
        if (settled) return 'up';
      } catch {
        wentDown = true;
        // The new origin may serve a certificate this browser has not
        // accepted yet; the old origin tells whether setup finished.
        if (settled) {
          const st = await setupStatus();
          if (st === 'active') return 'setup';
          if (st === 'done') return 'unverified';
        }
      }
    }
    return 'timeout';
  }

  function startRedirect() {
    redirectIn = 3;
    const t = setInterval(() => {
      redirectIn -= 1;
      if (redirectIn <= 0) {
        clearInterval(t);
        signIn();
      }
    }, 1000);
  }
  function signIn() {
    window.location.href = `${targetURL}/login`;
  }

  const applyItems = $derived([
    legacy ? m.sw_apply_prepare_legacy() : m.sw_apply_prepare({ path: dataDir.trim() }),
    path === 'restore' ? m.sw_apply_restore() : m.sw_apply_database(),
    m.sw_apply_check(),
    m.sw_apply_write(),
    m.sw_apply_restart(),
    m.sw_apply_wait()
  ].slice(0, legacy ? 2 : 6));

  // ----- Restore (R2/R3) ------------------------------------------------------
  let restoreFile = $state<File | null>(null);
  let restoreKey = $state<File | null>(null);
  let restoreError = $state<string | null>(null);
  let restoreSummary = $state<string | null>(null);

  function restoreErrorText(res: { error?: string; sanity?: { summary?: string; checks?: { name: string; status: string }[] } }, status: number): string {
    const err = res.error ?? '';
    if (err.includes('backup is encrypted')) return m.sw_r2_err_needs_key();
    if (err.startsWith('decrypt backup') || err.startsWith('key file')) return m.sw_r2_err_key();
    if (res.sanity?.checks?.some((c) => c.name === 'users.admin' && c.status === 'fail')) return m.sw_r2_err_no_admin();
    return res.sanity?.summary ?? (err || `HTTP ${status}`);
  }

  async function restore() {
    if (!restoreFile) return;
    restoreError = null;
    restoreSummary = null;
    applying = true;
    applyRow = 0;
    applyFailRow = null;
    applyDone = false;
    const form = new FormData();
    form.append('file', restoreFile);
    if (restoreKey) form.append('key', restoreKey);
    form.append('data_dir', dataDir.trim());
    if (stacksCustom) form.append('stacks_dir', stacksDir);
    form.append('base_url', targetURL);
    await go('R3');
    try {
      const r = await fetch('/api/v1/setup/restore', { method: 'POST', headers: { 'X-Dockmesh-Setup': '1' }, body: form });
      if (r.status === 401) {
        applying = false;
        return sessionLost();
      }
      const res = await r.json().catch(() => ({}));
      if (!r.ok) {
        applying = false;
        restoreError = restoreErrorText(res, r.status);
        return go('R2');
      }
      const admins = (res.sanity?.checks ?? []).find((c: { name: string }) => c.name === 'users.admin')?.message;
      restoreSummary = m.sw_r3_checks({ files: res.counts?.files ?? 0, admins: admins ?? '' });
      if (res.stream_url) return follow(res.stream_url);
    } catch (e) {
      applying = false;
      restoreError = e instanceof Error ? e.message : String(e);
      return go('R2');
    }
    applyRow = 1;
    await afterRestart();
  }
</script>

<svelte:head>
  <title>{m.sw_title()}</title>
</svelte:head>

{#snippet cmd(text: string, key: string)}
  <div class="cmd">
    <span>{text}</span>
    <button type="button" class="btn sm" onclick={() => copy(key, text)} aria-label={m.sw_copy()}>
      {#if copied === key}<Check size={13} />{:else}<Copy size={13} />{/if}
      {m.sw_copy()}
    </button>
  </div>
{/snippet}

{#snippet head(title: string, text?: string)}
  <div class="head">
    <div class="hrow">
      <div class="counter">
        {#if early}{m.sw_counter_early({ n: idx + 1, name: stepName(step) })}{:else}{m.sw_counter({ n: idx + 1, total: steps.length, name: stepName(step) })}{/if}
      </div>
      <a class="docs" href={DOCS_BASE + DOCS[step]} target="_blank" rel="noopener noreferrer">{m.sw_docs()}</a>
    </div>
    {#if !early}
      <div class="mbar" aria-hidden="true"><i style:width="{Math.round(((idx + 1) / steps.length) * 100)}%"></i></div>
    {/if}
    <h1 id="step-title" tabindex="-1">{title}</h1>
    {#if text}<p>{text}</p>{/if}
  </div>
{/snippet}

{#snippet fingerprint(fp: string)}
  <div class="fp">{#each fingerprintLines(fp) as l (l)}<div>{l}</div>{/each}</div>
{/snippet}

{#snippet bannerBox()}
  {#if banner}
    <div class="callout bad" role="alert"><span class="ic">!</span><span>{banner}</span></div>
  {/if}
{/snippet}

{#snippet stateScreen(kind: 'warn' | 'bad', icon: string, title: string, text: string, button: string)}
  <div class="col">
    <div class="head">
      <div class="state-ic {kind}" aria-hidden="true">{icon}</div>
      <h1>{title}</h1>
    </div>
    <section class="panel">
      <p class="plain">{text}</p>
      {@render cmd(NEW_CODE_CMD, 'newcode')}
      <div class="foot"><span class="sp"></span><button type="button" class="btn primary" onclick={backToCode}>{button}</button></div>
    </section>
  </div>
{/snippet}

<div class="page">
  <header class="brand">
    <div class="logo"><Logo width={44} height={18} /><span>Dockmesh</span> <small>{m.sw_brand_setup()}</small></div>
    {#if serverInfo}
      <div class="meta dm-mono">
        <span>{window.location.hostname}</span><span class="sep">·</span>
        <span>dockmesh {fmtVersion(serverInfo.version)}</span><span class="sep">·</span>
        <span>Linux {serverInfo.arch ?? ''}</span>
      </div>
    {/if}
  </header>

  {#if gate === 'checking'}
    <div class="wiz nor"><p class="plain muted">{m.setup_checking_server()}</p></div>
  {:else if gate === 'expired'}
    <div class="wiz nor">{@render stateScreen('warn', '⏱', m.sw_s1_title(), m.sw_s1_text(), m.sw_s1_button())}</div>
  {:else if gate === 'locked'}
    <div class="wiz nor">{@render stateScreen('bad', '!', m.sw_s2_title(), m.sw_s2_text(), m.sw_s1_button())}</div>
  {:else if gate === 'no_code'}
    <div class="wiz nor">{@render stateScreen('warn', '!', m.sw_nocode_title(), m.sw_nocode_text(), m.sw_s1_button())}</div>
  {:else if gate === 'replaced'}
    <div class="wiz nor">{@render stateScreen('warn', '↗', m.sw_s4_title(), m.sw_s4_text(), m.sw_s4_button())}</div>
  {:else if rolledBack}
    <div class="wiz nor">
      <div class="col">
        <div class="head"><div class="state-ic bad" aria-hidden="true">!</div><h1>{m.sw_s3_title()}</h1></div>
        <section class="panel">
          <p class="plain">{m.sw_s3_text({ data: dataDir.trim() })}</p>
          <p class="hint plain">{m.sw_s3_hint()}</p>
          <div class="foot">
            <button type="button" class="btn" onclick={() => resumeAfterRollback(path === 'restore' ? 'R1' : 'N2')}>{m.sw_s3_back_data()}</button>
            <span class="sp"></span>
            <button type="button" class="btn primary" onclick={() => resumeAfterRollback(path === 'restore' ? 'R3' : 'N7')}>{m.sw_s3_retry()}</button>
          </div>
        </section>
      </div>
    </div>
  {:else if notAnswering}
    <div class="wiz nor">
      <div class="col">
        <div class="head"><div class="state-ic warn" aria-hidden="true">…</div><h1>{m.sw_s3b_title()}</h1></div>
        <section class="panel">
          <p class="plain">{m.sw_s3b_text()}</p>
          {@render cmd('sudo systemctl status dockmesh', 'status')}
          {@render cmd('sudo journalctl -u dockmesh -n 50', 'journal')}
          <div class="foot"><span class="sp"></span><button type="button" class="btn primary" onclick={checkAgain}>{m.sw_s3b_check()}</button></div>
        </section>
      </div>
    </div>
  {:else}
    <div class="wiz">
      <aside class="rail" aria-label={m.sw_rail_label()}>
        <div class="path">{early || !path ? m.sw_rail_setup() : path === 'restore' ? m.sw_rail_restore() : m.sw_rail_new()}</div>
        <ol>
          {#each railSteps as s, k (s)}
            {@const done = k < idx}
            {@const sub = s === 'N4' && tls === 'caddy' ? m.sw_step_proxy_required() : s === 'N4' || s === 'N5' ? m.sw_optional() : ''}
            <li class:done class:on={k === idx} aria-current={k === idx ? 'step' : undefined}>
              {#if railClickable(s, step, path) && gate === 'wizard'}
                <button type="button" class="rb" onclick={() => go(s)} title={m.sw_rail_back_to({ name: stepName(s) })}>
                  <span class="n">{#if done}<Check size={11} strokeWidth={3} />{:else}{k + 1}{/if}</span>
                  <span class="t">{stepName(s)}{#if sub}<span class="o">{sub}</span>{/if}</span>
                </button>
              {:else}
                <span class="n">{#if done}<Check size={11} strokeWidth={3} />{:else}{k + 1}{/if}</span>
                <span class="t">{stepName(s)}{#if sub}<span class="o">{sub}</span>{/if}</span>
              {/if}
            </li>
          {/each}
        </ol>
        {#if early}<div class="later">{m.sw_rail_later()}</div>{/if}
      </aside>

      <div class="content">
        {#if gate === 'code'}
          <div class="col">
            {@render head(m.sw_w1_title(), m.sw_w1_text())}
            <form class="panel" novalidate onsubmit={(e) => { e.preventDefault(); submitCode(); }}>
              <div class="field">
                <label class="lbl" for="setup-code">{m.sw_w1_field()}</label>
                <input id="setup-code" class="inp big" placeholder="XXXX-XXXX-XXXX" bind:value={codeInput}
                  autocomplete="off" spellcheck="false" autocapitalize="characters"
                  aria-invalid={codeError !== null} aria-describedby="setup-code-msg"
                  oninput={() => (codeError = null)}
                  onblur={() => { if (codeInput.trim() && !codeError) codeError = codeFieldError(); }} />
                <span id="setup-code-msg">
                  {#if codeError}
                    <span class="err">{codeError}</span>
                  {:else if codeFromLink}
                    <span class="hint">{m.sw_w1_from_link()}</span>
                  {:else if resumeTo}
                    <span class="hint">{m.sw_w1_again()}</span>
                  {/if}
                </span>
              </div>
              <div>
                <button type="button" class="link" onclick={() => (codeHelpOpen = !codeHelpOpen)} aria-expanded={codeHelpOpen}>{m.sw_w1_lost()}</button>
                {#if codeHelpOpen}
                  <p class="hint help">{m.sw_w1_lost_before()} <code>{NEW_CODE_CMD}</code> {m.sw_w1_lost_after()}</p>
                {/if}
              </div>
              <div class="foot">
                <span class="sp"></span>
                <button type="submit" class="btn primary" disabled={codeBusy}>{#if codeBusy}<span class="spin" aria-hidden="true"></span>{/if}{m.sw_continue()}</button>
              </div>
            </form>
          </div>
        {:else if early}
          <div class="col">
            {@render head(m.sw_w2_title())}
            <section class="panel">
              {@render bannerBox()}
              <div class="big2" role="radiogroup" aria-label={m.sw_w2_title()}>
                <label class="rc" class:on={choice === 'new'}>
                  <input type="radio" name="path" value="new" bind:group={choice} />
                  <div class="b"><div class="t">{m.sw_w2_new()}</div><div class="hint d">{m.sw_w2_new_text()}</div></div>
                </label>
                <label class="rc" class:on={choice === 'restore'}>
                  <input type="radio" name="path" value="restore" bind:group={choice} />
                  <div class="b"><div class="t">{m.sw_w2_restore()}</div><div class="hint d">{m.sw_w2_restore_text()}</div></div>
                </label>
              </div>
              {#if serverInfo?.tls_fingerprint}
                <div class="fpbox">
                  <span class="lbl">{m.sw_fingerprint_label()}</span>
                  {@render fingerprint(serverInfo.tls_fingerprint)}
                  <span class="hint">{m.sw_fingerprint_matches()}</span>
                </div>
              {/if}
              <div class="foot">
                <span class="sp"></span>
                <button type="button" class="btn primary" onclick={chooseAndContinue}>{m.sw_continue()}</button>
              </div>
            </section>
          </div>
        {:else if step === 'N1'}
          <div class="col">
            {@render head(m.sw_n1_title())}
            <form class="panel" novalidate onsubmit={(e) => { e.preventDefault(); adminNext(); }}>
              {@render bannerBox()}
              <div class="field">
                <label class="lbl" for="admin-user">{m.sw_n1_username()}</label>
                <input id="admin-user" class="inp dm-mono" bind:value={username} autocomplete="username" spellcheck="false"
                  aria-invalid={show('user', 'N1') && !!adminErr.username} onblur={() => (touched = { ...touched, user: true })} />
                {#if show('user', 'N1') && adminErr.username}<span class="err">{adminErr.username}</span>{/if}
              </div>
              <div class="field">
                <label class="lbl" for="admin-email">{m.sw_n1_email()}</label>
                <input id="admin-email" class="inp" type="email" placeholder="you@example.com" bind:value={email} autocomplete="email"
                  aria-invalid={show('email', 'N1') && !!adminErr.email} onblur={() => (touched = { ...touched, email: true })} />
                {#if show('email', 'N1') && adminErr.email}<span class="err">{adminErr.email}</span>
                {:else}<span class="hint">{m.sw_n1_email_hint()}</span>{/if}
              </div>
              <div class="field">
                <div class="lbl-row">
                  <label class="lbl" for="admin-pw">{m.sw_n1_password()}</label>
                  <button type="button" class="link" onclick={generate}>{m.sw_n1_generate()}</button>
                </div>
                <div class="row">
                  <input id="admin-pw" class="inp dm-mono" type={showPassword ? 'text' : 'password'} bind:value={password}
                    autocomplete="new-password" aria-invalid={show('pw', 'N1') && !!adminErr.password}
                    oninput={() => { if (generated && password !== generated) generated = null; }}
                    onblur={() => (touched = { ...touched, pw: true })} />
                  <button type="button" class="btn sm" onclick={() => (showPassword = !showPassword)} aria-pressed={showPassword}>
                    {showPassword ? m.sw_hide() : m.sw_show()}
                  </button>
                </div>
                {#if show('pw', 'N1') && adminErr.password}<span class="err">{adminErr.password}</span>
                {:else}<span class="hint">{m.sw_n1_password_hint({ n: MIN_PASSWORD_LENGTH, strength: strengthLabel })}</span>{/if}
              </div>
              {#if generated}
                <div class="pwshow">
                  <span class="dm-mono">{generated}</span>
                  <button type="button" class="btn sm" onclick={() => copy('pw', generated ?? '')}>
                    {#if copied === 'pw'}<Check size={13} />{:else}<Copy size={13} />{/if}{m.sw_copy()}
                  </button>
                </div>
                <div class="callout warn" role="note"><span class="ic">!</span><span>{m.sw_n1_generated_note()}</span></div>
              {/if}
              <div class="field">
                <label class="lbl" for="admin-pw2">{m.sw_n1_repeat()}</label>
                <input id="admin-pw2" class="inp dm-mono" type={showPassword ? 'text' : 'password'} bind:value={repeat}
                  autocomplete="new-password" aria-invalid={show('pw2', 'N1') && !!adminErr.repeat}
                  onblur={() => (touched = { ...touched, pw2: true })} />
                {#if show('pw2', 'N1') && adminErr.repeat}<span class="err">{adminErr.repeat}</span>{/if}
              </div>
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                <span class="sp"></span>
                <button type="submit" class="btn primary">{m.sw_next()}</button>
              </div>
            </form>
          </div>
        {:else if step === 'N2' || step === 'R1'}
          <div class="col">
            {@render head(m.sw_n2_title(), step === 'R1' ? `${m.sw_n2_text()} ${m.sw_r1_text()}` : m.sw_n2_text())}
            <form class="panel" novalidate onsubmit={(e) => { e.preventDefault(); dataNext(); }}>
              {@render bannerBox()}
              {#if legacy}
                <div class="callout" role="note"><span class="ic info">i</span><span>{m.sw_legacy_data()}</span></div>
              {:else}
              <div class="field">
                <label class="lbl" for="data-dir">{m.sw_n2_field()}</label>
                <div class="inp-wrap">
                  <input id="data-dir" class="inp dm-mono" bind:value={dataDir} spellcheck="false"
                    aria-invalid={dirCurrent?.status === 'fail'} class:warnb={dirCurrent?.status === 'warn'}
                    onblur={() => void checkDir()} />
                  {#if dirChecking}<span class="spin in" aria-label={m.sw_checking()}></span>{/if}
                </div>
                {#if dirCurrent?.status === 'fail'}
                  <span class="err">{dirIssueText(dirCurrent)}</span>
                {:else if dirCurrent}
                  <div class="result">
                    <span class="ok">✓</span>
                    {m.sw_n2_result({ free: fmtMem(dirCurrent.free_bytes), fs: dirCurrent.fs_type || '—', state: dirCurrent.will_create ? m.sw_n2_new_dir() : m.sw_n2_empty() })}
                  </div>
                  {#if dirCurrent.status === 'warn'}<span class="wrn">{dirIssueText(dirCurrent)}</span>{/if}
                {/if}
              </div>
              <div class="field">
                <span class="lbl">{m.sw_n2_disks()}</span>
                <DiskList {disks} selected={dataDir.trim()} onuse={useDisk} />
                <span class="hint">{m.sw_n2_disks_hint()}</span>
              </div>
              {#if !stacksOpen}
                <p class="hint plain">{m.sw_n2_stacks()} <span class="dm-mono">{stacksDir}</span>
                  <button type="button" class="link" onclick={openStacks}>{m.sw_n2_stacks_change()}</button></p>
              {:else}
                <div class="field">
                  <label class="lbl" for="stacks-dir">{m.sw_n2_stacks_field()}</label>
                  <div class="inp-wrap">
                    <input id="stacks-dir" class="inp dm-mono" bind:value={stacksInput} spellcheck="false"
                      aria-invalid={stacksCurrent?.status === 'fail'} class:warnb={stacksCurrent?.status === 'warn'}
                      onblur={() => void checkStacks()} />
                    {#if stacksChecking}<span class="spin in" aria-label={m.sw_checking()}></span>{/if}
                  </div>
                  {#if stacksCurrent?.status === 'fail'}
                    <span class="err">{stacksIssueText(stacksCurrent)}</span>
                    {#if stacksCurrent.command}{@render cmd(stacksCurrent.command, 'stacks-cmd')}{/if}
                  {:else if stacksCurrent && stacksCustom}
                    <div class="result">
                      <span class="ok">✓</span>
                      {m.sw_n2_result({ free: fmtMem(stacksCurrent.free_bytes ?? 0), fs: stacksCurrent.fs_type || '—', state: stacksCurrent.will_create ? m.sw_n2_new_dir() : m.sw_n2_empty() })}
                    </div>
                    {#if stacksCurrent.status === 'warn'}<span class="wrn">{stacksIssueText(stacksCurrent)}</span>{/if}
                  {/if}
                  {#if stacksCustom && stacksOutside(dataDir.trim(), stacksDir)}<span class="hint">{m.sw_n2_stacks_outside()}</span>{/if}
                  <button type="button" class="link" onclick={closeStacks}>{m.sw_n2_stacks_default()}</button>
                </div>
              {/if}
              {/if}
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                <span class="sp"></span>
                <button type="submit" class="btn primary" disabled={dirChecking || stacksChecking}>{m.sw_next()}</button>
              </div>
            </form>
          </div>
        {:else if step === 'N3'}
          {@const se = triedNext.N3}
          <div class="col">
            {@render head(m.sw_n3_title(), m.sw_n3_text())}
            <form class="panel" novalidate onsubmit={(e) => { e.preventDefault(); addressNext(); }}>
              {@render bannerBox()}
              <div class="field">
                <label class="lbl" for="base-url">{m.sw_n3_base()}</label>
                <div class="row">
                  <input id="base-url" class="inp dm-mono" bind:value={baseURL} spellcheck="false"
                    aria-invalid={(touched.base || se) && !!addrErr.base} onblur={() => (touched = { ...touched, base: true })} />
                  <button type="button" class="btn" onclick={testURL} disabled={urlTest.state === 'busy' || !!addrErr.base}>
                    {#if urlTest.state === 'busy'}<span class="spin" aria-hidden="true"></span>{/if}{m.sw_n3_test()}
                  </button>
                </div>
                {#if (touched.base || se) && addrErr.base}<span class="err">{addrErr.base}</span>
                {:else if urlTest.state === 'ok'}<span class="result"><span class="ok">✓</span> {m.sw_n3_test_ok({ ms: urlTest.ms ?? 0 })}</span>
                {:else if urlTest.state === 'fail'}<span class="err">{m.sw_n3_test_fail({ url: baseURL.trim(), reason: urlTest.reason ?? '' })}</span>{/if}
              </div>
              {#if legacy}
                <div class="callout" role="note"><span class="ic info">i</span><span>{m.sw_legacy_address()}</span></div>
              {:else}
              <div class="grid2">
                <div class="field">
                  <label class="lbl" for="web-port">{m.sw_n3_web_port()}</label>
                  <div class="inp-wrap">
                    <input id="web-port" class="inp dm-mono" bind:value={webPort} inputmode="numeric"
                      aria-invalid={(touched.port || se || !!portBusy.web) && !!addrErr.port} onblur={() => { touched = { ...touched, port: true }; void checkPorts(); }} />
                    {#if portsChecking}<span class="spin in" aria-label={m.sw_checking()}></span>{/if}
                  </div>
                  {#if (touched.port || se || portBusy.web) && addrErr.port}<span class="err">{addrErr.port}</span>{/if}
                </div>
                <div class="field">
                  <label class="lbl" for="agent-port">{m.sw_n3_agent_port()}</label>
                  <input id="agent-port" class="inp dm-mono" bind:value={agentPort} inputmode="numeric"
                    aria-invalid={(touched.agentPort || se || !!portBusy.agent) && !!addrErr.agentPort} onblur={() => { touched = { ...touched, agentPort: true }; void checkPorts(); }} />
                  {#if (touched.agentPort || se || portBusy.agent) && addrErr.agentPort}<span class="err">{addrErr.agentPort}</span>{/if}
                </div>
              </div>
              <div class="field">
                <span class="lbl" id="tls-label">{m.sw_n3_cert()}</span>
                <div class="choices" role="radiogroup" aria-labelledby="tls-label">
                  <label class="rc" class:on={tls === 'internal'}>
                    <input type="radio" name="tls" value="internal" bind:group={tls} />
                    <div class="b">
                      <div class="t">{m.sw_tls_internal()} <span class="chip acc">{m.sw_default()}</span></div>
                      <div class="hint d">{m.sw_tls_internal_text()}</div>
                      {#if tls === 'internal' && serverInfo?.tls_fingerprint}
                        <div class="sub">
                          <div>
                            <span class="lbl">{m.sw_fingerprint_label()}</span>
                            {@render fingerprint(serverInfo.tls_fingerprint)}
                            <span class="hint">{m.sw_fingerprint_matches()}</span>
                          </div>
                        </div>
                      {/if}
                    </div>
                  </label>
                  <label class="rc" class:on={tls === 'file'}>
                    <input type="radio" name="tls" value="file" bind:group={tls} />
                    <div class="b">
                      <div class="t">{m.sw_tls_file()}</div>
                      <div class="hint d">{m.sw_tls_file_text()}</div>
                      {#if tls === 'file'}
                        <div class="sub">
                          <div class="field">
                            <label class="lbl" for="cert-file">{m.sw_n3_cert_file()}</label>
                            <input id="cert-file" class="inp dm-mono" bind:value={certFile} spellcheck="false" placeholder="/etc/letsencrypt/live/example.org/fullchain.pem"
                              aria-invalid={(touched.cert || se || !!certCurrent) && !!addrErr.cert} onblur={() => { touched = { ...touched, cert: true }; void checkCert(); }} />
                            {#if (touched.cert || se || certCurrent) && addrErr.cert}<span class="err">{addrErr.cert}</span>{/if}
                          </div>
                          <div class="field">
                            <label class="lbl" for="key-file">{m.sw_n3_key_file()}</label>
                            <div class="inp-wrap">
                              <input id="key-file" class="inp dm-mono" bind:value={keyFile} spellcheck="false" placeholder="/etc/letsencrypt/live/example.org/privkey.pem"
                                aria-invalid={(touched.key || se) && !!addrErr.key} onblur={() => { touched = { ...touched, key: true }; void checkCert(); }} />
                              {#if certChecking}<span class="spin in" aria-label={m.sw_checking()}></span>{/if}
                            </div>
                            {#if (touched.key || se) && addrErr.key}<span class="err">{addrErr.key}</span>{/if}
                          </div>
                          {#if certCurrent && certCurrent.status !== 'fail'}
                            {#if certCurrent.status === 'warn'}<span class="wrn">{certNote(certCurrent)}</span>
                            {:else}<span class="result"><span class="ok">✓</span> {certNote(certCurrent)}</span>{/if}
                          {/if}
                          <span class="hint">{m.sw_n3_reload_hint()}</span>
                        </div>
                      {/if}
                    </div>
                  </label>
                  <label class="rc" class:on={tls === 'caddy'}>
                    <input type="radio" name="tls" value="caddy" bind:group={tls} />
                    <div class="b">
                      <div class="t">{m.sw_tls_caddy()}</div>
                      <div class="hint d">{m.sw_tls_caddy_text()} {m.sw_tls_caddy_proxy()}</div>
                      {#if tls === 'caddy' && dnsNote}
                        <div class="sub"><span class="wrn">{dnsNote}</span></div>
                      {/if}
                    </div>
                  </label>
                  <label class="rc" class:on={tls === 'proxy'}>
                    <input type="radio" name="tls" value="proxy" bind:group={tls} />
                    <div class="b">
                      <div class="t">{m.sw_tls_proxy()}</div>
                      <div class="hint d">{m.sw_tls_proxy_text()}</div>
                      {#if tls === 'proxy'}
                        <div class="sub">
                          <div class="grid2">
                            <div class="field">
                              <label class="lbl" for="listen">{m.sw_n3_listen()}</label>
                              <input id="listen" class="inp dm-mono" bind:value={listen} spellcheck="false"
                                aria-invalid={(touched.listen || se) && !!addrErr.listen} onblur={() => (touched = { ...touched, listen: true })} />
                              {#if (touched.listen || se) && addrErr.listen}<span class="err">{addrErr.listen}</span>
                              {:else if listenPublic}<span class="wrn">{m.sw_n3_listen_public({ addr: listen.trim() })}</span>{/if}
                            </div>
                            <div class="field">
                              <label class="lbl" for="trusted">{m.sw_n3_trusted()}</label>
                              <input id="trusted" class="inp dm-mono" value={trustedProxies} spellcheck="false"
                                placeholder={trustedDefault ? undefined : m.sw_n3_trusted_none()}
                                oninput={(e) => (trustedInput = e.currentTarget.value)}
                                aria-invalid={(touched.trusted || se) && !!addrErr.trusted} onblur={() => (touched = { ...touched, trusted: true })} />
                              {#if (touched.trusted || se) && addrErr.trusted}<span class="err">{addrErr.trusted}</span>
                              {:else}<span class="hint">{m.sw_n3_trusted_hint()}</span>{/if}
                            </div>
                          </div>
                          <div class="callout" role="note"><span class="ic info">i</span><span>{m.sw_n3_agent_note({ port: agentPort })}</span></div>
                        </div>
                      {/if}
                    </div>
                  </label>
                </div>
              </div>
              {/if}
              <div class="field">
                {#if agentURLOpen && !legacy}
                  <label class="lbl" for="agent-url">{m.sw_n3_agent_url()}</label>
                  <input id="agent-url" class="inp dm-mono" bind:value={agentURLInput} spellcheck="false"
                    aria-invalid={(touched.agentURL || se) && !!addrErr.agentURL} onblur={() => (touched = { ...touched, agentURL: true })} />
                  {#if (touched.agentURL || se) && addrErr.agentURL}<span class="err">{addrErr.agentURL}</span>{/if}
                  <button type="button" class="link" onclick={() => { agentURLOpen = false; agentURLInput = ''; }}>{m.sw_n3_agent_url_default()}</button>
                {:else}
                  <span class="lbl">{m.sw_n3_agent_url()}</span>
                  <span class="dm-mono ro">{agentURLValue || '—'}
                    {#if !legacy}<button type="button" class="link" onclick={openAgentURL}>{m.sw_change()}</button>{/if}</span>
                {/if}
              </div>
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                <span class="sp"></span>
                <button type="submit" class="btn primary">{m.sw_next()}</button>
              </div>
            </form>
          </div>
        {:else if step === 'N4'}
          {@const caddy = tls === 'caddy'}
          <div class="col">
            {@render head(m.sw_n4_title(), caddy ? m.sw_n4_text_caddy() : m.sw_n4_text())}
            <form class="panel" novalidate onsubmit={(e) => { e.preventDefault(); proxyNext(); }}>
              {@render bannerBox()}
              <label class="toggle">
                <input type="checkbox" bind:checked={proxyOn} disabled={caddy} />
                <span>{m.sw_n4_toggle()}</span>
              </label>
              {#if proxyOn || caddy}
                <div class="field">
                  <label class="lbl" for="acme-email">{caddy ? m.sw_n4_email() : m.sw_n4_email_optional()}</label>
                  <input id="acme-email" class="inp" type="email" bind:value={acmeEmail} placeholder="you@example.com"
                    aria-invalid={(touched.acme || triedNext.N4) && !!proxyErr} onblur={() => (touched = { ...touched, acme: true })} />
                  {#if (touched.acme || triedNext.N4) && proxyErr}<span class="err">{proxyErr}</span>{/if}
                </div>
                <div class="grid2">
                  <div class="field"><span class="lbl">{m.sw_n4_http()}</span><span class="dm-mono ro">80</span></div>
                  <div class="field"><span class="lbl">{m.sw_n4_https()}</span><span class="dm-mono ro">443</span></div>
                </div>
                {#if proxyChecking}
                  <div class="result"><span class="spin" aria-hidden="true"></span> {m.sw_n4_checking()}</div>
                {:else if proxyCheck}
                  {#each proxyCheck.busy_ports as p (p)}<span class="err">{m.sw_n4_err_port({ port: String(p) })}</span>{/each}
                  {#if proxyCheck.pull_error}<span class="err">{m.sw_n4_err_pull({ image: proxyCheck.image, reason: proxyCheck.pull_error })}</span>{/if}
                  {#if proxyCheckOK}
                    <div class="result"><span class="ok">✓</span> {m.sw_n4_ports_free()} · <span class="ok">✓</span> {m.sw_n4_pulled()}</div>
                  {/if}
                {/if}
              {/if}
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                {#if !caddy}<button type="button" class="link" onclick={skipProxy}>{m.sw_skip()}</button>{/if}
                <span class="sp"></span>
                <button type="submit" class="btn primary" disabled={proxyChecking}>{m.sw_next()}</button>
              </div>
              {#if !caddy}<p class="hint plain">{m.sw_n4_later_hint()}</p>{/if}
            </form>
          </div>
        {:else if step === 'N5'}
          <div class="col">
            {@render head(m.sw_n5_title(), m.sw_n5_text())}
            <section class="panel">
              <div class="callout" role="note"><span class="ic info">i</span><span>{m.sw_n5_later()}</span></div>
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                <span class="sp"></span>
                <button type="button" class="btn primary" onclick={next}>{m.sw_next()}</button>
              </div>
            </section>
          </div>
        {:else if step === 'N6'}
          {#snippet row(k: string, v: string, to: StepId, mono = false)}
            <div class="tr"><span class="k">{k}</span><span class:dm-mono={mono}>{v}</span><button type="button" class="link" onclick={() => go(to)}>{m.sw_change()}</button></div>
          {/snippet}
          <div class="col">
            {@render head(m.sw_n6_title(), m.sw_n6_text())}
            <section class="panel">
              {@render bannerBox()}
              <div class="tbl">
                <div class="sect">{m.sw_n6_admin()}</div>
                {@render row(m.sw_n1_username(), username, 'N1', true)}
                {@render row(m.sw_n6_email(), email.trim() || '—', 'N1')}
                {@render row(m.sw_n1_password(), '••••', 'N1')}
                <div class="sect">{m.sw_step_data()}</div>
                {#if legacy}
                  {@render row(m.sw_n6_data(), m.sw_legacy_unchanged(), 'N2')}
                {:else}
                  {@render row(m.sw_n6_data(), dataDir.trim(), 'N2', true)}
                  {@render row(m.sw_n6_stacks(), stacksDir, 'N2', true)}
                {/if}
                <div class="sect">{m.sw_step_address()}</div>
                {@render row(m.sw_n3_base(), targetURL, 'N3', true)}
                {#if legacy}
                  {@render row(m.sw_n3_cert(), m.sw_legacy_unchanged(), 'N3')}
                {:else}
                  {@render row(m.sw_n6_ports(), `${webPort} · ${agentPort}`, 'N3', true)}
                  {@render row(m.sw_n6_agent_url(), agentURLValue, 'N3', true)}
                  {@render row(m.sw_n3_cert(), tlsName, 'N3')}
                {/if}
                {#if tls === 'file'}{@render row(m.sw_n3_cert_file(), certFile.trim(), 'N3', true)}{/if}
                {#if tls === 'proxy'}{@render row(m.sw_n3_listen(), listen.trim(), 'N3', true)}
                  {@render row(m.sw_n3_trusted(), trustedProxies || m.sw_n3_trusted_none(), 'N3', true)}{/if}
                <div class="sect">{m.sw_step_proxy()}</div>
                {@render row('Caddy', proxyWanted ? (acmeEmail.trim() ? m.sw_n6_proxy_on_email({ email: acmeEmail.trim() }) : m.sw_n6_proxy_on()) : m.sw_n6_off(), 'N4')}
                <div class="sect">{m.sw_step_notifications()}</div>
                {@render row(m.sw_n6_channel(), m.sw_n6_none(), 'N5')}
              </div>
              <p class="hint plain final">{legacy ? m.sw_n6_final_legacy() : m.sw_n6_final({ data: dataDir.trim() })}</p>
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                <span class="sp"></span>
                <button type="button" class="btn primary" onclick={install}>{m.sw_install()}</button>
              </div>
            </section>
          </div>
        {:else if step === 'R2'}
          <div class="col">
            {@render head(m.sw_r2_title(), m.sw_r2_text())}
            <form class="panel" novalidate onsubmit={(e) => { e.preventDefault(); restore(); }}>
              {#if restoreError}<div class="callout bad" role="alert"><span class="ic">!</span><span>{restoreError}</span></div>{/if}
              <div class="field">
                <label class="lbl" for="backup-file">{m.sw_r2_file()}</label>
                <input id="backup-file" class="inp" type="file" accept=".gz,.age"
                  onchange={(e) => (restoreFile = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
                {#if restoreFile}<span class="hint dm-mono">{restoreFile.name} · {fmtMem(restoreFile.size)}</span>{/if}
              </div>
              {#if restoreFile?.name.endsWith('.age')}
                <div class="field">
                  <label class="lbl" for="backup-key">{m.sw_r2_key()}</label>
                  <input id="backup-key" class="inp" type="file"
                    onchange={(e) => (restoreKey = (e.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
                </div>
              {/if}
              <div class="foot">
                <button type="button" class="btn ghost" onclick={back}>{m.sw_back()}</button>
                <span class="sp"></span>
                <button type="submit" class="btn primary" disabled={!restoreFile}>{m.sw_r2_restore()}</button>
              </div>
            </form>
          </div>
        {:else if step === 'N7' || step === 'R3'}
          <div class="col">
            {@render head(applyDone ? m.sw_n7_ready() : m.sw_n7_title(), applyDone ? undefined : m.sw_n7_text())}
            <section class="panel" aria-live="polite">
              {#if path === 'restore' && restoreSummary}
                <div class="callout ok" role="status"><span class="ic">✓</span><span>{restoreSummary}</span></div>
              {/if}
              <ul class="prog">
                {#each applyItems as t, i (i)}
                  {@const failed = applyFailRow === i}
                  <li class:done={i < applyRow && !failed} class:run={i === applyRow && applying && !failed} class:fail={failed}>
                    <span class="mk" aria-hidden="true">{#if failed}!{:else if i < applyRow}✓{/if}</span>{t}
                  </li>
                {/each}
              </ul>
              {#if applyFailRow !== null}
                <div class="detail">{applyDetail}</div>
                <div class="foot">
                  <button type="button" class="btn" onclick={() => go(path === 'restore' ? 'R2' : 'N2')}>
                    {path === 'restore' ? m.sw_r2_title() : m.sw_s3_back_data()}
                  </button>
                  <span class="sp"></span>
                  <button type="button" class="btn primary" onclick={() => (path === 'restore' ? restore() : install())}>{m.sw_s3_retry()}</button>
                </div>
              {:else if applyDone}
                <div class="callout ok" role="status">
                  <span class="ic">✓</span>
                  <span>{path === 'restore' ? m.sw_r3_done() : newOriginUnverified ? m.sw_n7_open({ url: `${targetURL}/login` }) : m.sw_n7_redirect({ n: redirectIn })}</span>
                </div>
                {#if path !== 'restore' && !legacy && tls === 'internal' && serverInfo?.tls_fingerprint}
                  <div>
                    <span class="lbl">{m.sw_n7_fp_same()}</span>
                    {@render fingerprint(serverInfo.tls_fingerprint)}
                  </div>
                {/if}
                <div class="foot">
                  <span class="sp"></span>
                  <button type="button" class="btn primary" onclick={() => (path === 'restore' ? (window.location.href = '/login') : signIn())}>{m.sw_n7_sign_in()}</button>
                </div>
              {/if}
            </section>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .page {
    max-width: 960px;
    margin: 0 auto;
    padding: 28px 16px 64px;
    min-height: 100vh;
    color: var(--fg);
    font-size: var(--fs-base);
  }
  .brand {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 26px;
    flex-wrap: wrap;
  }
  .logo {
    display: flex;
    align-items: center;
    gap: 9px;
    font-weight: 700;
    font-size: var(--fs-md);
    letter-spacing: -0.01em;
  }
  .logo small {
    font-weight: 500;
    color: var(--subtle);
    font-size: var(--fs-sm);
  }
  .meta {
    color: var(--subtle);
    font-size: var(--fs-xs);
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }
  .meta .sep {
    color: var(--border-strong);
  }
  .wiz {
    display: grid;
    grid-template-columns: 210px minmax(0, 1fr);
    gap: 44px;
    align-items: start;
  }
  .wiz.nor {
    grid-template-columns: minmax(0, 680px);
    justify-content: center;
  }
  .content {
    max-width: 680px;
    min-width: 0;
  }
  .rail {
    position: sticky;
    top: 28px;
    border-right: 1px solid var(--border);
    padding-right: 20px;
  }
  .rail ol {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .rail li {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    position: relative;
    padding-bottom: 16px;
    font-size: var(--fs-sm);
    color: var(--subtle);
  }
  .rail li:not(:last-child)::after {
    content: '';
    position: absolute;
    left: 10px;
    top: 24px;
    bottom: 2px;
    width: 1px;
    background: var(--border);
  }
  .rail li.done:not(:last-child)::after {
    background: var(--accent);
  }
  .rail .n {
    width: 21px;
    height: 21px;
    border-radius: 11px;
    border: 1px solid var(--border);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    flex: none;
    background: var(--bg);
  }
  .rail .on {
    color: var(--fg);
    font-weight: 600;
  }
  .rail .on .n {
    border-color: var(--accent);
    color: var(--accent);
  }
  .rail .done {
    color: var(--muted);
  }
  .rail .done .n {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--on-accent);
  }
  .rail .t {
    padding-top: 1px;
    line-height: 1.35;
  }
  .rail .o {
    display: block;
    font-size: var(--fs-xs);
    color: var(--subtle);
    font-weight: 400;
  }
  .rail .rb {
    all: unset;
    display: flex;
    gap: 10px;
    align-items: flex-start;
    cursor: pointer;
    border-radius: var(--r-sm);
  }
  .rail .rb:hover .t {
    color: var(--fg);
    text-decoration: underline;
  }
  .rail .rb:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .rail .later {
    font-size: var(--fs-xs);
    color: var(--subtle);
    padding-left: 31px;
    margin-top: -4px;
  }
  .rail .path {
    font-size: var(--fs-xs);
    color: var(--subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
    margin-bottom: 12px;
  }
  .mbar {
    display: none;
    height: 3px;
    background: var(--border);
    border-radius: 2px;
    margin: 8px 0 14px;
    overflow: hidden;
  }
  .mbar i {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  @media (max-width: 780px) {
    .wiz,
    .wiz.nor {
      grid-template-columns: 1fr;
    }
    .rail {
      display: none;
    }
    .mbar {
      display: block;
    }
  }
  .col {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }
  .hrow {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 12px;
  }
  .counter {
    font-size: var(--fs-xs);
    color: var(--subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    font-weight: 600;
    margin-bottom: 6px;
  }
  .docs {
    font-size: var(--fs-sm);
    color: var(--accent);
    text-decoration: none;
    white-space: nowrap;
  }
  .docs:hover {
    text-decoration: underline;
  }
  .head h1 {
    margin: 0;
    font-size: var(--fs-xl);
    font-weight: 700;
    letter-spacing: -0.02em;
    line-height: 1.25;
  }
  .head h1:focus {
    outline: none;
  }
  .head p {
    margin: 6px 0 0;
    color: var(--muted);
    max-width: 62ch;
  }
  .panel {
    border-top: 1px solid var(--border);
    padding-top: 22px;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .foot {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    border-top: 1px solid var(--border);
    padding-top: 16px;
    margin-top: 4px;
  }
  .sp {
    flex: 1;
  }
  .plain {
    margin: 0;
  }
  .muted {
    color: var(--muted);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  .lbl {
    font-size: 12px;
    color: var(--muted);
    font-weight: 500;
  }
  .lbl-row {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 8px;
  }
  .hint {
    color: var(--subtle);
    font-size: var(--fs-xs);
  }
  .hint.d {
    font-size: var(--fs-sm);
  }
  .hint.help {
    margin: 6px 0 0;
    font-size: var(--fs-sm);
  }
  .hint.final {
    font-size: var(--fs-sm);
  }
  .err {
    color: var(--bad);
    font-size: var(--fs-sm);
  }
  .wrn {
    color: var(--warn);
    font-size: var(--fs-sm);
  }
  .ro {
    font-size: 13px;
    min-height: var(--ctl-h);
    display: flex;
    align-items: center;
  }
  .inp {
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--fg);
    border-radius: var(--r);
    padding: 6px 10px;
    min-height: var(--ctl-h);
    font: inherit;
    width: 100%;
    box-sizing: border-box;
  }
  .inp.dm-mono {
    font-family: var(--font-mono);
    font-size: 13px;
  }
  .inp.big {
    font-family: var(--font-mono);
    font-size: 20px;
    letter-spacing: 0.12em;
    text-align: center;
    min-height: 48px;
    text-transform: uppercase;
  }
  .inp[aria-invalid='true'] {
    border-color: var(--bad);
  }
  .inp.warnb {
    border-color: var(--warn);
  }
  .inp:focus {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .inp-wrap {
    position: relative;
  }
  .spin.in {
    position: absolute;
    right: 10px;
    top: 50%;
    margin-top: -6px;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .row > .inp {
    flex: 1;
    min-width: 0;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  @media (max-width: 560px) {
    .grid2,
    .big2 {
      grid-template-columns: 1fr;
    }
  }
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    font: inherit;
    font-weight: 500;
    border-radius: var(--r);
    padding: 0 12px;
    min-height: var(--ctl-h);
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--fg);
    cursor: pointer;
    white-space: nowrap;
  }
  .btn:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .btn.primary {
    background: var(--accent);
    color: var(--on-accent);
    border-color: var(--accent);
    font-weight: 600;
  }
  .btn.ghost {
    background: transparent;
    border-color: transparent;
    color: var(--muted);
  }
  .btn.sm {
    min-height: var(--ctl-h-sm);
    font-size: var(--fs-sm);
    padding: 0 9px;
  }
  .btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .link {
    all: unset;
    cursor: pointer;
    color: var(--accent);
    font-size: var(--fs-sm);
  }
  .link:hover {
    text-decoration: underline;
  }
  .link:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .choices {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .rc {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 9px;
    cursor: pointer;
    background: var(--surface);
  }
  .rc.on {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .rc.off {
    cursor: not-allowed;
    opacity: 0.6;
  }
  .rc input {
    margin-top: 3px;
    accent-color: var(--accent);
  }
  .rc .b {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .rc .t {
    font-weight: 600;
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .rc .sub {
    margin-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    cursor: auto;
  }
  .big2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .big2 .rc {
    padding: 18px;
  }
  .big2 .rc .t {
    font-size: var(--fs-md);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    padding: 1px 8px;
    border-radius: 999px;
    font-size: 11px;
    font-weight: 600;
    border: 1px solid var(--border);
    color: var(--muted);
  }
  .chip.acc {
    color: var(--accent);
    border-color: color-mix(in srgb, var(--accent) 40%, transparent);
    background: var(--accent-soft);
  }
  .callout {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    border-radius: var(--r);
    padding: 10px 12px;
    font-size: var(--fs-sm);
    border: 1px solid var(--border);
    background: var(--band);
  }
  .callout.bad {
    border-color: color-mix(in srgb, var(--bad) 45%, transparent);
    background: color-mix(in srgb, var(--bad) 8%, transparent);
  }
  .callout.warn {
    border-color: color-mix(in srgb, var(--warn) 45%, transparent);
    background: color-mix(in srgb, var(--warn) 8%, transparent);
  }
  .callout.ok {
    border-color: color-mix(in srgb, var(--ok) 45%, transparent);
    background: color-mix(in srgb, var(--ok) 8%, transparent);
  }
  .callout .ic {
    flex: none;
    width: 18px;
    height: 18px;
    border-radius: 9px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
    font-weight: 700;
  }
  .callout.bad .ic {
    background: var(--bad);
    color: var(--bg);
  }
  .callout.warn .ic {
    background: var(--warn);
    color: var(--bg);
  }
  .callout.ok .ic {
    background: var(--ok);
    color: var(--bg);
  }
  .callout .ic.info {
    background: var(--accent);
    color: var(--on-accent);
  }
  code {
    font-family: var(--font-mono);
    font-size: 12.5px;
    background: var(--field);
    border-radius: 5px;
    padding: 1px 6px;
  }
  .cmd {
    display: flex;
    align-items: center;
    gap: 8px;
    background: var(--field);
    border-radius: var(--r);
    padding: 8px 10px;
    font-family: var(--font-mono);
    font-size: 13px;
  }
  .cmd span {
    flex: 1;
    overflow-x: auto;
    white-space: nowrap;
  }
  .cmd .btn {
    font-family: var(--font-sans);
  }
  .fp {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
    word-break: break-all;
    color: var(--fg);
  }
  .fpbox {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 500;
  }
  .result {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    font-size: var(--fs-sm);
    color: var(--muted);
  }
  .result .ok {
    color: var(--ok);
  }
  .tbl {
    border: 1px solid var(--border);
    border-radius: var(--r);
    overflow: hidden;
  }
  .tr {
    display: grid;
    grid-template-columns: 9.5rem minmax(0, 1fr) auto;
    gap: 12px;
    padding: 9px 12px;
    border-top: 1px solid var(--border);
    align-items: baseline;
    font-size: var(--fs-sm);
    overflow-wrap: anywhere;
  }
  .tr .k {
    color: var(--muted);
  }
  .sect {
    font-size: 12px;
    color: var(--subtle);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 8px 12px;
    background: var(--band);
    border-top: 1px solid var(--border);
  }
  .sect:first-child {
    border-top: 0;
  }
  .prog {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .prog li {
    display: flex;
    gap: 10px;
    align-items: center;
    color: var(--subtle);
  }
  .prog .mk {
    width: 18px;
    height: 18px;
    border-radius: 9px;
    border: 1px solid var(--border);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 10px;
    flex: none;
  }
  .prog .done {
    color: var(--fg);
  }
  .prog .done .mk {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--on-accent);
  }
  .prog .run {
    color: var(--fg);
    font-weight: 600;
  }
  .prog .run .mk {
    border-color: var(--accent);
    border-top-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  .prog .fail {
    color: var(--bad);
  }
  .prog .fail .mk {
    background: var(--bad);
    border-color: var(--bad);
    color: var(--bg);
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .spin {
    display: inline-block;
    width: 12px;
    height: 12px;
    border: 2px solid var(--border-strong);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
    vertical-align: -2px;
  }
  .pwshow {
    display: flex;
    gap: 8px;
    align-items: center;
    background: var(--field);
    border-radius: var(--r);
    padding: 8px 10px;
  }
  .pwshow .dm-mono {
    flex: 1;
    font-size: 14px;
    letter-spacing: 0.03em;
    overflow-wrap: anywhere;
  }
  .detail {
    font-family: var(--font-mono);
    font-size: 12px;
    background: var(--field);
    border-radius: var(--r);
    padding: 8px 10px;
    color: var(--bad);
    overflow-wrap: anywhere;
  }
  .state-ic {
    width: 44px;
    height: 44px;
    border-radius: 22px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 20px;
    font-weight: 700;
    margin-bottom: 4px;
  }
  .state-ic.warn {
    background: color-mix(in srgb, var(--warn) 16%, transparent);
    color: var(--warn);
  }
  .state-ic.bad {
    background: color-mix(in srgb, var(--bad) 16%, transparent);
    color: var(--bad);
  }
</style>
