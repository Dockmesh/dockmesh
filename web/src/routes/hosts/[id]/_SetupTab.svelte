<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { untrack } from 'svelte';
  import { api, ApiError, type Agent, type StacksDirCheck, type StacksDirDisk } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { fmtMem } from '$lib/format';
  import { fmtVersion } from '$lib/version';
  import { copyWithToast } from '$lib/clipboard';
  import { DOCS_BASE } from '$lib/setupWizard';
  import { TagPill } from '$lib/components/ui';
  import DiskList from '$lib/components/setup/DiskList.svelte';
  import { Check, X, AlertTriangle, Info, Copy, Loader } from 'lucide-svelte';

  interface Props {
    agent: Agent;
    tags: string[];
    onedittags: () => void;
    onchanged: () => void;
  }
  let { agent, tags, onedittags, onchanged }: Props = $props();

  const DEFAULT_DATA = '/var/lib/dockmesh-agent';
  const online = $derived(agent.status === 'online');
  const legacy = $derived(online && agent.layout !== 'managed');
  const canEdit = $derived(allowed('hosts.update'));
  const current = $derived(agent.stacks_dirs?.find((d) => d.current)?.path ?? '');
  const earlier = $derived((agent.stacks_dirs ?? []).filter((d) => !d.current && d.stacks > 0));
  const dataDir = $derived(agent.data_dir || DEFAULT_DATA);

  let path = $state('');
  let touched = $state(false);
  let check = $state<StacksDirCheck | null>(null);
  let checkError = $state('');
  let checking = $state(false);
  let disks = $state<StacksDirDisk[]>([]);
  let disksUnsupported = $state(false);
  let saving = $state(false);
  let error = $state('');
  // Apply progress of a switch: 0 checking, 1 creating, 2 restarting, 3 done.
  let step = $state(-1);
  let applying = $state('');
  let confirmed = $state('');

  // The field starts at the current directory and follows it until the
  // user types.
  $effect(() => {
    const c = current || `${dataDir}/stacks`;
    if (!untrack(() => touched)) path = c;
  });

  $effect(() => {
    if (!online || !canEdit) return;
    const id = agent.id;
    api.hosts.stacksDirDisks(id)
      .then((r) => { disks = r.disks; disksUnsupported = !!r.unsupported; })
      .catch(() => { disks = []; });
  });

  // Live check, debounced; an older answer never replaces a newer one.
  $effect(() => {
    const p = path.trim();
    const id = agent.id;
    if (!online || !canEdit || legacy || !p) { check = null; return; }
    const ctl = new AbortController();
    const t = setTimeout(async () => {
      checking = true;
      checkError = '';
      try {
        check = await api.hosts.checkStacksDir(id, p, ctl.signal);
      } catch (err) {
        if (ctl.signal.aborted) return;
        check = null;
        checkError = err instanceof ApiError ? err.message : String(err);
      } finally {
        if (!ctl.signal.aborted) checking = false;
      }
    }, 400);
    return () => { clearTimeout(t); ctl.abort(); };
  });

  // Follows a switch through the agent's restart until GET /agents shows
  // the answer.
  $effect(() => {
    const req = agent.stacks_dir;
    if (!applying || !req || req.path !== applying) return;
    if (req.status === 'applied' && current === applying) {
      step = 3;
      applying = '';
      touched = false;
    } else if (req.status === 'refused') {
      error = m.hosts_setup_refused({ reason: req.error || '—' });
      step = -1;
      applying = '';
    } else if (!online) {
      step = 2;
    }
  });
  $effect(() => {
    if (!applying) return;
    const t = setInterval(onchanged, 2000);
    return () => clearInterval(t);
  });

  const blocked = $derived(!online || legacy || !canEdit || saving || !!applying || checking || check?.status === 'fail' || !path.trim());

  async function save() {
    const p = path.trim();
    error = '';
    confirmed = '';
    step = -1;
    saving = true;
    try {
      const before = current;
      const a = await api.hosts.setStacksDir(agent.id, p);
      if (a.stacks_dir?.status === 'applied' && before === p) {
        confirmed = p;
        touched = false;
      } else {
        applying = p;
        step = 1;
        setTimeout(() => { if (applying === p && step < 2) step = 2; }, 1500);
      }
      onchanged();
    } catch (err) {
      error = m.hosts_setup_failed({ message: err instanceof ApiError ? err.message : String(err) });
    } finally {
      saving = false;
    }
  }

  function stateText(c: StacksDirCheck): string {
    if (c.issue === 'unchanged') return m.hosts_setup_state_current();
    return c.will_create ? m.hosts_setup_state_new() : m.hosts_setup_state_empty();
  }
  const steps = $derived([m.hosts_setup_step_check(), m.hosts_setup_step_create({ path: applying || path }), m.hosts_setup_step_restart()]);
</script>

