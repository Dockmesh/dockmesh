<script lang="ts">
  // Step header for multi-decision dialogs: one question per step.
  let { steps, current }: { steps: string[]; current: number } = $props();
</script>

<ol class="st" aria-label="Steps">
  {#each steps as s, i (s)}
    <li class:done={i < current} class:on={i === current} aria-current={i === current ? 'step' : undefined}>
      <span class="n">{i + 1}</span><span>{s}</span>
    </li>
  {/each}
</ol>

<style>
  .st { display: flex; gap: 6px; list-style: none; margin: 0; padding: 0; flex-wrap: wrap; }
  li { display: flex; align-items: center; gap: 6px; color: var(--subtle); font-size: var(--fs-sm); }
  li + li::before { content: ''; width: 18px; height: 1px; background: var(--border); margin-right: 2px; }
  .n {
    width: 20px;
    height: 20px;
    border-radius: 10px;
    border: 1px solid var(--border);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
  }
  .on { color: var(--fg); font-weight: 600; }
  .on .n { border-color: var(--accent); color: var(--accent); }
  .done .n { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
</style>
