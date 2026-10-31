<script lang="ts" module>
  export interface ChartSeries {
    label: string;
    color: string;
    values: (number | null)[];
    fmt: (v: number) => string;
    // Fixed upper bound (100 for percentages); otherwise scaled to the data.
    max?: number;
  }
  export interface ChartEvent {
    ts: number;
    label: string;
    color?: string;
  }
</script>

<script lang="ts">
  // Small multiples that share one time axis: one crosshair across all
  // charts, a tooltip with every series, drag to zoom, double-click to reset
  // and dashed event markers.
  let {
    ts,
    series,
    from,
    to,
    events = [],
    onzoom,
    onreset,
    empty = 'No data for this range.'
  }: {
    ts: number[];
    series: ChartSeries[];
    from: number;
    to: number;
    events?: ChartEvent[];
    onzoom?: (from: number, to: number) => void;
    onreset?: () => void;
    empty?: string;
  } = $props();

  const W = 300;
  const H = 100;
  const span = $derived(Math.max(1, to - from));

  let hover = $state<number | null>(null);
  let drag = $state<{ a: number; b: number } | null>(null);
  let box: HTMLDivElement;
  let tipX = $state(0);
  let tipY = $state(0);

  function niceMax(v: number): number {
    if (!isFinite(v) || v <= 0) return 1;
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
    return 10 * p;
  }

  const scaled = $derived(
    series.map((s) => {
      const vals = s.values.filter((v): v is number => v != null && isFinite(v));
      const max = s.max ?? niceMax(Math.max(0, ...vals) * 1.1);
      const segs: string[] = [];
      let cur: string[] = [];
      s.values.forEach((v, i) => {
        if (v == null || !isFinite(v)) {
          if (cur.length) segs.push(cur.join(' '));
          cur = [];
          return;
        }
        const x = ((ts[i] - from) / span) * W;
        const y = H - Math.min(1, Math.max(0, v / max)) * H;
        cur.push(`${x.toFixed(1)},${y.toFixed(1)}`);
      });
      if (cur.length) segs.push(cur.join(' '));
      const last = [...s.values].reverse().find((v) => v != null && isFinite(v));
      return { ...s, segs, last: last ?? null };
    })
  );

  const hasData = $derived(ts.length > 0 && series.some((s) => s.values.some((v) => v != null)));

  function fmtT(t: number, withDate = span > 2 * 86400_000): string {
    const d = new Date(t);
    const p = (n: number) => String(n).padStart(2, '0');
    const hm = `${p(d.getHours())}:${p(d.getMinutes())}`;
    if (withDate) return `${p(d.getDate())}.${p(d.getMonth() + 1)}. ${hm}`;
    return span <= 3600_000 ? `${hm}:${p(d.getSeconds())}` : hm;
  }
  const ticks = $derived([0, 0.25, 0.5, 0.75, 1].map((f) => fmtT(from + f * span)));

  const hoverIdx = $derived.by(() => {
    if (hover == null || ts.length === 0) return -1;
    const t = from + hover * span;
    let best = 0;
    for (let i = 1; i < ts.length; i++) if (Math.abs(ts[i] - t) < Math.abs(ts[best] - t)) best = i;
    return best;
  });
  const hoverEvent = $derived(
    hover == null ? null : events.find((e) => Math.abs(e.ts - (from + hover! * span)) <= span * 0.015) ?? null
  );

  function frac(e: MouseEvent): number | null {
    const svg = (e.target as Element).closest('.ch')?.querySelector('svg');
    if (!svg) return null;
    const b = svg.getBoundingClientRect();
    return Math.max(0, Math.min(1, (e.clientX - b.left) / b.width));
  }
  function onMove(e: MouseEvent) {
    const f = frac(e);
    if (f == null) return;
    hover = f;
    const bb = box.getBoundingClientRect();
    let x = e.clientX - bb.left + 16;
    if (x > bb.width - 220) x -= 240;
    tipX = x;
    tipY = e.clientY - bb.top + 14;
    if (drag) drag = { ...drag, b: f };
  }
  function onDown(e: MouseEvent) {
    const f = frac(e);
    if (f == null || !onzoom) return;
    e.preventDefault();
    drag = { a: f, b: f };
  }
  function onUp() {
    if (!drag) return;
    const { a, b } = drag;
    drag = null;
    if (Math.abs(b - a) < 0.02) return;
    const lo = Math.min(a, b);
    const hi = Math.max(a, b);
    onzoom?.(from + lo * span, from + hi * span);
  }
  $effect(() => {
    if (!drag) return;
    window.addEventListener('mouseup', onUp);
    return () => window.removeEventListener('mouseup', onUp);
  });
