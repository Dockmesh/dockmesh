<script lang="ts" module>
  export type Tone = 'ok' | 'warn' | 'bad' | 'off' | 'acc';

  /** Sentence case for status words that arrive as API enums ("RUNNING",
   * "partially_failed"); mixed-case labels are left alone. */
  export function statusWord(label: string): string {
    const s = label.replace(/[_-]+/g, ' ').trim();
    if (!s) return s;
    const flat = s === s.toUpperCase() || s === s.toLowerCase() ? s.toLowerCase() : s;
    return flat.charAt(0).toUpperCase() + flat.slice(1);
  }
</script>

<script lang="ts">
  let { tone = 'off', label, title, ring = false }: { tone?: Tone; label: string; title?: string; ring?: boolean } = $props();
</script>

<span class="chip {tone}" {title}><i aria-hidden="true" class="dm-dot {tone}" class:ring></i>{statusWord(label)}</span>

<style>
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-sans);
    font-size: 12px;
    font-weight: 500;
    line-height: 20px;
    padding: 0 8px 0 7px;
    border-radius: 10px;
    color: var(--muted);
    background: var(--field);
    white-space: nowrap;
    vertical-align: middle;
    flex: none;
  }
  i {
    width: 7px;
    height: 7px;
  }
  .chip.ok { color: var(--ok); background: var(--ok-bg); }
  .chip.warn { color: var(--warn); background: var(--warn-bg); }
  .chip.bad { color: var(--bad); background: var(--bad-bg); }
  .chip.acc { color: var(--accent); background: var(--accent-soft); }
</style>
