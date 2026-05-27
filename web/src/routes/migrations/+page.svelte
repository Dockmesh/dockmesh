<script lang="ts">
  // The /migrations route was retired in favour of a global drawer
  // (opened from the topbar MigrationActivePill) plus the per-host
  // Migrations tab on /hosts/[id]. Old bookmarks land here and get
  // redirected to /hosts.
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  onMount(() => goto('/hosts', { replaceState: true }));
</script>
