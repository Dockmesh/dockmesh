<script lang="ts">
  // Renamed to /hosts/[id]. Old bookmarks redirect here.
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { onMount } from 'svelte';
  onMount(() => goto(`/hosts/${$page.params.id}`, { replaceState: true }));
</script>
