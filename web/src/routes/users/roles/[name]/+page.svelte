<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError, type Assignment, type CustomRole, type HostInfo, type RoleCatalog, type UserRow } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { fmtAgo } from '$lib/format';
  import { Skeleton, EmptyState, Switch } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { areaSummaries, roleOneLine, scopeLabel, type HostNames } from '$lib/access';
  import AssignmentDialog from '$lib/components/access/AssignmentDialog.svelte';
  import { Copy, Plus, Trash2 } from 'lucide-svelte';

  const param = $derived(page.params.name ?? '');
  const isNew = $derived(param === 'new');

  let role = $state<CustomRole | null>(null);
  let assignments = $state<Assignment[]>([]);
  let catalog = $state<RoleCatalog | null>(null);
  let roles = $state<CustomRole[]>([]);
  let users = $state<UserRow[]>([]);
  let hostNames = $state<HostNames>({});
  let loading = $state(true);
  let loadError = $state<string | null>(null);

  let dName = $state('');
  let dDisplay = $state('');
  let dDesc = $state('');
  let dLevels = $state<Record<string, string>>({});
  let dExtras = $state<string[]>([]);
  let busy = $state(false);

  const editable = $derived(isNew ? allowed('roles.create') : !!role && !role.builtin && allowed('roles.update'));

  function slug(s: string) {
    return s.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64);
  }

  async function loadAll() {
    loadError = null;
    try {
      const [c, rs, us, hs] = await Promise.all([
        api.roles.catalog(),
        api.roles.list().catch(() => [] as CustomRole[]),
        api.users.list().catch(() => [] as UserRow[]),
        api.hosts.list().catch(() => [] as HostInfo[])
      ]);
      catalog = c;
      roles = rs;
      users = us;
      hostNames = Object.fromEntries(hs.map((h) => [h.id, h.name]));
      if (isNew) {
        const from = page.url.searchParams.get('from');
        const base = from ? rs.find((r) => r.name === from) : undefined;
        role = null;
        assignments = [];
        dDisplay = base ? `${base.display} (copy)` : '';
        dName = base ? slug(dDisplay) : '';
        dDesc = base?.description ?? '';
        dLevels = base ? { ...base.levels } : Object.fromEntries(c.areas.map((a) => [a.key, 'none']));
        dExtras = base ? [...base.extras] : [];
      } else {
        const res = await api.roles.get(param);
        role = res.role;
        assignments = res.assignments;
        dDisplay = res.role.display;
        dDesc = res.role.description ?? '';
        dLevels = { ...res.role.levels };
        dExtras = [...res.role.extras];
      }
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : 'Could not load the role';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void param;
    untrack(() => { loading = true; loadAll(); });
  });
  $effect(() => {
    pageContext.set(isNew ? 'New role' : role?.display ?? param);
    return () => pageContext.clear();
  });

  const summaries = $derived(catalog ? areaSummaries(catalog, dLevels) : []);
  const dirty = $derived(
    isNew || (!!role && (dDisplay !== role.display || dDesc !== (role.description ?? '') ||
      JSON.stringify(dLevels) !== JSON.stringify(role.levels) || [...dExtras].sort().join() !== [...role.extras].sort().join()))
  );

  function toggleExtra(key: string, on: boolean) {
    dExtras = on ? [...new Set([...dExtras, key])] : dExtras.filter((e) => e !== key);
  }

  async function save() {
    busy = true;
    try {
      if (isNew) {
        const name = dName || slug(dDisplay);
        await api.roles.create({ name, display: dDisplay.trim(), description: dDesc.trim(), levels: dLevels, extras: dExtras });
        toast.success('Role created', dDisplay);
        goto(`/users/roles/${encodeURIComponent(name)}`);
      } else if (role) {
        await api.roles.update(role.name, { display: dDisplay.trim(), description: dDesc.trim(), levels: dLevels, extras: dExtras });
        toast.success('Role saved', dDisplay);
        await loadAll();
      }
    } catch (err) {
      toast.error('Could not save the role', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function remove() {
    if (!role) return;
    if (!(await confirm.ask({ title: `Delete role ${role.display}?`, message: 'Only roles without assignments or group mappings can be deleted.', confirmLabel: 'Delete', danger: true }))) return;
    try {
      await api.roles.delete(role.name);
      toast.success('Role deleted', role.display);
      goto('/users?tab=roles');
    } catch (err) {
      toast.error('Could not delete the role', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function removeAssignment(a: Assignment) {
    if (!(await confirm.ask({ title: 'Remove assignment?', message: `${a.username ?? a.user_id} loses ${role?.display} on ${scopeLabel(a.scope, hostNames)}.`, confirmLabel: 'Remove', danger: true }))) return;
    try {
      await api.assignments.delete(a.id);
      toast.success('Assignment removed');
      await loadAll();
    } catch (err) {
      toast.error('Could not remove it', err instanceof ApiError ? err.message : undefined);
    }
  }

  let assignOpen = $state(false);

  const M_COLS = 'minmax(180px, 1fr) 170px minmax(260px, 3fr)';
  const A_COLS = 'minmax(160px, 1fr) minmax(200px, 1.6fr) minmax(140px, 1fr) 110px 40px';
</script>

<section class="dm-page">
  {#if loading && !catalog}
    <Skeleton width="40%" height="1.6rem" />
    <Skeleton width="100%" height="4rem" />
  {:else if loadError || !catalog}
    <EmptyState tone="error" title="Could not load this role" description={loadError ?? ''}>
      {#snippet action()}<a class="dm-btn" href="/users?tab=roles">Back to roles</a>{/snippet}
    </EmptyState>
  {:else}
    <div class="dm-ph">
      <h1>{isNew ? 'New role' : role?.display}</h1>
      {#if role}<span class="dm-sub">{role.builtin ? 'Built-in, read-only' : 'Custom role'} · {assignments.length} assignment{assignments.length === 1 ? '' : 's'}</span>{/if}
      <span class="dm-sp"></span>
      {#if role && allowed('roles.create')}
        <a class="dm-btn" href="/users/roles/new?from={encodeURIComponent(role.name)}"><Copy size={13} strokeWidth={1.8} /> Duplicate</a>
      {/if}
      {#if role && !role.builtin && allowed('roles.delete')}
        <button type="button" class="dm-btn dm-btn-danger" onclick={remove}><Trash2 size={13} strokeWidth={1.8} /> Delete…</button>
      {/if}
      {#if editable}
        <button type="button" class="dm-btn dm-btn-primary" disabled={busy || !dirty || !dDisplay.trim()} onclick={save}>{busy ? 'Saving…' : isNew ? 'Create role' : 'Save'}</button>
      {/if}
    </div>

    <p class="summary">{roleOneLine(catalog, { levels: dLevels, extras: dExtras })}</p>

    {#if editable}
      <div class="meta">
        <div class="dm-field"><label for="rd-display">Name</label>
          <input id="rd-display" class="dm-input" bind:value={dDisplay} oninput={() => { if (isNew) dName = slug(dDisplay); }} placeholder="Backup operator" /></div>
        {#if isNew}<div class="dm-field"><label for="rd-name">ID</label><input id="rd-name" class="dm-input dm-mono" bind:value={dName} /></div>{/if}
        <div class="dm-field wide"><label for="rd-desc">Description</label><input id="rd-desc" class="dm-input" bind:value={dDesc} placeholder="What the role is for, in one line" /></div>
      </div>
    {:else if role?.description}
      <p class="dm-sub desc">{role.description}</p>
    {/if}

    <div>
      <h2 class="dm-h2">Level per area</h2>
      <div class="dm-tbl" role="table" aria-label="Levels per area">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {M_COLS}">
          <span role="columnheader">Area</span><span role="columnheader">Level</span><span role="columnheader">Can · cannot</span>
        </div>
        {#each summaries as s (s.key)}
          {@const area = catalog.areas.find((a) => a.key === s.key)}
          <div class="dm-tr" role="row" style="grid-template-columns: {M_COLS}">
            <span>{s.label}{#if !s.scoped}<span class="dm-dim" title="Only an assignment on everything grants this area"> · everything only</span>{/if}</span>
            <span>
              {#if editable && area}
                <select class="dm-input lvl" aria-label="Level for {s.label}" value={s.level} onchange={(e) => (dLevels = { ...dLevels, [s.key]: e.currentTarget.value })}>
                  {#each area.levels as l (l.key)}<option value={l.key}>{l.label}</option>{/each}
                </select>
              {:else}
                <span class={s.level === 'none' ? 'dm-dim' : ''}>{s.levelLabel}</span>
              {/if}
            </span>
            <span class="cc">
              {#each s.can as t (t)}<span class="can">✓ {t}</span>{/each}
              {#each s.cannot as t (t)}<span class="cannot">✕ {t}</span>{/each}
            </span>
          </div>
        {/each}
      </div>
    </div>

    <div>
      <h2 class="dm-h2">Risky extras</h2>
      <p class="dm-sub sub">No level includes these; turn them on only where needed.</p>
      <div class="dm-tbl" role="table" aria-label="Extras">
        {#each catalog.extras as e (e.key)}
          <div class="dm-tr" role="row" style="grid-template-columns: 56px minmax(220px, 1fr) minmax(260px, 2fr)">
            <Switch checked={dExtras.includes(e.key)} label={e.label} hideLabel disabled={!editable} onchange={(v) => toggleExtra(e.key, v)} />
            <span>{e.label}</span>
            <span class="warntxt">⚠ {e.warning}</span>
          </div>
        {/each}
      </div>
    </div>

    {#if role}
      <div>
        <div class="hrow">
          <h2 class="dm-h2">Assignments</h2>
          <span class="dm-sp"></span>
          {#if allowed('users.update')}
            <button type="button" class="dm-btn dm-btn-sm" onclick={() => (assignOpen = true)}><Plus size={13} strokeWidth={2} /> Assign</button>
          {/if}
        </div>
        {#if assignments.length === 0}
          <div class="dm-tbl"><div class="dm-empty">Nobody holds this role yet.</div></div>
        {:else}
          <div class="dm-tbl" role="table" aria-label="Assignments of {role.display}">
            <div class="dm-tr dm-th" role="row" style="grid-template-columns: {A_COLS}">
              <span role="columnheader">User</span><span role="columnheader">On</span><span role="columnheader">Source</span><span role="columnheader">Added</span><span></span>
            </div>
            {#each assignments as a (a.id)}
              <div class="dm-tr" role="row" style="grid-template-columns: {A_COLS}">
                <a class="lnk dm-trunc" href="/users/{a.user_id}">{a.username || a.user_id}</a>
                <span class="dm-trunc">{scopeLabel(a.scope, hostNames)}</span>
                <span class="dm-dim dm-trunc">{a.source === 'idp' ? `via ${a.via || 'identity provider'}` : 'manual'}</span>
                <span class="dm-num dm-dim">{fmtAgo(a.created_at)}</span>
                <span>{#if allowed('users.update')}<button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" aria-label="Remove assignment" onclick={() => removeAssignment(a)}><Trash2 size={13} /></button>{/if}</span>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</section>

{#if role}
  <AssignmentDialog bind:open={assignOpen} {users} {roles} {catalog} role={role.name} onsaved={loadAll} />
{/if}

<style>
  .summary { margin: -8px 0 0; font-size: var(--fs-sm); color: var(--muted); }
  .desc { margin: -12px 0 0; }
  .meta { display: grid; grid-template-columns: 260px 220px minmax(260px, 1fr); gap: 12px; }
  .meta .wide { grid-column: span 1; }
  .lvl { width: 150px; min-height: 30px; }
  .cc { display: flex; flex-wrap: wrap; gap: 2px 12px; font-size: var(--fs-sm); min-width: 0; }
  .can { color: var(--fg); }
  .cannot { color: var(--subtle); }
  .warntxt { color: var(--warn); font-size: var(--fs-sm); }
  .sub { margin: -4px 0 8px; }
  .hrow { display: flex; align-items: center; }
  .lnk { color: var(--fg); text-decoration: none; }
  .lnk:hover { text-decoration: underline; }
</style>
