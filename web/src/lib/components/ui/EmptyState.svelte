<script lang="ts">
  import type { Snippet } from 'svelte';

  // `icon` accepts any lucide-svelte icon; their exported type is a class
  // rather than Svelte's strict Component shape.
  interface Props {
    icon?: any;
    title: string;
    description?: string;
    class?: string;
    action?: Snippet;
    tone?: 'default' | 'error';
  }
  let { icon: Icon, title, description, class: klass = '', action, tone = 'default' }: Props = $props();
</script>

<div class="es {klass}" class:err={tone === 'error'} role={tone === 'error' ? 'alert' : undefined}>
  {#if Icon}
    <Icon size={22} strokeWidth={1.6} class="es-i" />
  {/if}
  <h3>{title}</h3>
  {#if description}
    <p>{description}</p>
  {/if}
  {#if action}
    <div class="es-a">{@render action()}</div>
  {/if}
</div>

<style>
  .es {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    padding: 40px 24px;
    color: var(--subtle);
  }
  .es :global(.es-i) { color: var(--subtle); margin-bottom: 10px; }
  h3 { margin: 0; font-size: var(--fs-base); font-weight: 600; color: var(--fg); }
  p { margin: 4px 0 0; font-size: var(--fs-sm); max-width: 46ch; }
  .err h3 { color: var(--bad); }
  .es-a { margin-top: 14px; display: flex; gap: 8px; }
</style>
