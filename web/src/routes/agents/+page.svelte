<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, ApiError, type Agent, type AgentCreateResult } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Server, Plus, Trash2, RefreshCw, Copy, CheckCircle2 } from 'lucide-svelte';

  let agents = $state<Agent[]>([]);
  let loading = $state(true);

  let showCreate = $state(false);
  let newName = $state('');
  let creating = $state(false);

  // After create, we keep the result around to show the token + install
  // command in a follow-up dialog. The token is shown ONCE.
  let createResult = $state<AgentCreateResult | null>(null);

  async function load() {
    loading = true;
    try {
      agents = await api.agents.list();
    } catch (err) {
      toast.error('Failed to load agents', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function create(e: Event) {
    e.preventDefault();
    creating = true;
    try {
      const res = await api.agents.create(newName);
      createResult = res;
      showCreate = false;
      newName = '';
      await load();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  async function del(a: Agent) {
    if (!confirm(`Delete agent "${a.name}"? This revokes its certificate.`)) return;
    try {
      await api.agents.delete(a.id);
      toast.success('Deleted', a.name);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function copyText(s: string) {
    if (typeof navigator !== 'undefined' && navigator.clipboard) {
      navigator.clipboard.writeText(s);
      toast.info('Copied');
    }
  }

  function statusVariant(s: Agent['status']): 'success' | 'warning' | 'danger' | 'default' {
    if (s === 'online') return 'success';
    if (s === 'pending') return 'warning';
    if (s === 'revoked') return 'danger';
    return 'default';
  }

  function fmtTime(ts?: string): string {
    if (!ts) return '—';
    const d = (Date.now() - new Date(ts).getTime()) / 1000;
    if (d < 60) return 'just now';
    if (d < 3600) return `${Math.floor(d / 60)}m ago`;
    if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
    return new Date(ts).toLocaleString();
  }

  $effect(() => {
    if (!allowed('user.manage')) {
      goto('/');
      return;
    }
    load();
    // Poll every 5s so status flips from pending → online without a manual refresh.
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') load();
    }, 5000);
    return () => clearInterval(timer);
  });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Remote Agents</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Outbound-only agents on remote docker hosts. mTLS-authenticated, no inbound ports needed on the agent side.
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
      <Button variant="primary" onclick={() => (showCreate = true)}>
        <Plus class="w-4 h-4" /> New agent
      </Button>
    </div>
  </div>

  {#if loading && agents.length === 0}
    <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
  {:else if agents.length === 0}
    <Card>
      <EmptyState
        icon={Server}
        title="No agents yet"
        description="Add a remote host to manage containers from a single Dockmesh instance. Each agent is a tiny binary that connects outbound via mTLS — your remote hosts never need to expose any inbound ports."
      >
        {#snippet action()}
          <Button variant="primary" onclick={() => (showCreate = true)}>
            <Plus class="w-4 h-4" /> Add your first agent
          </Button>
        {/snippet}
      </EmptyState>
    </Card>
  {:else}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each agents as a (a.id)}
          <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)]">
            <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
              <Server class="w-5 h-5" />
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <span class="font-medium text-sm">{a.name}</span>
                <Badge variant={statusVariant(a.status)} dot>{a.status}</Badge>
              </div>
              <div class="text-xs text-[var(--fg-muted)] font-mono truncate mt-0.5">
                {#if a.hostname}{a.hostname}{:else}—{/if}
                {#if a.os}· {a.os}/{a.arch}{/if}
                {#if a.docker_version}· docker {a.docker_version}{/if}
                {#if a.version}· agent {a.version}{/if}
                · last seen {fmtTime(a.last_seen_at)}
              </div>
            </div>
            <Button size="xs" variant="ghost" onclick={() => del(a)} aria-label="Delete">
              <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
            </Button>
          </div>
        {/each}
      </div>
    </Card>
  {/if}
</section>

<!-- Create modal -->
<Modal bind:open={showCreate} title="New agent" maxWidth="max-w-md">
  <form onsubmit={create} id="create-agent-form" class="space-y-3">
    <Input
      label="Name"
      placeholder="e.g. prod-host-01"
      bind:value={newName}
      hint="A friendly name. Must be unique."
    />
    <p class="text-xs text-[var(--fg-muted)]">
      A one-time enrollment token will be generated. You'll see the install command on the next screen.
    </p>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="create-agent-form" loading={creating} disabled={!newName.trim()}>
      Create
    </Button>
  {/snippet}
</Modal>

<!-- Install command modal — shown ONCE after create -->
<Modal bind:open={() => createResult !== null, (v) => { if (!v) createResult = null; }}
       title="Agent created — copy the install command"
       maxWidth="max-w-2xl"
       onclose={() => (createResult = null)}>
  {#if createResult}
    <div class="space-y-4">
      <div class="flex items-start gap-2 text-xs bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] rounded-lg px-3 py-2 text-[var(--color-warning-400)]">
        <CheckCircle2 class="w-4 h-4 shrink-0 mt-0.5" />
        <div>
          The token below is shown <strong>only once</strong>. Save it now — you can't recover it.
          If lost, delete the agent and create a new one.
        </div>
      </div>

      <div>
        <div class="text-xs text-[var(--fg-muted)] mb-1">Install command (run on the remote docker host)</div>
        <div class="relative">
          <pre class="dm-card p-3 font-mono text-xs whitespace-pre-wrap break-all max-h-64 overflow-auto">{createResult.install_hint}</pre>
          <button
            class="absolute top-2 right-2 px-2 py-1 text-xs rounded bg-[var(--surface)] border border-[var(--border)] hover:bg-[var(--surface-hover)]"
            onclick={() => copyText(createResult!.install_hint)}
          >
            <Copy class="w-3 h-3 inline" /> copy
          </button>
        </div>
      </div>

      <div class="grid grid-cols-1 gap-3">
        <div>
          <div class="text-xs text-[var(--fg-muted)] mb-1">Token</div>
          <div class="flex gap-2">
            <code class="dm-input font-mono text-xs select-all">{createResult.token}</code>
            <Button size="sm" variant="secondary" onclick={() => copyText(createResult!.token)}>
              <Copy class="w-3.5 h-3.5" />
            </Button>
          </div>
        </div>
        <div>
          <div class="text-xs text-[var(--fg-muted)] mb-1">Enroll URL</div>
          <code class="dm-input font-mono text-xs select-all">{createResult.enroll_url}</code>
        </div>
        <div>
          <div class="text-xs text-[var(--fg-muted)] mb-1">Agent URL (mTLS)</div>
          <code class="dm-input font-mono text-xs select-all">{createResult.agent_url}</code>
        </div>
      </div>

      <p class="text-xs text-[var(--fg-muted)]">
        After the agent enrolls, its status will flip from <strong>pending</strong> to <strong>online</strong> within
        a few seconds. The list refreshes every 5 s.
      </p>
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="primary" onclick={() => (createResult = null)}>I've saved the token</Button>
  {/snippet}
</Modal>
