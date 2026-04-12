<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Globe, Plus, Trash2, Power, PowerOff, RefreshCw, Lock, ShieldCheck } from 'lucide-svelte';

  interface ProxyRoute {
    id: number;
    host: string;
    upstream: string;
    tls_mode: 'auto' | 'internal' | 'none';
  }

  let status = $state<{ enabled: boolean; running: boolean; admin_ok: boolean; version?: string; container?: string } | null>(null);
  let routes = $state<ProxyRoute[]>([]);
  let loading = $state(true);
  let busy = $state(false);

  let showCreate = $state(false);
  let newHost = $state('');
  let newUpstream = $state('');
  let newTls = $state<'auto' | 'internal' | 'none'>('auto');
  let creating = $state(false);

  async function load() {
    loading = true;
    try {
      const [s, r] = await Promise.all([
        api.proxy.status().catch(() => null),
        api.proxy.listRoutes().catch(() => [])
      ]);
      status = s;
      routes = r;
    } finally {
      loading = false;
    }
  }

  async function enable() {
    busy = true;
    toast.info('Starting Caddy', 'pulling image if needed…');
    try {
      await api.proxy.enable();
      toast.success('Proxy enabled');
      await load();
    } catch (err) {
      toast.error('Enable failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function disable() {
    if (!confirm('Stop and remove the Caddy container? Existing routes stay in the DB.')) return;
    busy = true;
    try {
      await api.proxy.disable();
      toast.info('Proxy disabled');
      await load();
    } catch (err) {
      toast.error('Disable failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function createRoute(e: Event) {
    e.preventDefault();
    creating = true;
    try {
      await api.proxy.createRoute(newHost.trim(), newUpstream.trim(), newTls);
      toast.success('Route created', newHost);
      newHost = '';
      newUpstream = '';
      newTls = 'auto';
      showCreate = false;
      await load();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  async function deleteRoute(id: number, host: string) {
    if (!confirm(`Remove route "${host}"?`)) return;
    try {
      await api.proxy.deleteRoute(id);
      toast.success('Removed', host);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function tlsBadgeVariant(m: string): 'success' | 'info' | 'default' {
    if (m === 'auto') return 'success';
    if (m === 'internal') return 'info';
    return 'default';
  }

  function tlsLabel(m: string): string {
    if (m === 'auto') return 'auto-TLS';
    if (m === 'internal') return 'internal CA';
    return 'HTTP only';
  }

  $effect(() => { load(); });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Reverse proxy</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Caddy container managed by Dockmesh · opt-in · listens on :80 / :443
      </p>
    </div>
    <Button variant="secondary" size="sm" onclick={load}>
      <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
      Refresh
    </Button>
  </div>

  <!-- Status card -->
  <Card class="p-5">
    {#if loading}
      <Skeleton width="60%" height="1.25rem" />
    {:else if !status?.enabled}
      <div class="flex items-start gap-3">
        <div class="w-10 h-10 rounded-lg bg-[var(--surface)] border border-[var(--border)] flex items-center justify-center shrink-0">
          <PowerOff class="w-5 h-5 text-[var(--fg-muted)]" />
        </div>
        <div>
          <h3 class="font-semibold">Proxy disabled</h3>
          <p class="text-xs text-[var(--fg-muted)] mt-1 max-w-prose">
            Set <code class="font-mono text-[var(--fg)]">DOCKMESH_PROXY_ENABLED=true</code>
            in the environment to allow Dockmesh to manage a Caddy container.
            Restart the service afterwards.
          </p>
        </div>
      </div>
    {:else}
      <div class="flex items-start justify-between flex-wrap gap-4">
        <div class="flex items-start gap-3">
          <div class="w-10 h-10 rounded-lg {status.running ? 'bg-[color-mix(in_srgb,var(--color-success-500)_15%,transparent)] text-[var(--color-success-400)]' : 'bg-[var(--surface)] border border-[var(--border)] text-[var(--fg-muted)]'} flex items-center justify-center shrink-0">
            <Globe class="w-5 h-5" />
          </div>
          <div>
            <h3 class="font-semibold flex items-center gap-2">
              Caddy
              {#if status.running}
                <Badge variant="success" dot>running</Badge>
                {#if status.admin_ok}<Badge variant="info">admin API ok</Badge>{/if}
              {:else}
                <Badge variant="default">stopped</Badge>
              {/if}
            </h3>
            <div class="text-xs text-[var(--fg-muted)] mt-1 font-mono">
              {#if status.container}container: {status.container}{/if}
              {#if status.version}· {status.version}{/if}
            </div>
          </div>
        </div>
        <div class="flex gap-2">
          {#if status.running}
            <Button variant="secondary" size="sm" onclick={disable} loading={busy}>
              <PowerOff class="w-3.5 h-3.5" /> Stop
            </Button>
          {:else}
            <Button variant="primary" size="sm" onclick={enable} loading={busy}>
              <Power class="w-3.5 h-3.5" /> Start
            </Button>
          {/if}
        </div>
      </div>
    {/if}
  </Card>

  <!-- Routes -->
  <div class="flex items-center justify-between">
    <h3 class="font-semibold text-sm text-[var(--fg-muted)] uppercase tracking-wider">Routes</h3>
    {#if status?.enabled}
      <Button variant="primary" size="sm" onclick={() => (showCreate = true)}>
        <Plus class="w-3.5 h-3.5" /> New route
      </Button>
    {/if}
  </div>

  {#if loading}
    <Card>
      <Skeleton class="m-5" width="80%" height="1rem" />
    </Card>
  {:else if routes.length === 0}
    <Card>
      <EmptyState
        icon={Globe}
        title="No routes yet"
        description={status?.enabled
          ? 'Add a host → upstream mapping and Dockmesh will configure Caddy automatically.'
          : 'Enable the proxy first to start configuring reverse-proxy routes.'}
      />
    </Card>
  {:else}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each routes as r}
          <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)] transition-colors">
            <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
              {#if r.tls_mode === 'none'}
                <Globe class="w-5 h-5" />
              {:else if r.tls_mode === 'internal'}
                <ShieldCheck class="w-5 h-5" />
              {:else}
                <Lock class="w-5 h-5" />
              {/if}
            </div>
            <div class="flex-1 min-w-0">
              <div class="font-mono text-sm truncate flex items-center gap-2">
                {r.host}
                <Badge variant={tlsBadgeVariant(r.tls_mode)}>{tlsLabel(r.tls_mode)}</Badge>
              </div>
              <div class="text-xs text-[var(--fg-muted)] font-mono truncate">→ {r.upstream}</div>
            </div>
            <Button size="xs" variant="ghost" onclick={() => deleteRoute(r.id, r.host)} aria-label="Delete">
              <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
            </Button>
          </div>
        {/each}
      </div>
    </Card>
  {/if}
</section>

<Modal bind:open={showCreate} title="Create route" maxWidth="max-w-md">
  <form onsubmit={createRoute} class="space-y-4" id="create-route-form">
    <Input
      label="Host"
      placeholder="nextcloud.example.com"
      bind:value={newHost}
      hint="Public hostname Caddy should match"
    />
    <Input
      label="Upstream"
      placeholder="127.0.0.1:8081"
      bind:value={newUpstream}
      hint="host:port — Caddy connects over the host network"
    />
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">TLS mode</span>
      <select class="dm-input" bind:value={newTls}>
        <option value="auto">auto — Let's Encrypt (public DNS required)</option>
        <option value="internal">internal — Caddy internal CA (for .local)</option>
        <option value="none">none — HTTP only</option>
      </select>
    </div>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="create-route-form"
      loading={creating}
      disabled={creating || !newHost || !newUpstream}
    >
      Create
    </Button>
  {/snippet}
</Modal>
