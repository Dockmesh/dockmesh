<script lang="ts">
  // Container Registries — editorial rebuild based on
  // `Dockmesh Wizard/registries.jsx`.
  //
  // Mockup deviations (intentional, see chat 2026-05-09):
  //   - No default-registry star/concept. Docker-internal routing handles
  //     `nginx:tag` → docker.io already; no peer (Portainer/Komodo/Coolify)
  //     adds a manage-tool override. Tracked in future_ideas_slice3.md.
  //   - No pulls-last-7d column. Needs per-pull logging + aggregate
  //     backend support; only Portainer Business has this.
  //   - No images-preview / browse-content column. Needs a per-provider
  //     registry-API adapter (Docker Registry V2 vs. ECR vs. Harbor catalog).
  //   - No provider-specific auth (JSON-key for GCR, IAM for ECR). Backend
  //     accepts userpass only; Komodo/Portainer/Coolify do the same.
  //     ECR users use `aws ecr get-login-password` as the stored password.
  //   - No test-latency display (backend doesn't measure it).
  //   - No tweaks-panel (mockup artefact).
  //
  // Permission gating uses v1 RBAC string `user.manage`; migration to
  // v2.1 (registries.view / .create / .delete) happens in the
  // consolidated backend slice at the end of the frontend-rebuild phase.
  import {
    api, ApiError,
    type Registry, type RegistryInput, type RegistryTestResult,
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Plus, Trash2, Search, X, Edit2, Repeat, Package,
    CheckCircle2, XCircle, AlertTriangle,
  } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  // ── Provider catalogue ──────────────────────────────────────────────
  // Each entry seeds the modal when picked from the rail. Help-text gives
  // the user the smallest amount of context they need to pick the right
  // credential type — Docker Hub specifically wants a PAT, GHCR wants a
  // PAT with read:packages, Harbor accepts robot accounts. Anything else
  // (GCR JSON-key, ECR IAM) goes through "Generic" with the workaround
  // documented in the Slice-3 backlog.
  type ProviderId = 'dockerhub' | 'ghcr' | 'harbor' | 'generic';
  interface ProviderMeta {
    id: ProviderId;
    label: string;
    icon: string;
    url: string;
    help: string;
  }
  const PROVIDERS: ProviderMeta[] = [
    { id: 'dockerhub', label: 'Docker Hub',       icon: '🐳', url: 'docker.io',
      help: m.registries_help_dockerhub() },
    { id: 'ghcr',      label: 'GitHub Container', icon: '🐙', url: 'ghcr.io',
      help: m.registries_help_ghcr() },
    { id: 'harbor',    label: 'Harbor',           icon: '⚓', url: 'harbor.example',
      help: m.registries_help_harbor() },
    { id: 'generic',   label: m.registries_generic_oci(), icon: '📡', url: 'registry.example',
      help: m.registries_help_generic() },
  ];

  function detectProvider(url: string): ProviderId {
    const u = url.toLowerCase();
    if (/(^|\.)docker\.io|index\.docker\.io|^docker\.io$/.test(u)) return 'dockerhub';
    if (/ghcr\.io/.test(u)) return 'ghcr';
    if (/harbor/.test(u)) return 'harbor';
    return 'generic';
  }

  // ── State ──────────────────────────────────────────────────────────
  let registries = $state<Registry[]>([]);
  let registriesLoading = $state(true);
  let search = $state('');

  // Add/Edit modal
  let showModal = $state(false);
  let editing = $state<Registry | null>(null);
  let providerId = $state<ProviderId>('dockerhub');
  let formName = $state('');
  let formUrl = $state('');
  let formUser = $state('');
  let formPassword = $state('');
  let formScopeInput = $state('');
  let formScopeTags = $state<string[]>([]);
  let busy = $state(false);

  // Inline test state — both per-row (in the table) and in-modal (after edit).
  let testingRegistryId = $state<number | null>(null);
  let modalTestResult = $state<RegistryTestResult | null>(null);
  let modalTestRunning = $state(false);

  // ── Data loading ─────────────────────────────────────────────────────
  async function loadRegistries() {
    registriesLoading = true;
    try {
      registries = await api.registries.list();
    } catch (err) {
      toast.error(m.registries_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      registriesLoading = false;
    }
  }

  // ── Derived: filter + summary stats ─────────────────────────────────
  const filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    let arr = [...registries];
    if (q) {
      arr = arr.filter((r) =>
        r.name.toLowerCase().includes(q) ||
        r.url.toLowerCase().includes(q) ||
        (r.username || '').toLowerCase().includes(q),
      );
    }
    return arr.sort((a, b) => a.name.localeCompare(b.name));
  });

  const counts = $derived.by(() => ({
    total: registries.length,
    failing: registries.filter((r) => r.last_tested_at && r.last_test_ok === false).length,
    stale: registries.filter((r) => isStale(r)).length,
  }));

  // ── Helpers ──────────────────────────────────────────────────────────
  function rotationDays(r: Registry): number {
    return Math.floor((Date.now() - new Date(r.updated_at).getTime()) / 86400000);
  }
  function rotationTone(r: Registry): 'ok' | 'warn' | 'danger' {
    const d = rotationDays(r);
    if (d > 90) return 'danger';
    if (d > 60) return 'warn';
    return 'ok';
  }
  function isStale(r: Registry): boolean {
    return rotationDays(r) > 90;
  }

  function fmtAgo(iso?: string): string {
    if (!iso) return '—';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return m.account_ago_now();
    if (d < 3600) return m.account_ago_min({ n: Math.floor(d / 60) });
    if (d < 86400) return m.account_ago_h({ n: Math.floor(d / 3600) });
    if (d < 2 * 86400) return m.account_ago_yesterday();
    return m.account_ago_d({ n: Math.floor(d / 86400) });
  }

  function providerOf(r: Registry): ProviderMeta {
    return PROVIDERS.find((p) => p.id === detectProvider(r.url)) ?? PROVIDERS[3];
  }

  // ── Modal open / pre-fill ──────────────────────────────────────────
  function resetForm() {
    editing = null;
    providerId = 'dockerhub';
    formName = '';
    formUrl = '';
    formUser = '';
    formPassword = '';
    formScopeInput = '';
    formScopeTags = [];
    modalTestResult = null;
    modalTestRunning = false;
  }

  function openNew(preset?: ProviderId) {
    resetForm();
    if (preset) {
      providerId = preset;
      const p = PROVIDERS.find((x) => x.id === preset);
      if (p) formUrl = p.url;
    }
    showModal = true;
  }

  function openEdit(r: Registry) {
    resetForm();
    editing = r;
    providerId = detectProvider(r.url);
    formName = r.name;
    formUrl = r.url;
    formUser = r.username ?? '';
    formPassword = '';
    formScopeTags = r.scope_tags ? [...r.scope_tags] : [];
    showModal = true;
  }

  function pickProvider(id: ProviderId) {
    providerId = id;
    const p = PROVIDERS.find((x) => x.id === id);
    // Only auto-replace URL if the user hasn't started typing a custom one,
    // or if it currently matches a provider default.
    if (p && (!formUrl || PROVIDERS.some((x) => x.url === formUrl))) {
      formUrl = p.url;
    }
  }

  // ── Scope-tag editing ──────────────────────────────────────────────
  function addScopeTag() {
    const t = formScopeInput.trim().toLowerCase();
    if (!t) return;
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(t)) {
      toast.error(m.registries_invalid_tag(), m.registries_invalid_tag_hint());
      return;
    }
    if (formScopeTags.includes(t)) { formScopeInput = ''; return; }
    formScopeTags = [...formScopeTags, t];
    formScopeInput = '';
  }

  function removeScopeTag(t: string) {
    formScopeTags = formScopeTags.filter((x) => x !== t);
  }

  // ── Save / delete / test ───────────────────────────────────────────
  async function save(e: Event) {
    e.preventDefault();
    if (!formName.trim() || !formUrl.trim()) return;
    busy = true;
    try {
      const payload: RegistryInput = {
        name: formName.trim(),
        url: formUrl.trim(),
        username: formUser.trim() || undefined,
        password: formPassword || undefined,
        scope_tags: formScopeTags.length ? formScopeTags : undefined,
      };
      if (editing) {
        await api.registries.update(editing.id, payload);
        toast.success(m.registries_updated_toast(), formName);
      } else {
        await api.registries.create(payload);
        toast.success(m.registries_added_toast(), formName);
      }
      showModal = false;
      await loadRegistries();
    } catch (err) {
      toast.error(m.registries_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function deleteRegistry(r: Registry) {
    if (!(await confirm.ask({
      title: m.registries_delete_title(),
      message: m.registries_delete_message({ name: r.name }),
      body: m.registries_delete_body(),
      confirmLabel: m.common_delete(), danger: true,
    }))) return;
    try {
      await api.registries.delete(r.id);
      toast.success(m.registries_deleted_toast(), r.name);
      await loadRegistries();
    } catch (err) {
      toast.error(m.registries_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function testRow(r: Registry) {
    testingRegistryId = r.id;
    try {
      const res = await api.registries.test(r.id);
      if (res.ok) toast.success(m.registries_login_ok(), r.name);
      else        toast.error(m.registries_login_failed(), res.error || m.registries_unknown_error());
      await loadRegistries();
    } catch (err) {
      toast.error(m.registries_test_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      testingRegistryId = null;
    }
  }

  // Test from inside the modal — only available when editing an existing
  // registry, since the API tests by ID. New registries: save first, then
  // test from the row.
  async function testInModal() {
    if (!editing) return;
    modalTestRunning = true;
    modalTestResult = null;
    try {
      modalTestResult = await api.registries.test(editing.id);
    } catch (err) {
      modalTestResult = { ok: false, error: err instanceof ApiError ? err.message : m.registries_request_failed() };
    } finally {
      modalTestRunning = false;
    }
  }

  $effect(() => {
    if (allowed('registries.update')) loadRegistries();
    else registriesLoading = false;
  });

  const currentProvider = $derived(PROVIDERS.find((p) => p.id === providerId) ?? PROVIDERS[3]);
</script>

<EditorialPage>
  <section class="reg">
    <!-- ───────────────────────── Header ───────────────────────── -->
    <header class="reg-header">
      <div class="reg-header-text">
        <h1 class="ed-title reg-title">{m.registries_title()}</h1>
        <p class="ed-subtitle reg-subtitle">
          {m.registries_n_configured({ n: counts.total })}{counts.failing > 0 ? ` · ${m.registries_n_failing({ n: counts.failing })}` : ''}{counts.stale > 0 ? ` · ${m.registries_n_stale({ n: counts.stale })}` : ''}
        </p>
      </div>
    </header>

    {#if !allowed('registries.update')}
      <div class="dm-card reg-permission-block">
        <Package size={18} strokeWidth={1.5} />
        <div>
          <div class="reg-perm-title">{m.registries_admin_only()}</div>
          <p class="ed-subtitle reg-perm-blurb">
            {m.registries_perm_before()} <em>registries.update</em>{m.registries_perm_after()}
          </p>
        </div>
      </div>
    {:else}
      <!-- ───────────────────── Summary strip ───────────────────── -->
      <div class="reg-summary">
        <div class="reg-metric">
          <span class="reg-metric-label">{m.registries_metric_configured()}</span>
          <span class="reg-metric-value">{counts.total}</span>
          <span class="reg-metric-meta">{m.registries_n_healthy({ n: counts.total - counts.failing })}</span>
        </div>
        <div class="reg-metric" data-tone={counts.failing > 0 ? 'warn' : 'ok'}>
          <span class="reg-metric-label">{m.registries_metric_failing()}</span>
          <span class="reg-metric-value">{counts.failing}</span>
          <span class="reg-metric-meta">
            {counts.failing > 0 ? m.registries_needs_attention() : m.registries_all_clear()}
          </span>
        </div>
        <div class="reg-metric" data-tone={counts.stale > 0 ? 'warn' : 'ok'}>
          <span class="reg-metric-label">{m.registries_metric_stale()}</span>
          <span class="reg-metric-value">{counts.stale}</span>
          <span class="reg-metric-meta">{m.registries_stale_meta()}</span>
        </div>
      </div>

      <!-- ───────────────────── Provider rail ───────────────────── -->
      <div class="reg-rail">
        <div class="reg-rail-label">{m.registries_rail_label()}</div>
        <div class="reg-rail-grid">
          {#each PROVIDERS as p (p.id)}
            <button
              type="button"
              class="reg-rail-card"
              onclick={() => openNew(p.id)}
            >
              <span class="reg-rail-icon">{p.icon}</span>
              <div class="reg-rail-text">
                <div class="reg-rail-card-label">{p.label}</div>
                <div class="reg-rail-card-url">{p.url}</div>
              </div>
              <Plus size={11} strokeWidth={1.5} class="reg-rail-plus" />
            </button>
          {/each}
        </div>
      </div>

      <!-- ───────────────────── Toolbar ───────────────────── -->
      <div class="reg-toolbar">
        <div class="reg-search">
          <Search size={12} strokeWidth={1.5} class="reg-search-icon" />
          <input
            type="text"
            class="ed-underline-input reg-search-input"
            placeholder={m.registries_search_placeholder()}
            bind:value={search}
          />
        </div>
        <span class="reg-spacer"></span>
        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          onclick={() => openNew()}
        >
          <Plus size={12} strokeWidth={1.5} /> {m.registries_add_registry()}
        </button>
      </div>

      <!-- ───────────────────── Table ───────────────────── -->
      {#if registriesLoading}
        <div class="reg-loading">
          <Skeleton width="100%" height="6rem" />
        </div>
      {:else if filtered.length === 0}
        <div class="reg-empty">
          {#if search}
            <Eyebrow>{m.registries_no_match()}</Eyebrow>
            <p class="reg-empty-title">{m.registries_no_match_title({ search })}</p>
          {:else}
            <Eyebrow>{m.registries_empty()}</Eyebrow>
            <p class="reg-empty-title">{m.registries_empty_title()}</p>
            <p class="ed-subtitle reg-empty-blurb">
              {m.registries_empty_blurb()}
            </p>
          {/if}
        </div>
      {:else}
        <div class="reg-table">
          <div class="reg-row reg-row-head">
            <span>{m.registries_col_label()}</span>
            <span>{m.registries_col_url_user()}</span>
            <span>{m.registries_col_rotation()}</span>
            <span>{m.registries_col_last_test()}</span>
            <span>{m.registries_col_scope()}</span>
            <span class="reg-col-action">·</span>
          </div>
          {#each filtered as r (r.id)}
            {@const p = providerOf(r)}
            {@const days = rotationDays(r)}
            {@const rTone = rotationTone(r)}
            <div class="reg-row">
              <div class="reg-cell-name">
                <span class="reg-icon">{p.icon}</span>
                <div class="reg-name-text">
                  <button
                    type="button"
                    class="reg-name"
                    onclick={() => openEdit(r)}
                  >{r.name}</button>
                  <span class="reg-type">{p.label}</span>
                </div>
              </div>

              <div class="reg-cell-url">
                <span class="reg-url">{r.url}</span>
                <span class="reg-user">{r.username || '—'}</span>
              </div>

              <div class="reg-cell-rotation" data-tone={rTone}>
                {#if r.has_password}
                  <span class="reg-rotation-text">{m.registries_rotated_ago({ n: days })}</span>
                  {#if rTone !== 'ok'}
                    <span class="reg-rotation-note">
                      {rTone === 'danger' ? m.registries_rotate_danger() : m.registries_rotate_warn()}
                    </span>
                  {/if}
                {:else}
                  <span class="reg-rotation-text reg-muted">{m.registries_no_token()}</span>
                  <span class="reg-rotation-note">{m.registries_edit_to_add()}</span>
                {/if}
              </div>

              <div class="reg-cell-test">
                {#if r.last_tested_at}
                  <div class="reg-test-line">
                    {#if r.last_test_ok}
                      <CheckCircle2 size={12} strokeWidth={1.6} class="reg-test-ok" />
                    {:else}
                      <XCircle size={12} strokeWidth={1.6} class="reg-test-fail" />
                    {/if}
                    <span class="reg-test-time">{fmtAgo(r.last_tested_at)}</span>
                  </div>
                  {#if !r.last_test_ok && r.last_test_error}
                    <span class="reg-test-err" title={r.last_test_error}>{r.last_test_error}</span>
                  {/if}
                {:else}
                  <span class="reg-muted reg-test-time">{m.registries_never()}</span>
                {/if}
              </div>

              <div class="reg-cell-scope">
                {#if r.scope_tags && r.scope_tags.length > 0}
                  <div class="reg-scope-tags">
                    {#each r.scope_tags as t}
                      <span class="reg-scope-tag">{t}</span>
                    {/each}
                  </div>
                {:else}
                  <span class="reg-muted">{m.common_all_hosts()}</span>
                {/if}
              </div>

              <div class="reg-cell-action">
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-xs"
                  onclick={() => testRow(r)}
                  disabled={!r.has_password || testingRegistryId === r.id}
                  title={r.has_password ? m.registries_test_login() : m.registries_no_password_title()}
                >
                  <Repeat size={11} strokeWidth={1.5} />
                  {testingRegistryId === r.id ? '…' : m.common_test()}
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-xs"
                  onclick={() => openEdit(r)}
                  title={m.common_edit()}
                >
                  <Edit2 size={11} strokeWidth={1.5} />
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-xs reg-revoke-btn"
                  onclick={() => deleteRegistry(r)}
                  title={m.common_delete()}
                >
                  <Trash2 size={11} strokeWidth={1.5} />
                </button>
              </div>
            </div>
          {/each}
        </div>
      {/if}

      <div class="reg-hint">
        <div class="reg-hint-title">{m.registries_how_title()}</div>
        <p class="reg-hint-blurb">
          {m.registries_how_1()}
          <code class="reg-hint-code">ghcr.io/org/app:tag</code>{m.registries_how_2()}<code class="reg-hint-code">ghcr.io</code>{m.registries_how_3()}
        </p>
      </div>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── Add / Edit Modal ───────────────────────── -->
<EditorialModal
  bind:open={showModal}
  eyebrow={editing ? m.registries_eyebrow_edit() : m.registries_add_registry()}
  width={580}
>
  {#snippet title()}
    {#if editing}
      {m.registries_title_edit_before()} <em>{editing.name}</em> {m.registries_title_edit_after()}
    {:else}
      {m.registries_title_connect_before()} <em class="ed-accent">{currentProvider.label}</em> {m.registries_title_connect_after()}
    {/if}
  {/snippet}

  <form id="reg-form" class="reg-form" onsubmit={save}>
    {#if !editing}
      <Field label={m.registries_provider()}>
        <div class="reg-provider-grid">
          {#each PROVIDERS as p (p.id)}
            <button
              type="button"
              class="reg-provider-card"
              class:reg-provider-active={providerId === p.id}
              onclick={() => pickProvider(p.id)}
            >
              <span class="reg-provider-icon">{p.icon}</span>
              <span class="reg-provider-label">{p.label}</span>
            </button>
          {/each}
        </div>
      </Field>
    {/if}

    <div class="reg-help">
      <span class="reg-help-icon">ⓘ</span>
      <span class="reg-help-text">{currentProvider.help}</span>
    </div>

    <div class="reg-grid-2">
      <Field label={m.registries_display_label()} hint={m.registries_display_label_hint()}>
        <input
          class="dm-input"
          bind:value={formName}
          placeholder="GitHub — acme"
        />
      </Field>
      <Field label={m.registries_url()} hint={m.registries_url_hint()}>
        <input
          class="dm-input acc-input-mono"
          bind:value={formUrl}
          placeholder={currentProvider.url}
        />
      </Field>
    </div>

    <div class="reg-grid-2">
      <Field label={m.registries_username()}>
        <input
          class="dm-input acc-input-mono"
          bind:value={formUser}
          placeholder={providerId === 'ghcr' ? 'github-handle' : 'deploy-bot'}
        />
      </Field>
      <Field
        label={editing?.has_password ? m.registries_new_token_label() : m.registries_password_token()}
        hint={m.registries_password_hint()}
      >
        <input
          class="dm-input acc-input-mono"
          type="password"
          bind:value={formPassword}
          placeholder={editing?.has_password ? '••••••••' : m.registries_password_placeholder()}
        />
      </Field>
    </div>

    <Field label={m.registries_scope_label()} hint={m.registries_scope_hint()}>
      <div class="reg-scope-row">
        <input
          type="text"
          class="dm-input reg-scope-input"
          placeholder="prod"
          bind:value={formScopeInput}
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addScopeTag(); } }}
        />
        <button
          type="button"
          class="dm-btn dm-btn-secondary dm-btn-sm"
          onclick={addScopeTag}
        >
          {m.common_add()}
        </button>
      </div>
      {#if formScopeTags.length > 0}
        <div class="reg-scope-pills">
          {#each formScopeTags as t}
            <span class="reg-scope-pill">
              {t}
              <button type="button" onclick={() => removeScopeTag(t)} aria-label={m.common_remove()} class="reg-scope-pill-x">
                <X size={10} strokeWidth={1.5} />
              </button>
            </span>
          {/each}
        </div>
      {/if}
    </Field>

    {#if editing}
      <div class="reg-test-row">
        <button
          type="button"
          class="dm-btn dm-btn-secondary dm-btn-sm"
          onclick={testInModal}
          disabled={!editing.has_password || modalTestRunning}
        >
          <Repeat size={12} strokeWidth={1.5} />
          {modalTestRunning ? m.registries_testing() : m.registries_test_connection()}
        </button>
        {#if !editing.has_password}
          <span class="reg-test-hint">{m.registries_save_password_first()}</span>
        {/if}
      </div>

      {#if modalTestResult}
        <div
          class="reg-test-result"
          data-state={modalTestResult.ok ? 'ok' : 'fail'}
        >
          {#if modalTestResult.ok}
            <CheckCircle2 size={14} strokeWidth={1.6} />
            <span>{modalTestResult.identity ? m.registries_connection_ok_identity() : m.registries_connection_ok()}</span>
          {:else}
            <XCircle size={14} strokeWidth={1.6} />
            <span>{modalTestResult.error || m.registries_connection_failed()}</span>
          {/if}
        </div>
      {/if}
    {/if}
  </form>

  {#snippet footer()}
    <span></span>
    <div class="reg-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showModal = false)}>
        {m.common_cancel()}
      </button>
      <button
        type="submit"
        form="reg-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={!formName.trim() || !formUrl.trim() || busy}
      >
        {busy ? m.common_saving() : editing ? m.common_save() : m.registries_add_registry()}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .reg {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .reg-header {
    display: flex;
    align-items: flex-end;
    gap: 24px;
    flex-wrap: wrap;
  }
  .reg-header-text { min-width: 0; max-width: 70ch; }
  .reg-title {
    font-size: 30px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .reg-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }

  /* ── Permission gate ────────────────────────────────────────── */
  .reg-permission-block {
    padding: 22px;
    display: flex;
    gap: 14px;
    align-items: flex-start;
    color: var(--fg-muted);
  }
  .reg-perm-title {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .reg-perm-blurb { margin-top: 4px; }

  /* ── Summary metrics ────────────────────────────────────────── */
  .reg-summary {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 10px;
  }
  .reg-metric {
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .reg-metric[data-tone="warn"] {
    border-color: color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
  }
  .reg-metric-label {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .reg-metric-value {
    font-size: 22px;
    color: var(--fg);
    font-weight: 500;
    line-height: 1.2;
  }
  .reg-metric[data-tone="warn"] .reg-metric-value {
    color: var(--color-warning-400);
  }
  .reg-metric-meta {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  /* ── Provider rail ──────────────────────────────────────────── */
  .reg-rail-label {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-bottom: 8px;
  }
  .reg-rail-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 8px;
  }
  .reg-rail-card {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 5px;
    cursor: pointer;
    text-align: left;
    color: inherit;
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .reg-rail-card:hover {
    border-color: var(--border-strong);
    background: var(--surface-hover);
  }
  .reg-rail-icon {
    font-size: 18px;
    width: 28px;
    height: 28px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    flex-shrink: 0;
  }
  .reg-rail-text { min-width: 0; flex: 1; }
  .reg-rail-card-label {
    font-size: 12.5px;
    color: var(--fg);
    font-weight: 500;
  }
  .reg-rail-card-url {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .reg-rail-card :global(.reg-rail-plus) { color: var(--fg-subtle); }

  /* ── Toolbar ────────────────────────────────────────────────── */
  .reg-toolbar {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .reg-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .reg-search :global(.reg-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .reg-search-input {
    padding-left: 18px;
    font-size: 12.5px;
    font-family: var(--font-mono);
  }
  .reg-spacer { flex: 1; }

  /* ── Table ──────────────────────────────────────────────────── */
  .reg-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .reg-row {
    display: grid;
    grid-template-columns: minmax(170px, 1.2fr) minmax(170px, 1.2fr) 120px 120px minmax(120px, 1fr) 150px;
    gap: 12px;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .reg-row:last-child { border-bottom: 0; }
  .reg-row-head {
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
    font-size: 11.5px;
    color: var(--fg-subtle);
    padding: 10px 16px;
  }

  .reg-cell-name {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .reg-icon {
    font-size: 16px;
    width: 26px;
    height: 26px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    flex-shrink: 0;
  }
  .reg-name-text { min-width: 0; flex: 1; }
  .reg-name {
    background: transparent;
    border: 0;
    padding: 0;
    cursor: pointer;
    font: inherit;
    text-align: left;
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
    width: 100%;
  }
  .reg-name:hover { color: var(--accent-fg); }
  .reg-type {
    font-size: 11.5px;
    color: var(--fg-subtle);
    display: block;
  }

  .reg-cell-url { min-width: 0; }
  .reg-url {
    display: block;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .reg-user {
    font-size: 11.5px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
  }

  .reg-cell-rotation {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .reg-rotation-text {
    font-size: 11px;
    color: var(--fg-muted);
  }
  .reg-cell-rotation[data-tone="warn"] .reg-rotation-text { color: var(--color-warning-400); }
  .reg-cell-rotation[data-tone="danger"] .reg-rotation-text { color: var(--color-danger-400); }
  .reg-rotation-note {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .reg-cell-rotation[data-tone="warn"] .reg-rotation-note { color: var(--color-warning-400); }
  .reg-cell-rotation[data-tone="danger"] .reg-rotation-note { color: var(--color-danger-400); }

  .reg-cell-test {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .reg-test-line {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .reg-cell-test :global(.reg-test-ok) { color: var(--color-success-400); }
  .reg-cell-test :global(.reg-test-fail) { color: var(--color-danger-400); }
  .reg-test-time {
    font-size: 11px;
    color: var(--fg-muted);
  }
  .reg-test-err {
    font-size: 11.5px;
    color: var(--color-danger-400);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .reg-cell-scope { min-width: 0; }
  .reg-scope-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .reg-scope-tag {
    font-family: var(--font-mono);
    font-size: 11.5px;
    padding: 1px 6px;
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    color: var(--fg-muted);
  }

  .reg-muted { color: var(--fg-subtle); font-size: 11px; }

  .reg-cell-action {
    display: inline-flex;
    justify-content: flex-end;
    gap: 4px;
  }
  .reg-revoke-btn { color: var(--color-danger-400); }

  .reg-col-action { display: flex; justify-content: flex-end; }

  /* ── Empty / loading ────────────────────────────────────────── */
  .reg-loading { padding: 0; }
  .reg-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
  }
  .reg-empty-title {
    margin-top: 10px;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .reg-empty-blurb {
    margin-top: 6px;
    margin-inline: auto;
    max-width: 50ch;
  }

  /* ── Hint footer ────────────────────────────────────────────── */
  .reg-hint {
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 5px;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .reg-hint-title {
    font-weight: 500;
    color: var(--fg);
    margin-bottom: 4px;
  }
  .reg-hint-code {
    font-family: var(--font-mono);
    font-size: 11px;
    background: var(--bg-elevated);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border);
  }

  /* ── Modal ──────────────────────────────────────────────────── */
  .reg-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .reg-provider-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }
  .reg-provider-card {
    padding: 8px 10px;
    display: flex;
    align-items: center;
    gap: 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    color: var(--fg-muted);
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .reg-provider-card:hover { border-color: var(--border-strong); }
  .reg-provider-active {
    background: var(--accent-bg);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--fg);
  }
  .reg-provider-icon { font-size: 16px; }
  .reg-provider-label { font-size: 12px; font-weight: 500; }

  .reg-help {
    padding: 8px 10px;
    background: var(--bg-elevated);
    border: 1px dashed var(--border);
    border-radius: 4px;
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.55;
    display: flex;
    gap: 8px;
    align-items: flex-start;
  }
  .reg-help-icon {
    flex-shrink: 0;
    font-size: 13px;
    line-height: 1;
  }
  .reg-help-text { min-width: 0; }

  .reg-grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }

  .reg-scope-row {
    display: flex;
    gap: 6px;
  }
  .reg-scope-input { flex: 1; }
  .reg-scope-pills {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 6px;
  }
  .reg-scope-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 11.5px;
    padding: 1px 6px;
    border: 1px solid var(--border);
    border-radius: 3px;
    color: var(--fg-muted);
  }
  .reg-scope-pill-x {
    background: transparent;
    border: 0;
    padding: 0;
    cursor: pointer;
    color: var(--fg-subtle);
    display: inline-flex;
  }
  .reg-scope-pill-x:hover { color: var(--color-danger-400); }

  .reg-test-row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-top: 6px;
    border-top: 1px solid var(--border-subtle);
  }
  .reg-test-hint {
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .reg-test-result {
    padding: 10px 12px;
    border-radius: 4px;
    font-size: 11.5px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .reg-test-result[data-state="ok"] {
    border: 1px solid color-mix(in srgb, var(--color-success-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 5%, var(--surface));
    color: var(--color-success-400);
  }
  .reg-test-result[data-state="fail"] {
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--surface));
    color: var(--color-danger-400);
  }

  .reg-modal-actions {
    display: flex;
    gap: 8px;
  }

  /* Reuse helper from account page if present */
  :global(.acc-input-mono) { font-family: var(--font-mono); }

  @media (max-width: 880px) {
    .reg-summary { grid-template-columns: 1fr; }
    .reg-row {
      grid-template-columns: minmax(0, 1.4fr) auto;
      grid-auto-flow: row;
    }
    .reg-cell-url,
    .reg-cell-rotation,
    .reg-cell-test,
    .reg-cell-scope { grid-column: 1 / -1; }
    .reg-row-head { display: none; }
    .reg-grid-2,
    .reg-provider-grid { grid-template-columns: 1fr; }
  }
</style>
