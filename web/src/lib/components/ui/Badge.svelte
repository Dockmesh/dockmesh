<script lang="ts">
  import type { Snippet } from 'svelte';
  type Variant = 'default' | 'success' | 'warning' | 'danger' | 'info';
  interface Props {
    variant?: Variant;
    dot?: boolean;
    class?: string;
    children?: Snippet;
  }
  let { variant = 'default', dot = false, class: klass = '', children }: Props = $props();

  const variantCls: Record<Variant, string> = {
    default: '',
    success: 'dm-badge-success',
    warning: 'dm-badge-warning',
    danger: 'dm-badge-danger',
    info: 'dm-badge-info'
  };
  const dotCls: Record<Variant, string> = {
    default: 'bg-[var(--fg-muted)]',
    success: 'bg-success-500',
    warning: 'bg-warning-500',
    danger: 'bg-danger-500',
    info: 'bg-brand-500'
  };
</script>

<span class="dm-badge {variantCls[variant]} {klass}">
  {#if dot}
    <span class="w-1.5 h-1.5 rounded-full {dotCls[variant]}"></span>
  {/if}
  {@render children?.()}
</span>
