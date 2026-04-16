<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError, type ScaleCheck, type PreflightResult, type Migration } from '$lib/api';
  import { Button, Card, Badge, Skeleton, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream } from '$lib/events';
  import { ChevronLeft, Play, Square, Save, Trash2, AlertTriangle, RefreshCw, Server, Maximize2, ArrowRightLeft, CheckCircle2, XCircle, Loader2 } from 'lucide-svelte';

  const canWrite = $derived(allowed('stack.write'));
  const canDeploy = $derived(allowed('stack.deploy'));
  const isRemote = $derived(hosts.id !== 'local');

  const name = $derived($page.params.name);

  let compose = $state('');
  let env = $state('');
  let services = $state<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>([]);
  let loading = $state(true);
  let busy = $state(false);

  let externalChange = $state<{ file: string; type: string } | null>(null);
  let dirty = $state(false);

  // Scaling state (P.8)
  let replicaCounts = $state<Map<string, number>>(new Map());
  let showScale = $state(false);
  let scaleTarget = $state('');
  let scaleValue = $state(1);
  let scaleCheck = $state<ScaleCheck | null>(null);
  let scaleBusy = $state(false);
  let scaleForce = $state(false);

  async function loadReplicaCounts() {
    try {
      const list = await api.stacks.listScale(name, hosts.id);
      const m = new Map<string, number>();
      for (const e of list) m.set(e.service, e.replicas);
      replicaCounts = m;
    } catch { /* ignore — not critical */ }
  }

  async function openScale(service: string) {
    scaleTarget = service;
    scaleValue = replicaCounts.get(service) ?? 1;
    scaleCheck = null;
    scaleForce = false;
    showScale = true;
    try {
      scaleCheck = await api.stacks.getScale(name, service, hosts.id);
      if (scaleCheck) scaleValue = scaleCheck.current_replicas || 1;
    } catch { /* fail open */ }
  }

  async function doScale() {
    scaleBusy = true;
    try {
      const res = await api.stacks.scale(name, scaleTarget, scaleValue, scaleForce, hosts.id);
      toast.success('Scaled', `${scaleTarget}: ${res.previous} → ${res.current}`);
      showScale = false;
      await loadReplicaCounts();
      await refreshStatus();
    } catch (err: any) {
      // Check for stateful warning (409 with force_needed).
      if (err?.status === 409) {
        try {
          const body = await err.json?.() ?? err;
          if (body?.force_needed) {
            toast.warning(body.message ?? 'Stateful service warning — enable force to proceed');
            return;
          }
        } catch {}
      }
      toast.error('Scale failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      scaleBusy = false;
    }
  }

  // Migration state (P.9)
  let showMigrate = $state(false);
  let migrateTarget = $state('');
  let migratePreflight = $state<PreflightResult | null>(null);
  let migratePreflightLoading = $state(false);
  let migrateBusy = $state(false);
  let activeMigration = $state<Migration | null>(null);

  async function openMigrate() {
    migrateTarget = '';
    migratePreflight = null;
    showMigrate = true;
  }

  async function runPreflight() {
    if (!migrateTarget) return;
    migratePreflightLoading = true;
    migratePreflight = null;
    try {
      migratePreflight = await api.migrations.preflight(name, migrateTarget);
    } catch (err) {
      toast.error('Preflight failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      migratePreflightLoading = false;
    }
  }

  async function startMigration() {
    migrateBusy = true;
    try {
      const m = await api.migrations.initiate(name, migrateTarget);
      activeMigration = m;
      showMigrate = false;
      toast.success('Migration started', `${name} → ${migrateTarget}`);
      pollMigration(m.id);
    } catch (err) {
      toast.error('Migration failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      migrateBusy = false;
    }
  }

  function pollMigration(id: string) {
    const iv = setInterval(async () => {
      try {
        const m = await api.migrations.get(name, id);
        activeMigration = m;
        if (['completed', 'failed', 'rolled_back'].includes(m.status)) {
          clearInterval(iv);
          if (m.status === 'completed') {
            toast.success('Migration completed');
            await load();
          } else {
            toast.error('Migration ' + m.status, m.error_message);
          }
        }
      } catch {
        clearInterval(iv);
      }
    }, 3000);
  }

  const stream = new EventStream({
    onMessage: (msg) => {
      if (msg.source === 'stacks' && msg.name === name) {
        if (msg.type === 'modified') {
          externalChange = { file: msg.file ?? 'compose.yaml', type: msg.type };
        } else if (msg.type === 'removed' && !msg.file) {
          externalChange = { file: '', type: 'removed' };
        }
      }
      if (msg.source === 'docker' && msg.type === 'container') {
        // Container lifecycle events in our stack → reload status.
        refreshStatus();
      }
    }
  });

  async function load() {
    loading = true;
    externalChange = null;
    dirty = false;
    try {
      const detail = await api.stacks.get(name);
      compose = detail.compose;
      env = detail.env ?? '';
      try {
        services = await api.stacks.status(name, hosts.id);
      } catch {
        services = [];
      }
      await loadReplicaCounts();
    } catch (err) {
      toast.error('Load failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function refreshStatus() {
    try {
      services = await api.stacks.status(name, hosts.id);
    } catch { /* ignore */ }
  }

  // Re-load whenever the user picks a different host from the top bar.
  let prevHost = hosts.id;
  $effect(() => {
    const cur = hosts.id;
    if (cur !== prevHost) {
      prevHost = cur;
      refreshStatus();
    }
  });

  async function save() {
    busy = true;
    try {
      await api.stacks.update(name, compose, env || undefined);
      dirty = false;
      toast.success('Saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function deploy() {
    busy = true;
    try {
      const res = await api.stacks.deploy(name, hosts.id);
      toast.success('Deployed', `${res.services.length} service(s) on ${hosts.selected?.name ?? 'local'}`);
      await refreshStatus();
    } catch (err) {
      toast.error('Deploy failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function stop() {
    busy = true;
    try {
      await api.stacks.stop(name, hosts.id);
      services = [];
      toast.info('Stopped');
    } catch (err) {
      toast.error('Stop failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function del() {
    if (!confirm(`Delete stack "${name}"? This removes the compose file from disk.`)) return;
    busy = true;
    try {
      await api.stacks.delete(name);
      toast.success('Deleted', name);
      goto('/stacks');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
      busy = false;
    }
  }

  $effect(() => {
    if (name) {
      load();
      stream.start();
    }
    return stream.stop;
  });
</script>

<section class="space-y-5">
  <!-- Breadcrumb -->
  <a href="/stacks" class="inline-flex items-center gap-1 text-sm text-[var(--fg-muted)] hover:text-[var(--fg)]">
    <ChevronLeft class="w-4 h-4" />
    Stacks
  </a>

  <!-- Header + actions -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div class="flex items-center gap-3 min-w-0">
      <h2 class="text-2xl font-semibold tracking-tight truncate">{name}</h2>
      {#if isRemote}
        <span class="inline-flex items-center gap-1.5 text-xs px-2 py-0.5 rounded border border-[var(--color-brand-500)]/40 bg-[color-mix(in_srgb,var(--color-brand-500)_10%,transparent)] text-[var(--color-brand-400)]">
          <Server class="w-3 h-3" />
          {hosts.selected?.name}
        </span>
      {/if}
      {#if services.length > 0}
        <Badge variant={services.every((s) => s.state === 'running') ? 'success' : 'warning'} dot>
          {services.filter((s) => s.state === 'running').length}/{services.length} running
        </Badge>
      {/if}
    </div>
    <div class="flex gap-2 flex-wrap">
      {#if canDeploy}
        <Button variant="primary" onclick={deploy} loading={busy} disabled={busy}>
          <Play class="w-4 h-4" />
          Deploy
        </Button>
        {#if hosts.available.length > 1}
          <Button variant="secondary" onclick={openMigrate} disabled={busy}>
            <ArrowRightLeft class="w-4 h-4" />
            Migrate
          </Button>
        {/if}
        <Button variant="secondary" onclick={stop} disabled={busy}>
          <Square class="w-4 h-4" />
          Stop
        </Button>
      {/if}
      {#if canWrite}
        <Button variant="secondary" onclick={save} disabled={busy || !dirty}>
          <Save class="w-4 h-4" />
          Save
        </Button>
        <Button variant="danger" onclick={del} disabled={busy}>
          <Trash2 class="w-4 h-4" />
          Delete
        </Button>
      {/if}
    </div>
  </div>

  {#if externalChange}
    <div class="dm-card p-4 border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] flex items-start gap-3">
      <AlertTriangle class="w-5 h-5 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="flex-1 text-sm">
        <div class="font-medium text-[var(--color-warning-400)]">
          {externalChange.file || 'Stack directory'} was {externalChange.type} outside Dockmesh
        </div>
        {#if dirty}
          <div class="text-xs text-[var(--color-danger-400)] mt-1">
            You have unsaved edits — reloading will discard them.
          </div>
        {:else}
          <div class="text-xs text-[var(--fg-muted)] mt-1">
            Reload to pick up the external change.
          </div>
        {/if}
      </div>
      <div class="flex gap-2 shrink-0">
        <Button size="sm" variant="secondary" onclick={load}>
          <RefreshCw class="w-3.5 h-3.5" />
          Reload
        </Button>
        <Button size="sm" variant="ghost" onclick={() => (externalChange = null)}>Ignore</Button>
      </div>
    </div>
  {/if}

  <!-- Active migration banner -->
  {#if activeMigration && !activeMigration.completed_at}
    <Card class="p-4 border-[var(--color-brand-500)]/30">
      <div class="flex items-center gap-3">
        <Loader2 class="w-5 h-5 text-[var(--color-brand-400)] animate-spin shrink-0" />
        <div class="flex-1 min-w-0">
          <div class="text-sm font-medium">
            Migrating to {activeMigration.target_host_id}
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            Phase: {activeMigration.phase ?? activeMigration.status}
            {#if activeMigration.progress?.current_volume}
              — Volume {activeMigration.progress.volume_index}/{activeMigration.progress.volumes_total}: {activeMigration.progress.current_volume}
            {/if}
          </div>
        </div>
        <Badge variant="info" dot>{activeMigration.status}</Badge>
      </div>
    </Card>
  {/if}

  <!-- Services -->
  {#if loading}
    <Card class="p-5 space-y-3">
      <Skeleton width="30%" height="1rem" />
      <Skeleton width="100%" height="3rem" />
    </Card>
  {:else if services.length > 0}
    <Card>
      <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
        Services
      </div>
      <div class="divide-y divide-[var(--border)]">
        {#each services as s}
          {@const count = replicaCounts.get(s.service) ?? 0}
          <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)] transition-colors">
            <a href={`/containers/${s.container_id}`} class="flex items-center gap-3 flex-1 min-w-0">
              <Badge variant={s.state === 'running' ? 'success' : 'default'} dot>{s.state}</Badge>
              <div class="min-w-0 flex-1">
                <div class="font-mono text-sm flex items-center gap-1.5">
                  {s.service}
                  {#if count > 1}
                    <span class="text-[10px] font-semibold px-1.5 py-0.5 rounded-full bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)]">
                      x{count}
                    </span>
                  {/if}
                </div>
                <div class="text-xs text-[var(--fg-muted)] truncate">{s.image}</div>
              </div>
              <div class="text-xs text-[var(--fg-subtle)] text-right">{s.status}</div>
            </a>
            {#if canDeploy}
              <button
                class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)] shrink-0"
                title="Scale {s.service}"
                aria-label="Scale {s.service}"
                onclick={() => openScale(s.service)}
              >
                <Maximize2 class="w-3.5 h-3.5" />
              </button>
            {/if}
          </div>
        {/each}
      </div>
    </Card>
  {/if}

  <!-- Editor -->
  <Card>
    <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center justify-between">
      <span>compose.yaml</span>
      {#if dirty}<span class="text-[var(--color-warning-400)] normal-case">unsaved</span>{/if}
    </div>
    <textarea
      class="dm-input rounded-none border-0 border-t-0 font-mono text-xs h-96 resize-y"
      style="border: none; background: transparent;"
      bind:value={compose}
      oninput={() => (dirty = true)}
    ></textarea>
  </Card>

  <Card>
    <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
      .env (optional)
    </div>
    <textarea
      class="dm-input rounded-none border-0 font-mono text-xs h-24 resize-y"
      style="border: none; background: transparent;"
      bind:value={env}
      oninput={() => (dirty = true)}
      placeholder="KEY=value"
    ></textarea>
  </Card>
</section>

<!-- Scale modal -->
<Modal bind:open={showScale} title="Scale {scaleTarget}" maxWidth="max-w-sm">
  <div class="space-y-4">
    <div>
      <label for="scale-slider" class="block text-xs font-medium text-[var(--fg-muted)] mb-2">
        Replicas: <span class="text-[var(--fg)] font-bold text-lg">{scaleValue}</span>
      </label>
      <input
        id="scale-slider"
        type="range"
        min="0"
        max="10"
        step="1"
        bind:value={scaleValue}
        class="w-full accent-[var(--color-brand-500)]"
      />
      <div class="flex justify-between text-[10px] text-[var(--fg-subtle)] mt-1">
        <span>0</span><span>5</span><span>10</span>
      </div>
    </div>

    {#if scaleCheck?.has_container_name}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>This service has <code class="font-mono">container_name</code> set. Remove it in the compose file to allow scaling beyond 1.</div>
      </div>
    {/if}

    {#if scaleCheck?.has_hard_port}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>Hard-coded host port <code class="font-mono">{scaleCheck.hard_port_detail}</code>. Use a port range or remove the binding to scale beyond 1.</div>
      </div>
    {/if}

    {#if scaleCheck?.is_stateful && scaleValue > 1}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)]">
        <div class="flex items-start gap-2">
          <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            This service looks like a database (<strong>{scaleCheck.stateful_image}</strong>) with mounted volumes.
            Scaling may cause data corruption.
          </div>
        </div>
        <label class="flex items-center gap-2 mt-2 cursor-pointer">
          <input type="checkbox" bind:checked={scaleForce} class="rounded" />
          <span>I understand the risk — proceed anyway</span>
        </label>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showScale = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={scaleBusy}
      disabled={scaleBusy || (scaleValue > 1 && (scaleCheck?.has_container_name || scaleCheck?.has_hard_port)) || (scaleCheck?.is_stateful && scaleValue > 1 && !scaleForce)}
      onclick={doScale}
    >
      Scale to {scaleValue}
    </Button>
  {/snippet}
</Modal>

<!-- Migrate modal -->
<Modal bind:open={showMigrate} title="Migrate {name}" maxWidth="max-w-lg">
  <div class="space-y-4">
    <div>
      <label for="migrate-target" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Target host</label>
      <select
        id="migrate-target"
        class="dm-input text-sm"
        bind:value={migrateTarget}
        onchange={() => { migratePreflight = null; if (migrateTarget) runPreflight(); }}
      >
        <option value="">Select a host…</option>
        {#each hosts.available.filter(h => h.id !== hosts.id && h.id !== 'all') as h}
          <option value={h.id} disabled={h.status !== 'online'}>{h.name} {h.status !== 'online' ? `(${h.status})` : ''}</option>
        {/each}
      </select>
    </div>

    {#if migratePreflightLoading}
      <div class="flex items-center gap-2 text-sm text-[var(--fg-muted)]">
        <Loader2 class="w-4 h-4 animate-spin" /> Running pre-flight checks…
      </div>
    {/if}

    {#if migratePreflight}
      <div class="space-y-1.5">
        <div class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Pre-flight checks</div>
        {#each migratePreflight.checks as check}
          <div class="flex items-center gap-2 text-xs">
            {#if check.passed}
              <CheckCircle2 class="w-3.5 h-3.5 text-[var(--color-success-400)] shrink-0" />
            {:else}
              <XCircle class="w-3.5 h-3.5 text-[var(--color-danger-400)] shrink-0" />
            {/if}
            <span class="font-medium">{check.name.replace(/_/g, ' ')}</span>
            {#if check.detail}
              <span class="text-[var(--fg-muted)] truncate">{check.detail}</span>
            {/if}
          </div>
        {/each}
      </div>

      {#if !migratePreflight.passed}
        <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)]">
          Pre-flight checks failed. Fix the issues above before migrating.
        </div>
      {/if}
    {/if}

    <p class="text-xs text-[var(--fg-muted)]">
      The stack will be <strong>stopped</strong> on the current host during transfer (Safe Mode).
      Downtime depends on volume size.
    </p>
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showMigrate = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={migrateBusy}
      disabled={migrateBusy || !migrateTarget || migratePreflightLoading || (migratePreflight && !migratePreflight.passed)}
      onclick={startMigration}
    >
      <ArrowRightLeft class="w-4 h-4" />
      Start migration
    </Button>
  {/snippet}
</Modal>
