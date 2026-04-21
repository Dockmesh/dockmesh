<script lang="ts">
  // Top-level "Registries" entry. Shim to the existing Settings
  // registries tab until full extraction.
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  onMount(() => goto('/settings?tab=registries', { replaceState: true }));
</script>

<p class="text-sm text-[var(--fg-muted)]">Loading…</p>
