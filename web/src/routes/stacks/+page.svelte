<script lang="ts">
  import { untrack } from 'svelte';
  import {
    api, ApiError, isFanOut,
    type StackDeployment, type DiscoveredStack, type StackGitSourceInput, type EnvDrift,
  } from '$lib/api';
  import { goto } from '$app/navigation';
  import { stackInLens } from '$lib/lens';
  import { Button, Modal, Input, Skeleton, Segmented, LiveIndicator, Menu, EmptyState } from '$lib/components/ui';
  import StackHealth from '$lib/components/ui/StackHealth.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { latest } from '$lib/latest';
  import { hosts } from '$lib/stores/host.svelte';
  import { Plus, Terminal, AlertTriangle, ArrowRight, FileCode2 } from 'lucide-svelte';
  import DeleteStackDialog from './_DeleteStackDialog.svelte';
  import { m } from '$lib/paraglide/messages.js';
  import { confirmOverride } from '$lib/deployOverride';
  import { fmtDateTime } from '$lib/i18n';

  const canWrite = $derived(allowed('stacks.create'));
  const canDeploy = $derived(allowed('stacks.deploy'));
  const canAdopt = $derived(allowed('stacks.adopt'));

  type SortMode = 'name' | 'state' | 'deployed';
  let sortMode = $state<SortMode>('state');

  type StackState = 'running' | 'stopped' | 'partial' | 'unhealthy' | 'undeployed';
  interface StackCard {
    name: string;
    state: StackState;
    services: Array<{ name: string; state: string; status: string }>;
    hosts: Array<{ id: string; name: string }>;
    deployment?: StackDeployment;
    targetHost?: string;
    status?: 'ok' | 'needs_recovery';
  }

  let stackCards = $state<StackCard[]>([]);
  let loading = $state(true);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let showAdoptPicker = $state(false);
  let showCreate = $state(false);
  let showImport = $state(false);

  let filter = $state<'all' | StackState>('all');
  let deleting = $state<{ name: string; host: string; containers: number } | null>(null);
  let showDelete = $state(false);
  const canDelete = $derived(allowed('stacks.delete'));
  let search = $state('');

  let newName = $state('');
  let newCompose = $state(
    'services:\n  web:\n    image: nginx:alpine\n    ports:\n      - "8080:80"\n'
  );
  let newEnv = $state('');
  let creating = $state(false);
  // Host the new stack should deploy onto when the operator picks
  // "Create & deploy". Defaults to whatever the sidebar host switcher
  // is currently on; ignored when only `local` is available. The picker
  // is hidden unless multi-host is configured to keep the modal lean for
  // single-host installs.
  let newHostId = $state('local');

  // Create-modal source toggle. "editor" = paste compose + env. "git" =
  // clone from a repository (the backend's POST /stacks/from-git path
  // does configure + initial sync in one shot).
  type CreateMode = 'editor' | 'git';
  let createMode = $state<CreateMode>('editor');
  let newGit = $state<StackGitSourceInput>({
    repo_url: '',
    branch: 'main',
    path_in_repo: '.',
    auth_kind: 'none',
    username: '',
    password: '',
    ssh_key: '',
    auto_deploy: false,
    poll_interval_sec: 300,
    webhook_secret: '',
  });
  // After a successful git import, show the drift summary so the user
  // immediately sees that new env vars need filling in.
  let lastGitDrift = $state<EnvDrift | null>(null);

  let runCommand = $state('');
  let convertWarnings = $state<string[]>([]);
  let converting = $state(false);

  let discoveredStacks = $state<DiscoveredStack[]>([]);
  let showAdopt = $state(false);
  let adoptTarget = $state<DiscoveredStack | null>(null);
  let adoptCompose = $state('');
  let adopting = $state(false);

  const nextLoad = latest();
  async function load() {
    const current = nextLoad();
    // Skeleton only on the first load; polls update the list in place.
    const isFirstLoad = untrack(() => stackCards.length === 0);
    if (isFirstLoad) loading = true;
    refreshing = true;
    try {
      const [stackList, containersRaw, discovered] = await Promise.all([
        api.stacks.list(),
        api.containers.list(true, hosts.id).catch(() => []),
        canAdopt
          ? api.stacks.discovered(hosts.id).catch(() => [] as DiscoveredStack[])
          : Promise.resolve([] as DiscoveredStack[]),
      ]);
      if (!current()) return;
      discoveredStacks = discovered;
      const containers: any[] = isFanOut(containersRaw) ? containersRaw.items : containersRaw;

      const byStack = new Map<string, any[]>();
      for (const c of containers) {
        const proj: string | undefined = c.Labels?.['com.docker.compose.project'];
        if (!proj) continue;
        if (!byStack.has(proj)) byStack.set(proj, []);
        byStack.get(proj)!.push(c);
      }

      stackCards = stackList.filter((s) => stackInLens(s, hosts.id, byStack.has(s.name))).map((s) => {
        const cs = byStack.get(s.name) ?? [];
        const running = cs.filter((c) => c.State === 'running').length;
        const unhealthy = cs.filter((c) =>
          (c.Status ?? '').toLowerCase().includes('unhealthy')
        ).length;
        let state: StackState;
        if (cs.length === 0) state = s.deployment ? 'stopped' : 'undeployed';
        else if (unhealthy > 0) state = 'unhealthy';
        else if (running === cs.length) state = 'running';
        else if (running === 0) state = 'stopped';
        else state = 'partial';

        const seenHost = new Map<string, string>();
        for (const c of cs) {
          const id = c.host_id ?? 'local';
          const name = c.host_name ?? m.common_local();
          if (!seenHost.has(id)) seenHost.set(id, name);
        }

        return {
          name: s.name,
          state,
          services: cs.map((c) => ({
            name:
              c.Labels?.['com.docker.compose.service'] ??
              (c.Names?.[0] ?? '').replace(/^\//, ''),
            state: c.State,
            status: c.Status ?? '',
          })),
          hosts: [...seenHost.entries()].map(([id, name]) => ({ id, name })),
          deployment: s.deployment,
          targetHost: s.target_host,
          status: s.status ?? 'ok',
        };
      });
      lastLoaded = Date.now();
    } catch (err) {
      if (current()) toast.error(m.stacks_list_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      if (current()) {
        loading = false;
        refreshing = false;
      }
    }
  }

  // The new stack belongs to the picked host until it is deployed, so a
  // user limited to some hosts can keep working on it.
  const createHosts = $derived(hosts.available.filter((h) => h.kind !== 'all'));
  function openCreate() {
    newHostId = hosts.isAll
      ? (createHosts.find((h) => h.id === 'local') ?? createHosts[0])?.id ?? 'local'
      : hosts.id;
    showCreate = true;
  }

  async function create(e: Event) {
    e.preventDefault();
    await runCreate(false);
  }

  async function createAndDeploy() {
    await runCreate(true);
  }

  // Shared create flow. `deploy` controls whether we follow the create
  // call with a deploy on the picked host — for the git path the deploy
  // is unconditional because the import endpoint already syncs but
  // doesn't bring up containers.
  async function runCreate(deploy: boolean) {
    creating = true;
    lastGitDrift = null;
    const stackName = newName;
    const targetHost = newHostId || 'local';
    try {
      if (createMode === 'git') {
        const res = await api.stacks.createFromGit(stackName, normalizeGitInput(newGit), targetHost);
        const driftCount =
          (res.sync?.env_drift?.new_from_repo?.length ?? 0) +
          (res.sync?.env_drift?.new_from_compose?.length ?? 0);
        toast.success(
          m.stacks_list_imported(),
          driftCount > 0
            ? m.stacks_list_imported_drift({ name: stackName, count: driftCount })
            : m.stacks_list_imported_synced({ name: stackName, sha: res.sync?.new_sha?.slice(0, 7) ?? '' }),
        );
        lastGitDrift = res.sync?.env_drift ?? null;
      } else {
        await api.stacks.create(stackName, newCompose, newEnv || undefined, targetHost);
        toast.success(m.stacks_list_created(), stackName);
      }
      if (deploy) {
        try {
          await api.stacks.deploy(stackName, targetHost);
          toast.success(m.stacks_deploy_started(), `${stackName} → ${targetHost}`);
        } catch (err) {
          toast.error(m.stacks_deploy_failed(), err instanceof ApiError ? err.message : undefined);
        }
      }
      showCreate = false;
      newName = '';
      resetGitForm();
      await load();
    } catch (err) {
      toast.error(m.stacks_list_create_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  function resetGitForm() {
    newGit = {
      repo_url: '',
      branch: 'main',
      path_in_repo: '.',
      auth_kind: 'none',
      username: '',
      password: '',
      ssh_key: '',
      auto_deploy: false,
      poll_interval_sec: 300,
      webhook_secret: '',
    };
    createMode = 'editor';
  }

  // Strip secrets the user didn't fill in so the backend doesn't store
  // empty-string auth (which would shadow a missing field). 0 turns
  // polling off; anything else is raised to the backend's 60 s minimum.
  function normalizeGitInput(g: StackGitSourceInput): StackGitSourceInput {
    const out: StackGitSourceInput = {
      repo_url: g.repo_url.trim(),
      branch: g.branch?.trim() || 'main',
      path_in_repo: g.path_in_repo?.trim() || '.',
      auth_kind: g.auth_kind ?? 'none',
      auto_deploy: g.auto_deploy ?? false,
      poll_interval_sec: g.poll_interval_sec === 0 ? 0 : Math.max(60, g.poll_interval_sec ?? 300),
    };
    if (out.auth_kind === 'http') {
      out.username = (g.username ?? '').trim();
      out.password = g.password ?? '';
    } else if (out.auth_kind === 'ssh') {
      out.username = (g.username ?? 'git').trim() || 'git';
      out.ssh_key = g.ssh_key ?? '';
    }
    if (g.webhook_secret && g.webhook_secret.trim().length > 0) {
      out.webhook_secret = g.webhook_secret.trim();
    }
    return out;
  }

  function openAdopt(ds: DiscoveredStack) {
    adoptTarget = ds;
    adoptCompose = `${m.stacks_adopt_compose_comment()}\n#\nservices:\n${ds.services.map((s) => `  ${s.name}:\n    image: ${s.image ?? ''}`).join('\n')}\n`;
    showAdopt = true;
  }

  async function submitAdopt(e: Event) {
    e.preventDefault();
    if (!adoptTarget) return;
    adopting = true;
    try {
      const res = await api.stacks.adopt({
        name: adoptTarget.project_name,
        host_id: adoptTarget.host_id,
        compose: adoptCompose,
        accepted_warnings: ['metadata-only-adoption'],
      });
      toast.success(
        m.stacks_adopted(),
        m.stacks_adopted_detail({ name: res.name, count: res.bound_containers })
      );
      showAdopt = false;
      adoptTarget = null;
      adoptCompose = '';
      await load();
    } catch (err) {
      toast.error(m.stacks_adopt_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      adopting = false;
    }
  }

  async function convertRun() {
    converting = true;
    convertWarnings = [];
    try {
      const res = await api.convert.runToCompose(runCommand);
      newCompose = res.yaml;
      convertWarnings = res.warnings ?? [];
      showImport = false;
      if (convertWarnings.length > 0) {
        toast.warning(m.stacks_converted_warnings(), m.stacks_converted_unsupported({ count: convertWarnings.length }));
      } else {
        toast.success(m.stacks_converted(), m.stacks_converted_detail());
      }
    } catch (err) {
      toast.error(m.stacks_convert_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      converting = false;
    }
  }

  $effect(() => {
    hosts.id;
    untrack(load);
  });

  $effect(() => autoRefresh(load, 5_000));

  const counts = $derived({
    all: stackCards.length,
    running: stackCards.filter((s) => s.state === 'running').length,
    stopped: stackCards.filter((s) => s.state === 'stopped').length,
    unhealthy: stackCards.filter((s) => s.state === 'unhealthy' || s.state === 'partial').length,
    undeployed: stackCards.filter((s) => s.state === 'undeployed').length,
  });

  const needsRecovery = $derived(stackCards.filter((s) => s.status === 'needs_recovery'));

  const visible = $derived(
    stackCards
      .filter((s) => s.status !== 'needs_recovery')
      .filter((s) => {
        if (filter === 'all') return true;
        if (filter === 'unhealthy') return s.state === 'unhealthy' || s.state === 'partial';
        return s.state === filter;
      })
      .filter((s) => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        return (
          s.name.toLowerCase().includes(q) ||
          s.services.some((svc) => svc.name.toLowerCase().includes(q))
        );
      })
      .sort((a, b) => {
        if (sortMode === 'state') {
          // Attention first, then running, stopped, not deployed.
          const order: Record<string, number> = {
            unhealthy: 0, partial: 1, running: 2, stopped: 3, undeployed: 4,
          };
          return (order[a.state] ?? 9) - (order[b.state] ?? 9) || a.name.localeCompare(b.name);
        }
        if (sortMode === 'deployed') {
          const aTime = a.deployment?.deployed_at ?? '';
          const bTime = b.deployment?.deployed_at ?? '';
          return bTime.localeCompare(aTime);
        }
        return a.name.localeCompare(b.name);
      })
  );

  function fmtRelTime(ts?: string): string {
    if (!ts) return '';
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return m.time_just_now();
    if (secs < 3600) return m.stacks_ago_min({ n: Math.floor(secs / 60) });
    if (secs < 86400) return m.stacks_ago_h({ n: Math.floor(secs / 3600) });
    return m.stacks_ago_d({ n: Math.floor(secs / 86400) });
  }

  const COLS = 'minmax(240px, 2.4fr) 210px minmax(100px, 1fr) 110px 40px';

  const isUnhealthy = (svc: { status: string }) => svc.status.toLowerCase().includes('unhealthy');
  function stackHost(s: StackCard): string {
    return s.deployment?.host_id ?? s.hosts[0]?.id ?? s.targetHost ?? (hosts.isAll ? 'local' : hosts.id);
  }
  function hostLabel(s: StackCard): string {
    if (s.deployment) return s.deployment.host_name || s.deployment.host_id;
    if (s.hosts.length) return s.hosts.map((h) => h.name).join(', ');
    if (s.targetHost) return hosts.available.find((h) => h.id === s.targetHost)?.name ?? s.targetHost;
    return '—';
  }

  async function confirmExternalChanges(err: unknown, name: string): Promise<boolean> {
    if (!(err instanceof ApiError) || err.status !== 409 || err.body?.code !== 'modified_outside') return false;
    const files: string = (err.body.files as string[] | undefined)?.join(', ') || m.stacks_ext_files_fallback();
    return confirm.ask({
      title: m.stacks_ext_title(),
      message: m.stacks_ext_message({ files, name }),
      body: m.stacks_ext_body(),
      confirmLabel: m.stacks_ext_confirm(),
      danger: true,
    });
  }

  async function deployStack(name: string, host: string) {
    try {
      const environment = await confirmOverride(name);
      if (environment === null) return;
      const res = await stackOps.run(host, name, async () => {
        try {
          return await api.stacks.deploy(name, host, environment || undefined);
        } catch (err) {
          if (!(await confirmExternalChanges(err, name))) throw err;
          return api.stacks.deploy(name, host, environment || undefined, true);
        }
      });
      toast.success(m.stacks_deployed(), m.stacks_deployed_services({ name, count: res.services.length }));
      await load();
    } catch (err) {
      toast.error(m.stacks_deploy_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function stopStack(name: string, host: string) {
    const ok = await confirm.ask({
      title: m.stacks_stop_title({ name }),
      message: m.stacks_stop_message(),
      confirmLabel: m.common_stop(),
    });
    if (!ok) return;
    try {
      await stackOps.run(host, name, () => api.stacks.stop(name, host));
      toast.info(m.stacks_stopped_toast(), name);
      await load();
    } catch (err) {
      toast.error(m.stacks_stop_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  const subtitleLine = $derived.by(() => {
    const totalHosts = hosts.available.filter((h) => h.kind !== 'all').length;
    const base = m.stacks_n_stacks({ count: counts.all });
    if (totalHosts <= 1) return base;
    return hosts.isAll ? m.stacks_list_on_n_hosts({ base, count: totalHosts }) : m.stacks_list_on_host({ base, host: hosts.selected?.name ?? m.stacks_this_host() });
  });
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>{m.common_stacks()}</h1>
    <span class="dm-sub">{subtitleLine}</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if canAdopt}
      <button type="button" class="dm-btn" onclick={() => (showAdoptPicker = true)}>
        {m.stacks_adopt_existing()}{#if discoveredStacks.length}<span class="dm-num dm-dim">{discoveredStacks.length}</span>{/if}
      </button>
    {/if}
    {#if canWrite}
      <button
        type="button"
        class="dm-btn dm-btn-primary"
        onclick={openCreate}
      >
        <Plus size={14} strokeWidth={2} /> {m.stacks_new()}
      </button>
    {/if}
  </div>

  {#if needsRecovery.length > 0}
    <div class="st-attn" role="status">
      <div class="st-attn-h">
        <AlertTriangle size={14} strokeWidth={1.8} />
        <b>{m.stacks_list_need_recovery({ count: needsRecovery.length })}</b>
        <span class="dm-dim">{m.stacks_list_recovery_hint()}</span>
      </div>
      {#each needsRecovery as s (s.name)}
        <a class="st-attn-row" href="/stacks/{encodeURIComponent(s.name)}">
          <span class="dm-mono">{s.name}</span>
          <span class="dm-dim dm-trunc">
            {#if s.deployment}
              {m.stacks_list_last_deployed_on({ ago: fmtRelTime(s.deployment.deployed_at), host: s.deployment.host_name || s.deployment.host_id })}
            {:else}
              {m.stacks_list_found_via_label()}
            {/if}
          </span>
          <span class="st-attn-cta">{m.stacks_list_open_recovery()} <ArrowRight size={12} /></span>
        </a>
      {/each}
    </div>
  {/if}

  {#if !loading && stackCards.length > 0}
    <div class="dm-toolbar">
      <Segmented
        label={m.stacks_list_filter_label()}
        bind:value={filter}
        options={[
          { key: 'all', label: m.common_all(), count: counts.all },
          { key: 'running', label: m.common_running(), count: counts.running },
          { key: 'unhealthy', label: m.stacks_list_filter_attention(), count: counts.unhealthy },
          { key: 'stopped', label: m.common_stopped(), count: counts.stopped },
          ...(counts.undeployed ? [{ key: 'undeployed', label: m.stacks_not_deployed(), count: counts.undeployed }] : [])
        ]}
      />
      <span class="dm-sp"></span>
      <input type="search" class="dm-filter" placeholder={m.stacks_list_filter_placeholder()} aria-label={m.stacks_list_filter_label()} bind:value={search} style="width: 240px" />
    </div>

  {/if}

  {#if loading}
    <div class="dm-tbl">
      {#each Array(5) as _, i (i)}
        <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>
      {/each}
    </div>
  {:else if stackCards.length === 0}
    <EmptyState title={m.stacks_list_empty_title()} description={m.stacks_list_empty_desc()}>
      {#snippet action()}
        {#if canWrite}
          <button type="button" class="dm-btn dm-btn-primary" onclick={openCreate}>
            <Plus size={14} strokeWidth={2} /> {m.stacks_new()}
          </button>
        {/if}
      {/snippet}
    </EmptyState>
  {:else}
    <div class="dm-tbl" role="table" aria-label={m.common_stacks()}>
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
        <span role="columnheader"><button type="button" onclick={() => (sortMode = 'name')}>{m.stacks_list_col_name()}{sortMode === 'name' ? ' ↓' : ''}</button></span>
        <span role="columnheader"><button type="button" onclick={() => (sortMode = 'state')}>{m.common_status()}{sortMode === 'state' ? ' ↓' : ''}</button></span>
        <span role="columnheader">{m.common_host()}</span>
        <span role="columnheader"><button type="button" onclick={() => (sortMode = 'deployed')}>{m.stacks_list_col_last_deploy()}{sortMode === 'deployed' ? ' ↓' : ''}</button></span>
        <span role="columnheader"></span>
      </div>
      {#each visible as s (s.name)}
        {@const running = s.services.filter((x) => x.state === 'running').length}
        {@const host = stackHost(s)}
        <div
          class="dm-tr dm-click"
          role="row"
          style="grid-template-columns: {COLS}"
          onclick={() => goto(`/stacks/${encodeURIComponent(s.name)}`)}
          onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(`/stacks/${encodeURIComponent(s.name)}`); }}
          tabindex="0"
        >
          <span role="cell" class="dm-nm">
            <a href="/stacks/{encodeURIComponent(s.name)}" onclick={(e) => e.stopPropagation()}>{s.name}</a>
            <small title={s.services.map((x) => `${x.name}: ${x.status || x.state}`).join('\n')}>
              {#if s.services.length}{s.services.map((x) => x.name).join(' · ')}{:else if s.state === 'undeployed'}{m.stacks_list_deploy_to_start()}{:else}{m.stacks_list_no_containers()}{/if}
            </small>
          </span>
          <span role="cell">
            <StackHealth
              {running}
              total={s.services.length}
              unhealthy={s.services.filter(isUnhealthy).length}
              members={s.services.map((x) => ({ name: x.name, state: x.state, unhealthy: isUnhealthy(x) }))}
              notDeployed={s.state === 'undeployed'}
            />
          </span>
          <span role="cell" class="dm-dim" title={hostLabel(s)}>{hostLabel(s)}</span>
          <span role="cell" class="dm-num dm-dim" title={s.deployment?.deployed_at ? fmtDateTime(s.deployment.deployed_at) : ''}>
            {s.deployment?.deployed_at ? fmtRelTime(s.deployment.deployed_at) : '—'}
          </span>
          <span role="cell" class="dm-tr-menu">
            <Menu
              label={m.stacks_actions_for({ name: s.name })}
              items={[
                { label: m.common_open(), kbd: '↵', href: `/stacks/${encodeURIComponent(s.name)}` },
                ...(canDeploy
                  ? [
                      { separator: true },
                      { label: s.state === 'undeployed' ? m.common_deploy() : s.state === 'stopped' ? m.common_start() : m.stacks_redeploy(), disabled: stackOps.isBusy(host, s.name), onclick: () => deployStack(s.name, host) },
                      ...(running > 0 ? [{ label: m.common_stop(), disabled: stackOps.isBusy(host, s.name), onclick: () => stopStack(s.name, host) }] : [])
                    ]
                  : []),
                ...(canDelete
                  ? [
                      { separator: true },
                      { label: m.common_delete_ellipsis(), danger: true, disabled: stackOps.isBusy(host, s.name), onclick: () => { deleting = { name: s.name, host, containers: s.services.length }; showDelete = true; } }
                    ]
                  : [])
              ]}
            />
          </span>
        </div>
      {:else}
        <div class="dm-empty">{m.stacks_list_no_match()}</div>
      {/each}
    </div>
  {/if}
</section>

{#if deleting}
  <DeleteStackDialog bind:open={showDelete} name={deleting.name} host={deleting.host} containers={deleting.containers} ondeleted={load} />
{/if}

<Modal bind:open={showAdoptPicker} title={m.stacks_adopt_picker_title()} maxWidth="max-w-xl">
  <div class="st-adopt">
    <p class="dm-sub">
      {m.stacks_adopt_picker_p1()} <code class="dm-mono">docker compose up</code> {m.stacks_adopt_picker_p2()}
      <code class="dm-mono">dmctl stack adopt &lt;path&gt;</code> {m.stacks_adopt_picker_p3()}
    </p>
    {#if discoveredStacks.length === 0}
      <EmptyState title={m.stacks_adopt_nothing_title()} description={m.stacks_adopt_nothing_desc({ where: hosts.isAll ? m.stacks_any_host() : (hosts.selected?.name ?? m.stacks_this_host()) })} />
    {:else}
      <div class="dm-tbl">
        {#each discoveredStacks as ds (ds.host_id + ds.project_name)}
          <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) auto">
            <span class="dm-nm">
              <span class="dm-t">{ds.project_name}</span>
              <small>{m.stacks_adopt_services_on({ count: ds.service_count, host: ds.host_name, list: ds.services.map((x) => x.name).join(', ') })}</small>
            </span>
            <button type="button" class="dm-btn dm-btn-sm" onclick={() => { showAdoptPicker = false; openAdopt(ds); }}>{m.stacks_adopt_ellipsis()}</button>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</Modal>

<Modal bind:open={showCreate} title={m.stacks_create_title()} maxWidth="max-w-3xl">
  <form onsubmit={create} class="space-y-4" id="create-stack-form">
    <div class="flex items-center justify-between">
      <div class="text-xs text-[var(--fg-muted)]">
        {m.stacks_create_name_rule_before()} <code class="font-mono">[a-z0-9][a-z0-9-]*[a-z0-9]</code>{m.stacks_create_name_rule_after()}
      </div>
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-xs"
        onclick={() => (showImport = true)}
      >
        <Terminal class="w-3.5 h-3.5" />
        {m.stacks_import_docker_run()}
      </button>
    </div>

    <Input label={m.common_name()} placeholder="my-stack" bind:value={newName} disabled={creating} />

    {#if createHosts.length > 1}
      <div>
        <label for="new-host" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
          {m.common_host()}
        </label>
        <select id="new-host" class="dm-input text-sm w-full" bind:value={newHostId} disabled={creating}>
          {#each createHosts as h (h.id)}
            <option value={h.id}>{h.name}{h.id === 'local' ? m.stacks_server_suffix() : ''}</option>
          {/each}
        </select>
        <p class="text-[10.5px] text-[var(--fg-subtle)] mt-1">
          {m.stacks_create_host_hint_before()} <em>{m.stacks_create_and_deploy()}</em> {m.stacks_create_host_hint_after()}
        </p>
      </div>
    {/if}

    <!-- Source picker: paste-it or pull-it-from-a-repo. -->
    <div class="flex gap-1 p-1 rounded bg-[var(--bg-muted,rgba(0,0,0,0.04))] w-fit">
      <button
        type="button"
        class="px-3 py-1.5 text-xs rounded transition"
        class:bg-[var(--bg)]={createMode === 'editor'}
        class:font-medium={createMode === 'editor'}
        class:text-[var(--fg-muted)]={createMode !== 'editor'}
        onclick={() => (createMode = 'editor')}
      >
        {m.stacks_create_mode_editor()}
      </button>
      <button
        type="button"
        class="px-3 py-1.5 text-xs rounded transition"
        class:bg-[var(--bg)]={createMode === 'git'}
        class:font-medium={createMode === 'git'}
        class:text-[var(--fg-muted)]={createMode !== 'git'}
        onclick={() => (createMode = 'git')}
      >
        {m.stacks_create_mode_git()}
      </button>
    </div>

    {#if createMode === 'editor'}
      <div>
        <label for="compose" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
          <span class="inline-flex items-center gap-1"><FileCode2 class="w-3 h-3" /> compose.yaml</span>
        </label>
        <textarea
          id="compose"
          class="dm-input font-mono text-xs h-64 resize-y"
          bind:value={newCompose}
          disabled={creating}
        ></textarea>
      </div>

      <div>
        <label for="env" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.stacks_env_optional()}</label>
        <textarea
          id="env"
          class="dm-input font-mono text-xs h-20 resize-y"
          bind:value={newEnv}
          disabled={creating}
          placeholder="KEY=value"
        ></textarea>
      </div>
    {:else}
      <!-- ───── git import form ───── -->
      <Input
        label={m.stacks_git_repo_url()}
        placeholder="https://github.com/acme/stack.git"
        bind:value={newGit.repo_url}
        disabled={creating}
      />
      <div class="grid grid-cols-2 gap-3">
        <Input
          label={m.stacks_git_branch()}
          placeholder="main"
          bind:value={newGit.branch as any}
          disabled={creating}
        />
        <Input
          label={m.stacks_git_path()}
          placeholder="."
          bind:value={newGit.path_in_repo as any}
          disabled={creating}
        />
      </div>
      <div>
        <label for="git-auth" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.stacks_git_auth()}</label>
        <select id="git-auth" class="dm-input" bind:value={newGit.auth_kind as any} disabled={creating}>
          <option value="none">{m.stacks_git_auth_none()}</option>
          <option value="http">{m.stacks_git_auth_http()}</option>
          <option value="ssh">{m.stacks_git_auth_ssh()}</option>
        </select>
      </div>
      {#if newGit.auth_kind === 'http'}
        <div class="grid grid-cols-2 gap-3">
          <Input
            label={m.stacks_git_username()}
            placeholder={m.stacks_git_username_placeholder()}
            bind:value={newGit.username as any}
            disabled={creating}
          />
          <Input
            label={m.stacks_git_pat()}
            type="password"
            placeholder={m.stacks_git_pat_or()}
            bind:value={newGit.password as any}
            disabled={creating}
          />
        </div>
        {#if newGit.repo_url && /github\.com/.test(newGit.repo_url)}
          <div class="dm-card p-3 text-xs text-[var(--fg-muted)] space-y-1">
            <p>
              <strong class="text-[var(--fg)]">{m.stacks_git_gh_requires()}</strong>{m.stacks_git_gh_not_password()}
            </p>
            <p>
              {m.stacks_git_gh_create_at()}
              <a href="https://github.com/settings/personal-access-tokens" target="_blank" rel="noopener" class="underline">
                github.com/settings/personal-access-tokens
              </a>
              {m.stacks_git_gh_with()} <code class="font-mono">Contents: Read-only</code> {m.stacks_git_gh_permission()}
              <a href="https://github.com/settings/tokens" target="_blank" rel="noopener" class="underline">
                {m.stacks_git_gh_classic()}
              </a>
              {m.stacks_git_gh_with_the()} <code class="font-mono">repo</code>{m.stacks_git_gh_scope()}
            </p>
          </div>
        {/if}
      {:else if newGit.auth_kind === 'ssh'}
        <Input
          label={m.stacks_git_ssh_user()}
          placeholder="git"
          bind:value={newGit.username as any}
          disabled={creating}
        />
        <div>
          <label for="git-ssh-key" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
            {m.stacks_git_ssh_key()}
          </label>
          <textarea
            id="git-ssh-key"
            class="dm-input font-mono text-xs h-24 resize-y"
            bind:value={newGit.ssh_key as any}
            disabled={creating}
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          ></textarea>
        </div>
      {/if}

      <div class="grid grid-cols-2 gap-3">
        <Input
          label={m.stacks_git_poll()}
          type="number"
          min={0}
          bind:value={newGit.poll_interval_sec as any}
          disabled={creating}
        />
        <Input
          label={m.stacks_git_webhook_secret()}
          type="password"
          bind:value={newGit.webhook_secret as any}
          disabled={creating}
        />
      </div>

      <label class="flex items-center gap-2 text-xs">
        <input
          type="checkbox"
          bind:checked={newGit.auto_deploy as any}
          disabled={creating}
        />
        {m.stacks_git_auto_deploy()}
      </label>

      <div class="dm-card p-3 text-xs text-[var(--fg-muted)] space-y-1">
        <p>{m.stacks_git_seed_before()} <code class="font-mono">.env</code> {m.stacks_git_seed_mid()} <code class="font-mono">${`{VAR}`}</code> {m.stacks_git_seed_after()}</p>
      </div>
    {/if}

    {#if convertWarnings.length > 0}
      <div class="dm-card p-3 text-xs border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
        <div class="font-medium text-[var(--color-warning-400)] mb-1">{m.stacks_converter_warnings()}</div>
        <ul class="list-disc list-inside text-[var(--fg-muted)] space-y-0.5">
          {#each convertWarnings as w}<li>{w}</li>{/each}
        </ul>
      </div>
    {/if}
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>{m.common_cancel()}</Button>
    <Button
      variant="secondary"
      type="submit"
      form="create-stack-form"
      loading={creating}
      disabled={creating || !newName ||
        (createMode === 'editor' ? !newCompose : !newGit.repo_url)}
    >
      {createMode === 'git' ? m.stacks_import_sync() : m.common_create()}
    </Button>
    <Button
      variant="primary"
      loading={creating}
      disabled={creating || !newName || !canDeploy ||
        (createMode === 'editor' ? !newCompose : !newGit.repo_url)}
      onclick={createAndDeploy}
    >
      {m.stacks_create_and_deploy()}
    </Button>
  {/snippet}
</Modal>

{#if lastGitDrift && ((lastGitDrift.new_from_repo?.length ?? 0) + (lastGitDrift.new_from_compose?.length ?? 0) > 0)}
  <div class="fixed bottom-4 right-4 max-w-md dm-card p-4 text-xs space-y-2 border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] z-50">
    <div class="font-medium">{m.stacks_drift_detected()}</div>
    {#if lastGitDrift.new_from_repo?.length}
      <div>
        <span class="text-[var(--fg-muted)]">{m.stacks_drift_new_from_repo()}</span>
        <code class="font-mono">{lastGitDrift.new_from_repo.join(', ')}</code>
      </div>
    {/if}
    {#if lastGitDrift.new_from_compose?.length}
      <div>
        <span class="text-[var(--fg-muted)]">{m.stacks_drift_needed_by_compose()}</span>
        <code class="font-mono">{lastGitDrift.new_from_compose.join(', ')}</code>
      </div>
    {/if}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={() => (lastGitDrift = null)}
    >
      {m.stacks_dismiss()}
    </button>
  </div>
{/if}

<Modal bind:open={showImport} title={m.stacks_import_docker_run()} maxWidth="max-w-xl">
  <p class="text-sm text-[var(--fg-muted)] mb-4">
    {m.stacks_import_p_before()} <code class="font-mono">docker run</code>{m.stacks_import_p_after()}
  </p>
  <textarea
    class="dm-input font-mono text-xs h-32"
    placeholder="docker run -d --name web -p 8080:80 nginx:alpine"
    bind:value={runCommand}
  ></textarea>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showImport = false)}>{m.common_cancel()}</Button>
    <Button
      variant="primary"
      loading={converting}
      disabled={converting || !runCommand.trim()}
      onclick={convertRun}
    >
      {m.stacks_convert()}
    </Button>
  {/snippet}
</Modal>

<Modal bind:open={showAdopt} title={adoptTarget ? m.stacks_adopt_title({ name: adoptTarget.project_name }) : m.stacks_adopt()} maxWidth="max-w-3xl">
  {#if adoptTarget}
    <form onsubmit={submitAdopt} class="space-y-4">
      <div class="rounded-md border border-[var(--border)] bg-[var(--bg-card)] px-3 py-2 text-xs text-[var(--fg-muted)]">
        <div class="text-[var(--fg)] font-medium mb-1">{m.stacks_adopt_meta_only()}</div>
        {m.stacks_adopt_meta_1()} <code class="font-mono">stacks/{adoptTarget.project_name}/</code> {m.stacks_adopt_meta_2()}
        <strong>{adoptTarget.service_count}</strong> {m.stacks_adopt_meta_3({ count: adoptTarget.service_count })} <code class="font-mono">./config.yml</code> {m.stacks_adopt_meta_4()}
        <code class="font-mono">dmctl stack adopt &lt;path&gt;</code> {m.stacks_adopt_meta_5()}
      </div>
      <label class="block text-xs font-medium">compose.yaml
        <textarea
          class="dm-input font-mono text-xs mt-1 h-64 w-full"
          required
          bind:value={adoptCompose}
        ></textarea>
      </label>
    </form>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showAdopt = false)}>{m.common_cancel()}</Button>
    <Button
      variant="primary"
      loading={adopting}
      disabled={adopting || !adoptCompose.trim()}
      onclick={submitAdopt}
    >
      {m.stacks_adopt()}
    </Button>
  {/snippet}
</Modal>

<style>
  .st-attn {
    border: 1px solid var(--warn-bd);
    background: var(--warn-bg);
    border-radius: 9px;
    padding: 4px 14px;
  }
  .st-attn-h {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 36px;
    color: var(--warn);
    font-size: 13px;
  }
  .st-attn-h b { color: var(--fg); font-weight: 600; }
  .st-attn-row {
    display: grid;
    grid-template-columns: 200px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: center;
    min-height: 36px;
    border-top: 1px solid var(--warn-bd);
    color: var(--fg);
    text-decoration: none;
    font-size: 13px;
  }
  .st-attn-row:hover .st-attn-cta { text-decoration: underline; }
  .st-attn-cta { color: var(--accent); display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
  .st-adopt { display: flex; flex-direction: column; gap: 12px; }
  .st-adopt p { margin: 0; }
</style>
