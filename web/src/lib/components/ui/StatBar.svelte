<script lang="ts" module>
  export interface Stat {
    label: string;
    value: string | number;
    note?: string;
    tone?: 'warn' | 'bad' | '';
    href?: string;
  }
</script>

<script lang="ts">
  // One quiet row of key figures separated by hairlines; no tiles.
  let { items }: { items: Stat[] } = $props();
</script>

<div class="sb">
  {#each items as s (s.label)}
    <div class="it">
      <small>{s.label}</small>
      {#if s.href}
        <a class="v" href={s.href}>{s.value}</a>
      {:else}
        <b class="v">{s.value}</b>
      {/if}
      <span class="n {s.tone ?? ''}" title={s.note ?? ''}>{s.note || ' '}</span>
    </div>
  {/each}
</div>

<style>
  .sb {
    display: flex;
    flex-wrap: wrap;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
  }
  .it {
    flex: 1 1 120px;
    min-width: 0;
    padding: 10px 16px 10px 0;
    margin-right: 16px;
    border-right: 1px solid var(--border);
  }
  .it:last-child { border-right: 0; margin-right: 0; }
  small { display: block; font-size: var(--fs-xs); color: var(--subtle); }
  .v {
    display: block;
    font-size: var(--fs-lg);
    font-weight: 600;
    color: var(--fg);
    text-decoration: none;
    font-variant-numeric: tabular-nums;
  }
  a.v:hover { color: var(--accent); }
  .n {
    display: block;
    font-size: 12px;
    color: var(--subtle);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .n.warn { color: var(--warn); }
  .n.bad { color: var(--bad); }
</style>
