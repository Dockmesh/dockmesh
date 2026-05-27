<script lang="ts">
  // The /agents route was renamed to /hosts when we adopted "host" as
  // the user-facing term for the connected machines (the agent binary
  // is an implementation detail). Old bookmarks land here and get a
  // permanent redirect.
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  onMount(() => goto('/hosts', { replaceState: true }));
</script>
