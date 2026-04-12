<script lang="ts">
  import type { Component, Snippet } from 'svelte';

  interface Props {
    icon?: Component;
    title: string;
    description?: string;
    class?: string;
    action?: Snippet;
  }
  let { icon: Icon, title, description, class: klass = '', action }: Props = $props();
</script>

<div class="flex flex-col items-center justify-center text-center py-16 px-6 {klass}">
  {#if Icon}
    <div class="w-14 h-14 rounded-2xl bg-[var(--surface)] border border-[var(--border)] flex items-center justify-center mb-4">
      <Icon class="w-7 h-7 text-[var(--fg-muted)]" />
    </div>
  {/if}
  <h3 class="text-base font-semibold text-[var(--fg)]">{title}</h3>
  {#if description}
    <p class="text-sm text-[var(--fg-muted)] mt-1 max-w-sm">{description}</p>
  {/if}
  {#if action}
    <div class="mt-5">
      {@render action?.()}
    </div>
  {/if}
</div>
