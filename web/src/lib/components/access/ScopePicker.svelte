<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type HostInfo, type Scope, type ScopeType, type StackListEntry } from '$lib/api';
  import { Segmented } from '$lib/components/ui';

  let {
    value = $bindable({ type: 'global' }),
    label = 'Scope',
    onchange
  }: { value: Scope; label?: string; onchange?: (s: Scope) => void } = $props();

  function set(v: Scope) {
    value = v;
    onchange?.(v);
  }

  let hosts = $state<HostInfo[]>([]);
  let tags = $state<string[]>([]);
  let stacks = $state<StackListEntry[]>([]);

  onMount(async () => {
    const [h, t, s] = await Promise.all([
      api.hosts.list().catch(() => [] as HostInfo[]),
      api.hosts.allTags().catch(() => [] as string[]),
      api.stacks.list().catch(() => [] as StackListEntry[])
    ]);
    hosts = h.filter((x) => x.kind !== 'all' && x.status !== 'revoked');
    tags = t;
    stacks = s;
  });

  const OPTIONS = [
    { key: 'global', label: 'Everything' },
    { key: 'host', label: 'Host' },
    { key: 'host_tag', label: 'Host tag' },
    { key: 'stack', label: 'Stack' }
  ];

  function setType(t: string) {
    const type = t as ScopeType;
    if (type === 'global') set({ type });
    else if (type === 'host') set({ type, host: hosts[0]?.id ?? 'local' });
    else if (type === 'host_tag') set({ type, value: tags[0] ?? '' });
    else {
      const s = stacks[0];
      set({ type, value: s?.name ?? '', host: s?.deployment?.host_id ?? '' });
    }
  }

  function pickStack(name: string) {
    const s = stacks.find((x) => x.name === name);
    set({ type: 'stack', value: name, host: s?.deployment?.host_id || '' });
  }

</script>

<div class="dm-field">
  <span class="lbl">{label}</span>
  <Segmented options={OPTIONS} value={value.type} onchange={setType} label={label} size="sm" />
  {#if value.type === 'host'}
    <select class="dm-input" aria-label="Host" value={value.host} onchange={(e) => set({ type: 'host', host: e.currentTarget.value })}>
      {#each hosts as h (h.id)}<option value={h.id}>{h.name}</option>{/each}
    </select>
  {:else if value.type === 'host_tag'}
    <input class="dm-input" aria-label="Host tag" list="scope-tags" placeholder="tag" value={value.value ?? ''}
      oninput={(e) => set({ type: 'host_tag', value: e.currentTarget.value.trim().toLowerCase() })} />
    <datalist id="scope-tags">{#each tags as t (t)}<option value={t}></option>{/each}</datalist>
  {:else if value.type === 'stack'}
    <div class="row">
      <select class="dm-input" aria-label="Stack" value={value.value} onchange={(e) => pickStack(e.currentTarget.value)}>
        {#each stacks as s (s.name)}<option value={s.name}>{s.name}</option>{/each}
      </select>
      <select class="dm-input" aria-label="Host of the stack" value={value.host}
        onchange={(e) => set({ ...value, host: e.currentTarget.value })}>
        <option value="">Any host</option>
        {#each hosts as h (h.id)}<option value={h.id}>{h.name}</option>{/each}
      </select>
    </div>
    <span class="dm-sub">{value.host ? 'Covers the stack while it runs on this host.' : 'Covers the stack on any host, also before it is deployed.'}</span>
  {/if}
</div>

<style>
  .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
  .row { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .dm-field { gap: 6px; }
</style>