<div class="st" data-testid="setup-tab">
  {#if !canEdit}
    <p class="hint">{m.hosts_setup_readonly()}</p>
  {/if}

  <section class="sec">
    <h2>{m.hosts_setup_general()}</h2>
    <dl class="kv">
      <dt>{m.hosts_setup_name()}</dt><dd>{agent.name}</dd>
      <dt>{m.hosts_col_tags()}</dt>
      <dd class="tags">
        {#each tags as t (t)}<TagPill label={t} />{:else}<span class="dm-dim">—</span>{/each}
        {#if allowed('hosts.tag')}<button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={onedittags}>{m.hosts_setup_tags_edit()}</button>{/if}
      </dd>
    </dl>
  </section>

  <section class="sec">
    <div class="hrow">
      <h2>{m.hosts_setup_title()}</h2>
      <a class="docs" href="{DOCS_BASE}features/multi-host#stacks-directory" target="_blank" rel="noopener noreferrer">Docs ↗</a>
    </div>
    <p class="sub">{m.hosts_setup_text()}</p>

    {#if !online}
      <div class="callout"><Info size={14} strokeWidth={1.8} /><span>{m.hosts_setup_offline()}</span></div>
    {:else if legacy}
      <div class="callout info" data-testid="setup-legacy">
        <Info size={14} strokeWidth={1.8} />
        <div class="cbody">
          <span>{m.hosts_setup_legacy()}</span>
          {#if agent.rerun_command}
            <div class="cmd">
              <code class="dm-mono">{agent.rerun_command}</code>
              <button type="button" class="dm-btn dm-btn-sm" onclick={() => copyWithToast(agent.rerun_command!, m.hosts_legacy_copied())}>
                <Copy size={12} strokeWidth={1.8} /> {m.common_copy()}
              </button>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    {#if error}
      <div class="callout bad" role="alert" data-testid="setup-error"><AlertTriangle size={14} strokeWidth={1.8} /><span>{error}</span></div>
    {/if}

    <div class="body" class:off={legacy || !online}>
      <div class="field">
        <label class="lbl" for="setup-stacks-dir">{m.hosts_setup_field()}</label>
        <input id="setup-stacks-dir" class="dm-input dm-mono" type="text" bind:value={path} disabled={legacy || !online || !canEdit || !!applying}
          aria-invalid={check?.status === 'fail' ? 'true' : undefined} aria-describedby="setup-stacks-dir-result"
          oninput={() => { touched = true; confirmed = ''; step = -1; error = ''; }} />
        <div id="setup-stacks-dir-result" class="result" data-testid="setup-check">
          {#if checking}
            <span class="dim"><Loader size={12} strokeWidth={1.8} class="spin" /> {m.hosts_setup_checking()}</span>
          {:else if checkError}
            <span class="err">{checkError}</span>
          {:else if check?.status === 'fail'}
            <span class="err">{check.message}</span>
          {:else if check}
            <span class="okline"><Check size={12} strokeWidth={2} />
              {m.hosts_setup_result({ free: fmtMem(check.free_bytes ?? 0), fs: check.fs_type || '—', state: stateText(check) })}</span>
            {#if check.status === 'warn'}<span class="wrn">{check.message}</span>{/if}
          {/if}
        </div>
      </div>

      {#if online && !legacy && canEdit}
        <div class="field">
          <span class="lbl">{m.hosts_setup_disks()}</span>
          {#if disksUnsupported}
            <span class="hint">{m.hosts_setup_disks_unsupported()}</span>
          {:else}
            <DiskList {disks} selected={path.trim()} disabled={!!applying} onuse={(p) => { path = p; touched = true; confirmed = ''; step = -1; error = ''; }} />
            <span class="hint">{m.hosts_setup_disks_hint()}</span>
          {/if}
        </div>
      {/if}

      <div class="row">
        <button type="button" class="dm-btn dm-btn-primary" data-testid="setup-save" disabled={blocked} onclick={save}>
          {agent.needs_setup ? m.hosts_setup_save_finish() : m.hosts_setup_save()}
        </button>
        {#if agent.needs_setup && online && !legacy}<span class="hint">{m.hosts_setup_save_hint()}</span>{/if}
      </div>

      {#if step >= 0}
        <ul class="steps" data-testid="setup-progress">
          {#each steps as s, i (i)}
            <li class:d={i < step} class:r={i === step && step < 3}>
              {#if i < step}<Check size={12} strokeWidth={2} />{:else if i === step}<Loader size={12} strokeWidth={1.8} class="spin" />{:else}<span class="dot"></span>{/if}
              {s}
            </li>
          {/each}
        </ul>
        {#if step === 3}
          <div class="callout ok" data-testid="setup-done"><Check size={14} strokeWidth={1.8} /><span>{m.hosts_setup_done({ name: agent.name, path: current })}</span></div>
        {/if}
      {/if}
      {#if confirmed}
        <div class="callout ok" data-testid="setup-done"><Check size={14} strokeWidth={1.8} /><span>{m.hosts_setup_confirmed({ name: agent.name, path: confirmed })}</span></div>
      {/if}

      {#if earlier.length}
        <div class="hint earlier">
          {m.hosts_setup_earlier()}
          {#each earlier as d (d.path)}
            <span><span class="dm-mono">{d.path}</span> ({m.hosts_setup_earlier_n({ n: d.stacks })}) · <span class="soon" title={m.hosts_setup_move_soon()}>{m.hosts_setup_move()}</span></span>
          {/each}
        </div>
      {/if}
    </div>
  </section>

  <section class="sec">
    <h2>{m.hosts_setup_agent()}</h2>
    <dl class="kv">
      <dt>{m.hosts_setup_data_dir()}</dt><dd><span class="dm-mono">{agent.data_dir || '—'}</span> · {m.hosts_setup_data_dir_note()}</dd>
      <dt>{m.hosts_setup_version()}</dt><dd>{agent.version ? fmtVersion(agent.version) : '—'}</dd>
      <dt>{m.hosts_setup_layout()}</dt><dd>{online ? (agent.layout === 'managed' ? m.hosts_setup_layout_managed() : m.hosts_setup_layout_legacy()) : '—'}</dd>
      <dt>{m.hosts_setup_user()}</dt><dd>{agent.run_user || '—'}</dd>
    </dl>
  </section>
</div>

<style>
  .st { display: flex; flex-direction: column; gap: 14px; max-width: 900px; }
  .sec { border: 1px solid var(--border); border-radius: var(--r); padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; }
  .sec h2 { margin: 0; font-size: var(--fs-md, 14px); font-weight: 600; }
  .hrow { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
  .sub { margin: 0; color: var(--muted); font-size: var(--fs-sm); }
  .docs { color: var(--accent); font-size: var(--fs-xs); text-decoration: none; }
  .docs:hover { text-decoration: underline; }
  .kv { display: grid; grid-template-columns: 140px 1fr; gap: 6px 12px; margin: 0; font-size: var(--fs-sm); }
  .kv dt { color: var(--muted); }
  .kv dd { margin: 0; }
  .tags { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .body { display: flex; flex-direction: column; gap: 14px; }
  .body.off .field { opacity: 0.55; }
  .field { display: flex; flex-direction: column; gap: 6px; }
  .lbl { font-size: var(--fs-xs); color: var(--muted); }
  .dm-input[aria-invalid='true'] { border-color: var(--bad); }
  .result { min-height: 18px; font-size: var(--fs-sm); display: flex; flex-direction: column; gap: 2px; }
  .okline { display: inline-flex; align-items: center; gap: 6px; }
  .okline :global(svg) { color: var(--ok); }
  .err { color: var(--bad); }
  .wrn { color: var(--warn); }
  .dim { color: var(--muted); display: inline-flex; align-items: center; gap: 6px; }
  .hint { color: var(--subtle, var(--muted)); font-size: var(--fs-xs); }
  .row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .callout { display: flex; gap: 10px; align-items: flex-start; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--r); font-size: var(--fs-sm); }
  .callout > :global(svg) { flex-shrink: 0; margin-top: 2px; }
  .callout.info { border-color: color-mix(in srgb, var(--accent) 35%, transparent); background: var(--accent-soft); }
  .callout.info > :global(svg) { color: var(--accent); }
  .callout.ok { border-color: color-mix(in srgb, var(--ok) 40%, transparent); background: var(--ok-bg); }
  .callout.ok > :global(svg) { color: var(--ok); }
  .callout.bad { border-color: color-mix(in srgb, var(--bad) 40%, transparent); background: var(--bad-bg); }
  .callout.bad > :global(svg) { color: var(--bad); }
  .cbody { display: flex; flex-direction: column; gap: 6px; flex: 1; min-width: 0; }
  .cmd { display: flex; gap: 8px; align-items: flex-start; }
  .cmd code { flex: 1; min-width: 0; font-size: 12px; white-space: pre-wrap; word-break: break-all; }
  .steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; font-size: var(--fs-sm); }
  .steps li { display: flex; align-items: center; gap: 8px; color: var(--muted); }
  .steps li.d { color: var(--fg); }
  .steps li.d :global(svg) { color: var(--ok); }
  .steps li.r { color: var(--fg); }
  .steps .dot { width: 12px; height: 12px; display: inline-block; border-radius: 999px; border: 1px solid var(--border-strong); }
  .earlier { display: flex; flex-direction: column; gap: 2px; }
  .soon { opacity: 0.5; cursor: not-allowed; }
  :global(.spin) { animation: st-spin 1s linear infinite; }
  @keyframes st-spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { :global(.spin) { animation: none; } }
  @media (max-width: 560px) { .kv { grid-template-columns: 1fr; } }
</style>
