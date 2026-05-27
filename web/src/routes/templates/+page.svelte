<script lang="ts">
  // Stack Templates — catalog of pre-baked compose stacks with parameter
  // forms. Built-ins ship with Dockmesh; operators can add their own.
  //
  // Slice 1: editorial card-grid + Deploy-Drawer + Editorial edit modal.
  // Categories/Featured/Popular wait for Backend additions (see
  // backend_slice_punch_list.md).
  import {
    api, ApiError,
    type StackTemplate, type StackTemplateInput,
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Package, Plus, Trash2, Edit2, Download, Rocket, Search, AlertCircle,
  } from 'lucide-svelte';
  import DeployDrawer from './_DeployDrawer.svelte';

  const canDeploy = $derived(allowed('stacks.create'));

  let templates = $state<StackTemplate[]>([]);
  let loading = $state(true);
  let search = $state('');
  let sortKey = $state<'name' | 'recent'>('name');

  // Deploy drawer
  let deployOpen = $state(false);
  let deployTarget = $state<StackTemplate | null>(null);

  // Edit modal
  let editOpen = $state(false);
  let editingId = $state<number | null>(null);
  let editTab = $state<'compose' | 'parameters'>('compose');
  let editForm = $state<StackTemplateInput>({
    slug: '',
    name: '',
    description: '',
    compose: 'services:\n  app:\n    image: alpine:3.20\n',
    parameters: [],
  });
  let editBusy = $state(false);
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
    if (sortKey === 'name') {
      arr.sort((a, b) => a.name.localeCompare(b.name));
    } else {
      // recent — best-effort using id descending (no updated_at in DTO today)
      arr.sort((a, b) => b.id - a.id);
    }
    return arr;
  });

  const counts = $derived({
    total: templates.length,
    builtin: templates.filter((t) => t.builtin).length,
    custom: templates.filter((t) => !t.builtin).length,
  });

  // Image extraction from compose — clientside regex. Captures the
  // first whitespace + image: line per service; deduplicates; collapses
  // ${VAR} substitutions to "?" so the pill stays short.
  function extractImages(compose: string): string[] {
    if (!compose) return [];
    const out = new Set<string>();
    const re = /^\s*image:\s*([^\s#].*)$/gim;
    let m: RegExpExecArray | null;
    while ((m = re.exec(compose)) !== null) {
      let img = m[1].trim();
      // Strip inline comments
      const hashIdx = img.indexOf('#');
      if (hashIdx > 0) img = img.slice(0, hashIdx).trim();
      // Strip quotes
      if ((img.startsWith('"') && img.endsWith('"')) || (img.startsWith("'") && img.endsWith("'"))) {
        img = img.slice(1, -1);
      }
      // Collapse ${VAR} for display
      img = img.replace(/\$\{[^}]+\}/g, '?');
      // Just the image-name without tag for the pill, and without
      // registry prefix
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
    editForm = {
      slug: '',
      name: '',
      description: '',
      compose: 'services:\n  app:\n    image: alpine:3.20\n    restart: unless-stopped\n',
      parameters: [],
    };
    editTab = 'compose';
    editErr = null;
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

  function addEditParam() {
    editForm.parameters = [...(editForm.parameters ?? []), { name: '', default: '' }];
  }
  function removeEditParam(i: number) {
    editForm.parameters = (editForm.parameters ?? []).filter((_, idx) => idx !== i);
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

<EditorialPage>
  <section class="tpl">
    <!-- ───────────────────────── Header ───────────────────────── -->
    <header class="tpl-header">
      <div class="tpl-header-text">
        <h1 class="ed-title tpl-title">Templates</h1>
        <p class="ed-subtitle tpl-subtitle">
          {#if templates.length > 0}
            {counts.total} template{counts.total === 1 ? '' : 's'} · {counts.builtin} built-in{counts.custom > 0 ? ` · ${counts.custom} custom` : ''}
          {:else}
            Pre-baked compose stacks. Pick one, fill in the blanks, choose a host.
          {/if}
        </p>
      </div>
      {#if canDeploy}
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-sm"
          onclick={openNewTemplate}
        >
          <Plus size={12} strokeWidth={1.5} /> New custom template
        </button>
      {/if}
    </header>

    <!-- ───────────────────── Toolbar ───────────────────── -->
    <div class="tpl-toolbar">
      <div class="tpl-search">
        <Search size={12} strokeWidth={1.5} class="tpl-search-icon" />
        <input
          type="search"
          placeholder="search name · slug · description…"
          bind:value={search}
          class="ed-underline-input tpl-search-input"
        />
      </div>

      <span class="tpl-spacer"></span>

      <div class="tpl-sort" role="tablist">
        {#each [
          { id: 'name'   as const, label: 'name' },
          { id: 'recent' as const, label: 'recent' },
        ] as opt}
          <button
            type="button"
            class="tpl-sort-pill"
            class:tpl-sort-active={sortKey === opt.id}
            onclick={() => (sortKey = opt.id)}
          >
            {opt.label}
          </button>
        {/each}
      </div>
    </div>

    <!-- ───────────────────── Body ───────────────────── -->
    {#if loading && templates.length === 0}
      <div class="tpl-grid">
        {#each Array(6) as _, i (i)}
          <Skeleton width="100%" height="11rem" />
        {/each}
      </div>
    {:else if templates.length === 0}
      <div class="tpl-empty">
        <Package size={20} strokeWidth={1.4} />
        <p class="tpl-empty-title">No templates yet</p>
        <p class="ed-subtitle tpl-empty-blurb">
          Built-in templates should seed on first boot. If you see this, the seed
          may have failed — restart Dockmesh or add a custom template.
        </p>
        {#if canDeploy}
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={openNewTemplate}>
            <Plus size={12} strokeWidth={1.5} /> New custom template
          </button>
        {/if}
      </div>
    {:else if filtered.length === 0}
      <div class="tpl-empty">
        <Eyebrow>no match</Eyebrow>
        <p class="tpl-empty-title">No templates match „{search}".</p>
      </div>
    {:else}
      <div class="tpl-grid">
        {#each filtered as t (t.id)}
          {@const images = extractImages(t.compose)}
          <article class="tpl-card">
            <div class="tpl-card-head">
              <div class="tpl-card-icon">
                {#if t.icon_url}
                  <img src={t.icon_url} alt="" />
                {:else}
                  <Package size={18} strokeWidth={1.4} />
                {/if}
              </div>
              <div class="tpl-card-titleblock">
                <div class="tpl-card-title-line">
                  <span class="tpl-card-name">{t.name}</span>
                </div>
                <span class="tpl-card-slug">{t.slug}</span>
              </div>
              <span class="tpl-source-badge" data-source={t.builtin ? 'builtin' : 'custom'}>
                {t.builtin ? 'built-in' : 'custom'}
              </span>
            </div>

            {#if t.description}
              <p class="tpl-card-blurb">{t.description}</p>
            {/if}

            {#if images.length > 0}
              <div class="tpl-card-images">
                {#each images.slice(0, 3) as img}
                  <span class="tpl-image-pill">{img}</span>
                {/each}
                {#if images.length > 3}
                  <span class="tpl-image-more">+{images.length - 3}</span>
                {/if}
              </div>
            {/if}

            <div class="tpl-card-foot">
              <span class="tpl-card-params">
                {t.parameters?.length ?? 0} param{(t.parameters?.length ?? 0) === 1 ? '' : 's'}
                {#if t.version} · v{t.version}{/if}
              </span>
              <div class="tpl-card-actions">
                <a
                  href={api.templates.exportURL(t.id)}
                  class="tpl-icon-btn"
                  title="Download YAML"
                  aria-label="Download YAML"
                >
                  <Download size={11} strokeWidth={1.5} />
                </a>
                {#if canDeploy && !t.builtin}
                  <button
                    type="button"
                    class="tpl-icon-btn"
                    onclick={() => openEditTemplate(t)}
                    title="Edit"
                    aria-label="Edit"
                  >
                    <Edit2 size={11} strokeWidth={1.5} />
                  </button>
                  <button
                    type="button"
                    class="tpl-icon-btn tpl-icon-danger"
                    onclick={() => deleteTemplate(t)}
                    title="Delete"
                    aria-label="Delete"
                  >
                    <Trash2 size={11} strokeWidth={1.5} />
                  </button>
                {/if}
                {#if canDeploy}
                  <button
                    type="button"
                    class="dm-btn dm-btn-primary dm-btn-xs tpl-deploy-btn"
                    onclick={() => openDeploy(t)}
                  >
                    <Rocket size={11} strokeWidth={1.5} /> Deploy
                  </button>
                {/if}
              </div>
            </div>
          </article>
        {/each}
      </div>
    {/if}
  </section>
</EditorialPage>

<DeployDrawer bind:open={deployOpen} template={deployTarget} />

<!-- ───────────────────────── Custom Template Edit Modal ───────────────────────── -->
<EditorialModal
  bind:open={editOpen}
  eyebrow={editingId == null ? 'New custom template' : 'Edit template'}
  width={720}
>
  {#snippet title()}
    {#if editingId == null}
      Create a <em class="ed-accent">custom</em> template
    {:else}
      Edit <em class="ed-accent">{editForm.name}</em>
    {/if}
  {/snippet}

  <form id="tpl-edit-form" class="tpl-edit" onsubmit={(e) => { e.preventDefault(); saveTemplate(); }}>
    <div class="tpl-edit-grid">
      <Field label="Slug" hint="Lowercase, dashes, underscores.">
        <input
          class="dm-input tpl-edit-input"
          bind:value={editForm.slug}
          pattern="^[a-z0-9][a-z0-9_-]*$"
          placeholder="my-app"
          required
        />
      </Field>
      <Field label="Display name">
        <input
          class="dm-input tpl-edit-input"
          bind:value={editForm.name}
          placeholder="My App"
          required
        />
      </Field>
    </div>

    <Field label="Description" hint="One sentence shown on the card.">
      <input
        class="dm-input tpl-edit-input"
        bind:value={editForm.description}
        placeholder="What this stack is for"
      />
    </Field>

    <div class="tpl-edit-tabs" role="tablist">
      {#each [
        { id: 'compose'    as const, label: 'compose.yaml' },
        { id: 'parameters' as const, label: 'parameters' },
      ] as t}
        <button
          type="button"
          class="tpl-edit-tab"
          class:tpl-edit-tab-active={editTab === t.id}
          onclick={() => (editTab = t.id)}
        >
          {t.label}
        </button>
      {/each}
    </div>

    {#if editTab === 'compose'}
      <Field
        label="compose.yaml"
        hint={'Placeholders {{param}} / {{param|default:x}} / {{param|secret}} get substituted at deploy time.'}
      >
        <textarea
          class="dm-input tpl-edit-textarea"
          rows="14"
          bind:value={editForm.compose}
          spellcheck="false"
          required
        ></textarea>
      </Field>
    {:else}
      <div class="tpl-edit-params">
        <div class="tpl-edit-params-head">
          <span class="tpl-edit-params-hint">
            Explicit parameter declarations (optional — placeholders are auto-discovered from compose).
          </span>
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs"
            onclick={addEditParam}
          >
            <Plus size={11} strokeWidth={1.5} /> Add parameter
          </button>
        </div>
        {#if (editForm.parameters ?? []).length === 0}
          <p class="tpl-edit-params-empty">
            No explicit parameter definitions. Any
            <code class="tpl-inline-code">{`{{name}}`}</code> in the compose will still be
            prompted on deploy.
          </p>
        {:else}
          <div class="tpl-edit-param-rows">
            {#each (editForm.parameters ?? []) as p, i (i)}
              <div class="tpl-edit-param-row">
                <input
                  class="dm-input tpl-edit-input"
                  placeholder="name"
                  bind:value={p.name}
                />
                <input
                  class="dm-input tpl-edit-input"
                  placeholder="default"
                  bind:value={p.default}
                />
                <input
                  class="dm-input tpl-edit-input tpl-edit-param-desc"
                  placeholder="description"
                  bind:value={p.description}
                />
                <button
                  type="button"
                  class="tpl-icon-btn tpl-icon-danger"
                  onclick={() => removeEditParam(i)}
                  aria-label="Remove parameter"
                >
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    {#if editErr}
      <div class="tpl-edit-error" role="alert">
        <AlertCircle size={12} strokeWidth={1.6} />
        <span>{editErr}</span>
      </div>
    {/if}
  </form>

  {#snippet footer()}
    <span></span>
    <div class="tpl-edit-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (editOpen = false)}>
        Cancel
      </button>
      <button
        type="submit"
        form="tpl-edit-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={editBusy || !editForm.slug.trim() || !editForm.name.trim() || !editForm.compose.trim()}
      >
        {editBusy ? 'Saving…' : editingId == null ? 'Create' : 'Save'}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .tpl {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .tpl-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .tpl-header-text { min-width: 0; max-width: 70ch; }
  .tpl-title {
    font-size: 28px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .tpl-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }

  /* ── Toolbar ────────────────────────────────────────────────── */
  .tpl-toolbar {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .tpl-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .tpl-search :global(.tpl-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .tpl-search-input {
    padding-left: 18px;
    font-size: 12.5px;
    font-family: var(--font-mono);
  }
  .tpl-spacer { flex: 1; }
  .tpl-sort {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }
  .tpl-sort-pill {
    padding: 4px 10px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    line-height: 1.2;
    color: var(--fg-subtle);
    background: transparent;
    border: 0;
    border-right: 1px solid var(--border);
    cursor: pointer;
  }
  .tpl-sort-pill:last-child { border-right: 0; }
  .tpl-sort-pill:hover { color: var(--fg); }
  .tpl-sort-active {
    background: var(--bg-elevated);
    color: var(--fg);
  }

  /* ── Grid ───────────────────────────────────────────────────── */
  .tpl-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 12px;
  }

  .tpl-card {
    padding: 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    transition: border-color 120ms;
  }
  .tpl-card:hover { border-color: var(--border-strong); }

  .tpl-card-head {
    display: grid;
    grid-template-columns: 40px 1fr auto;
    gap: 10px;
    align-items: center;
  }
  .tpl-card-icon {
    width: 40px;
    height: 40px;
    border: 1px solid var(--border);
    background: var(--bg);
    border-radius: 5px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--fg-subtle);
    overflow: hidden;
  }
  .tpl-card-icon img {
    width: 28px;
    height: 28px;
    object-fit: contain;
  }
  .tpl-card-titleblock { min-width: 0; }
  .tpl-card-title-line {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .tpl-card-name {
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tpl-card-slug {
    display: block;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tpl-source-badge {
    font-family: var(--font-mono);
    font-size: 9.5px;
    padding: 2px 6px;
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    flex-shrink: 0;
  }
  .tpl-source-badge[data-source="custom"] {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
  }

  .tpl-card-blurb {
    font-size: 12px;
    color: var(--fg-muted);
    line-height: 1.5;
    margin: 0;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .tpl-card-images {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .tpl-image-pill {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 1px 6px;
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 130px;
  }
  .tpl-image-more {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    align-self: center;
  }

  .tpl-card-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: auto;
    padding-top: 8px;
    border-top: 1px solid var(--border-subtle);
  }
  .tpl-card-params {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tpl-card-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .tpl-icon-btn {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 0;
    border-radius: 4px;
    color: var(--fg-muted);
    cursor: pointer;
    text-decoration: none;
  }
  .tpl-icon-btn:hover {
    color: var(--fg);
    background: var(--surface-hover);
  }
  .tpl-icon-danger { color: var(--color-danger-400); }
  .tpl-icon-danger:hover {
    color: var(--color-danger-400);
    background: color-mix(in srgb, var(--color-danger-500) 12%, transparent);
  }
  .tpl-deploy-btn {
    margin-left: 2px;
  }

  /* ── Empty ──────────────────────────────────────────────────── */
  .tpl-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    color: var(--fg-subtle);
  }
  .tpl-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .tpl-empty-blurb {
    margin: 4px 0 12px;
    max-width: 50ch;
  }

  /* ── Edit Modal ─────────────────────────────────────────────── */
  .tpl-edit {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .tpl-edit :global(.dm-input) {
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .tpl-edit-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .tpl-edit-input {
    font-family: var(--font-mono);
  }
  .tpl-edit-textarea {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.55;
    resize: vertical;
  }
  .tpl-edit-tabs {
    display: flex;
    border-bottom: 1px solid var(--border-subtle);
  }
  .tpl-edit-tab {
    padding: 8px 14px;
    background: transparent;
    border: 0;
    border-bottom: 2px solid transparent;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 11.5px;
    letter-spacing: 0.04em;
    cursor: pointer;
  }
  .tpl-edit-tab:hover { color: var(--fg); }
  .tpl-edit-tab-active {
    color: var(--fg);
    border-bottom-color: var(--accent);
  }

  .tpl-edit-params {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .tpl-edit-params-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    flex-wrap: wrap;
  }
  .tpl-edit-params-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .tpl-edit-params-empty {
    padding: 10px 12px;
    border: 1px dashed var(--border);
    border-radius: 4px;
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin: 0;
  }
  .tpl-edit-param-rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .tpl-edit-param-row {
    display: grid;
    grid-template-columns: 1fr 1fr 2fr 28px;
    gap: 6px;
    align-items: center;
  }

  .tpl-inline-code {
    font-family: var(--font-mono);
    font-size: 11px;
    background: var(--bg);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border-subtle);
  }

  .tpl-edit-error {
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    border-radius: 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--color-danger-400);
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .tpl-edit-actions {
    display: flex;
    gap: 8px;
  }

  @media (max-width: 720px) {
    .tpl-edit-grid { grid-template-columns: 1fr; }
    .tpl-edit-param-row {
      grid-template-columns: 1fr 28px;
      grid-auto-flow: row;
    }
    .tpl-edit-param-desc { grid-column: 1 / -1; }
  }
</style>
