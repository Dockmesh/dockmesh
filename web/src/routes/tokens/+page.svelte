<script lang="ts">
  // API tokens — editorial rebuild based on `Dockmesh Wizard/tokens.jsx`.
  //
  // Mockup deviations (intentional, see chat 2026-05-09):
  //   - No Personal vs Service token split. Direct peers (Komodo,
  //     Portainer Business, Coolify, Rancher) all use a single
  //     "token bound to user" model. Splitting requires a service-
  //     account entity + acts-as audit trail — overkill for this tool's
  //     scale and not what users coming from Komodo/Portainer expect.
  //   - No 7-day usage sparkline. Same peers don't show per-token usage
  //     charts — needs an aggregate table or per-request logging that
  //     isn't worth the engineering for this slice.
  //   - No TabStrip across settings cluster — those are separate routes.
  import { api, ApiError, type ApiToken, type CustomRole, type RoleCatalog, type Scope } from '$lib/api';
  import { scopeLabel, roleOneLine } from '$lib/access';
  import ScopePicker from '$lib/components/access/ScopePicker.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyToClipboard } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Plus, Trash2, KeyRound, Copy, Search, AlertCircle,
  } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime } from '$lib/i18n';

  let apiTokens = $state<ApiToken[]>([]);
  let apiTokensLoading = $state(true);
  let roles = $state<CustomRole[]>([]);

  // Search + filter
  let search = $state('');

  // Create form state
  let showCreate = $state(false);
  let newName = $state('');
  let newRole = $state('');
  let newScope = $state<Scope>({ type: 'global' });
  let catalog = $state<RoleCatalog | null>(null);
  let newExpiresKey = $state<'30d' | '90d' | '180d' | '365d' | 'never'>('90d');
  let creating = $state(false);

  // Reveal-once card state — set by a successful create. We render the
  // card inline above the toolbar (not a modal) and auto-mask the value
  // after 60 seconds, mirroring how 1Password / GitLab present a
  // "you'll never see this again" surface.
  let freshToken = $state<{ name: string; value: string } | null>(null);
  let freshRevealed = $state(false);
  let freshSeconds = $state(60);
  let freshCopied = $state(false);
  let freshTimer: ReturnType<typeof setInterval> | null = null;

  $effect(() => {
    if (!freshToken) return;
    freshRevealed = true;
    freshSeconds = 60;
    if (freshTimer) clearInterval(freshTimer);
    freshTimer = setInterval(() => {
      freshSeconds = freshSeconds - 1;
      if (freshSeconds <= 0) {
        freshRevealed = false;
        if (freshTimer) { clearInterval(freshTimer); freshTimer = null; }
      }
    }, 1000);
    return () => {
      if (freshTimer) { clearInterval(freshTimer); freshTimer = null; }
    };
  });

  // ── Data loading ─────────────────────────────────────────────────────
  async function loadApiTokens() {
    apiTokensLoading = true;
    try {
      apiTokens = await api.apiTokens.list();
    } catch (err) {
      toast.error(m.tokens_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      apiTokensLoading = false;
    }
  }

  async function loadRoles() {
    // Without Users & roles: View, offer the roles the caller holds itself.
    if (allowed('roles.view')) {
      try { roles = await api.roles.list(); } catch { /* the picker then offers no restriction only */ }
    } else {
      const me = await api.users.me().catch(() => null);
      const own = [...new Set((me?.assignments ?? []).map((a) => a.role))];
      roles = own.map((name) => ({ name, display: name, description: '', builtin: false, levels: {}, extras: [], permissions: [] }) as CustomRole);
    }
    catalog = await api.roles.catalog().catch(() => null);
  }

  // ── Filter + stats ───────────────────────────────────────────────────
  function isActive(t: ApiToken): boolean {
    if (t.revoked_at) return false;
    if (t.expires_at && new Date(t.expires_at).getTime() < Date.now()) return false;
    return true;
  }

  function isExpiringSoon(t: ApiToken): boolean {
    if (!isActive(t)) return false;
    if (!t.expires_at) return false;
    const days = (new Date(t.expires_at).getTime() - Date.now()) / 86400000;
    return days >= 0 && days <= 30;
  }

  function isStale(t: ApiToken): boolean {
    if (!isActive(t)) return false;
    if (!t.last_used_at) {
      // Never used — count as stale only if older than 90 days.
      return Date.now() - new Date(t.created_at).getTime() > 90 * 86400000;
    }
    return Date.now() - new Date(t.last_used_at).getTime() > 90 * 86400000;
  }

  const filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    let arr = [...apiTokens];
    if (q) {
      arr = arr.filter((t) =>
        t.name.toLowerCase().includes(q) || (t.prefix || '').toLowerCase().includes(q),
      );
    }
    // Active first, then expiring soon, then stale, then revoked/expired.
    arr.sort((a, b) => {
      const aActive = isActive(a) ? 0 : 1;
      const bActive = isActive(b) ? 0 : 1;
      if (aActive !== bActive) return aActive - bActive;
      return +new Date(b.created_at) - +new Date(a.created_at);
    });
    return arr;
  });

  const counts = $derived.by(() => ({
    active: apiTokens.filter(isActive).length,
    expiring: apiTokens.filter(isExpiringSoon).length,
    stale: apiTokens.filter(isStale).length,
    total: apiTokens.length,
  }));

  // ── Actions ──────────────────────────────────────────────────────────
  async function createApiToken(e: Event) {
    e.preventDefault();
    if (!newName.trim()) return;
    creating = true;
    const expiresMap = { '30d': 30, '90d': 90, '180d': 180, '365d': 365, 'never': 0 } as const;
    try {
      const res = await api.apiTokens.create({
        name: newName.trim(),
        role: newRole || undefined,
        scope: newRole ? newScope : undefined,
        expires_in_days: expiresMap[newExpiresKey],
      });
      freshToken = { name: res.name, value: res.token };
      freshCopied = false;
      showCreate = false;
      newName = '';
      newRole = '';
      newScope = { type: 'global' };
      newExpiresKey = '90d';
      await loadApiTokens();
    } catch (err) {
      toast.error(m.tokens_create_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  async function revokeApiToken(id: number, name: string) {
    if (!(await confirm.ask({
      title: m.tokens_revoke_title(),
      message: m.tokens_revoke_message({ name }),
      body: m.tokens_revoke_body(),
      confirmLabel: m.tokens_revoke(), danger: true,
    }))) return;
    try {
      await api.apiTokens.revoke(id);
      toast.success(m.tokens_revoked_toast());
      await loadApiTokens();
    } catch (err) {
      toast.error(m.tokens_revoke_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function copyFreshToken() {
    if (!freshToken || !freshRevealed) return;
    const ok = await copyToClipboard(freshToken.value);
    if (ok) {
      freshCopied = true;
      setTimeout(() => (freshCopied = false), 1800);
    } else {
      toast.error(m.tokens_copy_failed(), m.tokens_copy_failed_hint());
    }
  }

  function dismissFresh() {
    freshToken = null;
    freshRevealed = false;
    if (freshTimer) { clearInterval(freshTimer); freshTimer = null; }
  }

  // ── Helpers ──────────────────────────────────────────────────────────
  function fmtAgo(iso?: string): string {
    if (!iso) return '—';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return m.account_ago_now();
    if (d < 3600) return m.account_ago_min({ n: Math.floor(d / 60) });
    if (d < 86400) return m.account_ago_h({ n: Math.floor(d / 3600) });
    if (d < 2 * 86400) return m.account_ago_yesterday();
    return m.account_ago_d({ n: Math.floor(d / 86400) });
  }

  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    return fmtDateTime(iso, {
      year: 'numeric', month: 'short', day: '2-digit',
    });
  }

  function expiresLabel(t: ApiToken): string {
    if (!t.expires_at) return m.tokens_never();
    return fmtDate(t.expires_at);
  }

  function expiresTone(t: ApiToken): 'warn' | 'muted' | 'normal' {
    if (!t.expires_at) return 'muted';
    if (isExpiringSoon(t)) return 'warn';
    return 'normal';
  }

  function scopePillClass(role: string): string {
    if (!role || role === 'admin' || role === 'host-admin') return 'dm-pill dm-pill-warning';
    return 'dm-pill dm-pill-neutral';
  }

  function restrictionLabel(t: ApiToken): string {
    if (!t.role) return m.tokens_owners_access();
    const r = roles.find((x) => x.name === t.role);
    return m.tokens_role_on_scope({ role: r?.display ?? t.role, scope: scopeLabel(t.scope) });
  }

  function statusPill(t: ApiToken): { cls: string; label: string } | null {
    if (t.revoked_at) return { cls: 'dm-pill dm-pill-neutral', label: m.tokens_status_revoked() };
    if (t.expires_at && new Date(t.expires_at).getTime() < Date.now()) {
      return { cls: 'dm-pill dm-pill-neutral', label: m.tokens_status_expired() };
    }
    return null;
  }

  const pickableRoles = $derived<Array<Pick<CustomRole, 'name' | 'display'> & { blurb: string }>>([
    { name: '', display: m.tokens_no_restriction(), blurb: m.tokens_no_restriction_blurb() },
    ...roles.map((r) => ({ name: r.name, display: r.display, blurb: roleOneLine(catalog, r) || r.description || '' }))
  ]);

  $effect(() => {
    if (allowed('tokens.manage_others')) {
      loadApiTokens();
      loadRoles();
    } else {
      apiTokensLoading = false;
    }
  });
</script>

<EditorialPage>
  <section class="tok">
    <!-- ───────────────────────── Header ───────────────────────── -->
    <header class="tok-header">
      <div class="tok-header-text">
        <h1 class="ed-title tok-title">{m.tokens_title()}</h1>
        <p class="ed-subtitle tok-subtitle">
          {#if counts.total === 0}
            {m.tokens_none_yet()}
          {:else}
            {m.tokens_n_active({ n: counts.active })}{counts.expiring > 0 ? ` · ${m.tokens_n_expiring({ n: counts.expiring })}` : ''}{counts.stale > 0 ? ` · ${m.tokens_n_stale({ n: counts.stale })}` : ''}
          {/if}
        </p>
      </div>
    </header>

    {#if !allowed('tokens.manage_others')}
      <div class="dm-card tok-permission-block">
        <KeyRound size={18} strokeWidth={1.5} />
        <div>
          <div class="tok-perm-title">{m.tokens_admin_only()}</div>
          <p class="ed-subtitle tok-perm-blurb">
            {m.tokens_perm_before()} <em>tokens.manage_others</em>{m.tokens_perm_after()}
          </p>
        </div>
      </div>
    {:else}
      <!-- ─────────────── Reveal-Once Card (after create) ─────────────── -->
      {#if freshToken}
        <div class="tok-fresh">
          <div class="tok-fresh-head">
            <div class="tok-fresh-text">
              <Eyebrow>{m.tokens_created_eyebrow()}</Eyebrow>
              <h3 class="ed-title tok-fresh-title">
                {m.tokens_fresh_before()} <em class="ed-accent">{freshToken.name}</em>{m.tokens_fresh_after()}
              </h3>
              <p class="ed-subtitle tok-fresh-blurb">
                {m.tokens_never_stored()}
                {#if freshRevealed}{m.tokens_auto_masking({ n: freshSeconds })}{:else}{m.tokens_masked()}{/if}
              </p>
            </div>
            <button
              type="button"
              class="dm-btn dm-btn-ghost dm-btn-sm"
              onclick={dismissFresh}
            >
              {m.tokens_dismiss()}
            </button>
          </div>

          <div class="tok-fresh-row">
            <code class="tok-fresh-value" class:masked={!freshRevealed}>
              {freshRevealed ? freshToken.value : '••••••••••••••••••••••••••••••••'}
            </code>
            <button
              type="button"
              class="dm-btn dm-btn-secondary dm-btn-xs"
              onclick={copyFreshToken}
              disabled={!freshRevealed}
            >
              <Copy size={11} strokeWidth={1.5} /> {freshCopied ? m.tokens_copied() : m.tokens_copy()}
            </button>
          </div>
        </div>
      {/if}

      <!-- ───────────────────── Toolbar ───────────────────── -->
      <div class="tok-toolbar">
        <div class="tok-search">
          <Search size={12} strokeWidth={1.5} class="tok-search-icon" />
          <input
            type="text"
            class="ed-underline-input tok-search-input"
            placeholder={m.tokens_filter_placeholder()}
            bind:value={search}
          />
        </div>

        <span class="tok-spacer"></span>

        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          onclick={() => (showCreate = true)}
        >
          <Plus size={12} strokeWidth={1.5} /> {m.tokens_new_token()}
        </button>
      </div>

      <!-- ───────────────────── Table ───────────────────── -->
      {#if apiTokensLoading}
        <div class="tok-loading">
          <Skeleton width="100%" height="6rem" />
        </div>
      {:else if filtered.length === 0}
        <div class="tok-empty">
          {#if search}
            <Eyebrow>{m.tokens_no_match()}</Eyebrow>
            <p class="tok-empty-title">{m.tokens_no_match_title({ search })}</p>
          {:else}
            <Eyebrow>{m.tokens_empty()}</Eyebrow>
            <p class="tok-empty-title">{m.tokens_empty_title()}</p>
            <p class="ed-subtitle tok-empty-blurb">
              {m.tokens_empty_blurb()}
            </p>
          {/if}
        </div>
      {:else}
        <div class="tok-table">
          <div class="tok-row tok-row-head">
            <span>{m.tokens_col_name_prefix()}</span>
            <span>{m.tokens_col_scope()}</span>
            <span>{m.tokens_col_last_used()}</span>
            <span>{m.tokens_col_created()}</span>
            <span>{m.tokens_col_expires()}</span>
            <span class="tok-col-action">·</span>
          </div>
          {#each filtered as t (t.id)}
            {@const status = statusPill(t)}
            <div class="tok-row" class:tok-row-stale={isStale(t)} class:tok-row-revoked={!!status}>
              <div class="tok-cell-name">
                <div class="tok-name-line">
                  <span class="tok-name">{t.name}</span>
                  {#if status}
                    <span class={status.cls + ' tok-mini-pill'}>
                      <span class="dm-pill-dot"></span>{status.label}
                    </span>
                  {:else if isStale(t)}
                    <span class="dm-pill dm-pill-neutral tok-mini-pill">
                      <span class="dm-pill-dot"></span>{m.tokens_pill_stale()}
                    </span>
                  {:else if isExpiringSoon(t)}
                    <span class="dm-pill dm-pill-warning tok-mini-pill">
                      <span class="dm-pill-dot"></span>{m.tokens_pill_expiring()}
                    </span>
                  {/if}
                  {#if !t.revoked_at && t.legacy}
                    <span
                      class="dm-pill dm-pill-warning tok-mini-pill"
                      title={m.tokens_legacy_title()}
                    >
                      <span class="dm-pill-dot"></span>{m.tokens_pill_legacy()}
                    </span>
                  {:else if !t.revoked_at && t.owner_missing}
                    <span
                      class="dm-pill dm-pill-danger tok-mini-pill"
                      title={m.tokens_owner_missing_title()}
                    >
                      <span class="dm-pill-dot"></span>{m.tokens_pill_owner_deleted()}
                    </span>
                  {/if}
                </div>
                <span class="tok-prefix">{t.prefix}…</span>
              </div>

              <div class="tok-cell-scope">
                <span class={scopePillClass(t.role) + ' tok-scope-pill'} title={t.role ? m.tokens_restricted_title() : m.tokens_unrestricted_title()}>
                  <span class="dm-pill-dot"></span>{restrictionLabel(t)}
                </span>
              </div>

              <span class="tok-cell-mono">
                {fmtAgo(t.last_used_at)}
                {#if t.last_used_ip}<span class="tok-ip">· {t.last_used_ip}</span>{/if}
              </span>

              <span class="tok-cell-mono tok-muted">{fmtDate(t.created_at)}</span>

              <span class="tok-cell-mono" data-tone={expiresTone(t)}>
                {expiresLabel(t)}
              </span>

              <div class="tok-cell-action">
                {#if !t.revoked_at}
                  <button
                    type="button"
                    class="dm-btn dm-btn-ghost dm-btn-xs tok-revoke-btn"
                    onclick={() => revokeApiToken(t.id, t.name)}
                  >
                    {m.tokens_revoke()}
                  </button>
                {:else}
                  <span class="tok-dash">—</span>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Footer hint -->
      <div class="tok-hint">
        <div class="tok-hint-title">{m.tokens_hint_title()}</div>
        <p class="tok-hint-blurb">
          {m.tokens_hint_before()} <code class="tok-hint-code">Authorization: Bearer dmt_…</code>
          {m.tokens_hint_after()}
        </p>
      </div>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── Create Modal ───────────────────────── -->
<EditorialModal
  bind:open={showCreate}
  eyebrow={m.tokens_new_token()}
  width={560}
>
  {#snippet title()}
    {m.tokens_new_api_token()}
  {/snippet}

  <form id="tok-create-form" class="tok-create" onsubmit={createApiToken}>
    <Field label={m.tokens_label()} hint={m.tokens_label_hint()}>
      <input
        class="dm-input"
        bind:value={newName}
        placeholder="github actions · acme-api"
      />
    </Field>

    <Field label={m.tokens_restriction()} hint={m.tokens_restriction_hint()}>
      <div class="tok-scope-grid">
        {#each pickableRoles as r (r.name)}
          <button
            type="button"
            class="tok-scope-card"
            class:tok-scope-active={newRole === r.name}
            onclick={() => (newRole = r.name)}
          >
            <span class="tok-scope-card-label">{r.display || r.name}</span>
            <span class="tok-scope-card-blurb">{r.blurb}</span>
          </button>
        {/each}
      </div>
    </Field>
    {#if newRole}
      <ScopePicker bind:value={newScope} label={m.tokens_restricted_to()} />
    {/if}

    <Field label={m.tokens_expires()} hint={m.tokens_expires_hint()}>
      <div class="tok-expires-row">
        {#each [['30d', m.tokens_exp_30d()], ['90d', m.tokens_exp_90d()], ['180d', m.tokens_exp_180d()], ['365d', m.tokens_exp_365d()], ['never', m.tokens_never()]] as [key, label]}
          <button
            type="button"
            class="tok-expires-pill"
            class:tok-expires-active={newExpiresKey === key}
            onclick={() => (newExpiresKey = key as typeof newExpiresKey)}
          >
            {label}
          </button>
        {/each}
      </div>
    </Field>
  </form>

  {#snippet footer()}
    <span class="tok-modal-foothint">
      {m.tokens_footer_before()} <em class="ed-accent">{m.tokens_footer_once()}</em>{m.tokens_footer_after()}
    </span>
    <div class="tok-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showCreate = false)}>
        {m.common_cancel()}
      </button>
      <button
        type="submit"
        form="tok-create-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={!newName.trim() || creating}
      >
        {creating ? m.tokens_generating() : m.tokens_generate()}
      </button>
    </div>
  {/snippet}
</EditorialModal>

<style>
  .tok {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .tok-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .tok-header-text { min-width: 0; max-width: 70ch; }
  .tok-title {
    font-size: 38px;
    line-height: 1.05;
    margin-top: 12px;
  }
  .tok-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }

  /* ── Permission gate card ───────────────────────────────────── */
  .tok-permission-block {
    padding: 22px;
    display: flex;
    gap: 14px;
    align-items: flex-start;
    color: var(--fg-muted);
  }
  .tok-perm-title {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .tok-perm-blurb {
    margin-top: 4px;
  }

  /* ── Reveal-Once Card ───────────────────────────────────────── */
  .tok-fresh {
    padding: 16px 18px;
    border: 1px solid color-mix(in srgb, var(--color-brand-500) 45%, var(--border));
    background: color-mix(in srgb, var(--color-brand-500) 5%, var(--surface));
    border-radius: 5px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .tok-fresh-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    flex-wrap: wrap;
  }
  .tok-fresh-text { min-width: 0; max-width: 60ch; }
  .tok-fresh-title {
    font-size: 18px;
    margin-top: 6px;
    line-height: 1.25;
  }
  .tok-fresh-blurb {
    font-size: 12px;
    margin-top: 4px;
  }
  .tok-fresh-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .tok-fresh-value {
    flex: 1;
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    user-select: all;
  }
  .tok-fresh-value.masked { color: var(--fg-subtle); user-select: none; }

  /* ── Toolbar ────────────────────────────────────────────────── */
  .tok-toolbar {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .tok-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .tok-search :global(.tok-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .tok-search-input {
    padding-left: 18px;
    font-size: 12.5px;
  }
  .tok-spacer { flex: 1; }

  /* ── Table ──────────────────────────────────────────────────── */
  .tok-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .tok-row {
    display: grid;
    grid-template-columns: minmax(220px, 1.6fr) 130px 150px 130px 130px 90px;
    gap: 14px;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .tok-row:last-child { border-bottom: 0; }
  .tok-row-head {
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
    font-size: 11.5px;
    color: var(--fg-subtle);
    padding: 10px 16px;
  }
  .tok-row-stale { opacity: 0.6; }
  .tok-row-revoked { opacity: 0.45; }

  .tok-cell-name { min-width: 0; }
  .tok-name-line {
    display: flex;
    align-items: center;
    gap: 7px;
    flex-wrap: wrap;
  }
  .tok-name {
    font-size: 13px;
    font-weight: 500;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tok-prefix {
    display: block;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }
  .tok-mini-pill { padding: 0 5px; font-size: 11.5px; }

  .tok-cell-scope { min-width: 0; }
  .tok-scope-pill { padding: 1px 7px; font-size: 11.5px; }

  .tok-cell-mono {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
  }
  .tok-cell-mono.tok-muted { color: var(--fg-subtle); }
  .tok-cell-mono[data-tone="warn"] { color: var(--color-warning-400); }
  .tok-cell-mono[data-tone="muted"] { color: var(--fg-subtle); }
  .tok-ip {
    color: var(--fg-subtle);
    margin-left: 4px;
  }

  .tok-col-action,
  .tok-cell-action {
    display: flex;
    justify-content: flex-end;
  }
  .tok-revoke-btn { color: var(--color-danger-400); }
  .tok-dash {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  /* ── Empty / loading ────────────────────────────────────────── */
  .tok-loading { padding: 0; }
  .tok-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
  }
  .tok-empty-title {
    margin-top: 10px;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .tok-empty-blurb {
    margin-top: 6px;
    margin-inline: auto;
    max-width: 50ch;
  }

  /* ── Hint footer ────────────────────────────────────────────── */
  .tok-hint {
    padding: 12px 14px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 5px;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .tok-hint-title {
    font-weight: 500;
    color: var(--fg);
    margin-bottom: 4px;
  }
  .tok-hint-code {
    font-family: var(--font-mono);
    font-size: 11px;
    background: var(--bg-elevated);
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid var(--border);
  }

  /* ── Create Modal ───────────────────────────────────────────── */
  .tok-create {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .tok-scope-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }
  .tok-scope-card {
    padding: 8px 10px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    text-align: left;
    display: flex;
    flex-direction: column;
    gap: 2px;
    color: var(--fg-muted);
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .tok-scope-card:hover { border-color: var(--border-strong); }
  .tok-scope-active {
    background: var(--accent-bg);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--fg);
  }
  .tok-scope-card-label {
    font-size: 12.5px;
    font-weight: 500;
  }
  .tok-scope-card-blurb {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  .tok-expires-row {
    display: flex;
    gap: 6px;
  }
  .tok-expires-pill {
    flex: 1;
    padding: 6px 8px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 4px;
    cursor: pointer;
    font-size: 12px;
    color: var(--fg-muted);
    font: inherit;
    transition: border-color 0.12s, background 0.12s;
  }
  .tok-expires-pill:hover { border-color: var(--border-strong); }
  .tok-expires-active {
    background: var(--accent-bg);
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    color: var(--fg);
  }

  .tok-modal-foothint {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .tok-modal-actions {
    display: flex;
    gap: 8px;
  }

  @media (max-width: 880px) {
    .tok-row {
      grid-template-columns: minmax(0, 1.4fr) auto auto;
      grid-auto-flow: row;
    }
    .tok-cell-mono,
    .tok-col-action {
      grid-column: 2 / -1;
    }
    .tok-row-head { display: none; }
    .tok-scope-grid { grid-template-columns: 1fr; }
    .tok-expires-row { flex-wrap: wrap; }
  }
</style>
