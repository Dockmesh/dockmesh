<script lang="ts" module>
  export interface LiveSample {
    cpu_percent: number;
    mem_used: number;
    mem_unlimited?: boolean;
    mem_limit: number;
    net_rx: number;
    net_tx: number;
    blk_read: number;
    blk_write: number;
  }
</script>

<script lang="ts">
  import { fmtBytes, fmtMem } from '$lib/format';
  import { untrack } from 'svelte';
  import { api, type HistoryReason, type MetricsSample } from '$lib/api';
  import MetricsChart, { type ChartEvent, type ChartSeries } from './ui/MetricsChart.svelte';
  import Segmented from './ui/Segmented.svelte';

  // Stored history feeds the time ranges; the live range uses the stats
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
  // Set from the history response: hosts whose agent records no history
  // answer with history=false and a reason.
  let hasHistory = $state(true);
  let noHistoryReason = $state<HistoryReason | null>(null);
  let historyUnlimited = $state(false);

  let range = $state<Range>('1h');
  let zoom = $state<[number, number] | null>(null);
  let samples = $state<MetricsSample[]>([]);
  let liveBuf = $state<MetricsSample[]>([]);
  let cores = $state(0);
  let memTotal = $state(0);
  let now = $state(Date.now());

  $effect(() => {
    void host;
    hasHistory = true;
    noHistoryReason = null;
  });

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
      const res = await api.containers.metrics(id, from, to, resolution(to - from), host);
      samples = res.samples;
      historyUnlimited = res.mem_unlimited;
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
    void id;
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

  const memLimit = $derived(data.reduce((m, s) => Math.max(m, s.mem_limit || 0), latest?.mem_limit ?? 0));
  // Docker reports the host RAM as the limit when none is set.
  const limited = $derived(memLimit > 0 && !(range === 'live' ? latest?.mem_unlimited : historyUnlimited));
  const memBase = $derived(limited ? memLimit : memTotal || memLimit);

  function rate(cur: number, prev: number | undefined, dt: number): number | null {
    if (prev == null || dt <= 0 || cur < prev) return null;
    return (cur - prev) / dt;
  }

  const series = $derived.by<ChartSeries[]>(() => {
    const cpu = data.map((s) => (running || range !== 'live' ? s.cpu_percent : null));
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
        fmt: (v) => `${fmtMem((memBase * v) / 100)} · ${v.toFixed(0)} %`
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
      Memory is relative to its limit of {fmtMem(memLimit)}.
    {:else if memTotal}
      No memory limit, so memory is relative to host RAM ({fmtMem(memTotal)}).
    {/if}
    {#if cores}
      CPU % is relative to all {cores} cores of {hostName}.
    {/if}
    {#if noHistoryReason === 'agent_no_history'}
      History is not recorded for containers on agent hosts yet, so {hostName} shows live values only.
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
