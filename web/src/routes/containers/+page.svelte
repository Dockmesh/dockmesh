<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { goto } from '$app/navigation';
  import { onDestroy } from 'svelte';
  import { Card, Badge, EmptyState, Button, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { Box, Play, Square, RotateCw, Trash2, RefreshCw } from 'lucide-svelte';

  const canControl = $derived(allowed('container.control'));

  interface Container {
    Id: string;
    Names: string[];
    Image: string;
    State: string;
    Status: string;
    Ports: Array<{ PrivatePort: number; PublicPort?: number; Type: string }>;
    Labels?: Record<string, string>;
  }

  let containers = $state<Container[]>([]);
  let loading = $state(true);
  let showAll = $state(true);
  let live = $state(false);
  let ws: WebSocket | null = null;
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(load, 300);
  }

  async function connectEvents() {
    if (ws) return;
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      ws = new WebSocket(`${proto}//${location.host}/api/v1/ws/events?ticket=${ticket}`);
      ws.onopen = () => { live = true; };
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data);
          if (msg.source === 'docker' && msg.type === 'container') scheduleReload();
        } catch { /* ignore */ }
      };
      ws.onclose = () => { live = false; ws = null; };
      ws.onerror = () => { live = false; };
    } catch { /* ignore */ }
  }

  function disconnectEvents() {
    if (ws) { ws.close(); ws = null; }
    if (reloadTimer) clearTimeout(reloadTimer);
    live = false;
  }

  onDestroy(disconnectEvents);

  async function load() {
    loading = true;
    try {
      containers = await api.containers.list(showAll);
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function action(id: string, op: 'start' | 'stop' | 'restart' | 'remove') {
    try {
      if (op === 'start') await api.containers.start(id);
      else if (op === 'stop') await api.containers.stop(id);
      else if (op === 'restart') await api.containers.restart(id);
      else {
        if (!confirm('Remove this container?')) return;
        await api.containers.remove(id, true);
      }
      toast.success(op, id.slice(0, 12));
    } catch (err) {
      toast.error(`${op} failed`, err instanceof ApiError ? err.message : undefined);
    }
  }

  function portSummary(c: Container): string {
    if (!c.Ports) return '';
    const seen = new Set<string>();
    for (const p of c.Ports) if (p.PublicPort) seen.add(`${p.PublicPort}→${p.PrivatePort}/${p.Type}`);
    return [...seen].join(', ');
  }

  function stackOf(c: Container): string | null {
    return c.Labels?.['com.docker.compose.project'] ?? null;
  }

  $effect(() => {
    load();
    connectEvents();
    return disconnectEvents;
  });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <div class="flex items-center gap-2">
        <h2 class="text-2xl font-semibold tracking-tight">Containers</h2>
        {#if live}
          <Badge variant="success" dot>live</Badge>
        {/if}
      </div>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {containers.length} {containers.length === 1 ? 'container' : 'containers'}
      </p>
    </div>
    <div class="flex items-center gap-3">
      <label class="flex items-center gap-2 text-sm text-[var(--fg-muted)] cursor-pointer">
        <input type="checkbox" bind:checked={showAll} onchange={load} class="accent-[var(--color-brand-500)]" />
        show stopped
      </label>
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
        Refresh
      </Button>
    </div>
  </div>

  {#if loading && containers.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(4) as _}
          <div class="px-5 py-4 flex items-center gap-4">
            <Skeleton width="2rem" height="2rem" class="!rounded-full" />
            <div class="flex-1 space-y-1.5">
              <Skeleton width="40%" height="0.85rem" />
              <Skeleton width="60%" height="0.75rem" />
            </div>
          </div>
        {/each}
      </div>
    </Card>
  {:else if containers.length === 0}
    <Card>
      <EmptyState
        icon={Box}
        title="No containers"
        description="Deploy a stack or pull an image to get started."
      />
    </Card>
  {:else}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each containers as c}
          {@const running = c.State === 'running'}
          {@const stack = stackOf(c)}
          <div class="flex items-center gap-3 px-4 py-3 hover:bg-[var(--surface-hover)] transition-colors">
            <button
              class="flex items-center gap-3 flex-1 min-w-0 text-left py-1"
              onclick={() => goto(`/containers/${c.Id}`)}
            >
              <span class="w-2 h-2 rounded-full shrink-0 {running ? 'bg-[var(--color-success-500)]' : 'bg-[var(--fg-subtle)]'}"
                    style={running ? 'box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success-500) 20%, transparent);' : ''}></span>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="font-mono text-sm truncate">
                    {c.Names?.[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)}
                  </span>
                  {#if stack}
                    <Badge variant="info">{stack}</Badge>
                  {/if}
                </div>
                <div class="text-xs text-[var(--fg-muted)] truncate mt-0.5">
                  {c.Image} · {c.Status}{portSummary(c) ? ` · ${portSummary(c)}` : ''}
                </div>
              </div>
            </button>
            {#if canControl}
              <div class="flex gap-1 shrink-0">
                {#if running}
                  <Button size="xs" variant="ghost" onclick={() => action(c.Id, 'restart')} aria-label="Restart">
                    <RotateCw class="w-3.5 h-3.5" />
                  </Button>
                  <Button size="xs" variant="ghost" onclick={() => action(c.Id, 'stop')} aria-label="Stop">
                    <Square class="w-3.5 h-3.5" />
                  </Button>
                {:else}
                  <Button size="xs" variant="ghost" onclick={() => action(c.Id, 'start')} aria-label="Start">
                    <Play class="w-3.5 h-3.5" />
                  </Button>
                {/if}
                <Button size="xs" variant="ghost" onclick={() => action(c.Id, 'remove')} aria-label="Remove">
                  <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
                </Button>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    </Card>
  {/if}
</section>
