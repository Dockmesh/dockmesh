<script lang="ts">
  // Environment — global cross-stack env vars. Set BASE_DOMAIN=haus.lan
  // once, reference it from 14 stacks via ${BASE_DOMAIN} in their
  // compose files. Dockmesh-eigenes Feature, das die Wettbewerber
  // (Portainer/Komodo) so nicht haben.
  import { api, ApiError } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { Skeleton } from '$lib/components/ui';
  import {
    Variable, Plus, Trash2, Search, RefreshCw, ChevronDown, Edit2,
    Upload, ShieldAlert, ChevronRight,
  } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime } from '$lib/i18n';

  interface EnvVar {
    id: number;
    key: string;
    value: string;
    group_name: string;
    encrypted: boolean;
    unreadable?: boolean;
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
  let formSecret = $state(false);
  let saving = $state(false);

  // Templates dropdown
  let showTemplates = $state(false);

  // Bulk import modal
  let showImport = $state(false);
  let importText = $state('');
  let importGroup = $state('misc');
  let importBusy = $state(false);
  // Per-key choice in the import preview; keys not in here use the suggestion.
  let importSecret = $state<Record<string, boolean>>({});

  // Collapsed groups
  let collapsed = $state<Set<string>>(new Set());

  // Template presets — Dockmesh-eigene 1-Click-Add UX.
  // Groups + keys carry their own metadata so the operator doesn't have
  // to remember TZ format / PUID purpose.
  const TEMPLATES: Array<{ group: string; label: string; icon: string; vars: Array<{ key: string; value: string; hint: string }> }> = [
    {
      group: 'System', label: m.env_tpl_system(), icon: '⚙️',
      vars: [
        { key: 'TZ',    value: 'Europe/Vienna', hint: m.env_hint_tz() },
        { key: 'PUID',  value: '1000',          hint: m.env_hint_puid() },
        { key: 'PGID',  value: '1000',          hint: m.env_hint_pgid() },
        { key: 'UMASK', value: '022',           hint: m.env_hint_umask() },
      ],
    },
    {
      group: 'Database', label: m.env_tpl_database(), icon: '🗄️',
      vars: [
        { key: 'DB_HOST',     value: '',     hint: m.env_hint_db_host() },
        { key: 'DB_PORT',     value: '5432', hint: m.env_hint_db_port() },
        { key: 'DB_USER',     value: '',     hint: m.env_hint_db_user() },
        { key: 'DB_PASSWORD', value: '',     hint: m.env_hint_db_password() },
        { key: 'DB_NAME',     value: '',     hint: m.env_hint_db_name() },
      ],
    },
    {
      group: 'SMTP', label: m.env_tpl_smtp(), icon: '📧',
      vars: [
        { key: 'SMTP_HOST',     value: '',    hint: m.env_hint_smtp_host() },
        { key: 'SMTP_PORT',     value: '587', hint: m.env_hint_smtp_port() },
        { key: 'SMTP_USER',     value: '',    hint: m.env_hint_smtp_user() },
        { key: 'SMTP_PASSWORD', value: '',    hint: m.env_hint_smtp_password() },
        { key: 'SMTP_FROM',     value: '',    hint: m.env_hint_smtp_from() },
      ],
    },
    {
      group: 'Proxy', label: m.env_tpl_proxy(), icon: '🌐',
      vars: [
        { key: 'HTTP_PROXY',  value: '',                  hint: m.env_hint_http_proxy() },
        { key: 'HTTPS_PROXY', value: '',                  hint: m.env_hint_https_proxy() },
        { key: 'NO_PROXY',    value: 'localhost,127.0.0.1', hint: m.env_hint_no_proxy() },
      ],
    },
  ];

  async function load() {
    loading = true;
    try {
      vars = await api.globalEnv.list();
    } catch (err) {
      toast.error(m.env_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  $effect(() => { load(); });

  // ── Derived ──────────────────────────────────────────────────────────
  const groups = $derived([...new Set(vars.map((v) => v.group_name || m.env_no_group()))].sort());

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
    const byGroup = new Map<string, EnvVar[]>();
    for (const v of visible) {
      const g = v.group_name || m.env_no_group();
      if (!byGroup.has(g)) byGroup.set(g, []);
      byGroup.get(g)!.push(v);
    }
    // Sort rows within each group
    for (const arr of byGroup.values()) arr.sort((a, b) => a.key.localeCompare(b.key));
    return byGroup;
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
    formSecret = false;
    showModal = true;
  }

  function openEdit(v: EnvVar) {
    editing = v;
    formKey = v.key;
    formValue = v.encrypted ? '' : v.value;
    formGroup = v.group_name;
    formSecret = v.encrypted;
    showModal = true;
  }

  async function save(e: Event) {
    e.preventDefault();
    if (!formKey.trim()) return;
    saving = true;
    try {
      const payload = { key: formKey.trim(), value: formValue, group_name: formGroup.trim(), encrypted: formSecret };
      if (editing) {
        await api.globalEnv.update(editing.id, payload);
        toast.success(m.env_updated_toast(), formKey);
      } else {
        await api.globalEnv.create(payload);
        toast.success(m.env_created_toast(), formKey);
      }
      showModal = false;
      await load();
    } catch (err) {
      toast.error(m.env_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      saving = false;
    }
  }

  async function deleteVar(v: EnvVar) {
    if (!(await confirm.ask({
      title: m.env_delete_title(),
      message: m.env_delete_message({ key: v.key }),
      body: m.env_delete_body(),
      confirmLabel: m.common_delete(), danger: true,
    }))) return;
    try {
      await api.globalEnv.delete(v.id);
      toast.success(m.env_deleted_toast(), v.key);
      await load();
    } catch (err) {
      toast.error(m.env_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function addFromTemplate(
    tpl: { key: string; value: string; hint: string },
    group: string,
  ) {
    if (vars.some((v) => v.key === tpl.key)) {
      toast.info(m.env_already_exists(), m.env_already_defined({ key: tpl.key }));
      return;
    }
    try {
      await api.globalEnv.create({ key: tpl.key, value: tpl.value, group_name: group.toLowerCase() });
      toast.success(m.env_added(), tpl.key);
      await load();
    } catch (err) {
      toast.error(m.env_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function addAllFromGroup(group: { group: string; label: string; vars: Array<{ key: string; value: string; hint: string }> }) {
    let added = 0;
    for (const tpl of group.vars) {
      if (vars.some((v) => v.key === tpl.key)) continue;
      try {
        await api.globalEnv.create({ key: tpl.key, value: tpl.value, group_name: group.group.toLowerCase() });
        added++;
      } catch { /* skip duplicates */ }
    }
    if (added > 0) {
      toast.success(m.env_added_n({ count: added }), group.label);
      await load();
    } else {
      toast.info(m.env_all_exist());
    }
    showTemplates = false;
  }

  // Keys containing password/secret/key/token are suggested as secrets;
  // the preview lets the operator change each one before importing.
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

  function isImportSecret(p: { key: string; secretSuggested: boolean }): boolean {
    return importSecret[p.key] ?? p.secretSuggested;
  }

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
          encrypted: isImportSecret(entry),
        });
        added++;
      } catch { failed++; }
    }
    importBusy = false;
    showImport = false;
    importText = '';
    importSecret = {};
    let msg: string = m.env_import_added({ n: added });
    if (skipped > 0) msg += `, ${m.env_import_skipped({ n: skipped })}`;
    if (failed > 0) msg += `, ${m.env_import_failed({ n: failed })}`;
    toast[failed === 0 ? 'success' : 'info'](msg);
    await load();
  }

  // ── Helpers ──────────────────────────────────────────────────────────
  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    return fmtDateTime(iso, {
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
        <h1 class="ed-title env-title">{m.env_title()}</h1>
        <p class="ed-subtitle env-subtitle">
          {#if vars.length === 0}
            {m.env_no_vars_yet()}
          {:else}
            {m.env_n_variables({ count: vars.length })}{groups.length > 0 ? ` · ${m.env_n_groups({ count: groups.length })}` : ''}{secretsCount > 0 ? ` · ${m.env_n_secrets({ count: secretsCount })}` : ''}
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
          placeholder={m.env_search_placeholder()}
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
          <Variable size={12} strokeWidth={1.5} /> {m.env_templates()}
          <ChevronDown size={11} strokeWidth={1.5} />
        </button>
        {#if showTemplates}
          <button
            class="env-templates-scrim"
            aria-label={m.env_close_templates()}
            onclick={() => (showTemplates = false)}
          ></button>
          <div class="env-templates-menu" role="menu">
            {#each TEMPLATES as tplGroup (tplGroup.group)}
              <div class="env-templates-group">
                <div class="env-templates-group-head">
                  <span class="env-templates-group-label">
                    <span class="env-templates-group-icon">{tplGroup.icon}</span>
                    {tplGroup.label}
                  </span>
                  <button
                    type="button"
                    class="env-templates-add-all"
                    onclick={() => addAllFromGroup(tplGroup)}
                  >
                    {m.env_add_all()}
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
                      <span class="env-templates-item-exists">{m.env_added()}</span>
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
        <Upload size={12} strokeWidth={1.5} /> {m.env_bulk_import()}
      </button>

      <button
        type="button"
        class="dm-btn dm-btn-primary dm-btn-sm"
        onclick={openNew}
      >
        <Plus size={12} strokeWidth={1.5} /> {m.env_add_variable()}
      </button>

      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-sm"
        onclick={load}
        disabled={loading}
        title={m.common_refresh()}
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
        <p class="env-empty-title">{m.env_empty_title()}</p>
        <p class="ed-subtitle env-empty-blurb">
          {m.env_empty_blurb_before()} <code class="env-inline-code">TZ</code>,
          <code class="env-inline-code">PUID</code>, {m.env_empty_blurb_middle()}
          <code class="env-inline-code">{`\${KEY}`}</code>.
        </p>
        <div class="env-empty-actions">
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={openNew}>
            <Plus size={12} strokeWidth={1.5} /> {m.env_add_first()}
          </button>
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showImport = true)}>
            <Upload size={12} strokeWidth={1.5} /> {m.env_bulk_import()}
          </button>
        </div>
      </div>
    {:else if visible.length === 0}
      <div class="env-empty">
        <Eyebrow>{m.env_no_match()}</Eyebrow>
        <p class="env-empty-title">{m.env_no_match_title({ search })}</p>
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
              <span class="env-group-count">· {m.env_n_vars_short({ count: rows.length })}</span>
              {#if rows.some((r) => r.encrypted)}
                {@const secrets = rows.filter((r) => r.encrypted).length}
                <span class="env-group-secret-count">
                  <ShieldAlert size={10} strokeWidth={1.5} />
                  {m.env_n_secrets({ count: secrets })}
                </span>
              {/if}
            </button>

            {#if !isCollapsed}
              <div class="env-group-body">
                <div class="env-row env-row-head">
                  <span>{m.env_col_key()}</span>
                  <span>{m.env_col_value()}</span>
                  <span>{m.env_col_updated()}</span>
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
                          {#if v.unreadable}
                            <span class="env-value-encrypted-pill env-value-unreadable" title={m.env_unreadable_title()}>{m.env_cant_decrypt()}</span>
                          {:else}
                            <span class="env-value-encrypted-pill">{m.env_hidden()}</span>
                          {/if}
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
                        title={m.common_edit()}
                        aria-label={m.common_edit()}
                        onclick={() => openEdit(v)}
                      >
                        <Edit2 size={11} strokeWidth={1.5} />
                      </button>
                      <button
                        type="button"
                        class="env-icon-btn env-icon-danger"
                        title={m.common_delete()}
                        aria-label={m.common_delete()}
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
        <div class="env-hint-title">{m.env_hint_title()}</div>
        <p class="env-hint-blurb">
          {m.env_hint_1()} <code class="env-inline-code">compose.yaml</code>{m.env_hint_2()}
          <code class="env-inline-code">{`\${KEY}`}</code>{m.env_hint_3()} <code class="env-inline-code">.env</code>{m.env_hint_4()}
        </p>
      </div>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── Add / Edit Modal ───────────────────────── -->
<EditorialModal
  bind:open={showModal}
  eyebrow={editing ? m.env_eyebrow_edit() : m.env_eyebrow_new()}
  width={560}
>
  {#snippet title()}
    {#if editing}
      {m.env_title_edit_before()} <em class="ed-accent">{editing.key}</em> {m.env_title_edit_after()}
    {:else}
      {m.env_title_add_before()} <em>{m.env_title_add_global()}</em> {m.env_title_add_after()}
    {/if}
  {/snippet}

  <form id="env-form" class="env-form" onsubmit={save}>
    <div class="env-grid-2">
      <Field label={m.common_key()} hint={m.env_key_hint()}>
        <input
          class="dm-input acc-input-mono"
          value={formKey}
          oninput={(e) => (formKey = normalizeKey((e.target as HTMLInputElement).value))}
          disabled={saving || editing !== null}
          placeholder="BASE_DOMAIN"
        />
      </Field>
      <Field label={m.env_group()} hint={m.env_group_hint()}>
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
      label={m.env_value()}
      hint={editing?.unreadable
        ? m.env_value_hint_unreadable()
        : editing?.encrypted
        ? m.env_value_hint_secret()
        : m.env_value_hint_plain()}
    >
      <textarea
        class="dm-input acc-input-mono env-textarea"
        bind:value={formValue}
        disabled={saving}
        placeholder={editing?.encrypted ? m.env_value_placeholder_unchanged() : m.env_value_placeholder()}
        rows="4"
      ></textarea>
    </Field>

    <label class="env-secret-check">
      <input type="checkbox" bind:checked={formSecret} disabled={saving || editing?.encrypted} />
      <span>
        {m.env_secret()}
        <span class="env-secret-check-hint">
          {editing?.encrypted
            ? m.env_secret_hint_existing()
            : m.env_secret_hint_new()}
        </span>
      </span>
    </label>
  </form>

  {#snippet footer()}
    <span></span>
    <div class="env-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showModal = false)}>
        {m.common_cancel()}
      </button>
      <button
        type="submit"
        form="env-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={saving || !formKey.trim()}
      >
        {saving ? m.common_saving() : editing ? m.common_save() : m.common_create()}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<!-- ───────────────────────── Bulk Import Modal ───────────────────────── -->
<EditorialModal
  bind:open={showImport}
  eyebrow={m.env_bulk_import()}
  width={620}
>
  {#snippet title()}
    {m.env_import_title_before()} <em class="ed-accent">.env</em>{m.env_import_title_after()}
  {/snippet}

  <div class="env-form">
    <Field label={m.env_default_group()} hint={m.env_default_group_hint()}>
      <input
        class="dm-input acc-input-mono"
        bind:value={importGroup}
        list="env-group-suggestions"
        placeholder="misc"
      />
    </Field>

    <Field
      label={m.env_content()}
      hint={m.env_content_hint()}
    >
      <textarea
        class="dm-input acc-input-mono env-import-textarea"
        bind:value={importText}
        rows="9"
        placeholder={m.env_content_placeholder()}
      ></textarea>
    </Field>

    {#if parsedImport.length > 0}
      <div class="env-import-preview">
        <div class="env-import-preview-head">
          <Eyebrow>{m.env_preview({ entries: m.env_n_entries({ count: parsedImport.length }) })}</Eyebrow>
        </div>
        <div class="env-import-preview-body">
          {#each parsedImport as p (p.key)}
            {@const exists = vars.some((v) => v.key === p.key)}
            {@const secret = isImportSecret(p)}
            <div class="env-import-preview-row" class:env-import-preview-skip={exists}>
              <input
                type="checkbox"
                checked={secret}
                disabled={exists}
                title={m.env_secret_checkbox_title()}
                aria-label={m.env_store_as_secret({ key: p.key })}
                onchange={(e) => (importSecret = { ...importSecret, [p.key]: (e.target as HTMLInputElement).checked })}
              />
              {#if secret}
                <ShieldAlert size={11} strokeWidth={1.5} class="env-secret-icon" />
              {/if}
              <code class="env-import-preview-key">{p.key}</code>
              <span class="env-import-preview-eq">=</span>
              <span class="env-import-preview-value">
                {secret ? '•••' : p.value || ' '}
              </span>
              {#if exists}
                <span class="env-import-preview-skip-label">{m.env_already_exists()}</span>
              {/if}
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <span class="env-modal-foothint">
      {m.env_n_entries_detected({ count: parsedImport.length })}
    </span>
    <div class="env-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showImport = false)}>
        {m.common_cancel()}
      </button>
      <button
        type="button"
        class="dm-btn dm-btn-primary dm-btn-sm"
        onclick={runImport}
        disabled={importBusy || parsedImport.length === 0}
      >
        {importBusy ? m.env_importing() : m.env_import_n({ n: parsedImport.length })}
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
    font-size: 11px;
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
    font-size: 11.5px;
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
    font-size: 11.5px;
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
    font-size: 11px;
    color: var(--fg);
  }
  .env-group-count {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .env-group-secret-count {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 11.5px;
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
    font-size: 11.5px;
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
  .env-value-encrypted-pill.env-value-unreadable {
    color: var(--color-danger-400);
  }
  .env-value-encrypted-pill {
    font-size: 11.5px;
    padding: 1px 5px;
    border-radius: 999px;
    color: var(--color-warning-400);
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 6%, transparent);
  }

  .env-cell-updated {
    font-size: 11.5px;
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
  .env-secret-check {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    font-size: var(--fs-sm);
  }
  .env-secret-check input { margin-top: 3px; }
  .env-secret-check-hint {
    display: block;
    color: var(--fg-muted);
    font-size: 11px;
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
    font-size: 11.5px;
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
