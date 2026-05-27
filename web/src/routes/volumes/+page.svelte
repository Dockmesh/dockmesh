<script lang="ts">
  // Old /volumes list page — replaced by the unified /resources view.
  // Detail pages at /volumes/[name] still work for now (their own
  // editorial-rebuild slice is queued; see project_frontend_modernization_open.md).
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  onMount(() => goto('/resources?tab=volumes', { replaceState: true }));
</script>
