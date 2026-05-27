<script lang="ts">
  // Old /networks list page — replaced by the unified /resources view.
  // Topology used to be reachable here via ?topology=1; that's now
  // /topology directly. Detail pages at /networks/[id] are the new
  // editorial detail.
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  onMount(() => {
    const dest = $page.url.searchParams.get('topology') === '1' ? '/topology' : '/resources?tab=networks';
    goto(dest, { replaceState: true });
  });
</script>
