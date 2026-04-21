<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, ApiError, type Agent, type AgentCreateResult, type DrainPlan, type AgentUpgradePolicy } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Server, Plus, Trash2, RefreshCw, Copy, CheckCircle2, ArrowDownToLine, Tag, X, ArrowUpCircle, AlertTriangle } from 'lucide-svelte';

  let agents = $state<Agent[]>([]);
  let loading = $state(true);

  let showCreate = $state(false);
  let newName = $state('');
  let creating = $state(false);

  // After create, we keep the result around to show the token + install
  // command in a follow-up dialog. The token is shown ONCE. `showInstallModal`
  // mirrors the presence of createResult for the modal's bind:open —
  // avoids a custom getter/setter bind that the Svelte 5 parser chokes on.
  let createResult = $state<AgentCreateResult | null>(null);
  let showInstallModal = $state(false);

  // Drain state (P.10)
  let showDrain = $state(false);
  let drainHostId = $state('');
  let drainPlan = $state<DrainPlan | null>(null);
  let drainLoading = $state(false);
  let drainBusy = $state(false);

  // Host tags state (P.11.2) — tags per host are loaded from a separate
  // endpoint so the agents list stays cheap. The edit modal owns a
  // scratch copy that flushes back via setTags on save.
  let hostTags = $state<Record<string, string[]>>({});
  let showTagsFor = $state<string | null>(null);
  let tagsDraft = $state<string[]>([]);
  let tagInput = $state('');
  let allTagSuggestions = $state<string[]>([]);
  let tagsBusy = $state(false);

  async function loadTagsFor(hostId: string) {
    try {
      const tags = await api.hosts.listTags(hostId);
      hostTags[hostId] = tags;
    } catch (err) {
      // Non-fatal — host may never have been tagged. Treat as empty.
      hostTags[hostId] = [];
    }
  }

  async function openTags(hostId: string) {
    showTagsFor = hostId;
    tagsDraft = [...(hostTags[hostId] ?? [])];
    tagInput = '';
    try {
      allTagSuggestions = await api.hosts.allTags();
    } catch {
      allTagSuggestions = [];
    }
  }

  function addTagDraft(tag: string) {
    const t = tag.trim().toLowerCase();
    if (!t) return;
    // Same validation as server — surface early so the modal doesn't
    // eat the input and then 400 on save.
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(t)) {
      toast.error('Invalid tag', 'Use lowercase letters, digits, hyphens. 1-32 chars.');
      return;
    }
    if (tagsDraft.includes(t)) {
      tagInput = '';
      return;
    }
    if (tagsDraft.length >= 20) {
      toast.error('Too many tags', 'Max 20 per host.');
      return;
    }
    tagsDraft = [...tagsDraft, t];
    tagInput = '';
  }

  function removeTagDraft(tag: string) {
    tagsDraft = tagsDraft.filter((t) => t !== tag);
  }

  async function saveTags() {
    if (!showTagsFor) return;
    tagsBusy = true;
    try {
      const saved = await api.hosts.setTags(showTagsFor, tagsDraft);
      hostTags[showTagsFor] = saved;
      toast.success('Tags updated');
      showTagsFor = null;
    } catch (err) {
      toast.error('Failed to save tags', err instanceof ApiError ? err.message : undefined);
    } finally {
      tagsBusy = false;
    }
  }

  async function openDrain(hostId: string) {
    drainHostId = hostId;
    drainPlan = null;
    drainLoading = true;
    showDrain = true;
    try {
      drainPlan = await api.drains.plan(hostId);
    } catch (err) {
      toast.error('Plan failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      drainLoading = false;
    }
  }

  async function executeDrain() {
    drainBusy = true;
    try {
      const d = await api.drains.execute(drainHostId);
      toast.success('Drain started', `${d.plan.length} stack(s) queued`);
      showDrain = false;
    } catch (err) {
      toast.error('Drain failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      drainBusy = false;
    }
  }

  async function load() {
    loading = true;
    try {
      agents = await api.agents.list();
      // Fetch tags in parallel per host. Tags can be seen by anyone
      // (non-privileged read) so this works even for non-admins.
      await Promise.all(agents.map((a) => loadTagsFor(a.id)));
    } catch (err) {
      toast.error('Failed to load agents', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  // --- P.11.16 Upgrade policy ---
  let upgradePolicy = $state<AgentUpgradePolicy | null>(null);
  let upgradeBusy = $state(false);
  let upgradingAgent = $state<string | null>(null);

  async function loadUpgradePolicy() {
    if (!allowed('user.manage')) return;
    try { upgradePolicy = await api.agents.getUpgradePolicy(); } catch { /* ignore */ }
  }

  async function setUpgradeMode(mode: 'auto' | 'manual' | 'staged') {
    upgradeBusy = true;
    try {
      upgradePolicy = await api.agents.setUpgradePolicy({
        mode,
        stage_percent: upgradePolicy?.stage_percent || 10,
        stage_gap_sec: upgradePolicy?.stage_gap_sec || 300
      });
      toast.success(`Upgrade mode: ${mode}`);
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      upgradeBusy = false;
    }
  }

  async function runUpgradeNow() {
    upgradeBusy = true;
    try {
      upgradePolicy = await api.agents.runUpgradePolicy();
      toast.success('Evaluation triggered');
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      upgradeBusy = false;
    }
  }

  // Agents that pre-date the self-upgrade frame return 422 when the
  // server tries to ship the new binary. We surface that as a modal
  // with a one-shot manual-upgrade path instead of a toast the user
  // has to scroll back to read.
  let legacyUpgradeAgent = $state<{ id: string; name: string; message: string } | null>(null);

  async function upgradeAgent(id: string, name: string) {
    upgradingAgent = id;
    try {
      const res = await api.agents.upgrade(id);
      toast.success('Upgrade dispatched', `${name} → ${res.version}`);
      // Refresh both agent list + policy so the counts update.
      await Promise.all([load(), loadUpgradePolicy()]);
    } catch (err) {
      if (err instanceof ApiError && err.status === 422 && /too old to self-upgrade/i.test(err.message)) {
        legacyUpgradeAgent = { id, name, message: err.message };
      } else {
        toast.error('Upgrade failed', err instanceof ApiError ? err.message : undefined);
      }
    } finally {
      upgradingAgent = null;
    }
  }

  async function create(e: Event) {
    e.preventDefault();
    creating = true;
    try {
      const res = await api.agents.create(newName);
      createResult = res;
      showInstallModal = true;
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
    if (!(await confirm.ask({ title: `Delete agent ${a.name}`, message: `Delete agent "${a.name}"?`, body: 'The agent\u2019s certificate will be revoked. The remote host stays as-is — you can run the install script again to re-enroll.', confirmLabel: 'Delete', danger: true }))) return;
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
    loadUpgradePolicy();
    // Poll every 5s so status flips from pending → online without a manual refresh.
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') { load(); loadUpgradePolicy(); }
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

  <!-- P.11.16 Upgrade-policy panel -->
  {#if upgradePolicy}
    <Card class="p-4">
      <div class="flex items-start justify-between gap-4 flex-wrap">
        <div>
          <div class="flex items-center gap-2">
            <ArrowUpCircle class="w-4 h-4 text-[var(--fg-muted)]" />
            <h3 class="text-sm font-semibold">Agent upgrade policy</h3>
          </div>
          <p class="text-xs text-[var(--fg-muted)] mt-1">
            Server: <span class="font-mono">{upgradePolicy.server_version}</span>
            · {upgradePolicy.connected_up_to_date} / {upgradePolicy.connected_total} agents up-to-date
            {#if upgradePolicy.connected_pending > 0}
              · <span class="text-[var(--color-warning-400)]">{upgradePolicy.connected_pending} drifted</span>
            {/if}
          </p>
        </div>
        <div class="flex items-center gap-2 flex-wrap">
          <div class="inline-flex rounded-md border border-[var(--border)] p-0.5 text-xs">
            {#each ['manual', 'staged', 'auto'] as mode}
              <button
                class="px-2 py-1 rounded-sm {upgradePolicy.mode === mode ? 'bg-[var(--bg-muted)] text-[var(--fg)]' : 'text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
                disabled={upgradeBusy}
                onclick={() => setUpgradeMode(mode as 'auto' | 'manual' | 'staged')}
              >{mode}</button>
            {/each}
          </div>
          <Button variant="secondary" onclick={runUpgradeNow} disabled={upgradeBusy || upgradePolicy.mode === 'manual' || upgradePolicy.connected_pending === 0}>
            Run now
          </Button>
        </div>
      </div>
      {#if upgradePolicy.mode === 'staged'}
        <p class="text-xs text-[var(--fg-muted)] mt-2">
          Staged: {upgradePolicy.stage_percent ?? 10}% of drifted agents per tick (60s cadence). Safer for large fleets when a new binary might be incompatible.
        </p>
      {:else if upgradePolicy.mode === 'auto'}
        <p class="text-xs text-[var(--fg-muted)] mt-2">
          Auto: every drifted agent gets upgraded on the next tick. Good for homelabs, risky for large fleets.
        </p>
      {:else}
        <p class="text-xs text-[var(--fg-muted)] mt-2">
          Manual: nothing happens automatically — use the per-agent Upgrade button below.
        </p>
      {/if}
    </Card>
  {/if}

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
            <a href="/agents/{a.id}" class="flex items-center gap-3 flex-1 min-w-0 -my-3 py-3 rounded">
              <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
                <Server class="w-5 h-5" />
              </div>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="font-medium text-sm">{a.name}</span>
                  <Badge variant={statusVariant(a.status)} dot>{a.status}</Badge>
                  {#each (hostTags[a.id] ?? []) as t}
                    <span class="inline-flex items-center h-5 px-1.5 rounded text-[10px] font-mono bg-[var(--surface-hover)] text-[var(--fg-muted)] border border-[var(--border)]">
                      {t}
                    </span>
                  {/each}
                </div>
                <div class="text-xs text-[var(--fg-muted)] font-mono truncate mt-0.5">
                  {#if a.hostname}{a.hostname}{:else}—{/if}
                  {#if a.os}· {a.os}/{a.arch}{/if}
                  {#if a.docker_version}· docker {a.docker_version}{/if}
                  {#if a.version}· agent {a.version}{/if}
                  · last seen {fmtTime(a.last_seen_at)}
                </div>
              </div>
            </a>
            <Button size="xs" variant="ghost" onclick={() => openTags(a.id)} aria-label="Manage tags">
              <Tag class="w-3.5 h-3.5 text-[var(--fg-muted)]" />
            </Button>
            {#if a.status === 'online'}
              {#if upgradePolicy && a.version && a.version !== upgradePolicy.server_version}
                <Button
                  size="xs"
                  variant="ghost"
                  onclick={() => upgradeAgent(a.id, a.name)}
                  disabled={upgradingAgent === a.id}
                  aria-label="Upgrade agent"
                  title="Upgrade to {upgradePolicy.server_version}"
                >
                  <ArrowUpCircle class="w-3.5 h-3.5 text-[var(--color-brand-400)] {upgradingAgent === a.id ? 'animate-pulse' : ''}" />
                </Button>
              {/if}
              <Button size="xs" variant="ghost" onclick={() => openDrain(a.id)} aria-label="Drain host">
                <ArrowDownToLine class="w-3.5 h-3.5 text-[var(--color-warning-400)]" />
              </Button>
            {/if}
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
<!-- NB: earlier we used a custom getter/setter bind:open pattern here to
     drive the modal directly off `createResult !== null`, but the Svelte 5
     parser trips on the {() => get, (v) => set} syntax in attribute
     position and cascades into dozens of downstream errors. Using a
     separate $state boolean that mirrors createResult keeps the intent
     (modal auto-opens when a create succeeds, clears createResult on
     close) and parses cleanly. -->
<Modal bind:open={showInstallModal}
       title="Agent created — copy the install command"
       maxWidth="max-w-2xl"
       onclose={() => { createResult = null; showInstallModal = false; }}>
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

<!-- Drain modal -->
<Modal bind:open={showDrain} title="Drain Host" maxWidth="max-w-xl">
  <div class="space-y-4">
    {#if drainLoading}
      <div class="flex items-center gap-2 text-sm text-[var(--fg-muted)]">
        <RefreshCw class="w-4 h-4 animate-spin" /> Generating drain plan…
      </div>
    {:else if drainPlan}
      <p class="text-sm text-[var(--fg-muted)]">
        Move all {drainPlan.entries.length} stack(s) from <strong>{drainPlan.source_name}</strong> to other hosts.
        Stacks are migrated sequentially — the drain pauses on failure so you can decide how to proceed.
      </p>

      {#if drainPlan.entries.length === 0}
        <div class="text-sm text-[var(--fg-muted)]">No stacks deployed on this host.</div>
      {:else}
        <div class="border border-[var(--border)] rounded-lg overflow-hidden">
          <table class="w-full text-xs">
            <thead>
              <tr class="bg-[var(--surface)] text-[var(--fg-muted)] uppercase tracking-wider">
                <th class="text-left px-3 py-2">Stack</th>
                <th class="text-left px-3 py-2">Target</th>
                <th class="text-left px-3 py-2">Status</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each drainPlan.entries as entry}
                <tr>
                  <td class="px-3 py-2 font-mono">{entry.stack_name}</td>
                  <td class="px-3 py-2 font-mono">{entry.target_name || entry.target_host_id}</td>
                  <td class="px-3 py-2">
                    {#if entry.feasible}
                      <Badge variant="success" dot>ready</Badge>
                    {:else}
                      <Badge variant="danger" dot>infeasible</Badge>
                      {#if entry.detail}
                        <div class="text-[10px] text-[var(--color-danger-400)] mt-0.5">{entry.detail}</div>
                      {/if}
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        {#if !drainPlan.feasible}
          <div class="text-xs text-[var(--color-danger-400)]">
            Some stacks cannot be placed. Fix capacity issues before draining.
          </div>
        {/if}
      {/if}
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showDrain = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={drainBusy}
      disabled={drainBusy || drainLoading || !drainPlan || drainPlan.entries.length === 0}
      onclick={executeDrain}
    >
      <ArrowDownToLine class="w-4 h-4" />
      Execute drain
    </Button>
  {/snippet}
</Modal>

<!-- Host tags modal (P.11.2) -->
<Modal
  open={showTagsFor !== null}
  onclose={() => (showTagsFor = null)}
  title="Manage host tags"
  maxWidth="max-w-md"
>
  <div class="space-y-4">
    <p class="text-sm text-[var(--fg-muted)]">
      Tags drive RBAC scoping, alert routing, and backup job targeting. Use
      lowercase letters, digits, and hyphens. Max 20 per host.
    </p>

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-2">Current tags</span>
      {#if tagsDraft.length === 0}
        <p class="text-sm text-[var(--fg-muted)] italic">No tags yet — add one below.</p>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each tagsDraft as t}
            <span class="inline-flex items-center gap-1 h-6 px-2 rounded text-xs font-mono bg-[var(--surface-hover)] border border-[var(--border)]">
              {t}
              <button
                class="ml-0.5 text-[var(--fg-muted)] hover:text-[var(--danger)]"
                onclick={() => removeTagDraft(t)}
                aria-label="Remove {t}"
                type="button"
              >
                <X class="w-3 h-3" />
              </button>
            </span>
          {/each}
        </div>
      {/if}
    </div>

    <div>
      <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Add tag</span>
      <div class="flex gap-2">
        <input
          class="dm-input flex-1"
          placeholder="prod, eu-west, team-frontend..."
          bind:value={tagInput}
          onkeydown={(e) => {
            if (e.key === 'Enter') { e.preventDefault(); addTagDraft(tagInput); }
          }}
          list="tag-suggestions"
        />
        <datalist id="tag-suggestions">
          {#each allTagSuggestions.filter((s) => !tagsDraft.includes(s)) as s}
            <option value={s}></option>
          {/each}
        </datalist>
        <Button variant="secondary" onclick={() => addTagDraft(tagInput)}>Add</Button>
      </div>
      {#if allTagSuggestions.length > 0}
        <p class="text-xs text-[var(--fg-muted)] mt-1">Existing tags in your fleet will autocomplete.</p>
      {/if}
    </div>
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showTagsFor = null)}>Cancel</Button>
    <Button variant="primary" loading={tagsBusy} onclick={saveTags}>Save tags</Button>
  {/snippet}
</Modal>

<!-- Agent too old for self-upgrade (422) — shows a manual upgrade flow. -->
<Modal
  open={legacyUpgradeAgent !== null}
  title="Agent too old to self-upgrade"
  maxWidth="max-w-2xl"
  onclose={() => (legacyUpgradeAgent = null)}
>
  {#if legacyUpgradeAgent}
    {@const agent = legacyUpgradeAgent}
    <div class="space-y-4 text-sm">
      <div class="flex items-start gap-2 text-xs bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] rounded-lg px-3 py-2 text-[var(--color-warning-400)]">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>
          <strong>{agent.name}</strong> runs an agent binary from before the self-upgrade feature shipped. Run the command below once on the host; future upgrades go through the UI.
        </div>
      </div>

      <div>
        <div class="text-xs text-[var(--fg-muted)] mb-1">One-time manual upgrade command (run on the agent host as a user with sudo)</div>
        <div class="relative">
          <pre class="dm-card p-3 font-mono text-xs whitespace-pre-wrap break-all max-h-96 overflow-auto">{`# Move the binary into a directory the agent user owns, so future
# self-upgrades from the UI can rename the new binary in place
# without needing root (the old layout under /usr/local/bin is
# root-owned and the agent runs unprivileged).
sudo systemctl stop dockmesh-agent
sudo install -d -m 0750 -o dockmesh-agent -g dockmesh-agent \\
  /var/lib/dockmesh/bin
sudo curl -fsSL ${window.location.origin}/install/dockmesh-agent-linux-amd64 \\
  -o /var/lib/dockmesh/bin/dockmesh-agent
sudo chown dockmesh-agent:dockmesh-agent /var/lib/dockmesh/bin/dockmesh-agent
sudo chmod 0755 /var/lib/dockmesh/bin/dockmesh-agent
sudo ln -sf /var/lib/dockmesh/bin/dockmesh-agent /usr/local/bin/dockmesh-agent
sudo sed -i 's|^ExecStart=/usr/local/bin/dockmesh-agent$|ExecStart=/var/lib/dockmesh/bin/dockmesh-agent|' \\
  /etc/systemd/system/dockmesh-agent.service
sudo systemctl daemon-reload
sudo systemctl start dockmesh-agent`}</pre>
          <button
            class="absolute top-2 right-2 px-2 py-1 text-xs rounded bg-[var(--surface)] border border-[var(--border)] hover:bg-[var(--surface-hover)]"
            onclick={() => copyText(`sudo systemctl stop dockmesh-agent && sudo install -d -m 0750 -o dockmesh-agent -g dockmesh-agent /var/lib/dockmesh/bin && sudo curl -fsSL ${window.location.origin}/install/dockmesh-agent-linux-amd64 -o /var/lib/dockmesh/bin/dockmesh-agent && sudo chown dockmesh-agent:dockmesh-agent /var/lib/dockmesh/bin/dockmesh-agent && sudo chmod 0755 /var/lib/dockmesh/bin/dockmesh-agent && sudo ln -sf /var/lib/dockmesh/bin/dockmesh-agent /usr/local/bin/dockmesh-agent && sudo sed -i 's|^ExecStart=/usr/local/bin/dockmesh-agent$|ExecStart=/var/lib/dockmesh/bin/dockmesh-agent|' /etc/systemd/system/dockmesh-agent.service && sudo systemctl daemon-reload && sudo systemctl start dockmesh-agent`)}
          >
            <Copy class="w-3 h-3 inline" /> copy
          </button>
        </div>
      </div>

      <p class="text-xs text-[var(--fg-muted)]">
        After the restart the agent reconnects within ~15s. Once you see it online again, click Upgrade and it will self-update from here on.
      </p>
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (legacyUpgradeAgent = null)}>Close</Button>
  {/snippet}
</Modal>
