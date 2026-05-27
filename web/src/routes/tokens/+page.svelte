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
  //
  // Permission-gating uses v1 RBAC string `user.manage` for now;
  // migration to v2.1 (`tokens.view` / `tokens.create` / `tokens.delete`)
  // happens in the consolidated backend slice at the end of the
  // frontend-rebuild phase.
  import { api, ApiError, type ApiToken, type CustomRole } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { EditorialPage, Eyebrow, Field, EditorialModal } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Plus, Trash2, KeyRound, Copy, Search, AlertCircle,
  } from 'lucide-svelte';

  let apiTokens = $state<ApiToken[]>([]);
  let apiTokensLoading = $state(true);
  let roles = $state<CustomRole[]>([]);

  // Search + filter
  let search = $state('');

  // Create form state
  let showCreate = $state(false);
  let newName = $state('');
  let newRole = $state('viewer');
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
      toast.error('Failed to load API tokens', err instanceof ApiError ? err.message : undefined);
    } finally {
      apiTokensLoading = false;
    }
  }

  async function loadRoles() {
    try { roles = await api.roles.list(); } catch { /* fall back to built-ins */ }
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
    if (!newName.trim() || !newRole) return;
    creating = true;
    const expiresMap = { '30d': 30, '90d': 90, '180d': 180, '365d': 365, 'never': 0 } as const;
    try {
      const res = await api.apiTokens.create({
        name: newName.trim(),
        role: newRole,
        expires_in_days: expiresMap[newExpiresKey],
      });
      freshToken = { name: res.name, value: res.token };
      freshCopied = false;
      showCreate = false;
      newName = '';
      newRole = 'viewer';
      newExpiresKey = '90d';
      await loadApiTokens();
    } catch (err) {
      toast.error('Failed to create token', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  async function revokeApiToken(id: number, name: string) {
    if (!(await confirm.ask({
      title: 'Revoke API token',
      message: `Revoke token "${name}"?`,
      body: 'Cannot be undone. Any scripts, CI jobs, or dmctl sessions using this token lose access on the next request.',
      confirmLabel: 'Revoke', danger: true,
    }))) return;
    try {
      await api.apiTokens.revoke(id);
      toast.success('Token revoked');
      await loadApiTokens();
    } catch (err) {
      toast.error('Failed to revoke', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function copyFreshToken() {
    if (!freshToken || !freshRevealed) return;
    try {
      if (typeof window !== 'undefined' && window.isSecureContext && navigator.clipboard) {
        await navigator.clipboard.writeText(freshToken.value);
      } else {
        const ta = document.createElement('textarea');
        ta.value = freshToken.value;
        ta.style.position = 'fixed'; ta.style.top = '-1000px';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      freshCopied = true;
      setTimeout(() => (freshCopied = false), 1800);
    } catch {
      toast.error('Copy failed', 'Select and copy the token manually');
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
    if (d < 60) return 'now';
    if (d < 3600) return `${Math.floor(d / 60)}m ago`;
    if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
    if (d < 2 * 86400) return 'yesterday';
    return `${Math.floor(d / 86400)}d ago`;
  }

  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    return new Date(iso).toLocaleDateString(undefined, {
      year: 'numeric', month: 'short', day: '2-digit',
    });
  }

  function expiresLabel(t: ApiToken): string {
    if (!t.expires_at) return 'never';
    return fmtDate(t.expires_at);
  }

  function expiresTone(t: ApiToken): 'warn' | 'muted' | 'normal' {
    if (!t.expires_at) return 'muted';
    if (isExpiringSoon(t)) return 'warn';
    return 'normal';
  }

  function scopePillClass(role: string): string {
    if (role === 'admin' || role === 'host-admin') return 'dm-pill dm-pill-warning';
    if (role === 'operator' || role === 'deployer') return 'dm-pill dm-pill-success';
    return 'dm-pill dm-pill-neutral';
  }

  function statusPill(t: ApiToken): { cls: string; label: string } | null {
    if (t.revoked_at) return { cls: 'dm-pill dm-pill-neutral', label: 'revoked' };
    if (t.expires_at && new Date(t.expires_at).getTime() < Date.now()) {
      return { cls: 'dm-pill dm-pill-neutral', label: 'expired' };
    }
    return null;
  }

  // Available roles for the picker — Backend accepts any role name from
  // the catalogue, plus the built-ins as a safety net if the roles store
  // is empty (pre-migration).
  const pickableRoles = $derived.by(() => {
    if (roles.length > 0) return roles;
    return [
      { name: 'viewer',    display: 'viewer',    permissions: [], builtin: true },
      { name: 'operator',  display: 'operator',  permissions: [], builtin: true },
      { name: 'admin',     display: 'admin',     permissions: [], builtin: true },
    ] as CustomRole[];
  });

  function roleBlurb(name: string): string {
    switch (name) {
      case 'viewer':     return 'GET on everything you can see.';
      case 'operator':   return 'Start, stop, restart. No deploys.';
      case 'deployer':   return 'Deploy + restart. No user mgmt.';
      case 'host-admin': return 'Hosts + agents + tags. No user mgmt.';
      case 'admin':      return 'Full control. Use sparingly in CI.';
      default:           return 'custom role';
    }
  }

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
        <h1 class="ed-title tok-title">API tokens</h1>
        <p class="ed-subtitle tok-subtitle">
          {#if counts.total === 0}
            No tokens yet
          {:else}
            {counts.active} active{counts.expiring > 0 ? ` · ${counts.expiring} expiring within 30 days` : ''}{counts.stale > 0 ? ` · ${counts.stale} unused for 90+ days` : ''}
          {/if}
        </p>
      </div>
    </header>

    {#if !allowed('tokens.manage_others')}
      <div class="dm-card tok-permission-block">
        <KeyRound size={18} strokeWidth={1.5} />
        <div>
          <div class="tok-perm-title">Admin-only</div>
          <p class="ed-subtitle tok-perm-blurb">
            API token management requires the <em>user.manage</em> permission.
          </p>
        </div>
      </div>
    {:else}
      <!-- ─────────────── Reveal-Once Card (after create) ─────────────── -->
      {#if freshToken}
        <div class="tok-fresh">
          <div class="tok-fresh-head">
            <div class="tok-fresh-text">
              <Eyebrow>↳ token created</Eyebrow>
              <h3 class="ed-title tok-fresh-title">
                Copy <em class="ed-accent">{freshToken.name}</em> now — it won't be shown again.
              </h3>
              <p class="ed-subtitle tok-fresh-blurb">
                We never store the cleartext value.
                {#if freshRevealed}Auto-masking in {freshSeconds}s.{:else}Masked.{/if}
              </p>
            </div>
            <button
              type="button"
              class="dm-btn dm-btn-ghost dm-btn-sm"
              onclick={dismissFresh}
            >
              Dismiss
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
              <Copy size={11} strokeWidth={1.5} /> {freshCopied ? 'copied' : 'copy'}
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
            placeholder="filter by name or prefix…"
            bind:value={search}
          />
        </div>

        <span class="tok-spacer"></span>

        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          onclick={() => (showCreate = true)}
        >
          <Plus size={12} strokeWidth={1.5} /> New token
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
            <Eyebrow>no match</Eyebrow>
            <p class="tok-empty-title">No tokens match „{search}".</p>
          {:else}
            <Eyebrow>empty</Eyebrow>
            <p class="tok-empty-title">No API tokens yet.</p>
            <p class="ed-subtitle tok-empty-blurb">
              Create a token to authenticate CI pipelines, scripts, or dmctl against the API.
            </p>
          {/if}
        </div>
      {:else}
        <div class="tok-table">
          <div class="tok-row tok-row-head">
            <span>name · prefix</span>
            <span>scope</span>
            <span>last used</span>
            <span>created</span>
            <span>expires</span>
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
                      <span class="dm-pill-dot"></span>stale
                    </span>
                  {:else if isExpiringSoon(t)}
                    <span class="dm-pill dm-pill-warning tok-mini-pill">
                      <span class="dm-pill-dot"></span>expiring
                    </span>
                  {/if}
                </div>
                <span class="tok-prefix">{t.prefix}…</span>
              </div>

              <div class="tok-cell-scope">
                <span class={scopePillClass(t.role) + ' tok-scope-pill'}>
                  <span class="dm-pill-dot"></span>{t.role}
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
                    Revoke
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
        <div class="tok-hint-title">Using a token</div>
        <p class="tok-hint-blurb">
          Send it as <code class="tok-hint-code">Authorization: Bearer dmt_…</code>
          on any API request. Tokens carry the role they were created with — scope narrowly to limit
          blast radius if leaked.
        </p>
      </div>
    {/if}
  </section>
</EditorialPage>

<!-- ───────────────────────── Create Modal ───────────────────────── -->
<EditorialModal
  bind:open={showCreate}
  eyebrow="New token"
  width={560}
>
  {#snippet title()}
    Mint a new <em>API</em> token
  {/snippet}

  <form id="tok-create-form" class="tok-create" onsubmit={createApiToken}>
    <Field label="Label" hint="What is this token for? Shown in audit logs.">
      <input
        class="dm-input"
        bind:value={newName}
        placeholder="github actions · acme-api"
      />
    </Field>

    <Field label="Scope" hint="Pick the narrowest role that works.">
      <div class="tok-scope-grid">
        {#each pickableRoles as r (r.name)}
          <button
            type="button"
            class="tok-scope-card"
            class:tok-scope-active={newRole === r.name}
            onclick={() => (newRole = r.name)}
          >
            <span class="tok-scope-card-label">{r.display || r.name}</span>
            <span class="tok-scope-card-blurb">{roleBlurb(r.name)}</span>
          </button>
        {/each}
      </div>
    </Field>

    <Field label="Expires" hint="Short-lived is safer. CI tokens often pin to 90 days.">
      <div class="tok-expires-row">
        {#each [['30d', '30 days'], ['90d', '90 days'], ['180d', '6 months'], ['365d', '1 year'], ['never', 'never']] as [key, label]}
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
      Value will be shown <em class="ed-accent">once</em>.
    </span>
    <div class="tok-modal-actions">
      <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showCreate = false)}>
        Cancel
      </button>
      <button
        type="submit"
        form="tok-create-form"
        class="dm-btn dm-btn-primary dm-btn-sm"
        disabled={!newName.trim() || creating}
      >
        {creating ? 'Generating…' : 'Generate token'}
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
    background: var(--bg);
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
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
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
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }
  .tok-mini-pill { padding: 0 5px; font-size: 9.5px; }

  .tok-cell-scope { min-width: 0; }
  .tok-scope-pill { padding: 1px 7px; font-size: 10.5px; }

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
    font-family: var(--font-mono);
    font-size: 10.5px;
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
    background: var(--bg);
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
    font-family: var(--font-mono);
    font-size: 10px;
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
