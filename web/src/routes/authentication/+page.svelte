<script lang="ts">
  // Top-level "Authentication" entry — covers SSO / OIDC today, will
  // expand to LDAP + password policy + session TTL in future slices.
  // Shim to the existing Settings sso tab until full extraction.
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  onMount(() => goto('/settings?tab=sso', { replaceState: true }));
</script>

<p class="text-sm text-[var(--fg-muted)]">Loading…</p>
