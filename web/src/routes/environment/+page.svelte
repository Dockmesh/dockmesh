<script lang="ts">
  // Environment — global cross-stack env vars. Set BASE_DOMAIN=haus.lan
  // once, reference it from 14 stacks via ${BASE_DOMAIN} in their
  // compose files. Dockmesh-eigenes Feature, das die Wettbewerber
  // (Portainer/Komodo) so nicht haben.
  //
  // Slice 1: editorial rebuild on existing backend surface. Refs-Count,
  // "Used by" popover, manual secret-toggle warten auf Backend-Slice.
  import { api, ApiError } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { Skeleton } from '$lib/components/ui';
  import {
    Variable, Plus, Trash2, Search, RefreshCw, ChevronDown, Edit2,
    Upload, ShieldAlert, ChevronRight,
  } from 'lucide-svelte';

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

  // Edit modal
  let showModal = $state(false);
  let editing = $state<EnvVar | null>(null);
  let formKey = $state('');
  let formValue = $state('');
  let formGroup = $state('');
  let saving = $state(false);

  // Templates dropdown
  let showTemplates = $state(false);

  // Bulk import modal
  let showImport = $state(false);
  let importText = $state('');
  let importGroup = $state('misc');
  let importBusy = $state(false);

  // Collapsed groups
  let collapsed = $state<Set<string>>(new Set());

  // Template presets — Dockmesh-eigene 1-Click-Add UX.
  // Groups + keys carry their own metadata so the operator doesn't have
  // to remember TZ format / PUID purpose.
  const TEMPLATES: Array<{ group: string; icon: string; vars: Array<{ key: string; value: string; hint: string }> }> = [
    {
      group: 'System', icon: '⚙️',
      vars: [
        { key: 'TZ',    value: 'Europe/Vienna', hint: 'Container timezone' },
        { key: 'PUID',  value: '1000',          hint: 'Process user ID for file permissions' },
        { key: 'PGID',  value: '1000',          hint: 'Process group ID for file permissions' },
        { key: 'UMASK', value: '022',           hint: 'File creation mask' },
      ],
    },
    {
      group: 'Database', icon: '🗄️',
      vars: [
        { key: 'DB_HOST',     value: '',     hint: 'Database hostname' },
        { key: 'DB_PORT',     value: '5432', hint: 'Database port' },
        { key: 'DB_USER',     value: '',     hint: 'Database username' },
        { key: 'DB_PASSWORD', value: '',     hint: 'Database password' },
        { key: 'DB_NAME',     value: '',     hint: 'Database name' },
      ],
    },
    {
      group: 'SMTP', icon: '📧',
      vars: [
        { key: 'SMTP_HOST',     value: '',    hint: 'Mail server hostname' },
        { key: 'SMTP_PORT',     value: '587', hint: 'Mail server port (587 for TLS)' },
        { key: 'SMTP_USER',     value: '',    hint: 'Mail server username' },
        { key: 'SMTP_PASSWORD', value: '',    hint: 'Mail server password' },
        { key: 'SMTP_FROM',     value: '',    hint: 'Sender email address' },
      ],
    },
    {
      group: 'Proxy', icon: '🌐',
      vars: [
        { key: 'HTTP_PROXY',  value: '',                  hint: 'HTTP proxy URL' },
        { key: 'HTTPS_PROXY', value: '',                  hint: 'HTTPS proxy URL' },
        { key: 'NO_PROXY',    value: 'localhost,127.0.0.1', hint: 'Comma-separated bypass list' },
      ],
    },
  ];

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

  // ── Derived ──────────────────────────────────────────────────────────
  const groups = $derived([...new Set(vars.map((v) => v.group_name || '(no group)'))].sort());

  const visible = $derived(
    vars.filter((v) => {
      if (!search.trim()) return true;
      const q = search.toLowerCase();
      return (
        v.key.toLowerCase().includes(q) ||
        v.value.toLowerCase().includes(q) ||
        v.group_name.toLowerCase().includes(q)
      );
    }),
  );

  // Group → rows map (filtered)
  const grouped = $derived.by(() => {
    const m = new Map<string, EnvVar[]>();
    for (const v of visible) {
      const g = v.group_name || '(no group)';
      if (!m.has(g)) m.set(g, []);
      m.get(g)!.push(v);
    }
    // Sort rows within each group
    for (const arr of m.values()) arr.sort((a, b) => a.key.localeCompare(b.key));
    return m;
  });

  const visibleGroups = $derived([...grouped.keys()].sort());

  const secretsCount = $derived(vars.filter((v) => v.encrypted).length);

  function toggleGroup(g: string) {
    const next = new Set(collapsed);
    if (next.has(g)) next.delete(g);
    else next.add(g);
    collapsed = next;
  }

  // ── Actions ──────────────────────────────────────────────────────────
  function openNew() {
    editing = null;
    formKey = '';
    formValue = '';
    formGroup = '';
    showModal = true;
  }

  function openEdit(v: EnvVar) {
    editing = v;
    formKey = v.key;
    formValue = v.value;
    formGroup = v.group_name;
    showModal = true;
  }

  async function save(e: Event) {
    e.preventDefault();
    if (!formKey.trim()) return;
    saving = true;
    try {
      const payload = { key: formKey.trim(), value: formValue, group_name: formGroup.trim() };
      if (editing) {
        await api.globalEnv.update(editing.id, payload);
        toast.success('Updated', formKey);
      } else {
        await api.globalEnv.create(payload);
        toast.success('Created', formKey);
      }
      showModal = false;
      await load();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      saving = false;
    }
  }

  async function deleteVar(v: EnvVar) {
    if (!(await confirm.ask({
      title: 'Delete environment variable',
      message: `Delete "${v.key}"?`,
      body: 'Stacks that reference this variable via ${…} will fail to deploy until it’s restored or the reference is removed.',
      confirmLabel: 'Delete', danger: true,
    }))) return;
    try {
      await api.globalEnv.delete(v.id);
      toast.success('Deleted', v.key);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function addFromTemplate(
    tpl: { key: string; value: string; hint: string },
    group: string,
  ) {
    if (vars.some((v) => v.key === tpl.key)) {
      toast.info('Already exists', `${tpl.key} is already defined`);
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
      if (vars.some((v) => v.key === tpl.key)) continue;
      try {
        await api.globalEnv.create({ key: tpl.key, value: tpl.value, group_name: group.group.toLowerCase() });
        added++;
      } catch { /* skip duplicates */ }
    }
    if (added > 0) {
      toast.success(`Added ${added} variable${added === 1 ? '' : 's'}`, group.group);
      await load();
    } else {
      toast.info('All variables already exist');
    }
    showTemplates = false;
  }

  // Bulk-import .env parsing — same heuristic as the mockup. Auto-flag
  // keys containing password/secret/key/token as worth-marking-secret.
  // Backend can't accept the secret flag yet (Slice 2), but we surface
  // the heuristic in the preview so operators see what would happen.
  function parseEnv(text: string): Array<{ key: string; value: string; secretSuggested: boolean }> {
    const out: Array<{ key: string; value: string; secretSuggested: boolean }> = [];
    for (const raw of text.split(/\r?\n/)) {
      const line = raw.trim();
      if (!line || line.startsWith('#')) continue;
      const eq = line.indexOf('=');
      if (eq < 0) continue;
      const key = line.slice(0, eq).trim();
      let value = line.slice(eq + 1).trim();
      if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
        value = value.slice(1, -1);
      }
      if (!/^[A-Z][A-Z0-9_]*$/i.test(key)) continue;
      out.push({
        key,
        value,
        secretSuggested: /password|secret|key|token/i.test(key),
      });
    }
    return out;
  }

  const parsedImport = $derived(parseEnv(importText));

  async function runImport() {
    if (parsedImport.length === 0) return;
    importBusy = true;
    let added = 0, skipped = 0, failed = 0;
    for (const entry of parsedImport) {
      if (vars.some((v) => v.key === entry.key)) { skipped++; continue; }
      try {
        await api.globalEnv.create({
          key: entry.key,
          value: entry.value,
          group_name: importGroup.trim().toLowerCase() || 'misc',
        });
        added++;
      } catch { failed++; }
    }
    importBusy = false;
    showImport = false;
    importText = '';
    let msg = `${added} added`;
    if (skipped > 0) msg += `, ${skipped} skipped`;
    if (failed > 0) msg += `, ${failed} failed`;
    toast[failed === 0 ? 'success' : 'info'](msg);
    await load();
  }

  // ── Helpers ──────────────────────────────────────────────────────────
  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    return new Date(iso).toLocaleDateString(undefined, {
      year: 'numeric', month: 'short', day: '2-digit',
    });
  }

  function normalizeKey(raw: string): string {
    return raw.toUpperCase().replace(/[^A-Z0-9_]/g, '');
  }
