<script lang="ts" module>
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
    if (total === 0) return 'No containers';
    if (running === 0) return 'Stopped';
    const base = `${running}/${total} running`;
    return unhealthy ? `${base} · ${unhealthy} unhealthy` : base;
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
    unknown = false
  }: { running: number; total: number; unhealthy?: number; members?: Member[]; unknown?: boolean } = $props();

  const tone = $derived(unknown ? 'off' : groupTone(running, total, unhealthy));
  const label = $derived(unknown ? 'No status' : groupLabel(running, total, unhealthy));
  const tip = $derived.by(() => {
    const bad = members.filter((m) => m.unhealthy || m.state !== 'running');
    if (!bad.length) return label;
    const lines = bad.slice(0, 8).map((m) => `${m.name}: ${m.unhealthy ? 'unhealthy' : m.state}`);
    if (bad.length > 8) lines.push(`and ${bad.length - 8} more`);
    return lines.join('\n');
  });
</script>

<span class="sh" title={tip}><span class="dm-dot {tone === 'off' ? '' : tone}"></span><span class="t">{label}</span></span>

<style>
  .sh { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; font-variant-numeric: tabular-nums; }
  .t { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
