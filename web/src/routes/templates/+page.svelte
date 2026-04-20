<script lang="ts">
  import { api, ApiError, type StackTemplate, type StackTemplateParam } from '$lib/api';
  import { Card, Button, Badge, Skeleton, EmptyState, Modal, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { goto } from '$app/navigation';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { Package, Rocket, Download, AlertCircle } from 'lucide-svelte';

  const canDeploy = $derived(allowed('stack.write'));

  let templates = $state<StackTemplate[]>([]);
  let loading = $state(true);
  let search = $state('');

  let deployOpen = $state(false);
  let deployBusy = $state(false);
  let selected = $state<StackTemplate | null>(null);
  let stackName = $state('');
  let targetHost = $state('local');
  let values = $state<Record<string, string>>({});
  let deployErr = $state<string | null>(null);
  // host-ports already bound on the target host — used to warn inline
  // when a port-shaped parameter conflicts with an existing container.
  let usedPorts = $state<Set<number>>(new Set());

  // Heuristic: parameters whose name contains "port" are treated as
  // host-port inputs. Close enough for the six built-in templates and
  // typical community templates.
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
        // Docker SDK shape: Ports: [{PrivatePort, PublicPort, IP, Type}]
        for (const p of (c.Ports ?? c.ports ?? [])) {
          const v = Number(p?.PublicPort ?? p?.public ?? p?.host);
          if (Number.isFinite(v) && v > 0) s.add(v);
        }
      }
      usedPorts = s;
    } catch {
      // Best effort — if we can't read the list, silently skip the warning.
    }
  }

  $effect(() => {
    if (deployOpen) void refreshUsedPorts(targetHost);
  });

  async function load() {
    loading = true;
    try {
      templates = await api.templates.list();
    } catch (err) {
      toast.error('Failed to load templates', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  $effect(() => { load(); });

  const filtered = $derived(
    templates.filter((t) => {
      if (!search.trim()) return true;
      const q = search.toLowerCase();
      return t.name.toLowerCase().includes(q) ||
             t.slug.toLowerCase().includes(q) ||
             (t.description ?? '').toLowerCase().includes(q);
    })
  );

  function openDeploy(t: StackTemplate) {
    selected = t;
    stackName = t.slug;
    targetHost = hosts.id && hosts.id !== 'all' ? hosts.id : 'local';
    deployErr = null;
    values = {};
    for (const p of t.parameters) {
      values[p.name] = p.default ?? '';
    }
    deployOpen = true;
  }

  async function doDeploy(e: Event) {
    e.preventDefault();
    if (!selected || !stackName.trim()) return;
    deployBusy = true;
    deployErr = null;
    try {
      const res = await api.templates.deploy(selected.id, {
        stack_name: stackName.trim(),
        host_id: targetHost || undefined,
        values
      });
      toast.success('Stack deployed', res.stack);
      deployOpen = false;
      goto(`/stacks/${encodeURIComponent(res.stack)}${targetHost && targetHost !== 'local' ? `?host=${encodeURIComponent(targetHost)}` : ''}`);
    } catch (err) {
      deployErr = err instanceof ApiError ? err.message : 'deploy failed';
    } finally {
      deployBusy = false;
    }
  }

  function paramInputType(p: StackTemplateParam): string {
    if (p.secret) return 'password';
    if (p.type === 'number') return 'number';
    return 'text';
  }
</script>

<section class="space-y-4">
  <div class="flex items-start justify-between gap-4 flex-wrap">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Stack templates</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Deploy common stacks with parameter-driven forms. Built-in templates ship with Dockmesh; you can add your own.
      </p>
    </div>
    <div class="flex items-center gap-2">
      <Input bind:value={search} placeholder="Search templates…" class="w-60" />
    </div>
  </div>

  {#if loading}
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {#each Array(6) as _}
        <Skeleton class="h-32" />
      {/each}
    </div>
  {:else if filtered.length === 0}
    <EmptyState
      icon={Package}
      title={search ? 'No matching templates' : 'No templates'}
      description="Templates let you ship reusable compose files with parameter forms. Built-in templates should seed on first boot."
    />
  {:else}
    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {#each filtered as t (t.id)}
        <Card class="p-4 flex flex-col gap-3">
          <div class="flex items-start gap-3">
            {#if t.icon_url}
              <img src={t.icon_url} alt="" class="w-10 h-10 rounded shrink-0 bg-[var(--bg-muted)]" />
            {:else}
              <div class="w-10 h-10 rounded bg-[var(--bg-muted)] flex items-center justify-center shrink-0">
                <Package class="w-5 h-5 text-[var(--fg-muted)]" />
              </div>
            {/if}
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 flex-wrap">
                <span class="font-medium text-sm truncate">{t.name}</span>
                {#if t.builtin}
                  <Badge variant="info">built-in</Badge>
                {/if}
              </div>
              <p class="text-xs text-[var(--fg-muted)] font-mono truncate">{t.slug}</p>
            </div>
          </div>
          {#if t.description}
            <p class="text-xs text-[var(--fg-muted)] line-clamp-3 flex-1">{t.description}</p>
          {/if}
          <div class="flex items-center justify-between gap-2 pt-1">
            <span class="text-[10px] text-[var(--fg-muted)]">
              {t.parameters.length} param{t.parameters.length === 1 ? '' : 's'}
              {#if t.version} · v{t.version}{/if}
            </span>
            <div class="flex items-center gap-1">
              <a
                href={api.templates.exportURL(t.id)}
                class="p-1.5 rounded hover:bg-[var(--bg-hover)] text-[var(--fg-muted)] hover:text-[var(--fg)]"
                title="Download YAML"
                aria-label="Download YAML"
              >
                <Download class="w-3.5 h-3.5" />
              </a>
              {#if canDeploy}
                <Button variant="primary" onclick={() => openDeploy(t)}>
                  <Rocket class="w-3.5 h-3.5" />
                  Deploy
                </Button>
              {/if}
            </div>
          </div>
        </Card>
      {/each}
    </div>
  {/if}
</section>

<!-- Deploy dialog -->
<Modal bind:open={deployOpen} title={selected ? `Deploy ${selected.name}` : 'Deploy'} maxWidth="max-w-lg">
  {#if selected}
    <form onsubmit={doDeploy} id="deploy-template-form" class="space-y-4">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="tpl-stack-name">Stack name</label>
        <input id="tpl-stack-name" class="dm-input" bind:value={stackName} placeholder="my-app" />
        <p class="text-xs text-[var(--fg-muted)] mt-1">Lowercase letters, numbers, hyphens. Must be unique.</p>
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="tpl-host">Host</label>
        <select id="tpl-host" class="dm-input" bind:value={targetHost}>
          <option value="local">Local (central daemon)</option>
          {#each hosts.available.filter((h) => h.id !== 'local' && h.kind !== 'all') as h}
            <option value={h.id} disabled={h.status !== 'online'}>
              {h.name}{h.status !== 'online' ? ` (${h.status})` : ''}
            </option>
          {/each}
        </select>
        <p class="text-xs text-[var(--fg-muted)] mt-1">Target host for the stack.</p>
      </div>
      {#if selected.parameters.length > 0}
        <div class="pt-2 border-t border-[var(--border)] space-y-3">
          <p class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Parameters</p>
          {#each selected.parameters as p}
            <div>
              <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="tpl-param-{p.name}">
                {p.name}
                {#if p.secret}<span class="font-normal normal-case">— auto-generated if blank</span>{/if}
                {#if p.required}<span class="text-[var(--color-danger-400)]">*</span>{/if}
              </label>
              {#if p.enum && p.enum.length > 0}
                <select id="tpl-param-{p.name}" class="dm-input" bind:value={values[p.name]}>
                  {#each p.enum as opt}
                    <option value={opt}>{opt}</option>
                  {/each}
                </select>
              {:else}
                <input
                  id="tpl-param-{p.name}"
                  type={paramInputType(p)}
                  class="dm-input"
                  bind:value={values[p.name]}
                  placeholder={p.default ?? ''}
                  pattern={p.pattern ?? undefined}
                />
              {/if}
              {#if isPortParam(p) && usedPorts.has(Number(values[p.name]))}
                <p class="text-xs text-[var(--color-warning-500)] mt-1">
                  <AlertCircle class="w-3.5 h-3.5 inline mr-1" />
                  Port {values[p.name]} is already bound on this host. Pick a different port or free the existing one first.
                </p>
              {:else if p.description}
                <p class="text-xs text-[var(--fg-muted)] mt-1">{p.description}</p>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
      {#if deployErr}
        <div class="p-3 text-xs rounded border border-[var(--color-danger-400)] text-[var(--color-danger-500)]">
          <AlertCircle class="w-4 h-4 inline mr-1" />
          {deployErr}
        </div>
      {/if}
    </form>
  {/if}
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (deployOpen = false)}>Cancel</Button>
    <Button variant="primary" onclick={doDeploy} disabled={deployBusy || !stackName.trim()}>
      <Rocket class="w-3.5 h-3.5" />
      {deployBusy ? 'Deploying…' : 'Deploy'}
    </Button>
  {/snippet}
</Modal>
