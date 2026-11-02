<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, ApiError, type CustomRole, type RoleCatalog, type Scope, type UserRow } from '$lib/api';
  import { Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { roleOneLine, scopeLabel } from '$lib/access';
  import ScopePicker from './ScopePicker.svelte';

  let {
    open = $bindable(false),
    users,
    roles,
    catalog,
    userId = '',
    role = '',
    onsaved
  }: {
    open: boolean;
    users: UserRow[];
    roles: CustomRole[];
    catalog: RoleCatalog | null;
    userId?: string;
    role?: string;
    onsaved?: () => void;
  } = $props();

  let who = $state('');
  let what = $state('');
  let scope = $state<Scope>({ type: 'global' });
  let busy = $state(false);

  $effect(() => {
    if (open) {
      who = userId || users[0]?.id || '';
      what = role || roles.find((r) => r.name === 'viewer')?.name || roles[0]?.name || '';
      scope = { type: 'global' };
    }
  });

  const selectedRole = $derived(roles.find((r) => r.name === what));
  const narrow = $derived(scope.type !== 'global');
  const globalOnlyLost = $derived(
    narrow && catalog && selectedRole
      ? catalog.areas.filter((a) => !a.scoped && (selectedRole.levels[a.key] ?? 'none') !== 'none').map((a) => a.label)
      : []
  );

  async function save() {
    if (!who || !what) return;
    busy = true;
    try {
      await api.assignments.create({ user_id: who, role: what, scope });
      toast.success(m.access_assign_added(), m.access_assign_on({ role: selectedRole?.display ?? what, scope: scopeLabel(scope) }));
      open = false;
      onsaved?.();
    } catch (err) {
      toast.error(m.access_assign_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={m.access_assign_add()} maxWidth="max-w-xl">
  <div class="form">
    <div class="dm-field">
      <label for="as-who">{m.common_user()}</label>
      <select id="as-who" class="dm-input" bind:value={who} disabled={!!userId}>
        {#each users as u (u.id)}<option value={u.id}>{u.username}</option>{/each}
      </select>
    </div>
    <div class="dm-field">
      <label for="as-role">{m.common_role()}</label>
      <select id="as-role" class="dm-input" bind:value={what} disabled={!!role}>
        {#each roles as r (r.name)}<option value={r.name}>{r.display}{r.builtin ? '' : m.access_role_custom_suffix()}</option>{/each}
      </select>
      {#if selectedRole}<span class="dm-sub">{roleOneLine(catalog, selectedRole)}</span>{/if}
    </div>
    <ScopePicker bind:value={scope} label={m.access_assign_scope()} />
    {#if globalOnlyLost.length}
      <p class="note">{m.access_assign_global_only({ areas: globalOnlyLost.join(', ') })}</p>
    {/if}
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (open = false)}>{m.common_cancel()}</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={busy || !who || !what || (scope.type === 'host_tag' && !scope.value)} onclick={save}>
      {busy ? m.access_assign_adding() : m.access_assign_add()}
    </button>
  {/snippet}
</Modal>

<style>
  .form { display: flex; flex-direction: column; gap: 14px; }
  .note { margin: 0; font-size: var(--fs-sm); color: var(--warn); }
</style>
