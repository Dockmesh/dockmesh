<script lang="ts">
  // Old bookmarks: migrations live in the top-bar dialog and on each host.
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  onMount(() => goto('/hosts', { replace: true }));
</script>
