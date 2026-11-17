<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { dialogs } from '$lib/stores/dialogs.svelte';
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { trapFocus } from '$lib/a11y';
  import { api, ApiError, type Agent, type AgentCreateResult } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { fmtTime } from '$lib/i18n';
  import { copyToClipboard } from '$lib/clipboard';
  import { DOCS_BASE } from '$lib/setupWizard';
  import { tagProblem } from './_host';
  import { X, Check, Copy, Info, AlertTriangle, ChevronRight, ChevronDown } from 'lucide-svelte';

  // Add host, spec D1–D4: create (name, tags) → command → wait → connected.
  interface Props {
    onclose: () => void;
    onconnected: (agent: { id: string; name: string }) => void;
    knownTags?: string[];
  }
  let { onclose, onconnected, knownTags = [] }: Props = $props();

  const SUGGESTIONS = ['edge', 'production', 'staging', 'gpu', 'storage', 'arm', 'backup'];
  const suggestionPool = $derived([...new Set([...SUGGESTIONS, ...knownTags])]);

  let name = $state('');
  let tags = $state<string[]>([]);
  let tagInput = $state('');
  let nameError = $state('');
  let error = $state('');
  let creating = $state(false);
  let created = $state<AgentCreateResult | null>(null);
  let agent = $state<Agent | null>(null);
  let agentPort = $state('');
  let showAuto = $state(false);
  let copied = $state<'' | 'cmd' | 'token'>('');
  let rotating = $state(false);
  let now = $state(Date.now());
  let waitStarted = $state(0);
  let reach = $state<{ ok: boolean; url: string } | null>(null);

  const expired = $derived(!!created && Date.parse(created.token_expires_at) <= now);
  const connected = $derived(agent?.status === 'online');
  const displayName = $derived(agent?.name ?? created?.agent.name ?? '');
  const summaryName = $derived(name.trim() || m.hostsadd_new_host());
  const readyPath = $derived(agent && !agent.needs_setup ? agent.stacks_dirs?.find((d) => d.current)?.path ?? '' : '');
  const lateWait = $derived(!!created && !connected && waitStarted > 0 && now - waitStarted >= 120_000);

  function portOf(url: string): string {
    try {
      const u = new URL(url);
      return u.port || (u.protocol === 'wss:' || u.protocol === 'https:' ? '443' : '80');
    } catch {
      return '';
    }
  }

  function addTag(t: string) {
    const v = t.trim().toLowerCase().replace(/,$/, '');
    if (!v || tags.includes(v)) { tagInput = ''; return; }
    const problem = tagProblem(v, tags);
    if (problem) { toast.error(problem.title, problem.detail); return; }
    tags = [...tags, v];
    tagInput = '';
  }
  function removeTag(t: string) { tags = tags.filter((x) => x !== t); }

  async function create() {
    creating = true;
    error = '';
    nameError = '';
    try {
      created = await api.agents.create({ name: name.trim(), tags });
      agentPort = portOf(created.agent_url) || agentPort;
      now = Date.now();
      waitStarted = now;
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) nameError = m.hostsadd_name_taken({ name: name.trim() });
      else error = m.hostsadd_error({ message: err instanceof Error ? err.message : String(err) });
    } finally {
      creating = false;
    }
  }

  // Rotates the token; the previous command stops working.
  async function newCommand() {
    if (!created) return;
    rotating = true;
    try {
      created = await api.agents.rotateToken(created.agent.id);
      now = Date.now();
      waitStarted = now;
      reach = null;
    } catch (err) {
      toast.error(m.hosts_add_new_command_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      rotating = false;
    }
  }

  async function copy(what: 'cmd' | 'token', text: string) {
    if (await copyToClipboard(text)) {
      copied = what;
      setTimeout(() => (copied = ''), 1500);
    } else {
      toast.error(m.hosts_add_copy_failed(), m.hosts_add_copy_failed_hint());
    }
  }

  async function checkReach() {
    try {
      const r = await api.agents.reachability();
      reach = { ok: r.ok, url: r.agent_url };
    } catch (err) {
      reach = { ok: false, url: created?.agent_url ?? '' };
      if (err instanceof ApiError && err.status !== 429) toast.error(err.message);
    }
  }
  $effect(() => {
    if (lateWait && !reach) checkReach();
  });

  // Polls until the agent connects, then while its setup state may still
  // settle (a stacks directory from the install ends the wait a moment
  // after the connect).
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    if (!created || (connected && !agent?.needs_setup)) return;
    const id = created.agent.id;
    pollTimer = setInterval(async () => {
      now = Date.now();
      try {
        const a = await api.agents.get(id);
        const first = a.status === 'online' && agent?.status !== 'online';
        agent = a;
        if (first) onconnected({ id: a.id, name: a.name });
      } catch { /* keep polling */ }
    }, 2000);
    return () => { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } };
  });
  onDestroy(() => { if (pollTimer) clearInterval(pollTimer); });

  function openHost(tab?: string) {
    if (!agent) return;
    const target = `/hosts/${agent.id}${tab ? `?tab=${tab}` : ''}`;
    onclose();
    goto(target);
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
  }
  onMount(() => dialogs.track());
  onMount(() => {
    window.addEventListener('keydown', onKey);
    // The agent port is only known to the server; the reachability check
    // reports the URL agents use.
    api.agents.reachability().then((r) => { agentPort ||= portOf(r.agent_url); }).catch(() => {});
    return () => window.removeEventListener('keydown', onKey);
  });
