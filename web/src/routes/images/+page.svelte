<script lang="ts">
  // Old /images list page — replaced by the unified /resources view.
  // Kept as a thin client-side redirect so existing bookmarks and
  // any stale back-links from detail pages still land somewhere
  // sensible. Detail pages (none currently) would still resolve.
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  onMount(() => goto('/resources?tab=images', { replaceState: true }));
</script>
