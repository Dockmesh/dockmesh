<script lang="ts">
  import { api, ApiError, isFanOut } from '$lib/api';
  import { goto } from '$app/navigation';
  import { Card, Badge, EmptyState, Button, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import { Box, Play, Square, RotateCw, Trash2, RefreshCw, Server, Layers, AlertTriangle } from 'lucide-svelte';

  const canControl = $derived(allowed('container.control'));
  const isRemote = $derived(hosts.id !== 'local' && hosts.id !== 'all');
  const isAll = $derived(hosts.isAll);

  interface Container {
    Id: string;
    Names: string[];
    Image: string;
    State: string;
    Status: string;
    Ports: Array<{ PrivatePort: number; PublicPort?: number; Type: string }>;
    Labels?: Record<string, string>;
    // Present only in all-mode — attached by the backend fan-out via
    // struct embedding so we can render a Host column and route detail
    // clicks to the correct host.
    host_id?: string;
    host_name?: string;
  }

  let containers = $state<Container[]>([]);
  let unreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let loading = $state(true);
  let showAll = $state(true);
  let connStatus = $state<ConnStatus>('connecting');
  const live = $derived(connStatus === 'live');
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(load, 300);
  }

  const stream = new EventStream({
    onMessage: (msg) => {
      if (msg.source === 'docker' && msg.type === 'container') scheduleReload();
    },
    onStatus: (s) => { connStatus = s; }
  });

  function disconnectEvents() {
    stream.stop();
    if (reloadTimer) clearTimeout(reloadTimer);
  }

  // Load handles both response shapes:
  //  - single-host → bare Container[] array
  //  - all-mode    → FanOutResponse { items: Container[], unreachable_hosts }
  // The isFanOut() narrow keeps the type checker happy without a cast.
  async function load() {
    loading = true;
    try {
      const res = await api.containers.list(showAll, hosts.id);
      if (isFanOut(res)) {
        containers = res.items as Container[];
        unreachable = res.unreachable_hosts;
      } else {
        containers = res as Container[];
        unreachable = [];
      }
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  // Re-load whenever the user picks a different host from the top bar.
  let prevHost = hosts.id;
  $effect(() => {
    const cur = hosts.id;
    if (cur !== prevHost) {
      prevHost = cur;
      load();
    }
  });

  // Action is host-specific even in all-mode: mutations always target
  // exactly one host. The row's own host_id (set by backend fan-out)
  // takes precedence over the global picker so clicking "stop" on a
  // container in the all-mode list stops it on its actual host, not
  // on whatever host=all would send.
  async function action(c: Container, op: 'start' | 'stop' | 'restart' | 'remove') {
    const targetHost = c.host_id ?? hosts.id;
    const id = c.Id;
    try {
      if (op === 'start') await api.containers.start(id, targetHost);
      else if (op === 'stop') await api.containers.stop(id, targetHost);
      else if (op === 'restart') await api.containers.restart(id, targetHost);
      else {
        if (!confirm('Remove this container?')) return;
        await api.containers.remove(id, true, targetHost);
      }
      toast.success(op, id.slice(0, 12));
      await load();
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
    stream.start();
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
        {:else if connStatus === 'reconnecting'}
          <Badge variant="warning" dot>reconnecting…</Badge>
        {/if}
      </div>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5 flex items-center gap-2">
        {#if isAll}
          <Layers class="w-3.5 h-3.5 text-[var(--color-brand-400)]" />
          <span>Aggregated across all online hosts</span>
          <span>·</span>
        {:else if isRemote}
          <Server class="w-3.5 h-3.5 text-[var(--color-brand-400)]" />
          <span>Showing remote host <span class="font-mono text-[var(--fg)]">{hosts.selected?.name}</span></span>
          <span>·</span>
        {/if}
        <span>{containers.length} {containers.length === 1 ? 'container' : 'containers'}</span>
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

  {#if unreachable.length > 0}
    <div class="dm-card p-3 flex items-start gap-2.5 border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
      <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="text-xs flex-1">
        <div class="font-medium text-[var(--color-warning-400)]">
          Partial results — {unreachable.length} host{unreachable.length === 1 ? '' : 's'} did not respond
        </div>
        <div class="text-[var(--fg-muted)] mt-0.5">
          {#each unreachable as u, i}<span class="font-mono">{u.host_name}</span>{#if u.reason} ({u.reason}){/if}{#if i < unreachable.length - 1}, {/if}{/each}
        </div>
      </div>
    </div>
  {/if}

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
          {@const rowHost = c.host_id ?? hosts.id}
          {@const detailHref = `/containers/${c.Id}${rowHost && rowHost !== 'local' ? '?host=' + rowHost : ''}`}
          <div class="flex items-center gap-3 px-4 py-3 hover:bg-[var(--surface-hover)] transition-colors">
            <button
              class="flex items-center gap-3 flex-1 min-w-0 text-left py-1"
              onclick={() => goto(detailHref)}
            >
              <span class="w-2 h-2 rounded-full shrink-0 {running ? 'bg-[var(--color-success-500)]' : 'bg-[var(--fg-subtle)]'}"
                    style={running ? 'box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success-500) 20%, transparent);' : ''}></span>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="font-mono text-sm truncate">
                    {c.Names?.[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)}
                  </span>
                  {#if stack}
                    <Badge variant="info">{stack}</Badge>
                  {/if}
                  {#if isAll && c.host_name}
                    <!-- Host pill only in all-mode — single-host view
                         already has the host name in the page header
                         so repeating it per row is noise. -->
                    <span class="inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)] font-mono">
                      <Server class="w-2.5 h-2.5" />
                      {c.host_name}
                    </span>
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
                  <Button size="xs" variant="ghost" onclick={() => action(c, 'restart')} aria-label="Restart">
                    <RotateCw class="w-3.5 h-3.5" />
                  </Button>
                  <Button size="xs" variant="ghost" onclick={() => action(c, 'stop')} aria-label="Stop">
                    <Square class="w-3.5 h-3.5" />
                  </Button>
                {:else}
                  <Button size="xs" variant="ghost" onclick={() => action(c, 'start')} aria-label="Start">
                    <Play class="w-3.5 h-3.5" />
                  </Button>
                {/if}
                <Button size="xs" variant="ghost" onclick={() => action(c, 'remove')} aria-label="Remove">
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
