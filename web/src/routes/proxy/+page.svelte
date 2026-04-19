<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import {
    Globe, Plus, Trash2, Power, PowerOff, RefreshCw, Lock, ShieldCheck,
    Search, ExternalLink, Pencil, ChevronUp, ChevronDown
  } from 'lucide-svelte';

  interface ProxyRoute {
    id: number;
    host: string;
    upstream: string;
    tls_mode: 'auto' | 'internal' | 'none';
    created_at?: string;
    updated_at?: string;
  }

  let status = $state<{ enabled: boolean; running: boolean; admin_ok: boolean; version?: string; container?: string } | null>(null);
  let routes = $state<ProxyRoute[]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let search = $state('');

  // Sort
  type SortKey = 'host' | 'upstream' | 'tls';
  let sortKey = $state<SortKey>('host');
  let sortAsc = $state(true);

  // Bulk
  let selected = $state<Set<number>>(new Set());
  let bulkBusy = $state(false);

  // Modal (shared for create + edit)
  let showModal = $state(false);
  let editingRoute = $state<ProxyRoute | null>(null);
  let formHost = $state('');
  let formUpstream = $state('');
  let formTls = $state<'auto' | 'internal' | 'none'>('auto');
  let saving = $state(false);

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

  $effect(() => {
    if (!allowed('user.manage')) { goto('/'); return; }
    load();
  });
  // Caddy container status + route certificate lifecycle change over
  // time — poll every 10s.
  $effect(() => autoRefresh(load, 10_000));

  // Filtering + sorting
  const visible = $derived(
    routes
      .filter(r => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        return r.host.toLowerCase().includes(q) || r.upstream.toLowerCase().includes(q);
      })
      .sort((a, b) => {
        let cmp = 0;
        switch (sortKey) {
          case 'host': cmp = a.host.localeCompare(b.host); break;
          case 'upstream': cmp = a.upstream.localeCompare(b.upstream); break;
          case 'tls': cmp = a.tls_mode.localeCompare(b.tls_mode); break;
        }
        return sortAsc ? cmp : -cmp;
      })
  );

  const allSelected = $derived(visible.length > 0 && visible.every(r => selected.has(r.id)));
  function toggleAll() {
    if (allSelected) { selected = new Set(); }
    else { selected = new Set(visible.map(r => r.id)); }
  }
  function toggleOne(id: number) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  function toggleSort(key: SortKey) {
    if (sortKey === key) { sortAsc = !sortAsc; }
    else { sortKey = key; sortAsc = true; }
  }

  // Actions
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
    if (!(await confirm.ask({ title: 'Stop Caddy proxy', message: 'Stop and remove the Caddy container?', body: 'All route configurations stay in the database. Start the proxy again to reapply them.', confirmLabel: 'Stop', danger: true }))) return;
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

  function openCreate() {
    editingRoute = null;
    formHost = '';
    formUpstream = '';
    formTls = 'auto';
    showModal = true;
  }

  function openEdit(r: ProxyRoute) {
    editingRoute = r;
    formHost = r.host;
    formUpstream = r.upstream;
    formTls = r.tls_mode;
    showModal = true;
  }

  async function saveRoute(e: Event) {
    e.preventDefault();
    saving = true;
    try {
      if (editingRoute) {
        await api.proxy.updateRoute(editingRoute.id, formUpstream.trim(), formTls);
        toast.success('Route updated', formHost);
      } else {
        await api.proxy.createRoute(formHost.trim(), formUpstream.trim(), formTls);
        toast.success('Route created', formHost);
      }
      showModal = false;
      await load();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      saving = false;
    }
  }

  async function deleteRoute(id: number, host: string) {
    if (!(await confirm.ask({ title: 'Remove proxy route', message: `Remove route "${host}"?`, body: 'Incoming requests to this hostname will start returning 404 on next Caddy reload.', confirmLabel: 'Remove', danger: true }))) return;
    try {
      await api.proxy.deleteRoute(id);
      toast.success('Removed', host);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function bulkDelete() {
    if (!(await confirm.ask({ title: 'Delete proxy routes', message: `Delete ${selected.size} route(s)?`, confirmLabel: 'Delete', danger: true }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const r of routes.filter(r => selected.has(r.id))) {
      try { await api.proxy.deleteRoute(r.id); ok++; } catch { fail++; }
    }
    toast.success(`Deleted: ${ok}${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await load();
  }

  function tlsBadgeVariant(m: string): 'success' | 'info' | 'default' {
    if (m === 'auto') return 'success';
    if (m === 'internal') return 'info';
    return 'default';
  }

  function tlsLabel(m: string): string {
    if (m === 'auto') return 'Let\'s Encrypt';
    if (m === 'internal') return 'Internal CA';
    return 'HTTP only';
  }

  function fmtDate(ts?: string): string {
    if (!ts) return '—';
    return new Date(ts).toLocaleDateString();
  }
</script>

<section class="space-y-4">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Reverse Proxy</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Embedded Caddy for automatic HTTPS · {routes.length} route{routes.length === 1 ? '' : 's'}
      </p>
    </div>
    <Button variant="secondary" size="sm" onclick={load}>
      <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
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
            Enable the reverse proxy in <a href="/settings?tab=system" class="text-[var(--color-brand-400)] hover:underline">Settings → System</a>
            to let Dockmesh manage a Caddy container for automatic HTTPS.
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
                {#if status.admin_ok}<Badge variant="info">admin API</Badge>{/if}
              {:else}
                <Badge variant="default">stopped</Badge>
              {/if}
            </h3>
            <div class="text-xs text-[var(--fg-muted)] mt-1 font-mono flex gap-3">
              {#if status.version}<span>{status.version}</span>{/if}
              <span>:80 / :443</span>
              <span>{routes.length} route{routes.length === 1 ? '' : 's'}</span>
              {#if status.container}<span>{status.container.slice(0, 12)}</span>{/if}
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

  <!-- Search + actions -->
  {#if routes.length > 0}
    <div class="flex flex-wrap items-center gap-3">
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input type="search" placeholder="Search host or upstream…" bind:value={search} class="dm-input pl-8 pr-3 py-1.5 text-sm w-full" />
      </div>
      {#if status?.enabled}
        <Button variant="primary" size="sm" onclick={openCreate}>
          <Plus class="w-3.5 h-3.5" /> New route
        </Button>
      {/if}
    </div>
  {:else if status?.enabled}
    <div class="flex justify-end">
      <Button variant="primary" size="sm" onclick={openCreate}>
        <Plus class="w-3.5 h-3.5" /> New route
      </Button>
    </div>
  {/if}

  <!-- Bulk action bar -->
  {#if selected.size > 0}
    <div class="flex items-center gap-3 px-4 py-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-sm font-medium">{selected.size} selected</span>
      <div class="flex gap-1.5 ml-auto">
        <Button size="xs" variant="danger" onclick={bulkDelete} disabled={bulkBusy}>
          <Trash2 class="w-3.5 h-3.5" /> Delete
        </Button>
        <button class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] ml-2" onclick={() => (selected = new Set())}>Clear</button>
      </div>
    </div>
  {/if}

  <!-- Routes table -->
  {#if loading && routes.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(3) as _}
          <div class="px-5 py-3.5 flex items-center gap-4">
            <Skeleton width="1rem" height="1rem" />
            <Skeleton width="30%" height="0.85rem" />
            <Skeleton width="20%" height="0.75rem" />
          </div>
        {/each}
      </div>
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
  {:else if visible.length === 0}
    <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No routes match this search.</Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              <th class="w-10 px-3 py-3">
                <input type="checkbox" checked={allSelected} onchange={toggleAll} class="accent-[var(--color-brand-500)]" />
              </th>
              {#snippet sortHeader(key: SortKey, label: string)}
                <th class="text-left px-3 py-3 cursor-pointer select-none hover:text-[var(--fg)]" onclick={() => toggleSort(key)}>
                  <span class="inline-flex items-center gap-1">
                    {label}
                    {#if sortKey === key}
                      {#if sortAsc}<ChevronUp class="w-3 h-3" />{:else}<ChevronDown class="w-3 h-3" />{/if}
                    {/if}
                  </span>
                </th>
              {/snippet}
              {@render sortHeader('host', 'Host')}
              {@render sortHeader('upstream', 'Upstream')}
              {@render sortHeader('tls', 'TLS')}
              <th class="text-left px-3 py-3">Created</th>
              <th class="text-right px-3 py-3 w-28">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visible as r (r.id)}
              <tr class="hover:bg-[var(--surface-hover)] transition-colors {selected.has(r.id) ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_5%,transparent)]' : ''}">
                <td class="w-10 px-3 py-2.5">
                  <input type="checkbox" checked={selected.has(r.id)} onchange={() => toggleOne(r.id)} class="accent-[var(--color-brand-500)]" />
                </td>
                <td class="px-3 py-2.5">
                  <div class="flex items-center gap-1.5">
                    <span class="font-mono text-sm">{r.host}</span>
                    <a href="https://{r.host}" target="_blank" rel="noopener" class="text-[var(--fg-subtle)] hover:text-[var(--color-brand-400)]" title="Open in browser">
                      <ExternalLink class="w-3 h-3" />
                    </a>
                  </div>
                </td>
                <td class="px-3 py-2.5 font-mono text-xs text-[var(--fg-muted)]">{r.upstream}</td>
                <td class="px-3 py-2.5">
                  <Badge variant={tlsBadgeVariant(r.tls_mode)}>
                    {#if r.tls_mode === 'auto'}<Lock class="w-2.5 h-2.5 inline mr-0.5" />{/if}
                    {tlsLabel(r.tls_mode)}
                  </Badge>
                </td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtDate(r.created_at)}</td>
                <td class="px-3 py-2.5">
                  <div class="flex gap-0.5 justify-end">
                    <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Edit route" onclick={() => openEdit(r)}>
                      <Pencil class="w-3.5 h-3.5" />
                    </button>
                    <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete route" onclick={() => deleteRoute(r.id, r.host)}>
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>

<!-- Create / Edit route modal -->
<Modal bind:open={showModal} title={editingRoute ? `Edit route: ${editingRoute.host}` : 'Create route'} maxWidth="max-w-md">
  <form onsubmit={saveRoute} class="space-y-4" id="route-form">
    <Input
      label="Host"
      placeholder="nextcloud.example.com"
      bind:value={formHost}
      hint="Public hostname Caddy should match"
      disabled={saving}
    />
    <Input
      label="Upstream"
      placeholder="127.0.0.1:8081 or container-name:80"
      bind:value={formUpstream}
      hint="host:port — Caddy connects over the Docker network or host network"
      disabled={saving}
    />
    <div>
      <label for="tls-select" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">TLS mode</label>
      <select id="tls-select" class="dm-input text-sm" bind:value={formTls} disabled={saving}>
        <option value="auto">Auto — Let's Encrypt (requires public DNS)</option>
        <option value="internal">Internal CA — self-signed by Caddy (for .local domains)</option>
        <option value="none">None — HTTP only (not recommended for production)</option>
      </select>
    </div>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showModal = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="route-form"
      loading={saving}
      disabled={saving || !formHost.trim() || !formUpstream.trim()}
    >
      {editingRoute ? 'Save' : 'Create'}
    </Button>
  {/snippet}
</Modal>
