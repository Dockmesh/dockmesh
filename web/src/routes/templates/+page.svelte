<script lang="ts">
  // Stack templates: compose files with placeholders that become a form at
  // deploy time. Built-ins ship with Dockmesh; operators add their own.
  import {
    api, ApiError,
    type StackTemplate, type StackTemplateInput,
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, Modal, Segmented, Menu, ChipList, TagPill, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Package, Plus, Trash2, KeyRound } from 'lucide-svelte';
  import DeployDialog from './_DeployDialog.svelte';

  const canDeploy = $derived(allowed('stacks.deploy'));
  const canCreate = $derived(allowed('templates.create'));
  const canUpdate = $derived(allowed('templates.update'));
  const canDelete = $derived(allowed('templates.delete'));

  let templates = $state<StackTemplate[]>([]);
  let loading = $state(true);
  let search = $state('');
  let sortKey = $state<'name' | 'source'>('name');

  // Deploy drawer
  let deployOpen = $state(false);
  let deployTarget = $state<StackTemplate | null>(null);

  // Edit modal
  let editOpen = $state(false);
  let editingId = $state<number | null>(null);
  let editTab = $state<'compose' | 'parameters'>('compose');
  function blankTemplate(): StackTemplateInput {
    return {
      slug: '',
      name: '',
      description: '',
      compose: 'services:\n  app:\n    image: alpine:3.20\n    restart: unless-stopped\n',
      parameters: [],
    };
  }
  let editForm = $state<StackTemplateInput>(blankTemplate());
  let editBusy = $state(false);
  let slugTouched = $state(false);
  let editErr = $state<string | null>(null);

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

  // ── Derived ──────────────────────────────────────────────────────────
  const filtered = $derived.by(() => {
    let arr = [...templates];
    if (search.trim()) {
      const q = search.toLowerCase();
      arr = arr.filter((t) =>
        t.name.toLowerCase().includes(q) ||
        t.slug.toLowerCase().includes(q) ||
        (t.description ?? '').toLowerCase().includes(q),
      );
    }
    arr.sort((a, b) =>
      sortKey === 'source'
        ? Number(a.builtin) - Number(b.builtin) || a.name.localeCompare(b.name)
        : a.name.localeCompare(b.name),
    );
    return arr;
  });

  const counts = $derived({
    total: templates.length,
    builtin: templates.filter((t) => t.builtin).length,
    custom: templates.filter((t) => !t.builtin).length,
  });

  // Short image names (no registry, no tag) for the list; ${VAR} shows as "?".
  function extractImages(compose: string): string[] {
    if (!compose) return [];
    const out = new Set<string>();
    const re = /^\s*image:\s*([^\s#].*)$/gim;
    let m: RegExpExecArray | null;
    while ((m = re.exec(compose)) !== null) {
      let img = m[1].trim();
      const hashIdx = img.indexOf('#');
      if (hashIdx > 0) img = img.slice(0, hashIdx).trim();
      if ((img.startsWith('"') && img.endsWith('"')) || (img.startsWith("'") && img.endsWith("'"))) {
        img = img.slice(1, -1);
      }
      img = img.replace(/\$\{[^}]+\}/g, '?');
      const shortName = img.split(':')[0].split('/').pop() ?? img;
      out.add(shortName);
    }
    return [...out];
  }

  // ── Actions ──────────────────────────────────────────────────────────
  function openDeploy(t: StackTemplate) {
    deployTarget = t;
    deployOpen = true;
  }

  function openNewTemplate() {
    editingId = null;
    editForm = blankTemplate();
    editTab = 'compose';
    editErr = null;
    slugTouched = false;
    editOpen = true;
  }

  function openEditTemplate(t: StackTemplate) {
    editingId = t.id;
    editForm = {
      slug: t.slug,
      name: t.name,
      description: t.description ?? '',
      icon_url: t.icon_url,
      compose: t.compose,
      parameters: (t.parameters ?? []).map((p) => ({ ...p })),
    };
    editTab = 'compose';
    editErr = null;
    editOpen = true;
  }

  const COLS = '32px minmax(160px, 1fr) minmax(220px, 2fr) minmax(120px, 1fr) 90px 90px 130px';
  const PCOLS = '150px minmax(0, 1fr) minmax(0, 1fr) 70px 36px';

  // Mirrors internal/templates: {{name}}, {{name|default:x}}, {{name|secret}};
  // later occurrences add flags.
  interface Placeholder {
    name: string;
    def: string;
    secret: boolean;
  }
  function parsePlaceholders(compose: string): Placeholder[] {
    const byName = new Map<string, Placeholder>();
    for (const m of compose.matchAll(/\{\{\s*(.+?)\s*\}\}/g)) {
      const parts = m[1].split('|');
      const name = parts[0].trim();
      if (!name) continue;
      const ph = byName.get(name) ?? { name, def: '', secret: false };
      for (const raw of parts.slice(1)) {
        const flag = raw.trim();
        if (flag.startsWith('default:')) ph.def = flag.slice(8);
        else if (flag === 'secret') ph.secret = true;
      }
      byName.set(name, ph);
    }
    return [...byName.values()];
  }
  function paramNames(t: StackTemplate): string[] {
    return [...new Set([...parsePlaceholders(t.compose).map((p) => p.name), ...(t.parameters ?? []).map((p) => p.name)])];
  }

  interface ParamRow {
    name: string;
    label: string;
    def: string;
    composeDefault: string;
    secret: boolean;
    inCompose: boolean;
  }
  const paramRows = $derived.by<ParamRow[]>(() => {
    const found = parsePlaceholders(editForm.compose);
    const explicit = editForm.parameters ?? [];
    const rows: ParamRow[] = found.map((ph) => {
      const ex = explicit.find((p) => p.name === ph.name);
      return {
        name: ph.name,
        label: ex?.description ?? '',
        def: ex?.default ?? '',
        composeDefault: ph.def,
        secret: ph.secret || !!ex?.secret,
        inCompose: true,
      };
    });
    for (const ex of explicit) {
      if (!ex.name || rows.some((r) => r.name === ex.name)) continue;
      rows.push({ name: ex.name, label: ex.description ?? '', def: ex.default ?? '', composeDefault: '', secret: !!ex.secret, inCompose: false });
    }
    return rows;
  });

  function setParam(name: string, key: 'description' | 'default', value: string) {
    const list = [...(editForm.parameters ?? [])];
    const i = list.findIndex((p) => p.name === name);
    if (i >= 0) list[i] = { ...list[i], [key]: value };
    else list.push({ name, [key]: value });
    editForm.parameters = list.filter((p) => p.description || p.default || p.secret || p.type || p.enum || p.pattern || p.required);
  }
  function removeParam(name: string) {
    editForm.parameters = (editForm.parameters ?? []).filter((p) => p.name !== name);
  }
  function humanize(name: string): string {
    const t = name.replace(/[_-]+/g, ' ').trim();
    return t.charAt(0).toUpperCase() + t.slice(1);
  }
  function autoSlug() {
    if (editingId != null || slugTouched) return;
    editForm.slug = editForm.name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  }

  async function saveTemplate() {
    if (!editForm.slug.trim() || !editForm.name.trim() || !editForm.compose.trim()) return;
    editBusy = true;
    editErr = null;
    try {
      if (editingId == null) {
        await api.templates.create(editForm);
        toast.success('Template created', editForm.name);
      } else {
        await api.templates.update(editingId, editForm);
        toast.success('Template updated', editForm.name);
      }
      editOpen = false;
      await load();
    } catch (err) {
      editErr = err instanceof ApiError ? err.message : 'save failed';
    } finally {
      editBusy = false;
    }
  }

  async function deleteTemplate(t: StackTemplate) {
    if (!(await confirm.ask({
      title: `Delete template "${t.name}"?`,
      message: 'This removes the template from the library.',
      body: 'Already-deployed stacks are unaffected.',
      confirmLabel: 'Delete', danger: true,
    }))) return;
    try {
      await api.templates.delete(t.id);
      toast.success('Template deleted', t.name);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Templates</h1>
    <span class="dm-sub">
      {counts.total} template{counts.total === 1 ? '' : 's'} · {counts.builtin} built-in{counts.custom > 0 ? ` · ${counts.custom} custom` : ''}
    </span>
    <span class="dm-sp"></span>
    <input type="search" class="dm-filter tp-search" placeholder="Filter templates…" aria-label="Filter templates" bind:value={search} />
    {#if canCreate}
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNewTemplate}>
        <Plus size={14} strokeWidth={2} /> New template
      </button>
    {/if}
  </div>

  {#if loading && templates.length === 0}
    <div class="dm-tbl">
      {#each Array(6) as _, i (i)}
        <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div>
      {/each}
    </div>
  {:else if templates.length === 0}
    <EmptyState icon={Package} title="No templates yet" description="Built-in templates are added on first start. Add your own from a compose file.">
      {#snippet action()}
        {#if canCreate}
          <button type="button" class="dm-btn dm-btn-primary" onclick={openNewTemplate}><Plus size={14} strokeWidth={2} /> New template</button>
        {/if}
      {/snippet}
    </EmptyState>
  {:else}
    <div class="dm-tbl" role="table" aria-label="Templates">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
        <span role="columnheader"></span>
        <span role="columnheader"><button type="button" onclick={() => (sortKey = 'name')}>Name{sortKey === 'name' ? ' ↓' : ''}</button></span>
        <span role="columnheader">Description</span>
        <span role="columnheader">Images</span>
        <span role="columnheader">Parameters</span>
        <span role="columnheader"><button type="button" onclick={() => (sortKey = 'source')}>Source{sortKey === 'source' ? ' ↓' : ''}</button></span>
        <span role="columnheader"></span>
      </div>
      {#each filtered as t (t.id)}
        {@const images = extractImages(t.compose)}
        {@const nParams = paramNames(t).length}
        <div class="dm-tr" role="row" style="grid-template-columns: {COLS}">
          <span role="cell" class="tp-icon" aria-hidden="true">
            {#if t.icon_url}<img src={t.icon_url} alt="" />{:else}{t.name.slice(0, 1).toUpperCase()}{/if}
          </span>
          <span role="cell" class="dm-nm">
            <span class="dm-t" title={t.name}>{t.name}</span>
            <small class="dm-mono">{t.slug}{t.version ? ` · v${t.version}` : ''}</small>
          </span>
          <span role="cell" class="dm-dim" title={t.description ?? ''}>{t.description || '—'}</span>
          <span role="cell"><ChipList items={images} /></span>
          <span role="cell" class="dm-num dm-dim">{nParams} param{nParams === 1 ? '' : 's'}</span>
          <span role="cell"><TagPill label={t.builtin ? 'built-in' : 'custom'} /></span>
          <span role="cell" class="dm-tr-menu tp-actions">
            {#if canDeploy}
              <button type="button" class="dm-btn dm-btn-sm" onclick={() => openDeploy(t)}>Deploy</button>
            {/if}
            <Menu
              label="Actions for {t.name}"
              items={[
                { label: 'Download YAML', href: api.templates.exportURL(t.id) },
                ...(canUpdate && !t.builtin ? [{ label: 'Edit…', onclick: () => openEditTemplate(t) }] : []),
                ...(canDelete && !t.builtin ? [{ separator: true }, { label: 'Delete…', danger: true, onclick: () => deleteTemplate(t) }] : [])
              ]}
            />
          </span>
        </div>
      {:else}
        <div class="dm-empty">No templates match “{search}”.</div>
      {/each}
    </div>
  {/if}
</section>

<DeployDialog bind:open={deployOpen} template={deployTarget} />

<Modal bind:open={editOpen} title={editingId == null ? 'New template' : `Edit ${editForm.name}`} maxWidth="max-w-3xl" footer={editFooter}>
  <form id="tpl-edit-form" class="tp-form" onsubmit={(e) => { e.preventDefault(); saveTemplate(); }}>
    <div class="tp-grid3">
      <div class="dm-field">
        <label for="tp-name">Name</label>
        <input id="tp-name" class="dm-input" bind:value={editForm.name} oninput={autoSlug} placeholder="My app" required />
      </div>
      <div class="dm-field">
        <label for="tp-slug">Slug</label>
        <input id="tp-slug" class="dm-input dm-mono" bind:value={editForm.slug} oninput={() => (slugTouched = true)} pattern="^[a-z0-9][a-z0-9_-]*$" placeholder="my-app" required />
      </div>
      <div class="dm-field">
        <label for="tp-desc">Description</label>
        <input id="tp-desc" class="dm-input" bind:value={editForm.description} placeholder="One sentence shown in the list" />
      </div>
    </div>

    <Segmented
      label="Template editor"
      bind:value={editTab}
      options={[
        { key: 'compose', label: 'compose.yaml' },
        { key: 'parameters', label: 'Parameters', count: paramRows.length }
      ]}
    />

    {#if editTab === 'compose'}
      <textarea class="dm-editor" aria-label="compose.yaml" bind:value={editForm.compose} spellcheck="false" required></textarea>
      <span class="dm-sub">
        Placeholders <span class="dm-mono">{'{{name}}'}</span>, <span class="dm-mono">{'{{name|default:value}}'}</span> and
        <span class="dm-mono">{'{{name|secret}}'}</span> become fields in the deploy dialog. Secrets are generated when left empty.
      </span>
    {:else}
      {#if paramRows.length === 0}
        <EmptyState title="No placeholders yet" description={'Add {{name}} to the compose file to ask for a value at deploy time.'} />
      {:else}
        <div class="dm-tbl">
          <div class="dm-tr dm-th" style="grid-template-columns: {PCOLS}">
            <span>Placeholder</span><span>Label in the deploy dialog</span><span>Default</span><span>Secret</span><span></span>
          </div>
          {#each paramRows as r (r.name)}
            <div class="dm-tr" style="grid-template-columns: {PCOLS}">
              <span class="dm-mono" title={r.inCompose ? '' : 'Not used in compose.yaml'}>{r.name}{#if !r.inCompose}<span class="dm-dim"> · unused</span>{/if}</span>
              <input class="dm-input tp-cell" aria-label="Label for {r.name}" value={r.label} placeholder={humanize(r.name)} oninput={(e) => setParam(r.name, 'description', e.currentTarget.value)} />
              <input class="dm-input dm-mono tp-cell" aria-label="Default for {r.name}" value={r.def} placeholder={r.composeDefault || (r.secret ? 'generated' : '')} oninput={(e) => setParam(r.name, 'default', e.currentTarget.value)} />
              <span>{#if r.secret}<span class="tp-secret"><KeyRound size={12} /> yes</span>{:else}<span class="dm-dim">no</span>{/if}</span>
              <span class="dm-tr-menu">
                {#if !r.inCompose}
                  <button type="button" class="dm-menu-btn" aria-label="Remove {r.name}" title="Remove" onclick={() => removeParam(r.name)}><Trash2 size={13} /></button>
                {/if}
              </span>
            </div>
          {/each}
        </div>
        <span class="dm-sub">Found in the compose file; labels and defaults set here win.</span>
      {/if}
    {/if}

    {#if editErr}
      <p class="tp-err" role="alert">{editErr}</p>
    {/if}
  </form>
</Modal>

{#snippet editFooter()}
  <button type="button" class="dm-btn" onclick={() => (editOpen = false)}>Cancel</button>
  <button
    type="submit"
    form="tpl-edit-form"
    class="dm-btn dm-btn-primary"
    disabled={editBusy || !editForm.slug.trim() || !editForm.name.trim() || !editForm.compose.trim()}
  >
    {editBusy ? 'Saving…' : editingId == null ? 'Create template' : 'Save'}
  </button>
{/snippet}

<style>
  .tp-search { width: 220px; }
  .tp-icon {
    width: 28px;
    height: 28px;
    border-radius: 7px;
    background: var(--field);
    border: 1px solid var(--border);
    display: inline-flex !important;
    align-items: center;
    justify-content: center;
    font-size: 12px;
    font-weight: 600;
    overflow: hidden;
  }
  .tp-icon img { width: 18px; height: 18px; object-fit: contain; }
  .tp-actions { display: flex; align-items: center; justify-content: flex-end; gap: 4px; }
  .tp-form { display: flex; flex-direction: column; gap: 12px; }
  .tp-grid3 { display: grid; grid-template-columns: 1fr 1fr 1.4fr; gap: 10px; }
  .tp-cell { min-height: 30px; padding: 3px 8px; }
  .tp-secret { color: var(--warn); display: inline-flex; align-items: center; gap: 4px; }
  .tp-err { margin: 0; color: var(--bad); font-size: var(--fs-sm); }
  .tp-form :global(.dm-editor) { min-height: 240px; }
</style>
