<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';

  type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
  type Size = 'xs' | 'sm' | 'md';

  interface Props extends HTMLButtonAttributes {
    variant?: Variant;
    size?: Size;
    loading?: boolean;
    children?: Snippet;
  }

  let { variant = 'secondary', size = 'md', loading = false, disabled, class: klass = '', children, ...rest }: Props = $props();

  const variantCls: Record<Variant, string> = {
    primary: 'dm-btn-primary',
    secondary: 'dm-btn-secondary',
    ghost: 'dm-btn-ghost',
    danger: 'dm-btn-danger'
  };
  const sizeCls: Record<Size, string> = {
    xs: 'dm-btn-xs',
    sm: 'dm-btn-sm',
    md: ''
  };
</script>

<button
  class="dm-btn {variantCls[variant]} {sizeCls[size]} {klass}"
  disabled={disabled || loading}
  {...rest}
>
  {#if loading}
    <svg class="animate-spin h-4 w-4" viewBox="0 0 24 24" fill="none">
      <circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" opacity="0.25" />
      <path d="M12 2a10 10 0 0 1 10 10" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
    </svg>
  {/if}
  {@render children?.()}
</button>
