<script lang="ts" module>
  export interface LiveSample {
    cpu_percent: number;
    mem_used: number;
    mem_limit: number;
    net_rx: number;
    net_tx: number;
    blk_read: number;
    blk_write: number;
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { api, type MetricsSample } from '$lib/api';
  import MetricsChart, { type ChartEvent, type ChartSeries } from './ui/MetricsChart.svelte';
  import Segmented from './ui/Segmented.svelte';

  // "Container resources": the X1 chart fed by the stored history
  // (/containers/{id}/metrics) and, for the live range, by the stats
  // websocket samples the page already receives.
  let {
    id,
    host = 'local',
    hostName = 'this host',
    running,
    latest = null,
    events = []
  }: {
    id: string;
    host?: string;
    hostName?: string;
    running: boolean;
    latest?: LiveSample | null;
    events?: ChartEvent[];
  } = $props();

  type Range = 'live' | '1h' | '6h' | '24h' | '7d' | '30d';
  const RANGE_S: Record<Range, number> = { live: 120, '1h': 3600, '6h': 21600, '24h': 86400, '7d': 604800, '30d': 2592000 };
  // History is collected on the server for its own containers only.
  const hasHistory = $derived(host === 'local');

  let range = $state<Range>('1h');
  let zoom = $state<[number, number] | null>(null);
  let samples = $state<MetricsSample[]>([]);
  let liveBuf = $state<MetricsSample[]>([]);
  let cores = $state(0);
  let memTotal = $state(0);
  let now = $state(Date.now());

  $effect(() => {
    if (!hasHistory) range = 'live';
  });

  $effect(() => {
    const h = host;
    api.system
      .metrics(h)
      .then((m) => {
        if ('cpu_cores' in m) {
          cores = m.cpu_cores;
          memTotal = m.mem_total;
        }
      })
      .catch(() => {});
  });

  $effect(() => {
    if (!latest) return;
    const s: MetricsSample = { ts: Math.floor(Date.now() / 1000), ...latest };
    const prev = untrack(() => liveBuf);
    liveBuf = [...prev.filter((x) => x.ts !== s.ts), s].slice(-240);
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
      samples = await api.containers.metrics(id, from, to, resolution(to - from));
    } catch {
      samples = [];
    }
    now = Date.now();
  }

  $effect(() => {
    void range;
    void zoom;
    void id;
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

  const memLimit = $derived(data.reduce((m, s) => Math.max(m, s.mem_limit || 0), latest?.mem_limit ?? 0));
  // Docker reports the host RAM as the limit when none is set.
  const limited = $derived(memLimit > 0 && (!memTotal || memLimit < memTotal * 0.98));
  const memBase = $derived(limited ? memLimit : memTotal || memLimit);

  function rate(cur: number, prev: number | undefined, dt: number): number | null {
    if (prev == null || dt <= 0 || cur < prev) return null;
    return (cur - prev) / dt;
  }

  function fmtBytes(b: number): string {
    if (!isFinite(b)) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (b >= 1024 && i < u.length - 1) {
      b /= 1024;
      i++;
    }
    return `${b < 10 && i > 0 ? b.toFixed(1) : Math.round(b)} ${u[i]}`;
  }

  const series = $derived.by<ChartSeries[]>(() => {
    const c = cores || 1;
    const cpu = data.map((s) => (running || range !== 'live' ? s.cpu_percent / c : null));
    const mem = data.map((s) => (memBase > 0 ? (s.mem_used / memBase) * 100 : null));
    const net = data.map((s, i) =>
      i === 0 ? null : rate(s.net_rx + s.net_tx, data[i - 1].net_rx + data[i - 1].net_tx, s.ts - data[i - 1].ts)
    );
    const blk = data.map((s, i) =>
      i === 0
        ? null
        : rate(s.blk_read + s.blk_write, data[i - 1].blk_read + data[i - 1].blk_write, s.ts - data[i - 1].ts)
    );
    return [
      { label: 'CPU', color: 'var(--accent)', values: cpu, max: 100, fmt: (v) => `${v.toFixed(1)} %` },
      {
        label: 'Memory',
        color: 'var(--violet)',
        values: mem,
        max: 100,
        fmt: (v) => `${fmtBytes((memBase * v) / 100)} · ${v.toFixed(0)} %`
      },
      { label: 'Network', color: 'var(--ok)', values: net, fmt: (v) => `${fmtBytes(v)}/s` },
      { label: 'Block I/O', color: 'var(--warn)', values: blk, fmt: (v) => `${fmtBytes(v)}/s` }
    ];
  });

  const rangeOptions = $derived(
    (Object.keys(RANGE_S) as Range[])
      .filter((r) => r === 'live' || hasHistory)
      .map((r) => ({ key: r, label: r }))
  );
</script>

<section class="cm" aria-label="Container resources">
  <div class="cm-h">
    <h2 class="dm-h2">Container resources</h2>
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
    {events}
    onzoom={(a, b) => (zoom = [a, b])}
    onreset={() => (zoom = null)}
    empty={range === 'live'
      ? running
        ? 'Waiting for the first sample…'
        : 'The container is not running.'
      : 'No samples stored for this range yet.'}
  />
  <p class="dm-sub cm-hint">
    Measured for this container only.
    {#if limited}
      Memory is relative to its limit of {fmtBytes(memLimit)}.
    {:else if memTotal}
      No memory limit, so memory is relative to host RAM ({fmtBytes(memTotal)}).
    {/if}
    {#if cores}
      CPU % is relative to all {cores} cores of {hostName}.
    {/if}
    {#if !hasHistory}
      History is stored for containers on the server host; agent hosts show live values.
    {/if}
  </p>
  {#if events.some((e) => e.ts >= domain[0] && e.ts <= domain[1])}
    <p class="dm-sub cm-legend"><span style="color: var(--accent)">┊</span> event · drag across a chart to zoom</p>
  {/if}
</section>

<style>
  .cm { display: flex; flex-direction: column; gap: 10px; }
  .cm-h { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .cm-h .dm-h2 { margin: 0; }
  .zoomed { color: var(--accent); font-size: var(--fs-xs); }
  .cm-hint, .cm-legend { margin: 0; font-size: var(--fs-xs); }
</style>
