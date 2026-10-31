<script lang="ts" module>
  export type Tone = 'ok' | 'warn' | 'bad' | 'off' | 'acc';
</script>

<script lang="ts">
  let { tone = 'off', label, title }: { tone?: Tone; label: string; title?: string } = $props();
</script>

<span class="chip {tone}" {title}>{label}</span>

<style>
  .chip {
    display: inline-block;
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.05em;
    line-height: 1.5;
    padding: 1px 7px;
    border-radius: 2px;
    border: 1px solid var(--border);
    color: var(--off);
    white-space: nowrap;
    text-transform: uppercase;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    vertical-align: middle;
  }
  .ok { color: var(--ok); background: var(--ok-bg); border-color: var(--ok-bd); }
  .warn { color: var(--warn); background: var(--warn-bg); border-color: var(--warn-bd); }
  .bad { color: var(--bad); background: var(--bad-bg); border-color: var(--bad-bd); }
  .acc { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); }
</style>
