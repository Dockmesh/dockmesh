<script lang="ts">
  // API Tokens — long-lived bearer tokens for CI/CD, scripts, dmctl.
  // Extracted from Settings to a top-level route so the user-avatar
  // menu can link straight here. Personal + service tokens both live
  // on one page; the role picker signals intent.
  import { api, ApiError, type CustomRole } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Plus, Trash2, KeyRound, Copy, AlertCircle } from 'lucide-svelte';

  let apiTokens = $state<import('$lib/api').ApiToken[]>([]);
  let apiTokensLoading = $state(false);
  let showNewToken = $state(false);
  let newTokenForm = $state({ name: '', role: 'operator', expires_in_days: 90 });
  let freshTokenPlaintext = $state<string | null>(null);
  let freshTokenName = $state<string>('');
  let tokenCopied = $state(false);

  let roles = $state<CustomRole[]>([]);

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
    try { roles = await api.roles.list(); } catch { /* ignore */ }
  }

  async function createApiToken(e: Event) {
    e.preventDefault();
    if (!newTokenForm.name.trim() || !newTokenForm.role) return;
    try {
      const res = await api.apiTokens.create({
        name: newTokenForm.name.trim(),
        role: newTokenForm.role,
        expires_in_days: newTokenForm.expires_in_days
      });
      freshTokenPlaintext = res.token;
      freshTokenName = res.name;
      showNewToken = false;
      newTokenForm = { name: '', role: 'operator', expires_in_days: 90 };
      await loadApiTokens();
    } catch (err) {
      toast.error('Failed to create token', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function revokeApiToken(id: number, name: string) {
    if (!(await confirm.ask({ title: 'Revoke API token', message: `Revoke token "${name}"?`, body: 'Cannot be undone. Any scripts, CI jobs, or dmctl sessions using this token lose access on next request.', confirmLabel: 'Revoke', danger: true }))) return;
    try {
      await api.apiTokens.revoke(id);
      toast.success('Token revoked');
      await loadApiTokens();
    } catch (err) {
      toast.error('Failed to revoke', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function copyToken() {
    if (!freshTokenPlaintext) return;
    try {
      if (window.isSecureContext && navigator.clipboard) {
        await navigator.clipboard.writeText(freshTokenPlaintext);
      } else {
        const ta = document.createElement('textarea');
        ta.value = freshTokenPlaintext;
        ta.style.position = 'fixed'; ta.style.top = '-1000px';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      tokenCopied = true;
      setTimeout(() => (tokenCopied = false), 2000);
    } catch {
      toast.error('Copy failed', 'Select and copy the token manually');
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
    if (allowed('user.manage')) {
      loadApiTokens();
      loadRoles();
    }
  });
</script>

<section class="space-y-6">
  <div class="flex items-start justify-between gap-4">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">API tokens</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Long-lived bearer tokens for CI/CD, scripts, and external integrations.
        Unlike user sessions, these don't expire by default and can be revoked here.
      </p>
    </div>
    {#if allowed('user.manage')}
      <Button variant="primary" onclick={() => (showNewToken = true)}>
        <Plus class="w-3.5 h-3.5" />
        New token
      </Button>
    {/if}
  </div>

  {#if !allowed('user.manage')}
    <Card>
      <EmptyState icon={KeyRound} title="Admin-only" description="API token management requires user-manage permission." />
    </Card>
  {:else}
    <Card>
      {#if apiTokensLoading}
        <Skeleton class="h-24" />
      {:else if apiTokens.length === 0}
        <EmptyState
          icon={KeyRound}
          title="No API tokens yet"
          description="Create a token to authenticate CI pipelines or scripts against the dockmesh API."
        />
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-xs uppercase tracking-wider text-[var(--fg-muted)] border-b border-[var(--border)]">
              <tr>
                <th class="text-left py-2 px-3 font-medium">Name</th>
                <th class="text-left py-2 px-3 font-medium">Prefix</th>
                <th class="text-left py-2 px-3 font-medium">Role</th>
                <th class="text-left py-2 px-3 font-medium">Last used</th>
                <th class="text-left py-2 px-3 font-medium">Expires</th>
                <th class="text-left py-2 px-3 font-medium">Status</th>
                <th class="w-10"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each apiTokens as t (t.id)}
                <tr class:opacity-50={!!t.revoked_at}>
                  <td class="py-2 px-3 font-medium">{t.name}</td>
                  <td class="py-2 px-3 font-mono text-xs text-[var(--fg-muted)]">{t.prefix}…</td>
                  <td class="py-2 px-3"><Badge variant="default">{t.role}</Badge></td>
                  <td class="py-2 px-3 text-[var(--fg-muted)]">
                    {fmtAgo(t.last_used_at)}
                    {#if t.last_used_ip}<span class="text-xs ml-1">({t.last_used_ip})</span>{/if}
                  </td>
                  <td class="py-2 px-3 text-[var(--fg-muted)]">
                    {t.expires_at ? new Date(t.expires_at).toISOString().slice(0, 10) : 'never'}
                  </td>
                  <td class="py-2 px-3">
                    {#if t.revoked_at}<Badge variant="danger">Revoked</Badge>
                    {:else if t.expires_at && new Date(t.expires_at) < new Date()}<Badge variant="warning">Expired</Badge>
                    {:else}<Badge variant="success">Active</Badge>{/if}
                  </td>
                  <td class="py-2 px-3">
                    {#if !t.revoked_at}
                      <button
                        class="p-1.5 hover:bg-[var(--surface-hover)] rounded text-[var(--fg-muted)] hover:text-[var(--color-danger-400)]"
                        onclick={() => revokeApiToken(t.id, t.name)}
                        title="Revoke" aria-label="Revoke">
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Card>

    <div class="text-xs text-[var(--fg-muted)] bg-[var(--surface)] rounded-md p-3 border border-[var(--border)]">
      <p class="font-medium text-[var(--fg)] mb-1">Using a token</p>
      <p>
        Send it as <code class="text-[11px] font-mono bg-[var(--bg)] px-1 rounded">Authorization: Bearer dmt_...</code>
        on any API request. Tokens assume the role they were created with — scope
        narrowly to limit blast radius if leaked.
      </p>
    </div>
  {/if}
</section>

<Modal bind:open={showNewToken} title="Create API token" maxWidth="max-w-md">
  <form onsubmit={createApiToken} id="new-token-form" class="space-y-4">
    <Input
      label="Name"
      placeholder="github-actions-deploy"
      hint="A label to identify the token. Cannot be changed later."
      bind:value={newTokenForm.name}
    />
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Role</span>
      <select class="dm-input" bind:value={newTokenForm.role}>
        {#each roles as r}
          <option value={r.name}>{r.name} — {r.display}</option>
        {/each}
        {#if roles.length === 0}
          <option value="viewer">viewer</option>
          <option value="operator">operator</option>
          <option value="admin">admin</option>
        {/if}
      </select>
      <p class="text-xs text-[var(--fg-muted)] mt-1">
        The token will have the same permissions as this role. Prefer narrow roles for CI.
      </p>
    </div>
    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Expiration</span>
      <select class="dm-input" bind:value={newTokenForm.expires_in_days}>
        <option value={30}>30 days</option>
        <option value={90}>90 days (recommended)</option>
        <option value={180}>180 days</option>
        <option value={365}>1 year</option>
        <option value={0}>Never expire</option>
      </select>
      <p class="text-xs text-[var(--fg-muted)] mt-1">
        Rotation is a good habit. Never-expire tokens should be the exception.
      </p>
    </div>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showNewToken = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="new-token-form" disabled={!newTokenForm.name.trim()}>
      Create token
    </Button>
  {/snippet}
</Modal>

<Modal
  open={freshTokenPlaintext !== null}
  onclose={() => (freshTokenPlaintext = null)}
  title="Token created"
  maxWidth="max-w-lg"
>
  <div class="space-y-4">
    <div class="flex items-start gap-2 p-3 rounded-md bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_25%,transparent)]">
      <AlertCircle class="w-4 h-4 text-[var(--color-warning-400)] flex-shrink-0 mt-0.5" />
      <div class="text-sm">
        <p class="font-medium text-[var(--fg)]">Save this token now — you won't see it again.</p>
        <p class="text-[var(--fg-muted)] mt-0.5">
          dockmesh only stores a hash. If you lose the plaintext, revoke this token and create a new one.
        </p>
      </div>
    </div>

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
        Token for <span class="text-[var(--fg)]">{freshTokenName}</span>
      </span>
      <div class="flex gap-2">
        <code class="flex-1 font-mono text-xs bg-[var(--surface)] border border-[var(--border)] rounded px-3 py-2.5 break-all select-all">
          {freshTokenPlaintext}
        </code>
        <Button variant="secondary" onclick={copyToken}>
          <Copy class="w-3.5 h-3.5" />
          {tokenCopied ? 'Copied' : 'Copy'}
        </Button>
      </div>
    </div>

    <div class="text-xs text-[var(--fg-muted)]">
      <p class="font-medium text-[var(--fg)] mb-1">Example usage</p>
      <pre class="font-mono text-[11px] bg-[var(--surface)] border border-[var(--border)] rounded p-2 overflow-x-auto"><code>curl -H "Authorization: Bearer {freshTokenPlaintext}" \
  https://dockmesh.example.com/api/v1/stacks</code></pre>
    </div>
  </div>

  {#snippet footer()}
    <Button variant="primary" onclick={() => (freshTokenPlaintext = null)}>
      I've saved it
    </Button>
  {/snippet}
</Modal>
