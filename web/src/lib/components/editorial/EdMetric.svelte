<script lang="ts">
  import Sparkline from './Sparkline.svelte';

  interface Props {
    label: string;
    value: string | number;
    unit?: string;
    meta?: string;
    spark?: number[];
    sparkColor?: string;
  }

  let { label, value, unit, meta, spark, sparkColor }: Props = $props();
</script>

<div class="ed-metric">
  <span class="ed-metric-label">{label}</span>
  <span class="ed-metric-value">
    {value}{#if unit}<span class="unit">{unit}</span>{/if}
  </span>
  {#if spark}
    <Sparkline values={spark} color={sparkColor} />
  {/if}
  {#if meta}
    <span class="ed-metric-meta">{meta}</span>
  {/if}
</div>
