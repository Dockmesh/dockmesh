<script lang="ts">
  // Container Registries — extracted from Settings to a first-class
  // sidebar entry. Registries are feature-adjacent to Stacks / Containers
  // (you configure them specifically to enable private-image pulls at
  // deploy time), so burying them in a Settings tab hid a commonly-needed
  // admin surface.
  import { api, ApiError, type Registry, type RegistryInput } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Plus, Trash2, UserCog, Package, X, CheckCircle2, XCircle } from 'lucide-svelte';

  let registries = $state<Registry[]>([]);
  let registriesLoading = $state(false);
  let showRegistry = $state(false);
  let editingRegistry = $state<Registry | null>(null);
  let registryForm = $state<RegistryInput>({ name: '', url: '', username: '', password: '', scope_tags: [] });
  let registryScopeInput = $state('');
  let registryBusy = $state(false);
  let testingRegistryId = $state<number | null>(null);

  async function loadRegistries() {
    registriesLoading = true;
    try {
      registries = await api.registries.list();
    } catch (err) {
      toast.error('Failed to load registries', err instanceof ApiError ? err.message : undefined);
    } finally {
      registriesLoading = false;
    }
  }

  function openNewRegistry() {
    editingRegistry = null;
    registryForm = { name: '', url: '', username: '', password: '', scope_tags: [] };
    registryScopeInput = '';
    showRegistry = true;
  }

  function openEditRegistry(r: Registry) {
    editingRegistry = r;
    registryForm = {
      name: r.name,
      url: r.url,
      username: r.username ?? '',
      password: '',
      scope_tags: r.scope_tags ? [...r.scope_tags] : []
    };
    registryScopeInput = '';
    showRegistry = true;
  }

  function addRegistryScope() {
    const t = registryScopeInput.trim().toLowerCase();
    if (!t) return;
    registryForm.scope_tags = Array.from(new Set([...(registryForm.scope_tags ?? []), t]));
    registryScopeInput = '';
  }

  function removeRegistryScope(t: string) {
    registryForm.scope_tags = (registryForm.scope_tags ?? []).filter((x) => x !== t);
  }

  async function saveRegistry(e: Event) {
    e.preventDefault();
    if (!registryForm.name.trim() || !registryForm.url.trim()) return;
    registryBusy = true;
    try {
      const payload: RegistryInput = {
        name: registryForm.name.trim(),
        url: registryForm.url.trim(),
        username: registryForm.username?.trim() || undefined,
        password: registryForm.password || undefined,
        scope_tags: registryForm.scope_tags?.length ? registryForm.scope_tags : undefined
      };
      if (editingRegistry) {
        await api.registries.update(editingRegistry.id, payload);
        toast.success('Registry updated', registryForm.name);
      } else {
        await api.registries.create(payload);
        toast.success('Registry added', registryForm.name);
      }
      showRegistry = false;
      await loadRegistries();
    } catch (err) {
      toast.error('Failed to save registry', err instanceof ApiError ? err.message : undefined);
    } finally {
      registryBusy = false;
    }
  }

  async function deleteRegistry(r: Registry) {
    if (!(await confirm.ask({ title: 'Delete registry', message: `Delete registry "${r.name}"?`, body: 'Existing pulls fall back to anonymous access. Private images will fail to pull until the registry is re-added.', confirmLabel: 'Delete', danger: true }))) return;
    try {
      await api.registries.delete(r.id);
      toast.success('Registry deleted', r.name);
      await loadRegistries();
    } catch (err) {
      toast.error('Failed to delete', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function testRegistry(r: Registry) {
    testingRegistryId = r.id;
    try {
      const res = await api.registries.test(r.id);
      if (res.ok) {
        toast.success('Login successful', r.name);
      } else {
        toast.error('Login failed', res.error || 'unknown error');
      }
      await loadRegistries();
    } catch (err) {
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      testingRegistryId = null;
    }
  }

  function fmtAgo(ts?: string): string {
    if (!ts) return 'never';
    const diff = (Date.now() - new Date(ts).getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  $effect(() => {
    if (allowed('user.manage')) loadRegistries();
  });
</script>

<section class="space-y-6">
  <div class="flex items-start justify-between gap-4">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Registries</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Save credentials for private registries once. dockmesh will apply them
        automatically when pulling images from the matching host.
      </p>
    </div>
    {#if allowed('user.manage')}
      <Button variant="primary" onclick={openNewRegistry}>
        <Plus class="w-3.5 h-3.5" />
        Add registry
      </Button>
    {/if}
  </div>

  {#if !allowed('user.manage')}
    <Card>
      <EmptyState icon={Package} title="Admin-only" description="Registry configuration requires user-manage permission." />
    </Card>
  {:else}
    <Card>
      {#if registriesLoading}
        <Skeleton class="h-24" />
      {:else if registries.length === 0}
        <EmptyState
          icon={Package}
          title="No registries configured"
          description="Add credentials for ghcr.io, registry.gitlab.com, Harbor, or any other private registry. dockmesh auto-applies them based on the image reference."
        />
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-xs uppercase tracking-wider text-[var(--fg-muted)] border-b border-[var(--border)]">
              <tr>
                <th class="text-left py-2 px-3 font-medium">Name</th>
                <th class="text-left py-2 px-3 font-medium">URL</th>
                <th class="text-left py-2 px-3 font-medium">Username</th>
                <th class="text-left py-2 px-3 font-medium">Scope</th>
                <th class="text-left py-2 px-3 font-medium">Last tested</th>
                <th class="w-28"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each registries as r (r.id)}
                <tr>
                  <td class="py-2 px-3 font-medium">{r.name}</td>
                  <td class="py-2 px-3 font-mono text-xs">{r.url}</td>
                  <td class="py-2 px-3 text-[var(--fg-muted)]">{r.username || '—'}</td>
                  <td class="py-2 px-3">
                    {#if r.scope_tags && r.scope_tags.length > 0}
                      <div class="flex flex-wrap gap-1">
                        {#each r.scope_tags as t}
                          <span class="text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">{t}</span>
                        {/each}
                      </div>
                    {:else}
                      <span class="text-xs text-[var(--fg-muted)]">all hosts</span>
                    {/if}
                  </td>
                  <td class="py-2 px-3 text-[var(--fg-muted)]">
                    {#if r.last_tested_at}
                      <span class="inline-flex items-center gap-1">
                        {#if r.last_test_ok}
                          <CheckCircle2 class="w-3.5 h-3.5 text-[var(--color-success-500)]" />
                        {:else}
                          <XCircle class="w-3.5 h-3.5 text-[var(--color-danger-500)]" />
                        {/if}
                        {fmtAgo(r.last_tested_at)}
                      </span>
                    {:else}
                      <span class="text-xs">never</span>
                    {/if}
                  </td>
                  <td class="py-2 px-3">
                    <div class="flex items-center gap-1 justify-end">
                      <button
                        class="px-2 py-1 text-xs rounded hover:bg-[var(--surface-hover)] text-[var(--fg-muted)] hover:text-[var(--fg)] disabled:opacity-50"
                        disabled={!r.has_password || testingRegistryId === r.id}
                        onclick={() => testRegistry(r)}
                        title={r.has_password ? 'Test login' : 'No password stored — edit first'}
                      >
                        {testingRegistryId === r.id ? '…' : 'Test'}
                      </button>
                      <button
                        class="p-1.5 rounded hover:bg-[var(--surface-hover)] text-[var(--fg-muted)] hover:text-[var(--fg)]"
                        onclick={() => openEditRegistry(r)}
                        title="Edit"
                        aria-label="Edit"
                      >
                        <UserCog class="w-3.5 h-3.5" />
                      </button>
                      <button
                        class="p-1.5 rounded hover:bg-[var(--surface-hover)] text-[var(--fg-muted)] hover:text-[var(--color-danger-400)]"
                        onclick={() => deleteRegistry(r)}
                        title="Delete"
                        aria-label="Delete"
                      >
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Card>

    <div class="text-xs text-[var(--fg-muted)] bg-[var(--surface)] rounded-md p-3 border border-[var(--border)]">
      <p class="font-medium text-[var(--fg)] mb-1">How it works</p>
      <p>
        When you pull an image like <code class="text-[11px] font-mono bg-[var(--bg)] px-1 rounded">ghcr.io/org/app:tag</code>,
        dockmesh looks up the matching registry (<code class="text-[11px] font-mono bg-[var(--bg)] px-1 rounded">ghcr.io</code>)
        and applies the stored credentials automatically. Currently applies to the central server's local pulls —
        remote-agent pulls with credentials are tracked as a follow-up.
      </p>
    </div>
  {/if}
</section>

<Modal bind:open={showRegistry} title={editingRegistry ? 'Edit registry' : 'Add registry'} maxWidth="max-w-md">
  <form onsubmit={saveRegistry} id="registry-form" class="space-y-4">
    <Input
      label="Name"
      placeholder="GitHub Container Registry"
      hint="A label shown in the registry list. Free form."
      bind:value={registryForm.name}
    />
    <Input
      label="URL"
      placeholder="ghcr.io"
      hint="Host only — scheme and trailing slashes are ignored."
      bind:value={registryForm.url}
    />
    <Input
      label="Username"
      placeholder="deploy-bot"
      bind:value={registryForm.username as any}
    />
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
        Password / Token
        {#if editingRegistry?.has_password}
          <span class="font-normal normal-case">— stored; leave blank to keep existing</span>
        {/if}
      </span>
      <input
        type="password"
        class="dm-input"
        placeholder={editingRegistry?.has_password ? '••••••••' : 'Personal access token or password'}
        bind:value={registryForm.password as any}
      />
      <p class="text-xs text-[var(--fg-muted)] mt-1">
        Stored encrypted at rest (age). Never returned via the API once saved.
      </p>
    </div>
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Scope (host tags)</span>
      <div class="flex gap-2">
        <input
          type="text"
          class="dm-input flex-1"
          placeholder="e.g. prod"
          bind:value={registryScopeInput}
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addRegistryScope(); } }}
        />
        <Button variant="secondary" onclick={addRegistryScope}>Add</Button>
      </div>
      {#if registryForm.scope_tags && registryForm.scope_tags.length > 0}
        <div class="flex flex-wrap gap-1 mt-2">
          {#each registryForm.scope_tags as t}
            <span class="text-[11px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)] inline-flex items-center gap-1">
              {t}
              <button type="button" onclick={() => removeRegistryScope(t)} aria-label="Remove"><X class="w-3 h-3" /></button>
            </span>
          {/each}
        </div>
      {/if}
      <p class="text-xs text-[var(--fg-muted)] mt-1">
        Leave empty to apply to all hosts. When set, only applies to pulls on hosts tagged with any of these.
      </p>
    </div>
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (showRegistry = false)}>Cancel</Button>
    <Button variant="primary" onclick={saveRegistry} disabled={registryBusy}>
      {registryBusy ? 'Saving…' : editingRegistry ? 'Save' : 'Add registry'}
    </Button>
  {/snippet}
</Modal>
