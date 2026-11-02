<script lang="ts" module>
  import { m } from '$lib/paraglide/messages.js';

  export interface Member {
    name: string;
    state: string;
    unhealthy?: boolean;
  }

  export type HealthTone = 'ok' | 'warn' | 'bad' | 'off';

  /** Worst state of a group: any unhealthy member is bad, some members
   * down is a warning, none running is stopped. */
  export function groupTone(running: number, total: number, unhealthy: number): HealthTone {
    if (total === 0) return 'off';
    if (unhealthy > 0) return 'bad';
    if (running === total) return 'ok';
    if (running === 0) return 'off';
    return 'warn';
  }

  export function groupLabel(running: number, total: number, unhealthy: number): string {
    if (total === 0) return m.ui_health_no_containers();
    if (running === 0) return m.common_stopped();
    const base = m.ui_health_running({ running, total });
    return unhealthy ? `${base} · ${m.ui_health_n_unhealthy({ count: unhealthy })}` : base;
  }
</script>

<script lang="ts">
  // One indicator for a group of containers: the worst state as a dot plus
  // a count; the members that are not fine go into the tooltip.
  let {
    running,
    total,
    unhealthy = 0,
    members = [],
    unknown = false,
    notDeployed = false
  }: { running: number; total: number; unhealthy?: number; members?: Member[]; unknown?: boolean; notDeployed?: boolean } = $props();

  const tone = $derived(unknown || notDeployed ? 'off' : groupTone(running, total, unhealthy));
  const label = $derived(notDeployed ? m.ui_health_not_deployed() : unknown ? m.ui_health_no_status() : groupLabel(running, total, unhealthy));
  const tip = $derived.by(() => {
    const bad = members.filter((x) => x.unhealthy || x.state !== 'running');
    if (!bad.length) return label;
    const lines = bad.slice(0, 8).map((x) => `${x.name}: ${x.unhealthy ? m.ui_health_unhealthy() : x.state}`);
    if (bad.length > 8) lines.push(m.ui_health_and_more({ count: bad.length - 8 }));
    return lines.join('\n');
  });
</script>

<span class="sh" title={tip}><span class="dm-dot {tone === 'off' ? '' : tone}"></span><span class="t">{label}</span></span>

<style>
  .sh { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; font-variant-numeric: tabular-nums; }
  .t { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
