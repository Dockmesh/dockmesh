<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError, type BackupTarget, type BackupJob } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, EmptyState, StatBar } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { fmtBytes, fmtAgo, cronHuman, targetTypeLabel } from '../../_format';
  import { Pencil, Trash2, RefreshCw, Box } from 'lucide-svelte';

  const id = $derived(parseInt(page.params.id ?? '', 10));

  let target = $state<BackupTarget | null>(null);
  let jobs = $state<BackupJob[]>([]);
  let loading = $state(true);
  let notFound = $state(false);

  $effect(() => {
    if (target) pageContext.set(target.name);
    return () => pageContext.clear();
  });

  async function load() {
    try {
      const [allTargets, allJobs] = await Promise.all([api.backups.listTargets(), api.backups.listJobs()]);
      const t = allTargets.find((x) => x.id === id);
      notFound = !t;
      target = t ?? null;
      jobs = allJobs;
    } catch (err) {
      toast.error('Failed to load target', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  $effect(() => { void id; untrack(load); });
  $effect(() => autoRefresh(load, 15_000));

  let testing = $state(false);
  async function runTest() {
    if (!target) return;
    testing = true;
    try {
      const res = await api.backups.testTarget(target.id);
      if (res.status === 'connected') toast.success('Connected', res.total_bytes > 0 ? `${fmtBytes(res.free_bytes)} free of ${fmtBytes(res.total_bytes)}` : undefined);
      else toast.error('Connection failed', res.error);
      await load();
    } catch (err) {
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      testing = false;
    }
  }
  async function deleteTarget() {
    if (!target) return;
    if (!(await confirm.ask({
      title: 'Delete backup target',
      message: `Delete target "${target.name}"?`,
      body: 'Backup jobs that write to this target will fail on their next run until you reassign them.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.backups.deleteTarget(target.id);
      toast.success('Deleted', target.name);
      goto('/backups?tab=targets');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  const pendingHostKey = $derived(target?.type === 'sftp' && !!(target.config as { host_key_pending?: string } | null)?.host_key_pending);
  async function acceptHostKey() {
    if (!target) return;
    if (!(await confirm.ask({
      title: 'Accept new SFTP host key',
      message: `Trust the new host key for target "${target.name}"?`,
      body: 'Only do this if you know why the server key changed (server reinstalled, key rotated). Otherwise someone may be intercepting the connection.',
      confirmLabel: 'Accept key', danger: true
    }))) return;
    try {
      await api.backups.acceptTargetHostKey(target.id);
      toast.success('Host key accepted');
      await load();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const usedPct = $derived(target && target.total_bytes > 0 ? (target.used_bytes / target.total_bytes) * 100 : null);
  const jobsHere = $derived(target ? jobs.filter((j) => j.target_id === target!.id) : []);
  // The API returns secret fields empty with a "<field>_set" marker.
  const cfgEntries = $derived.by(() => {
    const cfg = (target?.config ?? {}) as Record<string, any>;
    return Object.entries(cfg)
      .filter(([k, v]) => !k.endsWith('_set') && k !== 'host_key_pending' && v !== '' && v != null)
      .concat(Object.keys(cfg).filter((k) => k.endsWith('_set') && cfg[k]).map((k) => [k.slice(0, -4), '••••••••']));
  });
  const LABELS: Record<string, string> = {
    path: 'Path', host: 'Server', port: 'Port', username: 'User', share: 'Share', endpoint: 'Endpoint',
    bucket: 'Bucket', region: 'Region', prefix: 'Prefix', use_ssl: 'TLS', access_key: 'Access key', url: 'URL',
    host_key: 'Host key', password: 'Password', secret_key: 'Secret key', private_key: 'Private key'
  };
</script>

{#if loading && !target}
  <section class="dm-page"><div class="dm-tbl"><div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div></div></section>
{:else if notFound || !target}
  <section class="dm-page">
    <EmptyState icon={Box} title="Target not found" description="It was deleted, or you do not have access to it.">
      {#snippet action()}<a class="dm-btn" href="/backups?tab=targets">All targets</a>{/snippet}
    </EmptyState>
  </section>
{:else}
  <section class="dm-page">
    <div class="dm-ph">
      <h1>{target.name}</h1>
      <span class="dm-sub">{targetTypeLabel(target.type)}</span>
      <span class="dm-sp"></span>
      {#if allowed('backups.delete')}
        <button type="button" class="dm-btn dm-btn-danger" onclick={deleteTarget}><Trash2 size={13} /> Delete</button>
      {/if}
      {#if allowed('backups.update')}
        <a href="/backups?tab=targets&edit={target.id}" class="dm-btn"><Pencil size={13} /> Edit</a>
        <button type="button" class="dm-btn dm-btn-primary" onclick={runTest} disabled={testing}>
          <RefreshCw size={13} /> {testing ? 'Testing…' : 'Test connection'}
        </button>
      {/if}
    </div>

    {#if pendingHostKey}
      <div class="fail" role="alert">
        <b>The SFTP server presented a different host key.</b>
        <span class="dm-dim">Backups to this target stop until the key is accepted.</span>
        <span class="dm-sp"></span>
        {#if allowed('backups.update')}<button type="button" class="dm-btn dm-btn-sm dm-btn-danger" onclick={acceptHostKey}>Accept new host key…</button>{/if}
      </div>
    {/if}

    <StatBar
      items={[
        {
          label: 'Connection',
          value: target.status === 'connected' ? 'Connected' : target.status === 'error' ? 'Failed' : 'Not tested',
          note: `tested ${fmtAgo(target.last_checked_at)}`,
          tone: target.status === 'error' ? 'bad' : ''
        },
        { label: 'Used', value: target.total_bytes > 0 ? fmtBytes(target.used_bytes) : '—', note: usedPct != null ? `${usedPct.toFixed(1)} % of ${fmtBytes(target.total_bytes)}` : 'size unknown', tone: usedPct != null && usedPct > 80 ? 'warn' : '' },
        { label: 'Free', value: target.total_bytes > 0 ? fmtBytes(target.free_bytes) : '—' },
        { label: 'Jobs', value: jobsHere.length, note: 'writing here' }
      ]}
    />

    <div class="grid">
      <section>
        <h2 class="dm-h2">Jobs writing here</h2>
        <div class="dm-tbl">
          {#each jobsHere as j (j.id)}
            <a class="dm-tr dm-click row" style="grid-template-columns: 14px minmax(0, 1fr) minmax(0, 1fr)" href="/backups/jobs/{j.id}">
              <span><span class="dm-dot {j.enabled ? 'ok' : ''}"></span></span>
              <span class="dm-nm"><span class="dm-t">{j.name}</span>{#if !j.enabled}<small>disabled</small>{/if}</span>
              <span class="dm-dim dm-trunc">{cronHuman(j.schedule)}</span>
            </a>
          {:else}
            <div class="dm-empty">No job writes to this target yet.</div>
          {/each}
        </div>
      </section>
      <section>
        <h2 class="dm-h2">Configuration</h2>
        {#if cfgEntries.length === 0}
          <p class="dm-sub">No configuration recorded.</p>
        {:else}
          <dl class="dm-kv">
            {#each cfgEntries as [k, v] (k)}
              <dt>{LABELS[k] ?? k}</dt>
              <dd class="dm-mono small">{String(v)}</dd>
            {/each}
          </dl>
        {/if}
      </section>
    </div>
  </section>
{/if}

<style>
  .grid { display: grid; grid-template-columns: minmax(0, 1.2fr) minmax(280px, 1fr); gap: 28px; align-items: start; }
  .row { color: inherit; text-decoration: none; }
  .small { font-size: 12px; overflow-wrap: anywhere; }
  .fail { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 8px 12px; border-radius: var(--r); border: 1px solid color-mix(in srgb, var(--bad) 45%, transparent); background: color-mix(in srgb, var(--bad) 8%, transparent); font-size: var(--fs-sm); }
  .fail b { color: var(--bad); }
  @media (max-width: 1100px) { .grid { grid-template-columns: minmax(0, 1fr); } }
</style>
