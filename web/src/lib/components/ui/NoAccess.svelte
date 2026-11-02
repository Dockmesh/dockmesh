<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { Lock } from 'lucide-svelte';
  import EmptyState from './EmptyState.svelte';

  interface Props {
    title?: string;
    description?: string;
  }
  let {
    title = m.ui_noaccess_title(),
    description = m.ui_noaccess_desc()
  }: Props = $props();
</script>

<EmptyState icon={Lock} {title} {description}>
  {#snippet action()}
    <a class="dm-btn dm-btn-secondary dm-btn-sm" href="/">{m.ui_noaccess_back()}</a>
  {/snippet}
</EmptyState>