</script>

<div class="ed-modal-backdrop" role="presentation" onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}>
  <div class="ed-modal ah" data-testid="add-host-dialog" data-step={!created ? 'create' : !connected ? 'command' : readyPath ? 'ready' : 'setup'} role="dialog" aria-modal="true" aria-labelledby="add-host-title" use:trapFocus>
    <header class="ah-head">
      <h2 id="add-host-title">{m.hosts_add_title()}</h2>
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={onclose} aria-label={m.common_close()}>
        <X size={13} strokeWidth={1.5} />
      </button>
    </header>

    <div class="ah-body">
      {#if !created}
        <p class="ah-muted">{agentPort ? m.hostsadd_intro({ port: agentPort }) : m.hostsadd_intro_noport()}</p>
        {#if error}
          <div class="ah-callout bad" role="alert"><AlertTriangle size={14} strokeWidth={1.8} /><span>{error}</span></div>
        {/if}
        <div class="ah-field">
          <label class="ah-lbl" for="add-host-name">{m.hostsadd_name()}</label>
          <input id="add-host-name" class="dm-input" type="text" placeholder={m.hostsadd_name_ph()} bind:value={name}
            aria-invalid={nameError ? 'true' : undefined} aria-describedby="add-host-name-hint"
            oninput={() => (nameError = '')}
            onkeydown={(e) => { if (e.key === 'Enter' && !creating) create(); }} />
          {#if nameError}
            <span class="ah-err" id="add-host-name-hint" data-testid="add-host-name-error">{nameError}</span>
          {:else}
            <span class="ah-hint" id="add-host-name-hint">{m.hostsadd_name_hint()}</span>
          {/if}
        </div>
        <div class="ah-field">
          <label class="ah-lbl" for="add-host-tag">{m.hostsadd_tags()}</label>
          <div class="ah-chips">
            {#each tags as t (t)}
              <span class="ah-chip">{t}
                <button type="button" onclick={() => removeTag(t)} aria-label={m.common_remove()}><X size={10} strokeWidth={1.5} /></button>
              </span>
            {/each}
            <input id="add-host-tag" type="text" class="ah-chip-input" placeholder={m.hostsadd_tag_ph()} bind:value={tagInput}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTag(tagInput); } }} />
          </div>
          {#if suggestionPool.some((s) => !tags.includes(s))}
            <div class="ah-suggest">
              {#each suggestionPool.filter((s) => !tags.includes(s)).slice(0, 8) as s (s)}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => addTag(s)}>+ {s}</button>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        <div class="ah-collapsed" data-testid="add-host-summary"><span class="dm-mono">{connected ? displayName : summaryName}</span>{#if tags.length} · {tags.join(', ')}{/if}</div>

        {#if !connected}
          <div class="ah-field">
            <span class="ah-title">{m.hostsadd_run_title()}</span>
            <div class="ah-cmd" class:dead={expired}>
              <code class="dm-mono" data-testid="install-command">{created.install_hint}</code>
              <button type="button" class="dm-btn dm-btn-sm" disabled={expired} onclick={() => copy('cmd', created!.install_hint)}>
                {#if copied === 'cmd'}<Check size={11} strokeWidth={1.5} /> {m.common_copied()}{:else}<Copy size={11} strokeWidth={1.5} /> {m.common_copy()}{/if}
              </button>
            </div>
            {#if expired}
              <div class="ah-row">
                <span class="ah-err">{m.hosts_add_expired()}</span>
                <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={rotating} onclick={newCommand}>{m.hosts_add_new_command()}</button>
              </div>
            {:else}
              <div class="ah-hint">
                {m.hostsadd_valid({ time: fmtTime(created.token_expires_at, { hour: '2-digit', minute: '2-digit' }) })}
                <button type="button" class="ah-link" data-testid="add-host-new-command" disabled={rotating} onclick={newCommand}>{m.hosts_add_new_command()}</button>
              </div>
            {/if}
            <span class="ah-hint">{m.hosts_add_pin_hint()}</span>
          </div>

          {#if created.behind_proxy}
            <div class="ah-callout info"><Info size={14} strokeWidth={1.8} />
              <span>{m.hostsadd_proxy_hint({ port: agentPort })}
                <a class="ah-docs" href="{DOCS_BASE}features/multi-host#agent-port" target="_blank" rel="noopener noreferrer">{m.hostsadd_docs()}</a></span>
            </div>
          {/if}

          <div>
            <button type="button" class="ah-link" data-testid="add-host-automation" aria-expanded={showAuto} onclick={() => (showAuto = !showAuto)}>
              {#if showAuto}<ChevronDown size={12} strokeWidth={1.8} />{:else}<ChevronRight size={12} strokeWidth={1.8} />{/if}
              {m.hostsadd_auto()}
            </button>
            {#if showAuto}
              <div class="ah-auto" data-testid="add-host-flags">
                <div class="ah-field">
                  <span class="ah-lbl">{m.hostsadd_token()}</span>
                  <div class="ah-cmd small">
                    <code class="dm-mono">{created.token}</code>
                    <button type="button" class="dm-btn dm-btn-sm" disabled={expired} onclick={() => copy('token', created!.token)}>
                      {#if copied === 'token'}<Check size={11} strokeWidth={1.5} /> {m.common_copied()}{:else}<Copy size={11} strokeWidth={1.5} /> {m.common_copy()}{/if}
                    </button>
                  </div>
                </div>
                <div class="ah-flag"><code>--data-dir &lt;path&gt;</code><span>{m.hostsadd_flag_data()}</span></div>
                <div class="ah-flag"><code>--stacks-dir &lt;path&gt;</code><span>{m.hostsadd_flag_stacks()}</span></div>
                <div class="ah-flag"><code>--skip-docker</code><span>{m.hostsadd_flag_docker()}</span></div>
                <div class="ah-flag"><code>API</code><span><code>POST /api/v1/agents</code> {m.hostsadd_api_with()} <code>name</code>, <code>tags</code>, <code>stacks_dir</code> ·
                  <a class="ah-docs" href="{DOCS_BASE}features/multi-host#automation" target="_blank" rel="noopener noreferrer">{m.hostsadd_api_docs()}</a></span></div>
              </div>
            {/if}
          </div>

          <div class="ah-callout" role="status" data-testid="add-host-waiting">
            <span class="ah-pulse" aria-hidden="true"></span>
            <span><b>{m.hostsadd_waiting({ name: displayName })}</b><br /><span class="ah-hint">{m.hostsadd_waiting_meta()}</span></span>
          </div>
          {#if lateWait && reach}
            <div class="ah-callout {reach.ok ? 'ok' : 'bad'}" data-testid="reachability">
              {#if reach.ok}<Check size={14} strokeWidth={1.8} />{:else}<AlertTriangle size={14} strokeWidth={1.8} />{/if}
              <span>{reach.ok ? m.hostsadd_reach_ok({ port: agentPort }) : m.hostsadd_reach_bad({ port: agentPort })}
                {#if !reach.ok}<br />{m.hostsadd_reach_bad_detail({ url: reach.url })}{/if}</span>
            </div>
          {/if}
        {:else}
          <div class="ah-done">
            <span class="ah-mark"><Check size={14} strokeWidth={1.8} /></span>
            <b>{m.hostsadd_connected({ name: displayName })}</b>
          </div>
          {#if readyPath}
            <p class="ah-p" data-testid="add-host-ready">{m.hostsadd_ready({ name: displayName, path: readyPath })}</p>
          {:else}
            <p class="ah-p">{m.hostsadd_needs_setup()}</p>
          {/if}
        {/if}
      {/if}
    </div>

    <footer class="ah-foot">
      {#if !connected}
        <a class="ah-docs" href="{DOCS_BASE}features/multi-host#add-a-host" target="_blank" rel="noopener noreferrer">{m.hostsadd_docs()}</a>
      {/if}
      <span class="dm-sp"></span>
      {#if !created}
        <button type="button" class="dm-btn dm-btn-ghost" onclick={onclose}>{m.common_cancel()}</button>
        <button type="button" class="dm-btn dm-btn-primary" data-testid="add-host-create" disabled={creating} onclick={create}>
          {creating ? m.hostsadd_creating() : m.hostsadd_create()}
        </button>
      {:else if !connected}
        <button type="button" class="dm-btn dm-btn-ghost" onclick={onclose}>{m.common_close()}</button>
      {:else if readyPath}
        <button type="button" class="dm-btn dm-btn-primary" data-testid="add-host-open" onclick={() => openHost()}>{m.hostsadd_open_host()}</button>
      {:else}
        <button type="button" class="dm-btn dm-btn-ghost" onclick={onclose}>{m.common_close()}</button>
        <button type="button" class="dm-btn dm-btn-primary" data-testid="add-host-setup" onclick={() => openHost('setup')}>{m.hostsadd_setup_btn()}</button>
      {/if}
    </footer>
  </div>
</div>

<style>
  .ed-modal-backdrop {
    position: fixed; inset: 0;
    background: color-mix(in srgb, var(--bg) 70%, transparent);
    backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px);
    z-index: 60;
    display: flex; align-items: center; justify-content: center;
  }
  .ah {
    width: min(620px, 96vw); max-height: 92vh;
    display: flex; flex-direction: column; overflow: hidden;
    background: var(--bg-elevated); border: 1px solid var(--border-strong); border-radius: 8px;
  }
  .ah-head { display: flex; align-items: center; justify-content: space-between; padding: 16px 20px; border-bottom: 1px solid var(--border); }
  .ah-head h2 { margin: 0; font-size: var(--fs-lg, 17px); font-weight: 600; color: var(--fg); }
  .ah-body { padding: 18px 20px; overflow-y: auto; display: flex; flex-direction: column; gap: 14px; font-size: var(--fs-sm); }
  .ah-foot { display: flex; align-items: center; gap: 8px; padding: 12px 20px; border-top: 1px solid var(--border); }
  .ah-muted, .ah-p { margin: 0; color: var(--muted); line-height: 1.5; }
  .ah-p { color: var(--fg); }
  .ah-field { display: flex; flex-direction: column; gap: 6px; }
  .ah-lbl { font-size: var(--fs-xs, 12px); color: var(--muted); }
  .ah-title { font-weight: 600; color: var(--fg); }
  .ah-hint { font-size: var(--fs-xs, 12px); color: var(--muted); }
  .ah-err { font-size: var(--fs-xs, 12px); color: var(--bad); }
  .dm-input[aria-invalid='true'] { border-color: var(--bad); }
  .ah-row { display: flex; align-items: center; gap: 10px; }
  .ah-link {
    background: none; border: 0; padding: 0; cursor: pointer; color: var(--accent);
    font: inherit; display: inline-flex; align-items: center; gap: 4px;
  }
  .ah-link:disabled { opacity: 0.5; cursor: default; }
  .ah-docs { color: var(--accent); font-size: var(--fs-xs, 12px); text-decoration: none; }
  .ah-docs:hover { text-decoration: underline; }

  .ah-chips {
    display: flex; flex-wrap: wrap; gap: 6px; align-items: center;
    padding: 6px 8px; border: 1px solid var(--border); border-radius: var(--r, 5px); background: var(--field, var(--surface));
  }
  .ah-chip {
    display: inline-flex; align-items: center; gap: 4px; padding: 1px 6px;
    border: 1px solid var(--border-strong); border-radius: 3px; font-size: var(--fs-xs, 12px); color: var(--fg);
  }
  .ah-chip button { background: none; border: 0; padding: 0; cursor: pointer; color: var(--muted); display: inline-flex; }
  .ah-chip button:hover { color: var(--bad); }
  .ah-chip-input { flex: 1 1 120px; min-width: 100px; border: 0; background: transparent; color: var(--fg); outline: none; font-size: var(--fs-sm); }
  .ah-suggest { display: flex; flex-wrap: wrap; gap: 4px; }

  .ah-collapsed { padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--r, 5px); color: var(--muted); }
  .ah-collapsed .dm-mono { color: var(--fg); }

  .ah-cmd {
    display: flex; gap: 10px; align-items: flex-start;
    padding: 10px 12px; border: 1px solid var(--border); border-radius: 6px; background: var(--field, var(--surface));
  }
  .ah-cmd code { flex: 1; min-width: 0; font-size: 12px; line-height: 1.6; white-space: pre-wrap; word-break: break-all; color: var(--fg); }
  .ah-cmd.dead { opacity: 0.45; }
  .ah-cmd.small { padding: 6px 10px; }

  .ah-auto { margin-top: 8px; padding: 10px 12px; border: 1px solid var(--border); border-radius: 6px; display: flex; flex-direction: column; gap: 8px; }
  .ah-flag { display: grid; grid-template-columns: 170px 1fr; gap: 10px; align-items: baseline; }
  .ah-flag > code { font-family: var(--font-mono); font-size: 12px; color: var(--fg); }
  .ah-flag span { color: var(--muted); }
  .ah-flag span code { font-family: var(--font-mono); font-size: 12px; color: var(--fg); }

  .ah-callout {
    display: flex; gap: 10px; align-items: flex-start; padding: 10px 12px;
    border: 1px solid var(--border); border-radius: var(--r, 5px); line-height: 1.5;
  }
  .ah-callout > :global(svg) { flex-shrink: 0; margin-top: 2px; }
  .ah-callout.info { border-color: color-mix(in srgb, var(--accent) 35%, transparent); background: var(--accent-soft); }
  .ah-callout.info > :global(svg) { color: var(--accent); }
  .ah-callout.ok { border-color: color-mix(in srgb, var(--ok) 40%, transparent); background: var(--ok-bg); }
  .ah-callout.ok > :global(svg) { color: var(--ok); }
  .ah-callout.bad { border-color: color-mix(in srgb, var(--bad) 40%, transparent); background: var(--bad-bg); }
  .ah-callout.bad > :global(svg) { color: var(--bad); }
  .ah-pulse {
    width: 9px; height: 9px; margin-top: 5px; flex-shrink: 0; border-radius: 999px; background: var(--accent);
    box-shadow: 0 0 0 4px color-mix(in srgb, var(--accent) 25%, transparent);
    animation: ah-blink 1.4s ease-in-out infinite;
  }
  @keyframes ah-blink { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
  @media (prefers-reduced-motion: reduce) { .ah-pulse { animation: none; } }

  .ah-done { display: flex; align-items: center; gap: 10px; font-size: var(--fs-md, 15px); }
  .ah-mark {
    width: 26px; height: 26px; border-radius: 999px; display: inline-flex; align-items: center; justify-content: center;
    background: var(--ok-bg); border: 1px solid var(--ok); color: var(--ok);
  }
  @media (max-width: 560px) {
    .ah-flag { grid-template-columns: 1fr; gap: 2px; }
  }
</style>
