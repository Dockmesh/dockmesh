<script lang="ts">
  // Deploy-Drawer — side panel from the right for deploying a template.
  // Shows: Stack name + Host picker + Parameter form + Preview compose.
  // Port-parameter inputs trigger a port-conflict check against the
  // target host's running containers (smart heuristic from the legacy
  // code — drop would be a regression).
  import { api, ApiError, type StackTemplate, type StackTemplateParam } from '$lib/api';
  import { goto } from '$app/navigation';
  import { hosts } from '$lib/stores/host.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { Eyebrow, Field } from '$lib/components/editorial';
  import { X, Rocket, AlertCircle, ChevronRight } from 'lucide-svelte';

  interface Props {
    open: boolean;
    template: StackTemplate | null;
  }
  let { open = $bindable(false), template }: Props = $props();

  let stackName = $state('');
  let targetHost = $state('local');
  let values = $state<Record<string, string>>({});
  let busy = $state(false);
  let err = $state<string | null>(null);
  let showPreview = $state(false);

  // Port-conflict heuristic: when the drawer opens, fetch running
  // containers on the target host and keep their host-published ports
  // in a Set. Parameters whose name contains "port" warn if the value
  // matches an already-bound port.
  let usedPorts = $state<Set<number>>(new Set());

  function isPortParam(p: StackTemplateParam): boolean {
    return /port/i.test(p.name);
  }
  async function refreshUsedPorts(hostId: string) {
    usedPorts = new Set();
    try {
      const res: any = await api.containers.list(false, hostId);
      const list: any[] = Array.isArray(res) ? res : (res?.items ?? []);
      const s = new Set<number>();
      for (const c of list) {
        for (const p of (c.Ports ?? c.ports ?? [])) {
          const v = Number(p?.PublicPort ?? p?.public ?? p?.host);
          if (Number.isFinite(v) && v > 0) s.add(v);
        }
      }
      usedPorts = s;
    } catch {
      /* best-effort */
    }
  }

  // Hydrate the form when the drawer opens with a new template.
  let lastOpen = false;
  let lastTemplateId: number | null = null;
  $effect(() => {
    if (open && template && (!lastOpen || lastTemplateId !== template.id)) {
      stackName = template.slug;
      targetHost = hosts.id && hosts.id !== 'all' ? hosts.id : 'local';
      values = {};
      for (const p of template.parameters ?? []) {
        values[p.name] = p.default ?? '';
      }
      err = null;
      showPreview = false;
      refreshUsedPorts(targetHost);
      lastTemplateId = template.id;
    }
    lastOpen = open;
  });

  // Re-check ports when the host changes.
  let lastHost = '';
  $effect(() => {
    if (open && targetHost && targetHost !== lastHost) {
      lastHost = targetHost;
      refreshUsedPorts(targetHost);
    }
  });

  function onKeydown(e: KeyboardEvent) {
    if (open && e.key === 'Escape' && !busy) close();
  }

  function close() {
    if (busy) return;
    open = false;
  }

  async function doDeploy() {
    if (!template || !stackName.trim()) return;
    busy = true;
    err = null;
    try {
      const res = await api.templates.deploy(template.id, {
        stack_name: stackName.trim(),
        host_id: targetHost || undefined,
        values,
      });
      toast.success('Stack deployed', res.stack);
      open = false;
      goto(`/stacks/${encodeURIComponent(res.stack)}${targetHost && targetHost !== 'local' ? `?host=${encodeURIComponent(targetHost)}` : ''}`);
    } catch (e) {
      err = e instanceof ApiError ? e.message : 'deploy failed';
    } finally {
      busy = false;
    }
  }

  function paramInputType(p: StackTemplateParam): string {
    if (p.secret) return 'password';
    if (p.type === 'number') return 'number';
    return 'text';
  }

  // Render a compose preview by substituting {{param}} / ${param}
  // placeholders with current values. Best-effort — operators read
  // this as a sanity-check, not a definitive plan.
  function previewCompose(t: StackTemplate, vals: Record<string, string>): string {
    let out = t.compose;
    for (const [k, v] of Object.entries(vals)) {
      const value = v || `<${k}>`;
      // {{name}} and {{name|default:x}} variants
      out = out.replace(new RegExp(`\\{\\{\\s*${k}\\s*(\\|[^}]*)?\\s*\\}\\}`, 'g'), value);
      // ${name} variant
      out = out.replace(new RegExp(`\\$\\{${k}\\}`, 'g'), value);
    }
    return out;
  }

  const preview = $derived(template ? previewCompose(template, values) : '');

  // Available remote hosts (excluding the synthetic "all" entry).
  const hostOptions = $derived(hosts.available.filter((h) => h.kind !== 'all'));
