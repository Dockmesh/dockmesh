<script lang="ts">
  import { onMount } from 'svelte';

  let {
    value,
    duration = 500,
    format = (n: number) => n.toString(),
  }: {
    value: number;
    duration?: number;
    format?: (n: number) => string;
  } = $props();

  // displayed is what we render right now. It animates toward `value`
  // over `duration` ms whenever `value` changes. The tween is kept in
  // requestAnimationFrame so there's no dependency on svelte/motion —
  // cheap enough for the 5–10 tiles a dashboard uses and cleanly
  // cancels itself on fast successive updates.
  let displayed = $state(0);
  let from = 0;
  let to = 0;
  let start = 0;
  let raf = 0;
  let initialised = false;

  // easeOutCubic — slightly bouncy settle, matches the feel Grafana
  // uses for its stat panels (values overshoot barely before locking).
  function ease(t: number): number {
    const x = 1 - t;
    return 1 - x * x * x;
  }

  function step(ts: number) {
    if (!start) start = ts;
    const t = Math.min(1, (ts - start) / duration);
    displayed = from + (to - from) * ease(t);
    if (t < 1) {
      raf = requestAnimationFrame(step);
    } else {
      displayed = to;
      raf = 0;
    }
  }

  $effect(() => {
    if (!initialised) {
      displayed = value;
      to = value;
      initialised = true;
      return;
    }
    if (value === to) return;
    from = displayed;
    to = value;
    start = 0;
    if (raf) cancelAnimationFrame(raf);
    raf = requestAnimationFrame(step);
  });

  onMount(() => () => {
    if (raf) cancelAnimationFrame(raf);
  });
</script>

<span>{format(displayed)}</span>
