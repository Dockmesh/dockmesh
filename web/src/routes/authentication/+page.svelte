<script lang="ts">
  // Authentication (previously the "SSO" tab in Settings). Owns the
  // OIDC provider CRUD + connection-test flow. Name widened to
  // "Authentication" so future additions — password policy, LDAP,
  // session-TTL — have a natural home without another rename.
  import { api, ApiError, type CustomRole } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Plus, Trash2, UserCog, Globe } from 'lucide-svelte';
  import type { OIDCProvider, OIDCProviderInput } from '$lib/api';

  let oidcProviders = $state<OIDCProvider[]>([]);
  let oidcLoading = $state(false);
  let showOIDC = $state(false);
  let editingOIDC = $state<OIDCProvider | null>(null);
  let oForm = $state<OIDCProviderInput>({
    slug: '',
    display_name: '',
    issuer_url: '',
    client_id: '',
    client_secret: '',
    scopes: 'openid,profile,email',
    group_claim: 'groups',
    admin_group: '',
    operator_group: '',
    default_role: 'viewer',
    enabled: true
  });
  let oidcTestState = $state<'idle' | 'testing' | 'ok' | 'fail'>('idle');
  let oidcTestMessage = $state<string>('');

  // Roles list drives the default-role <select> inside the form —
  // we load it once so a new OIDC install can pick any existing role
  // (not just admin/operator/viewer).
  let roles = $state<CustomRole[]>([]);

  async function testOIDCDiscovery() {
    const url = oForm.issuer_url.trim();
    if (!url) {
      oidcTestState = 'fail';
      oidcTestMessage = 'Enter an issuer URL first';
      return;
    }
    oidcTestState = 'testing';
    oidcTestMessage = '';
    try {
      const res = await api.oidc.testDiscovery(url);
      if (res.ok) {
        oidcTestState = 'ok';
        oidcTestMessage = `Discovery OK — issuer: ${res.issuer}`;
      } else {
        oidcTestState = 'fail';
        oidcTestMessage = res.error || 'Discovery failed';
      }
    } catch (err) {
      oidcTestState = 'fail';
      oidcTestMessage = err instanceof ApiError ? err.message : String(err);
    }
  }

  $effect(() => {
    // Reset test state whenever the issuer URL changes so a stale OK
    // badge doesn't mislead after the admin edits the URL.
    oForm.issuer_url;
    oidcTestState = 'idle';
    oidcTestMessage = '';
  });

  async function loadOIDC() {
    oidcLoading = true;
    try {
      oidcProviders = await api.oidc.listAdmin();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      oidcLoading = false;
    }
  }

  async function loadRoles() {
    try { roles = await api.roles.list(); } catch { /* ignore */ }
  }

  function resetOIDCForm() {
    oForm = {
      slug: '',
      display_name: '',
      issuer_url: '',
      client_id: '',
      client_secret: '',
      scopes: 'openid,profile,email',
      group_claim: 'groups',
      admin_group: '',
      operator_group: '',
      default_role: 'viewer',
      enabled: true
    };
    editingOIDC = null;
  }

  function openNewOIDC() {
    resetOIDCForm();
    showOIDC = true;
  }

  function openEditOIDC(p: OIDCProvider) {
    editingOIDC = p;
    oForm = {
      slug: p.slug,
      display_name: p.display_name,
      issuer_url: p.issuer_url,
      client_id: p.client_id,
      client_secret: '',
      scopes: p.scopes,
      group_claim: p.group_claim ?? '',
      admin_group: p.admin_group ?? '',
      operator_group: p.operator_group ?? '',
      default_role: p.default_role,
      enabled: p.enabled
    };
    showOIDC = true;
  }

  async function saveOIDC(e: Event) {
    e.preventDefault();
    try {
      if (editingOIDC) {
        await api.oidc.update(editingOIDC.id, oForm);
        toast.success('Provider updated', oForm.slug);
      } else {
        await api.oidc.create(oForm);
        toast.success('Provider created', oForm.slug);
      }
      showOIDC = false;
      resetOIDCForm();
      await loadOIDC();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function deleteOIDC(p: OIDCProvider) {
    if (!(await confirm.ask({ title: 'Delete SSO provider', message: `Delete provider "${p.display_name}"?`, body: 'Users who signed in via this provider must fall back to password login until it’s re-added.', confirmLabel: 'Delete', danger: true }))) return;
    try {
      await api.oidc.delete(p.id);
      toast.success('Deleted', p.slug);
      await loadOIDC();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  $effect(() => {
    if (allowed('user.manage')) {
      loadOIDC();
      loadRoles();
    }
  });
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-2xl font-semibold tracking-tight">Authentication</h2>
    <p class="text-sm text-[var(--fg-muted)] mt-0.5">
      Single-sign-on providers and OIDC configuration. Users can sign in via their organisation account instead of a local password.
    </p>
  </div>

  {#if !allowed('user.manage')}
    <Card>
      <EmptyState icon={Globe} title="Admin-only" description="Authentication configuration requires user-manage permission." />
    </Card>
  {:else}
    <section class="space-y-4">
      <div class="flex justify-between items-center">
        <span class="text-sm text-[var(--fg-muted)]">{oidcProviders.length} provider{oidcProviders.length === 1 ? '' : 's'}</span>
        <Button variant="primary" onclick={openNewOIDC}>
          <Plus class="w-4 h-4" /> Add provider
        </Button>
      </div>

      {#if oidcLoading && oidcProviders.length === 0}
        <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
      {:else if oidcProviders.length === 0}
        <Card>
          <EmptyState
            icon={Globe}
            title="No SSO providers"
            description="Add an OIDC provider (Azure AD, Google, Keycloak, Dex, Auth0, …) to let users sign in via their organisation account."
          />
        </Card>
      {:else}
        <Card>
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                  <th class="text-center px-3 py-3 w-10">Status</th>
                  <th class="text-left px-3 py-3">Name</th>
                  <th class="text-left px-3 py-3">Slug</th>
                  <th class="text-left px-3 py-3">Issuer URL</th>
                  <th class="text-left px-3 py-3">Default Role</th>
                  <th class="text-right px-3 py-3 w-24">Actions</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-[var(--border)]">
                {#each oidcProviders as p}
                  <tr class="hover:bg-[var(--surface-hover)]">
                    <td class="px-3 py-3 text-center">
                      <span class="w-2 h-2 rounded-full inline-block {p.enabled ? 'bg-[var(--color-success-500)]' : 'bg-[var(--fg-subtle)]'}"></span>
                    </td>
                    <td class="px-3 py-3 font-medium">{p.display_name}</td>
                    <td class="px-3 py-3 font-mono text-xs text-[var(--fg-muted)]">{p.slug}</td>
                    <td class="px-3 py-3 font-mono text-xs text-[var(--fg-muted)] truncate max-w-[250px]" title={p.issuer_url}>{p.issuer_url}</td>
                    <td class="px-3 py-3"><Badge variant="default">{p.default_role}</Badge></td>
                    <td class="px-3 py-3">
                      <div class="flex gap-0.5 justify-end">
                        <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Edit" onclick={() => openEditOIDC(p)}>
                          <UserCog class="w-3.5 h-3.5" />
                        </button>
                        <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteOIDC(p)}>
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

      <Card class="p-4">
        <div class="text-xs text-[var(--fg-muted)] space-y-1">
          <div class="font-medium text-[var(--fg)]">Callback URL</div>
          <code class="font-mono text-[var(--color-brand-400)]">{`${typeof window !== 'undefined' ? window.location.origin : ''}/api/v1/auth/oidc/{slug}/callback`}</code>
          <div>Configure this in your provider's app/client redirect URIs. Replace <code class="font-mono">{'{slug}'}</code> with the provider's slug.</div>
        </div>
      </Card>
    </section>
  {/if}
</section>

<Modal bind:open={showOIDC} title={editingOIDC ? 'Edit OIDC provider' : 'Add OIDC provider'} maxWidth="max-w-xl" onclose={resetOIDCForm}>
  <form onsubmit={saveOIDC} class="space-y-5" id="oidc-form">
    <div class="text-xs p-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-[var(--fg-muted)]">Callback URL:</span>
      <code class="font-mono text-[var(--color-brand-400)] ml-1 break-all">{`${typeof window !== 'undefined' ? window.location.origin : ''}/api/v1/auth/oidc/${oForm.slug || '{slug}'}/callback`}</code>
    </div>

    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">Provider</legend>
      <div class="grid grid-cols-2 gap-3">
        <Input label="Slug" hint="used in URLs, e.g. azure-ad" bind:value={oForm.slug} disabled={editingOIDC !== null} />
        <Input label="Display name" bind:value={oForm.display_name} />
      </div>
      <label class="flex items-center gap-2 text-sm cursor-pointer">
        <input type="checkbox" bind:checked={oForm.enabled} class="accent-[var(--color-brand-500)]" />
        Enabled
      </label>
    </fieldset>

    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">OIDC Configuration</legend>
      <div>
        <div class="flex items-end gap-2">
          <div class="flex-1">
            <Input label="Issuer URL" placeholder="https://login.microsoftonline.com/your-tenant/v2.0" bind:value={oForm.issuer_url} hint="OIDC discovery root (.well-known/openid-configuration)" />
          </div>
          <button type="button" class="dm-btn dm-btn-secondary shrink-0 mb-[22px]"
            disabled={oidcTestState === 'testing' || !oForm.issuer_url.trim()}
            onclick={testOIDCDiscovery}>
            {oidcTestState === 'testing' ? 'Testing…' : 'Test connection'}
          </button>
        </div>
        {#if oidcTestState === 'ok'}
          <p class="mt-1 text-xs text-green-600 dark:text-green-400">{oidcTestMessage}</p>
        {:else if oidcTestState === 'fail'}
          <p class="mt-1 text-xs text-red-600 dark:text-red-400">{oidcTestMessage}</p>
        {/if}
      </div>
      <div class="grid grid-cols-2 gap-3">
        <Input label="Client ID" bind:value={oForm.client_id} />
        <Input label="Client secret" type="password" bind:value={oForm.client_secret}
          hint={editingOIDC ? 'leave blank to keep existing' : undefined} />
      </div>
      <Input label="Scopes" bind:value={oForm.scopes} hint="comma-separated (default: openid,profile,email)" />
    </fieldset>

    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">Group Mapping</legend>
      <div class="grid grid-cols-3 gap-3">
        <Input label="Group claim" placeholder="groups" bind:value={oForm.group_claim} />
        <Input label="Admin group" bind:value={oForm.admin_group} />
        <Input label="Operator group" bind:value={oForm.operator_group} />
      </div>
      <div>
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Default role (when no group matches)</span>
        <select class="dm-input text-sm" bind:value={oForm.default_role}>
          {#each roles as r}
            <option value={r.name}>{r.display || r.name}</option>
          {/each}
        </select>
      </div>
    </fieldset>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => { showOIDC = false; resetOIDCForm(); }}>Cancel</Button>
    <Button variant="primary" type="submit" form="oidc-form">
      {editingOIDC ? 'Save' : 'Create'}
    </Button>
  {/snippet}
</Modal>
