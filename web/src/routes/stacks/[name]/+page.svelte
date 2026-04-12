<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { onDestroy } from 'svelte';
  import { Button, Card, Badge, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { ChevronLeft, Play, Square, Save, Trash2, AlertTriangle, RefreshCw } from 'lucide-svelte';

  const name = $derived($page.params.name);

  let compose = $state('');
  let env = $state('');
  let services = $state<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>([]);
  let loading = $state(true);
  let busy = $state(false);

  let externalChange = $state<{ file: string; type: string } | null>(null);
  let dirty = $state(false);
  let ws: WebSocket | null = null;

  async function load() {
    loading = true;
    externalChange = null;
    dirty = false;
    try {
      const detail = await api.stacks.get(name);
      compose = detail.compose;
      env = detail.env ?? '';
      try {
        services = await api.stacks.status(name);
      } catch {
        services = [];
      }
    } catch (err) {
      toast.error('Load failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function connectEvents() {
    disconnectEvents();
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      ws = new WebSocket(`${proto}//${location.host}/api/v1/ws/events?ticket=${ticket}`);
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data);
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
        } catch { /* ignore */ }
      };
    } catch { /* ignore */ }
  }

  function disconnectEvents() {
    if (ws) { ws.close(); ws = null; }
  }

  onDestroy(disconnectEvents);

  async function refreshStatus() {
    try {
      services = await api.stacks.status(name);
    } catch { /* ignore */ }
  }

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
      const res = await api.stacks.deploy(name);
      toast.success('Deployed', `${res.services.length} service(s) running`);
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
      await api.stacks.stop(name);
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
      connectEvents();
    }
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
      {#if services.length > 0}
        <Badge variant={services.every((s) => s.state === 'running') ? 'success' : 'warning'} dot>
          {services.filter((s) => s.state === 'running').length}/{services.length} running
        </Badge>
      {/if}
    </div>
    <div class="flex gap-2 flex-wrap">
      <Button variant="primary" onclick={deploy} loading={busy} disabled={busy}>
        <Play class="w-4 h-4" />
        Deploy
      </Button>
      <Button variant="secondary" onclick={stop} disabled={busy}>
        <Square class="w-4 h-4" />
        Stop
      </Button>
      <Button variant="secondary" onclick={save} disabled={busy || !dirty}>
        <Save class="w-4 h-4" />
        Save
      </Button>
      <Button variant="danger" onclick={del} disabled={busy}>
        <Trash2 class="w-4 h-4" />
        Delete
      </Button>
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
          <a
            href={`/containers/${s.container_id}`}
            class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)] transition-colors"
          >
            <Badge variant={s.state === 'running' ? 'success' : 'default'} dot>{s.state}</Badge>
            <div class="min-w-0 flex-1">
              <div class="font-mono text-sm">{s.service}</div>
              <div class="text-xs text-[var(--fg-muted)] truncate">{s.image}</div>
            </div>
            <div class="text-xs text-[var(--fg-subtle)] text-right">{s.status}</div>
          </a>
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
