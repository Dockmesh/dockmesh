<script lang="ts">
  type Status =
    | 'running'
    | 'degraded'
    | 'failing'
    | 'stopped'
    | 'ok'
    | 'warn'
    | 'pending'
    | 'invited'
    | 'neutral';

  interface Props {
    status: Status;
    label?: string;
  }

  let { status, label }: Props = $props();

  const map: Record<Status, { cls: string; label: string }> = {
    running:  { cls: 'dm-pill-success', label: 'running' },
    ok:       { cls: 'dm-pill-success', label: 'ok' },
    degraded: { cls: 'dm-pill-warning', label: 'degraded' },
    warn:     { cls: 'dm-pill-warning', label: 'warn' },
    failing:  { cls: 'dm-pill-danger',  label: 'failing' },
    stopped:  { cls: 'dm-pill-neutral', label: 'stopped' },
    pending:  { cls: 'dm-pill-neutral', label: 'pending' },
    invited:  { cls: 'dm-pill-neutral', label: 'invited' },
    neutral:  { cls: 'dm-pill-neutral', label: 'neutral' },
  };

  let entry = $derived(map[status] ?? map.neutral);
</script>

<span class="dm-pill {entry.cls}">
  <span class="dm-pill-dot"></span>
  {label ?? entry.label}
</span>
