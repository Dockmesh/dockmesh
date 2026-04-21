<script lang="ts">
  // Top-level "Users & Roles" entry. Content still lives in the
  // Settings page's users + roles tabs until full extraction (phase 2).
  // This shim auto-navigates so the sidebar item feels first-class while
  // we avoid a 400-line copy-paste that would later have to be reconciled.
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  onMount(() => goto('/settings?tab=users', { replaceState: true }));
</script>

<p class="text-sm text-[var(--fg-muted)]">Loading…</p>
