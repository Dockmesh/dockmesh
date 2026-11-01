<script lang="ts">
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
      toast.success('Assignment added', `${selectedRole?.display ?? what} on ${scopeLabel(scope)}`);
      open = false;
      onsaved?.();
    } catch (err) {
      toast.error('Could not add the assignment', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title="Add assignment" maxWidth="max-w-xl">
  <div class="form">
    <div class="dm-field">
      <label for="as-who">User</label>
      <select id="as-who" class="dm-input" bind:value={who} disabled={!!userId}>
        {#each users as u (u.id)}<option value={u.id}>{u.username}</option>{/each}
      </select>
    </div>
    <div class="dm-field">
      <label for="as-role">Role</label>
      <select id="as-role" class="dm-input" bind:value={what} disabled={!!role}>
        {#each roles as r (r.name)}<option value={r.name}>{r.display}{r.builtin ? '' : ' (custom)'}</option>{/each}
      </select>
      {#if selectedRole}<span class="dm-sub">{roleOneLine(catalog, selectedRole)}</span>{/if}
    </div>
    <ScopePicker bind:value={scope} label="On" />
    {#if globalOnlyLost.length}
      <p class="note">On a limited scope this role does not grant {globalOnlyLost.join(', ')}: those need an assignment on everything.</p>
    {/if}
  </div>
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (open = false)}>Cancel</button>
    <button type="button" class="dm-btn dm-btn-primary" disabled={busy || !who || !what || (scope.type === 'host_tag' && !scope.value)} onclick={save}>
      {busy ? 'Adding…' : 'Add assignment'}
    </button>
  {/snippet}
</Modal>

<style>
  .form { display: flex; flex-direction: column; gap: 14px; }
  .note { margin: 0; font-size: var(--fs-sm); color: var(--warn); }
</style>
