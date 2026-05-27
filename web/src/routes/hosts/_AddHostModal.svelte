<script lang="ts">
  // Add-Host Modal — 3-step inline flow per `Dockmesh Wizard (6)/
  // add-host-modal.jsx`. Section 01 collects identity (hostname + tags),
  // Section 02 unlocks once the token is generated and shows the install
  // command, Section 03 polls for the agent to dial home and auto-closes
  // on connection. Description + scope-radio + compose-alt link are
  // intentionally absent — see project_hosts_open_punch_list.md memory.
  import { onMount, onDestroy } from 'svelte';
  import { api, ApiError, type AgentCreateResult } from '$lib/api';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { Plus, X, Check, Copy, ArrowRight } from 'lucide-svelte';

  interface Props {
    onclose: () => void;
    onconnected: (agent: { id: string; name: string }) => void;
    knownTags?: string[];
  }
  let { onclose, onconnected, knownTags = [] }: Props = $props();

  // Section 01 state
  let hostname = $state('');
  let tags = $state<string[]>([]);
  let tagInput = $state('');
  // Function / role / capability tags only — no geographic regions.
  // Region tags become user-defined when actually needed.
  const SUGGESTIONS = ['edge', 'production', 'staging', 'gpu', 'storage', 'arm', 'backup'];
  const suggestionPool = $derived([...new Set([...SUGGESTIONS, ...knownTags])]);

  function addTag(t: string) {
    const v = t.trim().toLowerCase().replace(/,$/, '');
    if (!v || tags.includes(v)) { tagInput = ''; return; }
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(v)) {
      toast.error('Invalid tag', 'lowercase letters, digits, hyphens; 1-32 chars');
      return;
    }
    if (tags.length >= 20) {
      toast.error('Max 20 tags');
      return;
    }
    tags = [...tags, v];
    tagInput = '';
  }
  function removeTag(t: string) { tags = tags.filter((x) => x !== t); }

  // Section 02 state — generated install command
  let createResult = $state<AgentCreateResult | null>(null);
  let creating = $state(false);
  let copied = $state(false);
  let savingTags = $state(false);

  async function generateToken() {
    if (!hostname.trim()) {
      toast.error('Hostname required');
      return;
    }
    creating = true;
    try {
      createResult = await api.agents.create(hostname.trim());
      // Save tags right after enrolling so the host shows up tagged
      // even before it dials home.
      if (tags.length > 0) {
        savingTags = true;
        try { await api.hosts.setTags(createResult.agent.id, tags); }
        catch { /* non-fatal — admin can edit tags later */ }
        savingTags = false;
      }
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  async function copyCmd() {
    if (!createResult || typeof navigator === 'undefined' || !navigator.clipboard) return;
    try {
      await navigator.clipboard.writeText(createResult.install_hint);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch { toast.error('Copy failed'); }
  }

  // Section 03 — poll for agent to come online.
  let connected = $state(false);
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    if (!createResult || connected) return;
    pollTimer = setInterval(async () => {
      if (!createResult) return;
      try {
        const a = await api.agents.get(createResult.agent.id);
        if (a.status === 'online') {
          connected = true;
          if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
          // Auto-close after ~1.1s so the user sees the success state.
          setTimeout(() => onconnected({ id: a.id, name: a.name }), 1100);
        }
      } catch { /* keep polling */ }
    }, 2000);
    return () => { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } };
  });
  onDestroy(() => { if (pollTimer) clearInterval(pollTimer); });

  // ESC to close
  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && !connected) onclose();
  }
  onMount(() => {
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
</script>

<div class="ed-modal-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget && !connected) onclose(); }}>
  <div class="ed-modal ed-add-host-modal" role="dialog" aria-modal="true">
    <header class="ed-modal-head">
      <div class="ed-add-host-head-text">
        <Eyebrow>Hosts · enroll new</Eyebrow>
        <h2 class="ed-add-host-title">Bring a new host online.</h2>
        <p class="ed-add-host-blurb">
          Three steps. The new host runs <span class="ed-accent-mono">one</span> curl command, then it shows up here ready to take stacks.
        </p>
      </div>
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={onclose} aria-label="Close" disabled={connected}>
        <X size={13} strokeWidth={1.5} />
      </button>
    </header>

    <div class="ed-modal-body ed-add-host-body">
      <!-- ─── 01 Identify ─── -->
      <section class="ed-add-host-section" class:active={!createResult}>
        <span class="ed-add-host-step" class:active={!createResult}>01</span>
        <div class="ed-add-host-section-body">
          <h3 class="ed-add-host-section-title">Identify the host</h3>
          <div class="ed-add-host-fields">
            <div class="ed-field">
              <label class="ed-field-label" for="add-host-hostname">Hostname</label>
              <input
                id="add-host-hostname"
                type="text"
                class="ed-soft-input font-mono"
                placeholder="e.g. edge-fra-2"
                bind:value={hostname}
                disabled={!!createResult}
              />
            </div>
            <div class="ed-field">
              <label class="ed-field-label">
                Tags
                <span class="ed-field-hint">Press Enter or comma to add. Lowercase, max 20.</span>
              </label>
              <div class="ed-host-tags-edit">
                {#each tags as t (t)}
                  <span class="ed-hosts-tag-chip">
                    {t}
                    {#if !createResult}
                      <button type="button" class="ed-host-tags-remove" onclick={() => removeTag(t)} aria-label="Remove">
                        <X size={10} strokeWidth={1.5} />
                      </button>
                    {/if}
                  </span>
                {/each}
                {#if !createResult}
                  <input
                    type="text"
                    class="ed-host-tags-input font-mono"
                    placeholder={tags.length === 0 ? 'edge, production, gpu…' : '+ add tag'}
                    bind:value={tagInput}
                    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTag(tagInput); } }}
                  />
                {/if}
              </div>
              {#if !createResult && suggestionPool.filter((s) => !tags.includes(s)).length > 0}
                <div class="ed-host-tags-suggest">
                  <span class="font-mono ed-host-tags-suggest-label">suggestions</span>
                  {#each suggestionPool.filter((s) => !tags.includes(s)).slice(0, 8) as s (s)}
                    <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => addTag(s)}>+ {s}</button>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
          {#if !createResult}
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={generateToken} disabled={creating || !hostname.trim()}>
              {creating ? 'Generating…' : 'Generate install command'}
              <ArrowRight size={11} strokeWidth={1.5} />
            </button>
          {:else}
            <p class="ed-add-host-confirm font-mono">
              <Check size={11} strokeWidth={1.5} class="ed-add-host-check" />
              token issued for <span class="ed-accent-mono">{createResult.agent.name}</span> · ready in section 02
            </p>
          {/if}
        </div>
      </section>

      <!-- ─── 02 Run install ─── -->
      <section class="ed-add-host-section" class:active={!!createResult && !connected} class:muted={!createResult}>
        <span class="ed-add-host-step" class:active={!!createResult}>02</span>
        <div class="ed-add-host-section-body">
          <h3 class="ed-add-host-section-title">Run the install on the new host</h3>
          {#if !createResult}
            <p class="ed-add-host-blurb">Fill out section <span class="ed-accent-mono">01</span> first.</p>
          {:else}
            <p class="ed-add-host-blurb">
              Open a shell on <span class="ed-accent-mono">{createResult.agent.name}</span> and paste the command below.
              <span class="ed-accent-mono">Save the token now</span> — it won't be shown again.
            </p>
            <div class="ed-add-host-cmd-wrap">
              <pre class="ed-add-host-cmd">{createResult.install_hint}</pre>
              <button type="button" class="dm-btn dm-btn-secondary dm-btn-xs ed-add-host-copy" onclick={copyCmd}>
                {#if copied}<Check size={10} strokeWidth={1.5} /> Copied{:else}<Copy size={10} strokeWidth={1.5} /> Copy{/if}
              </button>
            </div>
            <div class="ed-add-host-tokenbox">
              <span class="font-mono ed-add-host-tokenbox-label">enrollment token</span>
              <code class="font-mono ed-add-host-tokenbox-value">{createResult.token}</code>
            </div>
          {/if}
        </div>
      </section>

      <!-- ─── 03 Wait ─── -->
      <section class="ed-add-host-section" class:active={!!createResult} class:muted={!createResult}>
        <span class="ed-add-host-step" class:active={!!createResult}>03</span>
        <div class="ed-add-host-section-body">
          <h3 class="ed-add-host-section-title">Wait for the agent to dial home</h3>
          {#if !createResult}
            <p class="ed-add-host-blurb">Once you've run the install, this section flips to live.</p>
          {:else if !connected}
            <div class="ed-add-host-waiting">
              <span class="ed-add-host-pulse" aria-hidden="true"></span>
              <div>
                <div class="ed-add-host-waiting-text">
                  Waiting for <span class="ed-accent-mono">{createResult.agent.name}</span> to call back…
                </div>
                <div class="font-mono ed-add-host-waiting-meta">
                  polling every 2s · usually 10–30 seconds after install
                </div>
              </div>
            </div>
            <p class="font-mono ed-add-host-troubleshoot">
              if it doesn't connect within 2 minutes — verify {createResult.agent.name} can reach <code>{createResult.agent_url}</code> outbound.
            </p>
          {:else}
            <div class="ed-add-host-success">
              <span class="ed-add-host-success-mark"><Check size={13} strokeWidth={1.5} /></span>
              <div>
                <div class="ed-add-host-success-text">
                  <span class="ed-accent-mono">{createResult.agent.name}</span> is online.
                </div>
                <div class="font-mono ed-add-host-success-meta">
                  enrolled · {tags.length} tag{tags.length === 1 ? '' : 's'} · closing automatically…
                </div>
              </div>
            </div>
          {/if}
        </div>
      </section>
    </div>

    <footer class="ed-modal-foot">
      <span class="font-mono ed-modal-foot-status">ESC to cancel</span>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={onclose}>
          {connected ? 'Done' : 'Cancel'}
        </button>
      </div>
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
  .ed-modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    max-height: 92vh;
    display: flex; flex-direction: column;
    overflow: hidden;
  }
  .ed-add-host-modal { width: min(640px, 96vw); }
  .ed-modal-head {
    display: flex; align-items: flex-start; justify-content: space-between;
    gap: 16px; padding: 22px 26px 18px;
    border-bottom: 1px solid var(--border);
  }
  .ed-add-host-title { font-size: 22px; font-weight: 600; margin: 6px 0 0; color: var(--fg); letter-spacing: -0.01em; }
  .ed-add-host-blurb { font-size: 13px; color: var(--fg-muted); margin: 6px 0 0; line-height: 1.55; }
  .ed-modal-body { padding: 22px 26px; overflow-y: auto; flex: 1; }
  .ed-add-host-body { display: flex; flex-direction: column; gap: 0; }

  .ed-modal-foot {
    display: flex; align-items: center; justify-content: space-between;
    gap: 12px; padding: 14px 26px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
  }
  .ed-modal-foot-status { font-size: 11px; color: var(--fg-subtle); }
  .ed-accent-mono { color: var(--accent-fg); font-family: var(--font-mono); font-style: normal; font-weight: 500; }

  /* ─── Step sections ─── */
  .ed-add-host-section {
    display: grid;
    grid-template-columns: 44px 1fr;
    gap: 16px;
    padding-top: 22px;
    margin-top: 22px;
    border-top: 1px solid var(--border);
    transition: opacity 0.18s;
  }
  .ed-add-host-section:first-child { margin-top: 0; padding-top: 0; border-top: 0; }
  .ed-add-host-section.muted { opacity: 0.5; }
  .ed-add-host-step {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.16em;
    padding-top: 4px;
  }
  .ed-add-host-step.active { color: var(--accent-fg); }
  .ed-add-host-section-title {
    font-family: var(--font-sans);
    font-size: 15px; font-weight: 600;
    color: var(--fg); letter-spacing: -0.01em;
    margin: 0 0 10px;
  }
  .ed-add-host-fields { display: flex; flex-direction: column; gap: 16px; margin-bottom: 14px; }

  .ed-field { display: flex; flex-direction: column; gap: 6px; }
  .ed-field-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    display: flex; align-items: baseline; justify-content: space-between;
  }
  .ed-field-hint { font-size: 10px; color: var(--fg-subtle); text-transform: none; letter-spacing: 0.02em; }
  .ed-soft-input {
    width: 100%;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    padding: 8px 2px;
    font-size: 13px;
    color: var(--fg);
    outline: none;
    transition: border-color 0.1s;
    border-radius: 0;
  }
  .ed-soft-input::placeholder { color: var(--fg-subtle); }
  .ed-soft-input:focus { border-bottom-color: var(--accent); }
  .ed-soft-input.font-mono { font-family: var(--font-mono); letter-spacing: 0.02em; }
  .ed-soft-input:disabled { opacity: 0.6; }

  .ed-host-tags-edit {
    display: flex; flex-wrap: wrap; gap: 6px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
  }
  .ed-host-tags-input {
    flex: 1 1 140px; border: 0; background: transparent;
    color: var(--fg); font-size: 11.5px; outline: none; min-width: 100px;
  }
  .ed-host-tags-input::placeholder { color: var(--fg-subtle); }
  .ed-hosts-tag-chip {
    display: inline-flex; align-items: center; gap: 4px;
    padding: 2px 6px;
    border: 1px solid var(--border-strong);
    border-radius: 3px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg);
    background: var(--surface);
  }
  .ed-host-tags-remove {
    background: transparent; border: 0; cursor: pointer;
    color: var(--fg-subtle); padding: 0;
    display: inline-flex;
  }
  .ed-host-tags-remove:hover { color: var(--color-danger-400); }
  .ed-host-tags-suggest {
    display: flex; gap: 6px; flex-wrap: wrap; margin-top: 8px; align-items: center;
  }
  .ed-host-tags-suggest-label {
    font-size: 9.5px; color: var(--fg-subtle); letter-spacing: 0.1em;
    text-transform: uppercase; margin-right: 4px;
  }

  .ed-add-host-confirm {
    font-size: 11px; color: var(--color-success-400);
    margin: 4px 0 0; display: inline-flex; align-items: center; gap: 6px;
  }
  :global(.ed-add-host-check) { color: var(--color-success-400); }

  /* ─── Section 02 ─── */
  .ed-add-host-cmd-wrap { position: relative; margin-top: 10px; }
  .ed-add-host-cmd {
    margin: 0;
    padding: 14px 16px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px; line-height: 1.65;
    color: var(--fg);
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 200px;
    overflow-y: auto;
  }
  .ed-add-host-copy { position: absolute; top: 8px; right: 8px; }
  .ed-add-host-tokenbox {
    margin-top: 12px;
    padding: 10px 14px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-warning-500) 6%, transparent);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .ed-add-host-tokenbox-label {
    font-size: 10px; color: var(--color-warning-400); letter-spacing: 0.06em; text-transform: uppercase;
  }
  .ed-add-host-tokenbox-value {
    font-size: 11.5px; color: var(--fg); word-break: break-all;
  }

  /* ─── Section 03 ─── */
  .ed-add-host-waiting {
    display: flex; align-items: flex-start; gap: 12px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
  }
  .ed-add-host-pulse {
    width: 9px; height: 9px;
    border-radius: 999px;
    background: var(--accent);
    box-shadow: 0 0 0 4px color-mix(in srgb, var(--accent) 25%, transparent);
    animation: ed-add-host-blink 1.4s ease-in-out infinite;
    flex-shrink: 0;
    margin-top: 4px;
  }
  @keyframes ed-add-host-blink {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.3; }
  }
  .ed-add-host-waiting-text { font-size: 13px; color: var(--fg); font-weight: 500; }
  .ed-add-host-waiting-meta { font-size: 11px; color: var(--fg-subtle); margin-top: 4px; letter-spacing: 0.02em; }
  .ed-add-host-troubleshoot {
    margin-top: 10px;
    font-size: 10.5px;
    color: var(--fg-subtle);
    line-height: 1.6;
  }
  .ed-add-host-troubleshoot code { color: var(--accent-fg); }

  .ed-add-host-success {
    display: flex; align-items: center; gap: 10px;
    padding: 14px 16px;
    border: 1px solid color-mix(in srgb, var(--color-success-500) 40%, var(--border));
    border-radius: 6px;
    background: color-mix(in srgb, var(--color-success-500) 6%, transparent);
  }
  .ed-add-host-success-mark {
    width: 24px; height: 24px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--color-success-500) 18%, transparent);
    border: 1px solid var(--color-success-500);
    display: inline-flex; align-items: center; justify-content: center;
    color: var(--color-success-400);
    flex-shrink: 0;
  }
  .ed-add-host-success-text { font-size: 13px; color: var(--fg); font-weight: 500; }
  .ed-add-host-success-meta { font-size: 11px; color: var(--fg-subtle); margin-top: 3px; }
</style>
