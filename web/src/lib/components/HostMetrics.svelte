<script lang="ts">
  import { fmtBytes, fmtMem } from '$lib/format';
  import { untrack } from 'svelte';
  import { api, type HistoryReason, type HostMetricsSample, type SystemMetrics } from '$lib/api';
  import MetricsChart, { type ChartSeries } from './ui/MetricsChart.svelte';
  import Segmented from './ui/Segmented.svelte';

  // Stored history feeds the time ranges; the live range collects the
  // snapshots the host page polls anyway.
  let {
    host,
    hostName,
    latest = null
  }: {
    host: string;
    hostName: string;
    latest?: SystemMetrics | null;
  } = $props();

  type Range = 'live' | '1h' | '6h' | '24h' | '7d' | '30d';
  const RANGE_S: Record<Range, number> = { live: 600, '1h': 3600, '6h': 21600, '24h': 86400, '7d': 604800, '30d': 2592000 };

  let hasHistory = $state(true);
  let noHistoryReason = $state<HistoryReason | null>(null);
  let range = $state<Range>('1h');
  let zoom = $state<[number, number] | null>(null);
  let samples = $state<HostMetricsSample[]>([]);
  let liveBuf = $state<HostMetricsSample[]>([]);
  let now = $state(Date.now());

  $effect(() => {
    void host;
    untrack(() => {
      hasHistory = true;
      noHistoryReason = null;
      liveBuf = [];
      samples = [];
      zoom = null;
    });
  });

  $effect(() => {
    if (!hasHistory) range = 'live';
  });

  $effect(() => {
    if (!latest) return;
    const s: HostMetricsSample = {
      ts: Math.floor(Date.now() / 1000),
      cpu_percent: latest.cpu_percent,
      cpu_cores: latest.cpu_cores,
      mem_used: latest.mem_used,
      mem_total: latest.mem_total,
      disk_used: latest.disk_used,
      disk_total: latest.disk_total
    };
    const prev = untrack(() => liveBuf);
    liveBuf = [...prev.filter((x) => x.ts !== s.ts), s].slice(-120);
  });

  function resolution(seconds: number): 'raw' | '1m' | '1h' {
    if (seconds <= 6 * 3600) return 'raw';
    if (seconds <= 7 * 86400) return '1m';
    return '1h';
  }

  async function load() {
    if (range === 'live' || !hasHistory) return;
    const to = zoom ? Math.floor(zoom[1] / 1000) : Math.floor(Date.now() / 1000);
    const from = zoom ? Math.floor(zoom[0] / 1000) : to - RANGE_S[range];
    try {
      const res = await api.hosts.metrics(host, from, to, resolution(to - from));
      samples = res.samples;
      hasHistory = res.history;
      noHistoryReason = res.reason ?? null;
    } catch {
      samples = [];
    }
    now = Date.now();
  }

  $effect(() => {
    void range;
    void zoom;
    void host;
    load();
    if (range === 'live') return;
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  });

  $effect(() => {
    if (range !== 'live') return;
    const t = setInterval(() => (now = Date.now()), 2000);
    return () => clearInterval(t);
  });

  const domain = $derived.by<[number, number]>(() => {
    if (zoom) return zoom;
    return [now - RANGE_S[range] * 1000, now];
  });
  const data = $derived(range === 'live' ? liveBuf : samples);
  const ts = $derived(data.map((s) => s.ts * 1000));

  function pct(used: number, total: number): number | null {
    return total > 0 ? (used / total) * 100 : null;
  }

  const series = $derived.by<ChartSeries[]>(() => [
    {
      label: 'CPU',
      color: 'var(--accent)',
      values: data.map((s) => s.cpu_percent),
      max: 100,
      fmt: (v) => `${v.toFixed(1)} %`
    },
    {
      label: 'Memory',
      color: 'var(--violet)',
      values: data.map((s) => pct(s.mem_used, s.mem_total)),
      max: 100,
      fmt: (v) => `${v.toFixed(0)} %`
    },
    {
      label: 'Disk',
      color: 'var(--warn)',
      values: data.map((s) => pct(s.disk_used, s.disk_total)),
      max: 100,
      fmt: (v) => `${v.toFixed(1)} %`
    }
  ]);

  const rangeOptions = $derived(
    (Object.keys(RANGE_S) as Range[])
      .filter((r) => r === 'live' || hasHistory)
      .map((r) => ({ key: r, label: r }))
  );
</script>

<section class="hm" aria-label="Host resources">
  <div class="hm-h">
    <h2 class="dm-h2">Resources</h2>
    {#if zoom}
      <span class="zoomed">Zoomed · double-click a chart to reset</span>
    {/if}
    <span class="dm-sp"></span>
    <Segmented
      label="Time range"
      size="sm"
      options={rangeOptions}
      value={zoom ? '' : range}
      onchange={(k) => {
        zoom = null;
        range = k as Range;
      }}
    />
  </div>
  <MetricsChart
    {ts}
    {series}
    from={domain[0]}
    to={domain[1]}
    onzoom={(a, b) => (zoom = [a, b])}
    onreset={() => (zoom = null)}
    empty={range === 'live' ? 'Waiting for the first sample…' : 'No samples stored for this range yet.'}
  />
  <p class="dm-sub hm-hint">
    {#if latest}
      CPU % is relative to all {latest.cpu_cores} cores, memory to {fmtMem(latest.mem_total)} of RAM{latest.disk_total > 0
        ? `, disk to the ${fmtBytes(latest.disk_total)} filesystem at ${latest.disk_path}`
        : ''}.
    {/if}
    {#if noHistoryReason === 'agent_no_history'}
      History is not recorded for agent hosts yet, so {hostName} shows live values while this page is open.
    {/if}
  </p>
</section>

<style>
  .hm { display: flex; flex-direction: column; gap: 10px; }
  .hm-h { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .hm-h .dm-h2 { margin: 0; }
  .zoomed { color: var(--accent); font-size: var(--fs-xs); }
  .hm-hint { margin: 0; font-size: var(--fs-xs); }
</style>
