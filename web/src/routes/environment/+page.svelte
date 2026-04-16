<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { Card, Badge, Button, EmptyState, Skeleton, Modal, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Variable, Plus, Trash2, Search, RefreshCw, FolderOpen } from 'lucide-svelte';

  interface EnvVar {
    id: number;
    key: string;
    value: string;
    group_name: string;
    encrypted: boolean;
    created_at: string;
    updated_at: string;
  }

  let vars = $state<EnvVar[]>([]);
  let loading = $state(true);
  let search = $state('');
  let groupFilter = $state('');

  // Modal
  let showModal = $state(false);
  let editingVar = $state<EnvVar | null>(null);
  let form = $state({ key: '', value: '', group_name: '' });
  let saving = $state(false);

  async function load() {
    loading = true;
    try {
      vars = await api.globalEnv.list();
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  $effect(() => { load(); });

  // Derived
  const groups = $derived([...new Set(vars.filter(v => v.group_name).map(v => v.group_name))].sort());

  const visible = $derived(
    vars.filter(v => {
      if (groupFilter && v.group_name !== groupFilter) return false;
      if (!search.trim()) return true;
      const q = search.toLowerCase();
      return v.key.toLowerCase().includes(q) || v.value.toLowerCase().includes(q) || v.group_name.toLowerCase().includes(q);
    })
  );

  function openNew() {
    editingVar = null;
    form = { key: '', value: '', group_name: '' };
    showModal = true;
  }

  function openEdit(v: EnvVar) {
    editingVar = v;
    form = { key: v.key, value: v.value, group_name: v.group_name };
    showModal = true;
  }

  async function save(e: Event) {
    e.preventDefault();
    saving = true;
    try {
      if (editingVar) {
        await api.globalEnv.update(editingVar.id, form);
      } else {
        await api.globalEnv.create(form);
      }
      showModal = false;
      toast.success(editingVar ? 'Updated' : 'Created');
      await load();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      saving = false;
    }
  }

  async function deleteVar(v: EnvVar) {
    if (!confirm(`Delete "${v.key}"?`)) return;
    try {
      await api.globalEnv.delete(v.id);
      toast.success('Deleted', v.key);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<section class="space-y-4">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Environment</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Global variables injected into every stack deploy. Stack-level .env values override globals.
      </p>
    </div>
    <div class="flex items-center gap-2">
      <Button variant="primary" size="sm" onclick={openNew}>
        <Plus class="w-3.5 h-3.5" /> Add variable
      </Button>
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
    </div>
  </div>

  <!-- Search + group filter -->
  {#if vars.length > 0}
    <div class="flex flex-wrap items-center gap-3">
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input type="search" placeholder="Search key, value, group…" bind:value={search} class="dm-input pl-8 pr-3 py-1.5 text-sm w-full" />
      </div>
      {#if groups.length > 0}
        <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={groupFilter}>
          <option value="">All groups</option>
          {#each groups as g}
            <option value={g}>{g}</option>
          {/each}
        </select>
      {/if}
    </div>
  {/if}

  <!-- Table -->
  {#if loading && vars.length === 0}
    <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
  {:else if vars.length === 0}
    <Card>
      <EmptyState
        icon={Variable}
        title="No global variables"
        description="Add variables like TZ, PUID, database credentials — they'll be injected into every stack deploy."
      >
        {#snippet action()}
          <Button variant="primary" onclick={openNew}>
            <Plus class="w-4 h-4" /> Add first variable
          </Button>
        {/snippet}
      </EmptyState>
    </Card>
  {:else if visible.length === 0}
    <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No variables match this filter.</Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              <th class="text-left px-5 py-3">Key</th>
              <th class="text-left px-3 py-3">Value</th>
              <th class="text-left px-3 py-3">Group</th>
              <th class="text-right px-3 py-3 w-24">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visible as v (v.id)}
              <tr class="hover:bg-[var(--surface-hover)]">
                <td class="px-5 py-3 font-mono text-sm font-medium">{v.key}</td>
                <td class="px-3 py-3">
                  {#if v.encrypted}
                    <span class="text-xs text-[var(--fg-subtle)] italic">encrypted</span>
                  {:else}
                    <span class="font-mono text-xs text-[var(--fg-muted)] truncate block max-w-[300px]" title={v.value}>{v.value}</span>
                  {/if}
                </td>
                <td class="px-3 py-3">
                  {#if v.group_name}
                    <Badge variant="info">{v.group_name}</Badge>
                  {:else}
                    <span class="text-xs text-[var(--fg-subtle)]">—</span>
                  {/if}
                </td>
                <td class="px-3 py-3">
                  <div class="flex gap-0.5 justify-end">
                    <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Edit" onclick={() => openEdit(v)}>
                      <Variable class="w-3.5 h-3.5" />
                    </button>
                    <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteVar(v)}>
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

<!-- Create/Edit modal -->
<Modal bind:open={showModal} title={editingVar ? `Edit ${editingVar.key}` : 'Add variable'} maxWidth="max-w-md">
  <form onsubmit={save} id="env-form" class="space-y-3">
    <Input label="Key" placeholder="MY_VARIABLE" bind:value={form.key} disabled={saving || editingVar !== null} hint="Uppercase with underscores recommended" />
    <div>
      <label for="env-value" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Value</label>
      <textarea id="env-value" class="dm-input font-mono text-xs h-24 resize-y" bind:value={form.value} disabled={saving} placeholder="value or multi-line content"></textarea>
    </div>
    <Input label="Group (optional)" placeholder="database, smtp, common" bind:value={form.group_name} disabled={saving} hint="Organize variables into categories" />
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showModal = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="env-form" loading={saving} disabled={saving || !form.key.trim()}>
      {editingVar ? 'Update' : 'Create'}
    </Button>
  {/snippet}
</Modal>
