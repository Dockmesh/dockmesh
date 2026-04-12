<script lang="ts">
  import { page } from '$app/stores';
  const name = $derived($page.params.name);
  let compose = $state('# compose.yaml\n');
</script>

<section class="space-y-4">
  <h2 class="text-xl font-semibold">Stack: {name}</h2>
  <div>
    <label class="block text-sm mb-1 text-[var(--muted)]">compose.yaml</label>
    <textarea
      class="w-full h-80 px-3 py-2 font-mono text-sm rounded border border-[var(--border)] bg-[var(--bg)]"
      bind:value={compose}
    ></textarea>
  </div>
  <div class="flex gap-2">
    <button class="px-4 py-2 rounded bg-brand-500 text-white font-semibold">Deploy</button>
    <button class="px-4 py-2 rounded border border-[var(--border)]">Stop</button>
  </div>
</section>