</script>

<svelte:window onkeydown={onKeydown} />

{#if open && template}
  <div class="dd-scrim" onclick={close} role="presentation"></div>
  <aside class="dd-drawer" role="dialog" aria-label="Deploy template">
    <header class="dd-head">
      <div class="dd-head-icon">
        {#if template.icon_url}
          <img src={template.icon_url} alt="" />
        {:else}
          📦
        {/if}
      </div>
      <div class="dd-head-text">
        <Eyebrow>Deploy template</Eyebrow>
        <h2 class="ed-title dd-head-title">{template.name}</h2>
        {#if template.description}
          <p class="ed-subtitle dd-head-blurb">{template.description}</p>
        {/if}
      </div>
      <button
        type="button"
        class="dd-close"
        onclick={close}
        aria-label="Close"
        disabled={busy}
      >
        <X size={14} strokeWidth={1.5} />
      </button>
    </header>

    <form id="dd-form" class="dd-body" onsubmit={(e) => { e.preventDefault(); doDeploy(); }}>
      <div class="dd-grid-2">
        <Field label="Stack name" hint="Lowercase, dashes. Must be unique on the host.">
          <input
            class="dm-input dd-input"
            bind:value={stackName}
            placeholder="my-app"
          />
        </Field>
        <Field label="Target host" hint="Where the stack will run.">
          <select class="dm-input dd-input" bind:value={targetHost}>
            <option value="local">local (central daemon)</option>
            {#each hostOptions as h (h.id)}
              {#if h.id !== 'local'}
                <option value={h.id} disabled={h.status !== 'online'}>
                  {h.name}{h.status !== 'online' ? ` (${h.status})` : ''}
                </option>
              {/if}
            {/each}
          </select>
        </Field>
      </div>

      {#if template.parameters && template.parameters.length > 0}
        <div class="dd-section-head">Parameters</div>
        <div class="dd-params">
          {#each template.parameters as p (p.name)}
            {@const portConflict = isPortParam(p) && usedPorts.has(Number(values[p.name]))}
            <Field
              label={p.name}
              hint={portConflict
                ? `Port ${values[p.name]} is already bound on this host.`
                : (p.description || (p.secret ? 'Secret — auto-generated if blank.' : ''))}
            >
              {#snippet right()}
                {#if p.required}
                  <span class="dd-required">required</span>
                {/if}
              {/snippet}
              {#if p.enum && p.enum.length > 0}
                <select class="dm-input dd-input" bind:value={values[p.name]}>
                  {#each p.enum as opt}
                    <option value={opt}>{opt}</option>
                  {/each}
                </select>
              {:else}
                <input
                  type={paramInputType(p)}
                  class="dm-input dd-input"
                  bind:value={values[p.name]}
                  placeholder={p.default ?? ''}
                  pattern={p.pattern ?? undefined}
                />
              {/if}
              {#if portConflict}
                <div class="dd-port-warn">
                  <AlertCircle size={11} strokeWidth={1.5} />
                  <span>Will collide with an existing container's port.</span>
                </div>
              {/if}
            </Field>
          {/each}
        </div>
      {:else}
        <div class="dd-no-params">No parameters — this template deploys as-is.</div>
      {/if}

      <button
        type="button"
        class="dd-preview-toggle"
        onclick={() => (showPreview = !showPreview)}
        aria-expanded={showPreview}
      >
        <ChevronRight size={11} strokeWidth={1.5} class="dd-preview-chev" data-open={showPreview} />
        {showPreview ? 'Hide' : 'Preview'} compose.yaml
      </button>
      {#if showPreview}
        <pre class="dd-preview">{preview}</pre>
      {/if}

      {#if err}
        <div class="dd-error" role="alert">
          <AlertCircle size={12} strokeWidth={1.6} />
          <span>{err}</span>
        </div>
      {/if}
    </form>

    <footer class="dd-foot">
      <span class="dd-foot-meta">
        will create <em>{stackName || '—'}</em> on <em>{targetHost}</em>
      </span>
      <div class="dd-foot-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={close} disabled={busy}>
          Cancel
        </button>
        <button
          type="submit"
          form="dd-form"
          class="dm-btn dm-btn-primary dm-btn-sm"
          disabled={busy || !stackName.trim()}
        >
          <Rocket size={12} strokeWidth={1.5} />
          {busy ? 'Deploying…' : 'Deploy'}
        </button>
      </div>
    </footer>
  </aside>
{/if}

<style>
  .dd-scrim {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.55);
    z-index: 99;
    animation: dd-fade 150ms ease-out;
  }
  @keyframes dd-fade {
    from { opacity: 0; }
    to { opacity: 1; }
  }

  .dd-drawer {
    position: fixed;
    top: 0;
    right: 0;
    height: 100vh;
    width: min(560px, 92vw);
    background: var(--bg);
    border-left: 1px solid var(--border-strong);
    z-index: 100;
    display: flex;
    flex-direction: column;
    animation: dd-slide 180ms cubic-bezier(.2,.7,.3,1);
  }
  @keyframes dd-slide {
    from { transform: translateX(20px); opacity: 0.5; }
    to { transform: translateX(0); opacity: 1; }
  }

  .dd-head {
    padding: 22px 24px 16px;
    display: grid;
    grid-template-columns: 44px 1fr 28px;
    gap: 12px;
    align-items: flex-start;
    border-bottom: 1px solid var(--border-subtle);
  }
  .dd-head-icon {
    width: 44px;
    height: 44px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 24px;
    line-height: 1;
    overflow: hidden;
  }
  .dd-head-icon img {
    width: 32px;
    height: 32px;
    object-fit: contain;
  }
  .dd-head-text { min-width: 0; }
  .dd-head-title {
    font-size: 22px;
    line-height: 1.15;
    margin-top: 6px;
  }
  .dd-head-blurb {
    margin-top: 6px;
    max-width: 60ch;
    font-size: 12.5px;
  }
  .dd-close {
    width: 28px;
    height: 28px;
    background: transparent;
    border: 1px solid var(--border);
    color: var(--fg-subtle);
    border-radius: 4px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .dd-close:hover {
    color: var(--fg);
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }

  .dd-body {
    flex: 1;
    overflow-y: auto;
    padding: 18px 24px 12px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .dd-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .dd-input {
    font-family: var(--font-mono);
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .dd-section-head {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    margin-top: 8px;
  }
  .dd-params {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .dd-required {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--color-warning-400);
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .dd-no-params {
    padding: 10px 12px;
    border: 1px dashed var(--border);
    border-radius: 4px;
    font-size: 12px;
    color: var(--fg-subtle);
    text-align: center;
  }
  .dd-port-warn {
    margin-top: 4px;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--color-warning-400);
  }

  .dd-preview-toggle {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    background: transparent;
    border: 0;
    padding: 4px 0;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    align-self: flex-start;
    margin-top: 4px;
  }
  .dd-preview-toggle:hover { color: var(--fg); }
  .dd-preview-toggle :global(.dd-preview-chev) {
    transition: transform 120ms;
  }
  .dd-preview-toggle :global(.dd-preview-chev[data-open="true"]) {
    transform: rotate(90deg);
  }
  .dd-preview {
    padding: 12px 14px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.55;
    color: var(--fg-muted);
    overflow-x: auto;
    white-space: pre;
    max-height: 320px;
    overflow-y: auto;
  }

  .dd-error {
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--color-danger-400);
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .dd-foot {
    padding: 14px 24px;
    border-top: 1px solid var(--border-subtle);
    background: var(--surface);
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .dd-foot-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .dd-foot-meta em {
    color: var(--fg);
    font-style: normal;
  }
  .dd-foot-actions {
    display: flex;
    gap: 8px;
  }

  @media (max-width: 640px) {
    .dd-grid-2 { grid-template-columns: 1fr; }
  }
</style>
