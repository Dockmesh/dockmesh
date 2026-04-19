<script lang="ts">
  import { toast } from '$lib/stores/toast.svelte';
  import { CheckCircle2, XCircle, Info, AlertTriangle, X } from 'lucide-svelte';

  const iconMap = {
    success: CheckCircle2,
    error: XCircle,
    info: Info,
    warning: AlertTriangle
  };

  // Errors get a fully-tinted background + full-width left border so
  // they stand out from info/warning/success. They also stay on screen
  // until dismissed (toast.svelte.ts sets duration=0 for errors) — a
  // subtle border wasn't enough when the UI has background activity.
  const variantCls = {
    success: 'border-[color-mix(in_srgb,var(--color-success-500)_40%,transparent)] text-[var(--color-success-400)]',
    error:   'border border-[var(--color-danger-500)] bg-[color-mix(in_srgb,var(--color-danger-500)_10%,var(--surface))] text-[var(--color-danger-400)]',
    info:    'border-[color-mix(in_srgb,var(--color-brand-500)_40%,transparent)] text-[var(--color-brand-400)]',
    warning: 'border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] text-[var(--color-warning-400)]'
  };
</script>

<div class="fixed bottom-4 right-4 z-[100] flex flex-col gap-2 pointer-events-none max-w-sm w-full">
  {#each toast.items as t (t.id)}
    {@const Icon = iconMap[t.variant]}
    <div
      class="dm-card p-3 pr-9 flex gap-3 items-start pointer-events-auto shadow-xl dm-fade-in {t.variant === 'error' ? variantCls[t.variant] : 'border-l-2 ' + variantCls[t.variant]}"
      role={t.variant === 'error' ? 'alert' : 'status'}
      aria-live={t.variant === 'error' ? 'assertive' : 'polite'}
    >
      <Icon class="w-5 h-5 shrink-0 mt-0.5" />
      <div class="flex-1 min-w-0">
        <div class="text-sm font-medium text-[var(--fg)]">{t.title}</div>
        {#if t.description}
          <div class="text-xs text-[var(--fg-muted)] mt-0.5 break-words">{t.description}</div>
        {/if}
      </div>
      <button
        class="absolute top-2 right-2 p-1 rounded-md hover:bg-[var(--surface-hover)] text-[var(--fg-muted)]"
        onclick={() => toast.dismiss(t.id)}
        aria-label="Dismiss"
      >
        <X class="w-3.5 h-3.5" />
      </button>
    </div>
  {/each}
</div>
