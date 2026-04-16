<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { Card, Badge, Button, EmptyState, Skeleton, Modal, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Variable, Plus, Trash2, Search, RefreshCw, FolderOpen, BookTemplate, ChevronDown } from 'lucide-svelte';

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

  // Template categories
  const templates = [
    {
      group: 'System',
      icon: '⚙️',
      vars: [
        { key: 'TZ', value: 'Europe/Vienna', hint: 'Container timezone' },
        { key: 'PUID', value: '1000', hint: 'Process user ID for file permissions' },
        { key: 'PGID', value: '1000', hint: 'Process group ID for file permissions' },
        { key: 'UMASK', value: '022', hint: 'File creation mask' }
      ]
    },
    {
      group: 'Database',
      icon: '🗄️',
      vars: [
        { key: 'DB_HOST', value: '', hint: 'Database hostname' },
        { key: 'DB_PORT', value: '5432', hint: 'Database port' },
        { key: 'DB_USER', value: '', hint: 'Database username' },
        { key: 'DB_PASSWORD', value: '', hint: 'Database password' },
        { key: 'DB_NAME', value: '', hint: 'Database name' }
      ]
    },
    {
      group: 'SMTP',
      icon: '📧',
      vars: [
        { key: 'SMTP_HOST', value: '', hint: 'Mail server hostname' },
        { key: 'SMTP_PORT', value: '587', hint: 'Mail server port (587 for TLS)' },
        { key: 'SMTP_USER', value: '', hint: 'Mail server username' },
        { key: 'SMTP_PASSWORD', value: '', hint: 'Mail server password' },
        { key: 'SMTP_FROM', value: '', hint: 'Sender email address' }
      ]
    },
    {
      group: 'Proxy',
      icon: '🌐',
      vars: [
        { key: 'HTTP_PROXY', value: '', hint: 'HTTP proxy URL' },
        { key: 'HTTPS_PROXY', value: '', hint: 'HTTPS proxy URL' },
        { key: 'NO_PROXY', value: 'localhost,127.0.0.1', hint: 'Comma-separated bypass list' }
      ]
    }
  ];

  let showTemplates = $state(false);

  async function addFromTemplate(tpl: { key: string; value: string; hint: string }, group: string) {
    // Check if already exists
    if (vars.some(v => v.key === tpl.key)) {
      toast.warning('Already exists', `${tpl.key} is already defined`);
      return;
    }
    try {
      await api.globalEnv.create({ key: tpl.key, value: tpl.value, group_name: group.toLowerCase() });
      toast.success('Added', tpl.key);
      await load();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function addAllFromGroup(group: { group: string; vars: Array<{ key: string; value: string; hint: string }> }) {
    let added = 0;
    for (const tpl of group.vars) {
      if (vars.some(v => v.key === tpl.key)) continue;
      try {
        await api.globalEnv.create({ key: tpl.key, value: tpl.value, group_name: group.group.toLowerCase() });
        added++;
      } catch { /* skip duplicates */ }
    }
    if (added > 0) {
      toast.success(`Added ${added} variable(s)`, group.group);
      await load();
    } else {
      toast.info('All variables already exist');
    }
  }

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
      <div class="relative">
        <Button variant="secondary" size="sm" onclick={() => (showTemplates = !showTemplates)}>
          <BookTemplate class="w-3.5 h-3.5" /> Templates <ChevronDown class="w-3 h-3" />
        </Button>
        {#if showTemplates}
          <button class="fixed inset-0 z-30 cursor-default" aria-label="Close templates" onclick={() => (showTemplates = false)}></button>
          <div class="absolute right-0 top-full mt-1 z-40 w-80 bg-[var(--bg-elevated)] border border-[var(--border-strong)] rounded-lg shadow-2xl overflow-hidden">
            {#each templates as tplGroup}
              <div class="border-b border-[var(--border)] last:border-0">
                <div class="flex items-center justify-between px-3 py-2 bg-[var(--surface)]">
                  <span class="text-xs font-medium">{tplGroup.icon} {tplGroup.group}</span>
                  <button
                    class="text-[10px] text-[var(--color-brand-400)] hover:underline"
                    onclick={() => { addAllFromGroup(tplGroup); showTemplates = false; }}
                  >Add all</button>
                </div>
                {#each tplGroup.vars as tpl}
                  {@const exists = vars.some(v => v.key === tpl.key)}
                  <button
                    class="w-full text-left px-3 py-1.5 text-xs hover:bg-[var(--surface-hover)] flex items-center justify-between gap-2 disabled:opacity-40"
                    disabled={exists}
                    onclick={() => { addFromTemplate(tpl, tplGroup.group); showTemplates = false; }}
                  >
                    <div>
                      <code class="font-mono font-medium">{tpl.key}</code>
                      <span class="text-[var(--fg-muted)] ml-1">{tpl.hint}</span>
                    </div>
                    {#if exists}<span class="text-[var(--fg-subtle)] shrink-0">added</span>{/if}
                  </button>
                {/each}
              </div>
            {/each}
          </div>
        {/if}
      </div>
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
