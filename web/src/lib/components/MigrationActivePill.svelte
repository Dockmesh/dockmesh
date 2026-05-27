<script lang="ts">
  // Topbar pill that surfaces active stack migrations + recent failures.
  // Migrations are operational events tied to stacks/hosts and have no
  // dedicated page anymore — this pill plus a global drawer takes the
  // sidebar entry's place.
  //
  // Three render states:
  //   - hidden — no active runs + no recent failures (don't nag)
  //   - active — warning-tinted, "N running"
  //   - failed — danger-tinted, "N failed" (over the last 24h)
  //
  // Polls every 5s while gated on stack.deploy — fast enough to feel
  // alive during a multi-minute migration, cheap enough at small scale.
  import { api, type Migration } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { ArrowRightLeft, AlertTriangle } from 'lucide-svelte';

  interface Props {
    onOpen?: () => void;
  }
  let { onOpen }: Props = $props();

  let active = $state<Migration[]>([]);
  let failedRecent = $state(0);
  let booted = $state(false);

  async function load() {
    try {
      const [a, recent] = await Promise.all([
        api.migrations.active().catch(() => [] as Migration[]),
        api.migrations.list(50).catch(() => [] as Migration[]),
      ]);
      active = a;
      const cutoff = Date.now() - 24 * 60 * 60 * 1000;
      failedRecent = recent.filter((m) => {
        if (m.status !== 'failed') return false;
        const when = m.completed_at ?? m.started_at ?? m.created_at;
        return when ? new Date(when).getTime() >= cutoff : false;
      }).length;
    } catch {
      /* silent — pill hides on error */
    } finally {
      booted = true;
    }
  }

  let timer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    if (!allowed('stacks.deploy')) return;
    load();
    timer = setInterval(load, 5_000);
    return () => { if (timer) clearInterval(timer); };
  });
</script>

{#if booted && allowed('stacks.deploy')}
  {#if active.length > 0}
    <button
      type="button"
      class="mig-pill mig-pill-active"
      onclick={onOpen}
      title="Migrations in progress — click for details"
    >
      <ArrowRightLeft size={11} strokeWidth={1.8} />
      <span class="font-mono">{active.length} migration{active.length === 1 ? '' : 's'} running</span>
    </button>
  {:else if failedRecent > 0}
    <button
      type="button"
      class="mig-pill mig-pill-fail"
      onclick={onOpen}
      title="Recent failures need attention"
    >
      <AlertTriangle size={11} strokeWidth={1.8} />
      <span class="font-mono">{failedRecent} migration{failedRecent === 1 ? '' : 's'} failed</span>
    </button>
  {/if}
{/if}

<style>
  .mig-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg);
    color: var(--fg-subtle);
    font-size: 10.5px;
    cursor: pointer;
    transition: border-color 120ms, background 120ms, color 120ms;
    font: inherit;
  }
  .mig-pill:hover { border-color: var(--border-strong); }
  .mig-pill-active {
    color: var(--color-warning-400);
    border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
  }
  .mig-pill-active:hover {
    background: color-mix(in srgb, var(--color-warning-500) 14%, transparent);
  }
  .mig-pill-fail {
    color: var(--color-danger-400);
    border-color: color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
  }
  .mig-pill-fail:hover {
    background: color-mix(in srgb, var(--color-danger-500) 14%, transparent);
  }
</style>