</script>

<div
  class="charts"
  bind:this={box}
  role="presentation"
  onmousemove={onMove}
  onmouseleave={() => (hover = null)}
  onmousedown={onDown}
  ondblclick={() => onreset?.()}
>
  {#each scaled as s (s.label)}
    <div class="ch" role="img" aria-label="{s.label}: {s.last != null ? s.fmt(s.last) : 'no data'}">
      <div class="hd">
        <span class="t">{s.label}</span>
        <b class="dm-num">{s.last != null ? s.fmt(s.last) : '—'}</b>
      </div>
      <svg viewBox="0 0 {W} {H}" preserveAspectRatio="none" aria-hidden="true">
        {#each [25, 50, 75] as y (y)}
          <line x1="0" x2={W} y1={H - y} y2={H - y} class="grid" vector-effect="non-scaling-stroke" />
        {/each}
        {#each [0.25, 0.5, 0.75] as x (x)}
          <line y1="0" y2={H} x1={x * W} x2={x * W} class="grid" vector-effect="non-scaling-stroke" />
        {/each}
        {#each events as ev, i (i)}
          {#if ev.ts >= from && ev.ts <= to}
            <line
              y1="0"
              y2={H}
              x1={((ev.ts - from) / span) * W}
              x2={((ev.ts - from) / span) * W}
              stroke={ev.color ?? 'var(--accent)'}
              stroke-dasharray="3 2"
              stroke-width="1"
              vector-effect="non-scaling-stroke"
            />
          {/if}
        {/each}
        {#each s.segs as pts, i (i)}
          {@const first = pts.split(' ')[0].split(',')[0]}
          {@const last = pts.split(' ').at(-1)?.split(',')[0]}
          <polygon points="{first},{H} {pts} {last},{H}" fill={s.color} opacity="0.08" />
          <polyline fill="none" stroke={s.color} stroke-width="1.6" vector-effect="non-scaling-stroke" points={pts} />
        {/each}
        {#if hover != null}
          <line class="xh" y1="0" y2={H} x1={hover * W} x2={hover * W} vector-effect="non-scaling-stroke" />
        {/if}
        {#if drag}
          <rect x={Math.min(drag.a, drag.b) * W} y="0" width={Math.abs(drag.b - drag.a) * W} height={H} fill="var(--accent)" opacity="0.15" />
        {/if}
      </svg>
      <div class="axis" aria-hidden="true">
        {#each ticks as t, i (i)}<span>{t}</span>{/each}
      </div>
    </div>
  {/each}

  {#if !hasData}
    <div class="nodata">{empty}</div>
  {/if}

  {#if hover != null && hasData && hoverIdx >= 0}
    <div class="tip" style="left: {tipX}px; top: {tipY}px">
      <div class="when dm-mono">{new Date(ts[hoverIdx]).toLocaleString()}</div>
      {#each scaled as s (s.label)}
        {@const v = s.values[hoverIdx]}
        <div class="row">
          <span><span style="color: {s.color}">●</span> {s.label}</span>
          <b class="dm-mono">{v != null ? s.fmt(v) : '—'}</b>
        </div>
      {/each}
      {#if hoverEvent}
        <div class="ev" style="color: {hoverEvent.color ?? 'var(--accent)'}">┊ {hoverEvent.label}</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .charts {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
    position: relative;
    user-select: none;
  }
  .ch {
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--surface);
    padding: 10px 12px;
    min-width: 0;
  }
  .hd { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
  .t { font-size: var(--fs-sm); font-weight: 600; color: var(--subtle); }
  svg { display: block; width: 100%; height: 110px; margin-top: 4px; cursor: crosshair; }
  .grid { stroke: var(--border); stroke-width: 0.6; }
  .xh { stroke: var(--fg); stroke-width: 1; opacity: 0.6; }
  .axis {
    display: flex;
    justify-content: space-between;
    font-size: 10.5px;
    color: var(--subtle);
    font-family: var(--font-mono);
    margin-top: 2px;
  }
  .nodata {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--subtle);
    font-size: var(--fs-sm);
    pointer-events: none;
  }
  .tip {
    position: absolute;
    pointer-events: none;
    background: var(--menu);
    border: 1px solid var(--border);
    border-radius: var(--r);
    padding: 7px 10px;
    font-size: 12px;
    box-shadow: var(--pop);
    z-index: 30;
    min-width: 200px;
  }
  .when { color: var(--subtle); margin-bottom: 4px; }
  .row { display: flex; justify-content: space-between; gap: 14px; }
  .ev { margin-top: 5px; }
  @media (max-width: 900px) {
    .charts { grid-template-columns: minmax(0, 1fr); }
  }
</style>
