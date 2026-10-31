<script lang="ts">
  // Renamed to /hosts/[id]. Old bookmarks redirect here.
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  onMount(() => goto(`/hosts/${page.params.id}`, { replace: true }));
</script>
