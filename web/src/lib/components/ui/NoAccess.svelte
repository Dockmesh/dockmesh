<script lang="ts">
  import { Lock } from 'lucide-svelte';
  import EmptyState from './EmptyState.svelte';

  interface Props {
    title?: string;
    description?: string;
  }
  let {
    title = "You don't have access to this page",
    description = 'None of your roles allows it. Ask an admin if you need it.'
  }: Props = $props();
</script>

<EmptyState icon={Lock} {title} {description}>
  {#snippet action()}
    <a class="dm-btn dm-btn-secondary dm-btn-sm" href="/">Back to overview</a>
  {/snippet}
</EmptyState>
