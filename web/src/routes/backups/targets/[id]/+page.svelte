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
  import { m } from '$lib/paraglide/messages.js';
  import { fmtNumber } from '$lib/i18n';

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
      toast.error(m.backups_target_load_failed(), err instanceof ApiError ? err.message : undefined);
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
      if (res.status === 'connected') toast.success(m.backups_connected(), res.total_bytes > 0 ? m.backups_free_of({ free: fmtBytes(res.free_bytes), total: fmtBytes(res.total_bytes) }) : undefined);
      else toast.error(m.backups_connection_failed(), res.error);
      await load();
    } catch (err) {
      toast.error(m.backups_test_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      testing = false;
    }
  }
  async function deleteTarget() {
    if (!target) return;
    if (!(await confirm.ask({
      title: m.backups_delete_target_title(),
      message: m.backups_delete_target_message({ name: target.name }),
      body: m.backups_delete_target_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    try {
      await api.backups.deleteTarget(target.id);
      toast.success(m.backups_deleted(), target.name);
      goto('/backups?tab=targets');
    } catch (err) {
      toast.error(m.backups_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }
  const pendingHostKey = $derived(target?.type === 'sftp' && !!(target.config as { host_key_pending?: string } | null)?.host_key_pending);
  async function acceptHostKey() {
    if (!target) return;
    if (!(await confirm.ask({
      title: m.backups_accept_host_key_title(),
      message: m.backups_accept_target_host_key_message({ name: target.name }),
      body: m.backups_host_key_body(),
      confirmLabel: m.backups_accept_key(), danger: true
    }))) return;
    try {
      await api.backups.acceptTargetHostKey(target.id);
      toast.success(m.backups_host_key_accepted());
      await load();
    } catch (err) {
      toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined);
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
  const LABELS: Record<string, () => string> = {
    path: m.common_path, host: m.backups_cfg_server, port: m.common_port, username: m.common_user, share: m.backups_cfg_share, endpoint: m.backups_cfg_endpoint,
    bucket: m.backups_cfg_bucket, region: m.backups_cfg_region, prefix: m.backups_cfg_prefix, use_ssl: () => 'TLS', access_key: m.backups_cfg_access_key, url: () => 'URL',
    host_key: m.backups_cfg_host_key, password: m.backups_cfg_password, secret_key: m.backups_cfg_secret_key, private_key: m.backups_cfg_private_key
  };
</script>

{#if loading && !target}
  <section class="dm-page"><div class="dm-tbl"><div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div></div></section>
{:else if notFound || !target}
  <section class="dm-page">
    <EmptyState icon={Box} title={m.backups_target_not_found()} description={m.backups_not_found_desc()}>
      {#snippet action()}<a class="dm-btn" href="/backups?tab=targets">{m.backups_all_targets()}</a>{/snippet}
    </EmptyState>
  </section>
{:else}
  <section class="dm-page">
    <div class="dm-ph">
      <h1>{target.name}</h1>
      <span class="dm-sub">{targetTypeLabel(target.type)}</span>
      <span class="dm-sp"></span>
      {#if allowed('backups.delete')}
        <button type="button" class="dm-btn dm-btn-danger" onclick={deleteTarget}><Trash2 size={13} /> {m.common_delete()}</button>
      {/if}
      {#if allowed('backups.update')}
        <a href="/backups?tab=targets&edit={target.id}" class="dm-btn"><Pencil size={13} /> {m.common_edit()}</a>
        <button type="button" class="dm-btn dm-btn-primary" onclick={runTest} disabled={testing}>
          <RefreshCw size={13} /> {testing ? m.backups_testing() : m.backups_test_connection()}
        </button>
      {/if}
    </div>

    {#if pendingHostKey}
      <div class="fail" role="alert">
        <b>{m.backups_host_key_different()}</b>
        <span class="dm-dim">{m.backups_host_key_stop_hint()}</span>
        <span class="dm-sp"></span>
        {#if allowed('backups.update')}<button type="button" class="dm-btn dm-btn-sm dm-btn-danger" onclick={acceptHostKey}>{m.backups_accept_new_host_key()}</button>{/if}
      </div>
    {/if}

    <StatBar
      items={[
        {
          label: m.backups_connection(),
          value: target.status === 'connected' ? m.backups_connected() : target.status === 'error' ? m.common_failed() : m.backups_not_tested(),
          note: m.backups_tested_ago({ ago: fmtAgo(target.last_checked_at) }),
          tone: target.status === 'error' ? 'bad' : ''
        },
        { label: m.backups_used(), value: target.total_bytes > 0 ? fmtBytes(target.used_bytes) : '—', note: usedPct != null ? m.backups_pct_of({ pct: fmtNumber(usedPct, { minimumFractionDigits: 1, maximumFractionDigits: 1 }), total: fmtBytes(target.total_bytes) }) : m.backups_size_unknown(), tone: usedPct != null && usedPct > 80 ? 'warn' : '' },
        { label: m.backups_free(), value: target.total_bytes > 0 ? fmtBytes(target.free_bytes) : '—' },
        { label: m.backups_tab_jobs(), value: jobsHere.length, note: m.backups_writing_here() }
      ]}
    />

    <div class="grid">
      <section>
        <h2 class="dm-h2">{m.backups_jobs_writing_here()}</h2>
        <div class="dm-tbl">
          {#each jobsHere as j (j.id)}
            <a class="dm-tr dm-click row" style="grid-template-columns: 14px minmax(0, 1fr) minmax(0, 1fr)" href="/backups/jobs/{j.id}">
              <span><span class="dm-dot {j.enabled ? 'ok' : ''}"></span></span>
              <span class="dm-nm"><span class="dm-t">{j.name}</span>{#if !j.enabled}<small>{m.backups_job_disabled()}</small>{/if}</span>
              <span class="dm-dim dm-trunc">{cronHuman(j.schedule)}</span>
            </a>
          {:else}
            <div class="dm-empty">{m.backups_no_job_writes_here()}</div>
          {/each}
        </div>
      </section>
      <section>
        <h2 class="dm-h2">{m.backups_configuration()}</h2>
        {#if cfgEntries.length === 0}
          <p class="dm-sub">{m.backups_no_configuration()}</p>
        {:else}
          <dl class="dm-kv">
            {#each cfgEntries as [k, v] (k)}
              <dt>{LABELS[k]?.() ?? k}</dt>
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
