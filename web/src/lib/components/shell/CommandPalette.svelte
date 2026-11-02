<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { goto } from '$app/navigation';
  import { api, isFanOut } from '$lib/api';
  import { hosts } from '$lib/stores/host.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { trapFocus, portal } from '$lib/a11y';
  import { NAV, SETTINGS_NAV } from './nav';
  import { Layers, Box, Server, Settings, Sun, Plus, CircleUser, KeyRound } from 'lucide-svelte';

  // ⌘K: finds pages, stacks, containers, hosts and settings, plus a few
  // actions. Objects load when the palette opens.
  let { open = $bindable(false), ontheme }: { open: boolean; ontheme: () => void } = $props();

  interface Entry {
    group: string;
    label: string;
    sub?: string;
    href?: string;
    act?: () => void;
    icon: any;
  }

  const GROUP: Record<string, () => string> = {
    Pages: m.shell_pal_pages,
    Settings: m.common_settings,
    Actions: m.shell_pal_actions,
    Stacks: m.common_stacks,
    Containers: m.common_containers,
    Hosts: m.common_hosts
  };

  let q = $state('');
  let idx = $state(0);
  let objects = $state<Entry[]>([]);
  let list: HTMLDivElement | undefined = $state();

  async function loadObjects() {
    const out: Entry[] = [];
    const host = hosts.id;
    const [stacks, containers] = await Promise.all([
      allowed('stacks.view') ? api.stacks.list().catch(() => []) : Promise.resolve([]),
      allowed('containers.view') ? api.containers.list(true, host).then((r) => (isFanOut(r) ? r.items : r)).catch(() => []) : Promise.resolve([])
    ]);
    for (const s of stacks) out.push({ group: 'Stacks', label: s.name, sub: s.deployment?.host_name ?? '', href: `/stacks/${encodeURIComponent(s.name)}`, icon: Layers });
    for (const c of containers as Array<{ Id?: string; id?: string; Names?: string[]; State?: string; host_id?: string; Labels?: Record<string, string> }>) {
      const id = c.Id ?? c.id ?? '';
      const name = (c.Names?.[0] ?? id).replace(/^\//, '');
      const stack = c.Labels?.['com.docker.compose.project'];
      const qs = c.host_id && c.host_id !== 'local' ? `?host=${encodeURIComponent(c.host_id)}` : '';
      out.push({ group: 'Containers', label: name, sub: `${stack ? m.shell_pal_stack({ name: stack }) : m.ui_standalone()} · ${c.State ?? ''}`, href: `/containers/${id}${qs}`, icon: Box });
    }
    for (const h of hosts.available) out.push({ group: 'Hosts', label: h.name, sub: `${h.kind} · ${h.status}`, href: `/hosts/${encodeURIComponent(h.id)}`, icon: Server });
    objects = out;
  }

  $effect(() => {
    if (!open) return;
    q = '';
    idx = 0;
    loadObjects();
  });

  const base = $derived.by<Entry[]>(() => {
    const pages: Entry[] = NAV.flatMap((g) =>
      g.items.filter((i) => i.show()).map((i) => ({ group: 'Pages', label: i.label, sub: g.title ?? '', href: i.href, icon: i.icon }))
    );
    const settings: Entry[] = SETTINGS_NAV.filter((s) => s.show()).map((s) => ({ group: 'Settings', label: s.label, sub: s.hint, href: s.href, icon: Settings }));
    const actions: Entry[] = [
      ...(allowed('stacks.create') ? [{ group: 'Actions', label: m.shell_pal_new_stack(), sub: m.common_stacks(), href: '/stacks?new=1', icon: Plus }] : []),
      { group: 'Actions', label: m.shell_my_account(), sub: m.shell_pal_account_sub(), href: '/account', icon: CircleUser },
      { group: 'Actions', label: m.shell_api_tokens(), sub: m.shell_pal_tokens_sub(), href: '/tokens', icon: KeyRound },
      { group: 'Actions', label: m.shell_pal_toggle_theme(), act: ontheme, icon: Sun }
    ];
    return [...pages, ...settings, ...actions];
  });

  const results = $derived.by<Entry[]>(() => {
    const s = q.trim().toLowerCase();
    if (!s) return base.filter((e) => e.group === 'Pages' || e.group === 'Actions');
    return [...base, ...objects].filter((e) => `${e.label} ${e.sub ?? ''}`.toLowerCase().includes(s)).slice(0, 40);
  });

  $effect(() => {
    void results;
    idx = 0;
  });

  function pick(e: Entry | undefined) {
    if (!e) return;
    open = false;
    if (e.href) goto(e.href);
    else e.act?.();
  }

  function onKey(ev: KeyboardEvent) {
    if (ev.key === 'Escape') {
      ev.preventDefault();
      open = false;
    } else if (ev.key === 'ArrowDown') {
      ev.preventDefault();
      idx = Math.min(results.length - 1, idx + 1);
      scrollActive();
    } else if (ev.key === 'ArrowUp') {
      ev.preventDefault();
      idx = Math.max(0, idx - 1);
      scrollActive();
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      pick(results[idx]);
    }
  }
  function scrollActive() {
    queueMicrotask(() => list?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' }));
  }
</script>

{#if open}
  <div class="ov" use:portal role="presentation" onmousedown={(e) => e.target === e.currentTarget && (open = false)}>
    <div class="pal" role="dialog" aria-modal="true" aria-label={m.common_search()} use:trapFocus>
      <input
        bind:value={q}
        placeholder={m.shell_pal_placeholder()}
        autocomplete="off"
        spellcheck="false"
        role="combobox"
        aria-expanded="true"
        aria-controls="pal-list"
        aria-activedescendant={results.length ? `pal-${idx}` : undefined}
        onkeydown={onKey}
        data-autofocus
      />
      <div class="list" id="pal-list" role="listbox" bind:this={list}>
        {#each results as e, i (e.group + e.label + (e.href ?? '') + i)}
          {#if i === 0 || results[i - 1].group !== e.group}
            <div class="g" role="presentation">{GROUP[e.group]?.() ?? e.group}</div>
          {/if}
          {@const Icon = e.icon}
          <div
            id="pal-{i}"
            class="it"
            role="option"
            aria-selected={i === idx}
            tabindex="-1"
            onmousemove={() => (idx = i)}
            onclick={() => pick(e)}
            onkeydown={() => {}}
          >
            <Icon size={15} strokeWidth={1.8} />
            <span class="l">{e.label}</span>
            {#if e.sub}<small>{e.sub}</small>{/if}
          </div>
        {:else}
          <div class="none">{m.shell_pal_nothing()}</div>
        {/each}
      </div>
    </div>
  </div>
{/if}

<style>
  .ov {
    position: fixed;
    inset: 0;
    background: var(--overlay);
    z-index: 120;
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-top: 12vh;
  }
  .pal {
    width: min(640px, 94vw);
    background: var(--menu);
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    box-shadow: var(--pop);
    overflow: hidden;
  }
  input {
    width: 100%;
    border: 0;
    border-bottom: 1px solid var(--border);
    min-height: 48px;
    font: inherit;
    font-size: var(--fs-md);
    background: transparent;
    color: var(--fg);
    padding: 0 16px;
  }
  input:focus { outline: none; }
  .list { max-height: 52vh; overflow: auto; padding: 6px; }
  .g { font-size: var(--fs-xs); color: var(--subtle); padding: 8px 10px 3px; }
  .it {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 10px;
    min-height: 36px;
    border-radius: 7px;
    cursor: pointer;
    min-width: 0;
  }
  .it[aria-selected='true'] { background: var(--field); }
  .l { white-space: nowrap; }
  small {
    color: var(--subtle);
    margin-left: auto;
    font-size: var(--fs-xs);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .none { padding: 24px; text-align: center; color: var(--subtle); }
</style>
