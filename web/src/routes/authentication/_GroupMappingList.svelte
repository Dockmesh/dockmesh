<script lang="ts">
  // Reusable group→role mapping list, used by all 4 provider kinds.
  // Renders the list of (group string, role select) rows plus the
  // default-role select that catches users with no matching group.
  import type { CustomRole } from '$lib/api';
  import type { GroupMapping } from './_types';
  import { Plus, X } from 'lucide-svelte';

  interface Props {
    mappings: GroupMapping[];
    defaultRole: string;
    roles: CustomRole[];
    groupClaim?: string;
    /** Optional label shown above the list. */
    title?: string;
    /** Hint shown below the title. */
    hint?: string;
  }

  let {
    mappings = $bindable(),
    defaultRole = $bindable(),
    roles,
    groupClaim,
    title = 'Group → role mappings',
    hint = 'Map IdP group claim values to Dockmesh roles. First match wins.',
  }: Props = $props();

  const builtIns = ['viewer', 'operator', 'deployer', 'host-admin', 'admin'] as const;

  const allRoleNames = $derived.by(() => {
    if (roles.length > 0) return roles.map((r) => r.name);
    return [...builtIns];
  });

  function addRow() {
    mappings = [...mappings, { group: '', role: 'operator' }];
  }
  function remove(i: number) {
    mappings = mappings.filter((_, idx) => idx !== i);
  }
  function setGroup(i: number, v: string) {
    mappings = mappings.map((m, idx) => (idx === i ? { ...m, group: v } : m));
  }
  function setRole(i: number, v: string) {
    mappings = mappings.map((m, idx) => (idx === i ? { ...m, role: v } : m));
  }
</script>

<div class="gml">
  <div class="gml-head">
    <span class="gml-title">{title}</span>
    {#if hint}
      <span class="gml-hint">{hint}</span>
    {/if}
  </div>

  {#if mappings.length > 0}
    <div class="gml-list">
      {#each mappings as m, i (i)}
        <div class="gml-row">
          <input
            class="dm-input gml-group"
            placeholder={groupClaim ? `value of "${groupClaim}" claim` : 'group name'}
            value={m.group}
            oninput={(e) => setGroup(i, (e.target as HTMLInputElement).value)}
          />
          <select
            class="dm-input gml-role"
            value={m.role}
            onchange={(e) => setRole(i, (e.target as HTMLSelectElement).value)}
          >
            {#each allRoleNames as r}
              <option value={r}>{r}</option>
            {/each}
          </select>
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs gml-remove"
            onclick={() => remove(i)}
            title="Remove mapping"
            aria-label="Remove mapping"
          >
            <X size={11} strokeWidth={1.5} />
          </button>
        </div>
      {/each}
    </div>
  {/if}

  <button
    type="button"
    class="dm-btn dm-btn-ghost dm-btn-xs gml-add"
    onclick={addRow}
  >
    <Plus size={11} strokeWidth={1.5} /> Add mapping
  </button>

  <div class="gml-default">
    <span class="gml-default-label">Default role</span>
    <select
      class="dm-input gml-default-select"
      value={defaultRole}
      onchange={(e) => (defaultRole = (e.target as HTMLSelectElement).value)}
    >
      {#each allRoleNames as r}
        <option value={r}>{r}</option>
      {/each}
    </select>
    <span class="gml-default-hint">Used when no mapping matches.</span>
  </div>
</div>

<style>
  .gml {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    border: 1px solid var(--border-subtle);
    border-radius: 5px;
    background: var(--surface);
  }
  .gml-head {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .gml-title {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .gml-hint {
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.5;
  }
  .gml-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .gml-row {
    display: grid;
    grid-template-columns: minmax(0, 1.6fr) 130px 30px;
    gap: 6px;
    align-items: center;
  }
  .gml-group {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .gml-role {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .gml-remove {
    color: var(--color-danger-400);
    padding: 4px 6px;
  }
  .gml-add {
    align-self: flex-start;
  }
  .gml-default {
    display: grid;
    grid-template-columns: 110px 200px 1fr;
    gap: 8px;
    align-items: center;
    margin-top: 4px;
    padding-top: 8px;
    border-top: 1px dashed var(--border-subtle);
  }
  .gml-default-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .gml-default-select {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .gml-default-hint {
    font-size: 11px;
    color: var(--fg-subtle);
  }
  @media (max-width: 720px) {
    .gml-row {
      grid-template-columns: 1fr 110px 28px;
    }
    .gml-default {
      grid-template-columns: 1fr;
    }
  }
</style>
