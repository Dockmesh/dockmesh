<script lang="ts">
  import { api, ApiError, type StackTemplate, type StackTemplateParam } from '$lib/api';
  import { goto } from '$app/navigation';
  import { hosts } from '$lib/stores/host.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { Field } from '$lib/components/editorial';
  import { Modal } from '$lib/components/ui';
  import { Rocket, AlertCircle, ChevronRight } from 'lucide-svelte';

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

  // Port parameters warn when the value is already published on the host.
  let usedPorts = $state<Set<number>>(new Set());
  let lastHost = '';

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
      lastHost = targetHost;
      refreshUsedPorts(targetHost);
      lastTemplateId = template.id;
    }
    lastOpen = open;
  });

  $effect(() => {
    if (open && targetHost && targetHost !== lastHost) {
      lastHost = targetHost;
      refreshUsedPorts(targetHost);
    }
  });

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

  // Compose with {{param}} / ${param} placeholders filled in.
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

  const hostOptions = $derived(hosts.available.filter((h) => h.kind !== 'all'));
</script>

<Modal bind:open title={template ? `Deploy ${template.name}` : 'Deploy template'} maxWidth="max-w-2xl" onclose={close}>
  {#if template}
    {#if template.description}<p class="dd-blurb">{template.description}</p>{/if}
    <form id="dd-form" class="dd-body" onsubmit={(e) => { e.preventDefault(); doDeploy(); }}>
      <div class="dd-grid-2">
        <Field label="Stack name" hint="Lowercase letters and dashes, unique on the host.">
          <input
            class="dm-input dd-input"
            bind:value={stackName}
            placeholder="my-app"
          />
        </Field>
        <Field label="Target host" hint="Where the stack will run.">
          <select class="dm-input dd-input" bind:value={targetHost}>
            <option value="local">Local</option>
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
                : (p.description || (p.secret ? 'Generated when left empty.' : ''))}
            >
              {#snippet right()}
                {#if p.required}
                  <span class="dd-required">Required</span>
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
        <div class="dd-no-params">This template has no parameters.</div>
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
  {/if}
  {#snippet footer()}
    <span class="dd-foot-meta">Creates <b>{stackName || '—'}</b> on <b>{hostOptions.find((h) => h.id === targetHost)?.name ?? targetHost}</b></span>
    <span class="dm-sp"></span>
    <button type="button" class="dm-btn dm-btn-ghost" onclick={close} disabled={busy}>Cancel</button>
    <button type="submit" form="dd-form" class="dm-btn dm-btn-primary" disabled={busy || !stackName.trim()}>
      <Rocket size={13} strokeWidth={1.6} />
      {busy ? 'Deploying…' : 'Deploy'}
    </button>
  {/snippet}
</Modal>

<style>
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
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-top: 8px;
  }
  .dd-params {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .dd-required {
    font-size: 11.5px;
    color: var(--color-warning-400);
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
    font-size: 11.5px;
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
    font-size: 11.5px;
    color: var(--color-danger-400);
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .dd-foot-meta {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .dd-foot-meta b {
    color: var(--fg);
    font-style: normal;
  }

  @media (max-width: 640px) {
    .dd-grid-2 { grid-template-columns: 1fr; }
  }
  .dd-blurb { margin: 0 0 14px; color: var(--muted); font-size: var(--fs-sm); }
  .dd-body { display: flex; flex-direction: column; gap: 14px; }
</style>