</script>

<EditorialPage>
  <section class="env">
    <!-- ───────────────────────── Header ───────────────────────── -->
    <header class="env-header">
      <div class="env-header-text">
        <h1 class="ed-title env-title">Environment</h1>
        <p class="ed-subtitle env-subtitle">
          {#if vars.length === 0}
            No global variables yet
          {:else}
            {vars.length} variable{vars.length === 1 ? '' : 's'}{groups.length > 0 ? ` · ${groups.length} group${groups.length === 1 ? '' : 's'}` : ''}{secretsCount > 0 ? ` · ${secretsCount} secret${secretsCount === 1 ? '' : 's'}` : ''}
          {/if}
        </p>
      </div>
    </header>

    <!-- ───────────────────── Toolbar ───────────────────── -->
    <div class="env-toolbar">
      <div class="env-search">
        <Search size={12} strokeWidth={1.5} class="env-search-icon" />
        <input
          type="search"
          placeholder="search key · value · group…"
          bind:value={search}
          class="ed-underline-input env-search-input"
        />
      </div>

      <span class="env-spacer"></span>

      <div class="env-templates-wrap">
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-sm"
          onclick={() => (showTemplates = !showTemplates)}
        >
          <Variable size={12} strokeWidth={1.5} /> Templates
          <ChevronDown size={11} strokeWidth={1.5} />
        </button>
        {#if showTemplates}
          <button
            class="env-templates-scrim"
            aria-label="Close templates"
            onclick={() => (showTemplates = false)}
          ></button>
          <div class="env-templates-menu" role="menu">
            {#each TEMPLATES as tplGroup (tplGroup.group)}
              <div class="env-templates-group">
                <div class="env-templates-group-head">
                  <span class="env-templates-group-label">
                    <span class="env-templates-group-icon">{tplGroup.icon}</span>
                    {tplGroup.group}
                  </span>
                  <button
                    type="button"
                    class="env-templates-add-all"
                    onclick={() => addAllFromGroup(tplGroup)}
                  >
                    Add all
                  </button>
                </div>
                {#each tplGroup.vars as tpl (tpl.key)}
                  {@const exists = vars.some((v) => v.key === tpl.key)}
                  <button
                    type="button"
                    class="env-templates-item"
                    disabled={exists}
                    onclick={() => {
                      addFromTemplate(tpl, tplGroup.group);
                      showTemplates = false;
                    }}
                  >
                    <div class="env-templates-item-text">
                      <code class="env-templates-item-key">{tpl.key}</code>
                      <span class="env-templates-item-hint">{tpl.hint}</span>
                    </div>
                    {#if exists}
                      <span class="env-templates-item-exists">added</span>
                    {/if}
                  </button>
                {/each}
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-sm"
        onclick={() => (showImport = true)}
      >
        <Upload size={12} strokeWidth={1.5} /> Bulk import
      </button>

      <button
        type="button"
        class="dm-btn dm-btn-primary dm-btn-sm"
        onclick={openNew}
      >
        <Plus size={12} strokeWidth={1.5} /> Add variable
      </button>

      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-sm"
        onclick={load}
        disabled={loading}
        title="Refresh"
      >
        <RefreshCw size={12} strokeWidth={1.5} class={loading ? 'env-spin' : ''} />
      </button>
    </div>

    <!-- ───────────────────── Body ───────────────────── -->
    {#if loading && vars.length === 0}
      <div class="env-loading">
        <Skeleton width="100%" height="8rem" />
      </div>
    {:else if vars.length === 0}
      <div class="env-empty">
        <Variable size={20} strokeWidth={1.4} />
        <p class="env-empty-title">No global variables</p>
        <p class="ed-subtitle env-empty-blurb">
          Add variables like <code class="env-inline-code">TZ</code>,
          <code class="env-inline-code">PUID</code>, database credentials —
          they'll be referenced by your compose files via
          <code class="env-inline-code">{`\${KEY}`}</code>.
        </p>
        <div class="env-empty-actions">
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={openNew}>
            <Plus size={12} strokeWidth={1.5} /> Add first variable
          </button>
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showImport = true)}>
            <Upload size={12} strokeWidth={1.5} /> Bulk import
          </button>
        </div>
      </div>
    {:else if visible.length === 0}
      <div class="env-empty">
        <Eyebrow>no match</Eyebrow>
        <p class="env-empty-title">No variables match „{search}".</p>
      </div>
    {:else}
      <div class="env-groups">
        {#each visibleGroups as g (g)}
          {@const rows = grouped.get(g) ?? []}
          {@const isCollapsed = collapsed.has(g)}
          <section class="env-group">
            <button
              type="button"
              class="env-group-head"
              onclick={() => toggleGroup(g)}
              aria-expanded={!isCollapsed}
            >
              <span class="env-group-chev" data-open={!isCollapsed}>
                <ChevronRight size={12} strokeWidth={1.5} />
              </span>
              <span class="env-group-name">{g}</span>
              <span class="env-group-count">· {rows.length} var{rows.length === 1 ? '' : 's'}</span>
              {#if rows.some((r) => r.encrypted)}
                {@const secrets = rows.filter((r) => r.encrypted).length}
                <span class="env-group-secret-count">
                  <ShieldAlert size={10} strokeWidth={1.5} />
                  {secrets} secret{secrets === 1 ? '' : 's'}
                </span>
              {/if}
            </button>

            {#if !isCollapsed}
              <div class="env-group-body">
                <div class="env-row env-row-head">
                  <span>key</span>
                  <span>value</span>
                  <span>updated</span>
                  <span class="env-col-right">·</span>
                </div>
                {#each rows as v (v.id)}
                  <div class="env-row">
                    <div class="env-cell-key">
                      {#if v.encrypted}
                        <ShieldAlert size={11} strokeWidth={1.5} class="env-secret-icon" />
                      {/if}
                      <code class="env-key">{v.key}</code>
                    </div>
                    <div class="env-cell-value">
                      {#if v.encrypted}
                        <span class="env-value-secret">
                          <span class="env-value-mask">•••••••••••••••</span>
                          <span class="env-value-encrypted-pill">encrypted</span>
                        </span>
                      {:else}
                        <code class="env-value-text" title={v.value}>{v.value || ' '}</code>
                      {/if}
                    </div>
                    <span class="env-cell-updated">{fmtDate(v.updated_at)}</span>
                    <div class="env-cell-actions">
                      <button
                        type="button"
                        class="env-icon-btn"
                        title="Edit"
                        aria-label="Edit"
                        onclick={() => openEdit(v)}
                      >
                        <Edit2 size={11} strokeWidth={1.5} />
                      </button>
                      <button
                        type="button"
                        class="env-icon-btn env-icon-danger"
                        title="Delete"
                        aria-label="Delete"
                        onclick={() => deleteVar(v)}
                      >
                        <Trash2 size={11} strokeWidth={1.5} />
                      </button>
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </section>
        {/each}
      </div>
    {/if}

    <!-- ───────────────── Footer hint ───────────────── -->
    {#if vars.length > 0}
      <div class="env-hint">
        <div class="env-hint-title">How to reference these</div>
        <p class="env-hint-blurb">
          In any stack's <code class="env-inline-code">compose.yaml</code>, reference a key as
          <code class="env-inline-code">{`\${KEY}`}</code>. Dockmesh interpolates global vars at
          deploy time. Stack-level <code class="env-inline-code">.env</code> files override globals.
        </p>
      </div>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── Add / Edit Modal ───────────────────────── -->
<EditorialModal
  bind:open={showModal}
  eyebrow={editing ? 'Edit variable' : 'New variable'}
  width={560}
>
  {#snippet title()}
    {#if editing}
      Edit <em class="ed-accent">{editing.key}</em>
    {:else}
      Add a <em>global</em> variable
    {/if}
  {/snippet}

  <form id="env-form" class="env-form" onsubmit={save}>
    <div class="env-grid-2">
      <Field label="Key" hint="UPPER_SNAKE_CASE convention.">
        <input
          class="dm-input acc-input-mono"
          value={formKey}
          oninput={(e) => (formKey = normalizeKey((e.target as HTMLInputElement).value))}
          disabled={saving || editing !== null}
          placeholder="BASE_DOMAIN"
        />
      </Field>
      <Field label="Group" hint="Free-form. Used to organise the list.">
        <input
          class="dm-input acc-input-mono"
          bind:value={formGroup}
          list="env-group-suggestions"
          placeholder="domain"
        />
        <datalist id="env-group-suggestions">
          {#each groups as g}
            <option value={g}></option>
          {/each}
        </datalist>
      </Field>
    </div>

    <Field
      label="Value"
      hint={editing?.encrypted
        ? 'This variable is encrypted at rest. Updating the value re-encrypts it.'
        : 'Plain string. Multi-line is fine.'}
    >
      <textarea
        class="dm-input acc-input-mono env-textarea"
        bind:value={formValue}
        disabled={saving}
        placeholder="value or multi-line content"
        rows="4"
      ></textarea>
    </Field>
  </form>

  {#snippet footer()}
    <span></span>
    <div class="env-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showModal = false)}>
        Cancel
      </button>
      <button
        type="submit"
        form="env-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={saving || !formKey.trim()}
      >
        {saving ? 'Saving…' : editing ? 'Save' : 'Create'}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<!-- ───────────────────────── Bulk Import Modal ───────────────────────── -->
<EditorialModal
  bind:open={showImport}
  eyebrow="Bulk import"
  width={620}
>
  {#snippet title()}
    Paste a <em class="ed-accent">.env</em> file
  {/snippet}

  <div class="env-form">
    <Field label="Default group" hint="All imported entries land in this group. Move them individually after.">
      <input
        class="dm-input acc-input-mono"
        bind:value={importGroup}
        list="env-group-suggestions"
        placeholder="misc"
      />
    </Field>

    <Field
      label=".env content"
      hint="Comments (#) and blank lines are skipped. Keys matching password/secret/key/token are flagged as worth-marking-secret (manual toggle arrives with the next backend update)."
    >
      <textarea
        class="dm-input acc-input-mono env-import-textarea"
        bind:value={importText}
        rows="9"
        placeholder={'# Paste a .env file here\nBASE_DOMAIN=haus.lan\nTZ=Europe/Berlin'}
      ></textarea>
    </Field>

    {#if parsedImport.length > 0}
      <div class="env-import-preview">
        <div class="env-import-preview-head">
          <Eyebrow>preview · {parsedImport.length} entr{parsedImport.length === 1 ? 'y' : 'ies'}</Eyebrow>
        </div>
        <div class="env-import-preview-body">
          {#each parsedImport as p (p.key)}
            {@const exists = vars.some((v) => v.key === p.key)}
            <div class="env-import-preview-row" class:env-import-preview-skip={exists}>
              {#if p.secretSuggested}
                <ShieldAlert size={11} strokeWidth={1.5} class="env-secret-icon" />
              {/if}
              <code class="env-import-preview-key">{p.key}</code>
              <span class="env-import-preview-eq">=</span>
              <span class="env-import-preview-value">
                {p.secretSuggested ? '•••' : p.value || ' '}
              </span>
              {#if exists}
                <span class="env-import-preview-skip-label">already exists</span>
              {/if}
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <span class="env-modal-foothint">
      {parsedImport.length} entr{parsedImport.length === 1 ? 'y' : 'ies'} detected
    </span>
    <div class="env-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showImport = false)}>
        Cancel
      </button>
      <button
        type="button"
        class="dm-btn dm-btn-primary dm-btn-sm"
        onclick={runImport}
        disabled={importBusy || parsedImport.length === 0}
      >
        {importBusy ? 'Importing…' : `Import ${parsedImport.length}`}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .env {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .env-header {
    display: flex;
    align-items: flex-end;
    gap: 24px;
    flex-wrap: wrap;
  }
  .env-header-text { min-width: 0; max-width: 70ch; }
  .env-title {
    font-size: 30px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .env-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }
  .env-inline-code {
    font-family: var(--font-mono);
    font-size: 11.5px;
    background: var(--bg-elevated);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border-subtle);
    color: var(--fg);
  }

  /* ── Toolbar ────────────────────────────────────────────────── */
  .env-toolbar {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .env-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .env-search :global(.env-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .env-search-input {
    padding-left: 18px;
    font-size: 12.5px;
    font-family: var(--font-mono);
  }
  .env-spacer { flex: 1; }
  .env-spin { animation: env-spin 0.9s linear infinite; }
  @keyframes env-spin {
    from { transform: rotate(0deg); }
    to   { transform: rotate(360deg); }
  }

  /* Templates dropdown */
  .env-templates-wrap {
    position: relative;
  }
  .env-templates-scrim {
    position: fixed;
    inset: 0;
    z-index: 30;
    background: transparent;
    border: 0;
    cursor: default;
  }
  .env-templates-menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 40;
    width: 340px;
    max-height: 60vh;
    overflow-y: auto;
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    box-shadow: 0 12px 30px rgba(2, 6, 23, 0.4);
  }
  .env-templates-group {
    border-bottom: 1px solid var(--border);
  }
  .env-templates-group:last-child { border-bottom: 0; }
  .env-templates-group-head {
    padding: 8px 12px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    background: var(--surface);
  }
  .env-templates-group-label {
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .env-templates-group-icon { font-size: 13px; }
  .env-templates-add-all {
    background: transparent;
    border: 0;
    color: var(--accent-fg);
    font-size: 10.5px;
    cursor: pointer;
    padding: 0;
  }
  .env-templates-add-all:hover { text-decoration: underline; }
  .env-templates-item {
    width: 100%;
    background: transparent;
    border: 0;
    padding: 8px 12px;
    text-align: left;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    color: var(--fg-muted);
  }
  .env-templates-item:hover:not(:disabled) {
    background: var(--surface-hover);
  }
  .env-templates-item:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .env-templates-item-text {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .env-templates-item-key {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg);
    font-weight: 500;
  }
  .env-templates-item-hint {
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .env-templates-item-exists {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    flex-shrink: 0;
  }

  /* ── Loading / empty ────────────────────────────────────────── */
  .env-loading { padding: 0; }
  .env-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
    color: var(--fg-subtle);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
  }
  .env-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .env-empty-blurb {
    margin: 4px 0 0;
    max-width: 50ch;
  }
  .env-empty-actions {
    display: flex;
    gap: 8px;
    margin-top: 14px;
  }

  /* ── Groups ─────────────────────────────────────────────────── */
  .env-groups {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .env-group {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .env-group-head {
    width: 100%;
    padding: 10px 14px;
    background: var(--bg-elevated);
    border: 0;
    text-align: left;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .env-group-head:hover { background: var(--surface-hover); }
  .env-group-chev {
    color: var(--fg-subtle);
    transition: transform 120ms;
    display: inline-flex;
  }
  .env-group-chev[data-open="true"] { transform: rotate(90deg); }
  .env-group-name {
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fg);
  }
  .env-group-count {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .env-group-secret-count {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--color-warning-400);
  }
  .env-group-body {
    /* nothing extra */
  }

  /* ── Rows ───────────────────────────────────────────────────── */
  .env-row {
    display: grid;
    grid-template-columns: minmax(200px, 1fr) minmax(240px, 1.6fr) 120px 80px;
    gap: 14px;
    align-items: center;
    padding: 10px 14px;
    border-top: 1px solid var(--border-subtle);
  }
  .env-row:first-child { border-top: 0; }
  .env-row-head {
    background: var(--surface);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    padding: 8px 14px;
  }
  .env-col-right { text-align: right; }

  .env-cell-key {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .env-key {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .env-cell-key :global(.env-secret-icon) {
    color: var(--color-warning-400);
    flex-shrink: 0;
  }

  .env-cell-value { min-width: 0; }
  .env-value-text {
    display: block;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    padding: 3px 7px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }
  .env-value-secret {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .env-value-mask {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-subtle);
    letter-spacing: 0.05em;
    user-select: none;
  }
  .env-value-encrypted-pill {
    font-family: var(--font-mono);
    font-size: 9.5px;
    padding: 1px 5px;
    border-radius: 999px;
    color: var(--color-warning-400);
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 6%, transparent);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .env-cell-updated {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }

  .env-cell-actions {
    display: inline-flex;
    justify-content: flex-end;
    gap: 2px;
  }
  .env-icon-btn {
    width: 24px;
    height: 24px;
    background: transparent;
    border: 0;
    border-radius: 4px;
    color: var(--fg-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .env-icon-btn:hover {
    color: var(--fg);
    background: var(--surface-hover);
  }
  .env-icon-danger { color: var(--color-danger-400); }
  .env-icon-danger:hover {
    color: var(--color-danger-400);
    background: color-mix(in srgb, var(--color-danger-500) 12%, transparent);
  }

  /* ── Footer hint ────────────────────────────────────────────── */
  .env-hint {
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 5px;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .env-hint-title {
    font-weight: 500;
    color: var(--fg);
    margin-bottom: 4px;
  }
  .env-hint-blurb {
    margin: 0;
    line-height: 1.5;
  }

  /* ── Modal ──────────────────────────────────────────────────── */
  .env-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  /* Tighter input sizing inside the modal — the default dm-input is
     14px, which combined with mono renders oversized in a small modal
     with only a handful of fields. */
  .env-form :global(.dm-input) {
    font-size: 12.5px;
    padding: 6px 10px;
    line-height: 1.4;
  }
  .env-grid-2 {
    display: grid;
    grid-template-columns: 1.4fr 1fr;
    gap: 10px;
  }
  .env-textarea {
    min-height: 80px;
    resize: vertical;
    font-size: 12px;
    line-height: 1.5;
  }
  .env-import-textarea {
    min-height: 160px;
    resize: vertical;
    font-size: 12px;
    line-height: 1.5;
  }
  .env-modal-actions {
    display: flex;
    gap: 8px;
  }
  .env-modal-foothint {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  /* Import preview */
  .env-import-preview {
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    overflow: hidden;
  }
  .env-import-preview-head {
    padding: 8px 12px;
    background: var(--surface);
    border-bottom: 1px solid var(--border-subtle);
  }
  .env-import-preview-body {
    max-height: 200px;
    overflow-y: auto;
    padding: 6px 12px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .env-import-preview-row {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 0;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg);
  }
  .env-import-preview-row :global(.env-secret-icon) {
    color: var(--color-warning-400);
    flex-shrink: 0;
  }
  .env-import-preview-skip {
    color: var(--fg-subtle);
    text-decoration: line-through;
  }
  .env-import-preview-key { color: var(--fg); }
  .env-import-preview-eq { color: var(--fg-subtle); }
  .env-import-preview-value {
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
    min-width: 0;
  }
  .env-import-preview-skip-label {
    font-size: 10px;
    color: var(--fg-subtle);
    margin-left: auto;
    text-decoration: none;
  }

  @media (max-width: 880px) {
    .env-row { grid-template-columns: 1fr; }
    .env-row-head { display: none; }
    .env-grid-2 { grid-template-columns: 1fr; }
  }
</style>
