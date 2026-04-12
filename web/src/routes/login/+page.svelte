<script lang="ts">
  let username = $state('');
  let password = $state('');

  function submit(e: Event) {
    e.preventDefault();
    // TODO: POST /api/v1/auth/login
  }
</script>

<div class="max-w-sm mx-auto mt-20 p-6 rounded border border-[var(--border)] bg-[var(--panel)]">
  <h2 class="text-xl font-semibold mb-4">Sign in</h2>
  <form onsubmit={submit} class="space-y-3">
    <input
      class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]"
      placeholder="Username"
      bind:value={username}
    />
    <input
      class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]"
      type="password"
      placeholder="Password"
      bind:value={password}
    />
    <button class="w-full py-2 rounded bg-brand-500 text-white font-semibold">Sign in</button>
  </form>
</div>
