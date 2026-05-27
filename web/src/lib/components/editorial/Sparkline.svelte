<script lang="ts">
  interface Props {
    values: number[];
    color?: string;
    width?: number;
    height?: number;
  }

  let { values, color, width = 100, height = 32 }: Props = $props();

  let path = $derived.by(() => {
    if (values.length === 0) return { line: '', fill: '' };
    const max = Math.max(...values, 1);
    const min = Math.min(...values, 0);
    const span = max - min || 1;
    const denom = Math.max(values.length - 1, 1);
    const pts = values.map((v, i) => {
      const x = (i / denom) * width;
      const y = height - ((v - min) / span) * (height - 2) - 1;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    });
    return {
      line: `M${pts.join(' L')}`,
      fill: `M0,${height} L${pts.join(' L')} L${width},${height} Z`,
    };
  });
</script>

<svg
  class="dm-spark"
  viewBox={`0 0 ${width} ${height}`}
  preserveAspectRatio="none"
  aria-hidden="true"
>
  <path d={path.fill} class="dm-spark-fill" />
  <path d={path.line} class="dm-spark-line" style={color ? `stroke: ${color}` : undefined} />
</svg>
