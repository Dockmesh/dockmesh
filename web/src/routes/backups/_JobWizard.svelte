<script lang="ts">
  // 4-step backup-job wizard. Step 1 picks Source, step 2 the Schedule,
  // step 3 the Target, step 4 retention + hooks. Mockup reference:
  // `Dockmesh Wizard (6)/backup-wizards.jsx::JobWizard`. Owns its
  // form state internally — the parent passes `editing` (or null) and
  // a `save` callback, the wizard packages the final BackupJobInput.
  import { api, ApiError, type BackupJob, type BackupJobInput, type BackupSource, type BackupTarget, type BackupHost, type StackListEntry } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { fmtBytes } from '$lib/format';
  import { HOOK_PRESETS, hookToRow, inlineTargetConfig, rowsToHooks, type HookRow } from '$lib/backupHooks';
  import WizardShell from './_WizardShell.svelte';
  import {
    LayoutDashboard, Layers, HardDrive, Lock, Clock,
    Check
  } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  interface Props {
    open: boolean;
    editing: BackupJob | null;
    targets: BackupTarget[];
    onclose: () => void;
    onsave: (payload: BackupJobInput, isEdit: boolean, originalId?: number) => Promise<void>;
  }
  let { open, editing, targets, onclose, onsave }: Props = $props();

  const STEPS = [m.common_source(), m.backups_col_schedule(), m.common_target(), m.backups_retention()];
  const WEEKDAYS = [m.backups_wd_sun(), m.backups_wd_mon(), m.backups_wd_tue(), m.backups_wd_wed(), m.backups_wd_thu(), m.backups_wd_fri(), m.backups_wd_sat()];
  let step = $state(0);
  let busy = $state(false);

  // ─── Form state
  let name = $state('new-backup-job');
  let sourceKind = $state<'system' | 'stack' | 'volume'>('system');
  let sourceRef = $state('');
  // Host of a volume job. Stack jobs run on the stack's deployment host,
  // system jobs on the server.
  let hostId = $state('local');

  // Schedule
  type Freq = 'hourly' | 'every-6h' | 'daily' | 'weekly' | 'cron';
  let freq = $state<Freq>('daily');
  let hour = $state(3);
  let minute = $state(0);
  let weekday = $state(0);
  let cronExpr = $state('0 3 * * *');

  // Target
  let targetType = $state<'local' | 's3' | 'sftp' | 'smb' | 'webdav'>('local');
  let targetConfig = $state<Record<string, any>>({ path: '' });
  let defaultPath = $state('');
  $effect(() => {
    if (!open) return;
    void api.backups.localPath().then((p) => {
      defaultPath = p;
      if (!editing && targetType === 'local' && !targetConfig.path) targetConfig = { ...targetConfig, path: p };
    });
  });
  let encrypt = $state(true);
  // Selected pre-configured target (-1 = inline / build-on-the-fly).
  let preTargetId = $state<number>(-1);

  // Retention + hooks
  let keepN = $state(14);
  let keepDays = $state(30);
  let preHooks = $state<HookRow[]>([]);
  let postHooks = $state<HookRow[]>([]);

  // Container names on the job's host for the hook pickers, narrowed to
  // the picked stack or the containers that mount the picked volume.
  let hookContainers = $state<string[]>([]);
  let hookContainersFor = $state('');
  $effect(() => {
    if (!open || step !== 3 || sourceKind === 'system' || !jobHost) return;
    const key = `${jobHost}|${sourceKind}|${sourceRef}`;
    if (hookContainersFor === key) return;
    hookContainersFor = key;
    const kind = sourceKind;
    const ref = sourceRef;
    api.containers.list(true, jobHost)
      .then((cs) => {
        if (hookContainersFor !== key) return;
        const list = Array.isArray(cs) ? cs : [];
        hookContainers = list
          .filter((c: any) => kind === 'stack'
            ? c.Labels?.['com.docker.compose.project'] === ref
            : (c.Mounts ?? []).some((mt: any) => mt.Type === 'volume' && mt.Name === ref))
          .map((c: any) => String(c.Names?.[0] ?? '').replace(/^\//, ''))
          .filter(Boolean)
          .sort();
      })
      .catch(() => {
        if (hookContainersFor === key) hookContainers = [];
      });
  });

  const HOOK_SECTIONS = [
    { kind: 'pre' as const, label: m.backups_jwiz_pre_hooks(), hint: m.backups_jwiz_pre_hooks_hint() },
    { kind: 'post' as const, label: m.backups_jwiz_post_hooks(), hint: m.backups_jwiz_post_hooks_hint() }
  ];

  function setHooks(kind: 'pre' | 'post', rows: HookRow[]) {
    if (kind === 'pre') preHooks = rows;
    else postHooks = rows;
  }

  function defaultHookContainer(): string {
    return hookContainers.length === 1 ? hookContainers[0] : '';
  }

  function addPreset(id: string) {
    const p = HOOK_PRESETS.find((x) => x.id === id);
    if (!p) return;
    preHooks = [...preHooks, { container: defaultHookContainer(), command: p.command }];
  }

  // ─── Available resources for the source picker (loaded lazily on
  // step 0 so the wizard opens fast even on a fleet with thousands of
  // resources).
  let availableStacks = $state<StackListEntry[]>([]);
  let backupHosts = $state<BackupHost[]>([]);
  let resourcesLoaded = $state(false);
  let timezone = $state('');

  async function loadResources() {
    if (resourcesLoaded) return;
    api.backups.schedule().then((s) => (timezone = s.timezone)).catch(() => {});
    const [stks, hs] = await Promise.all([
      api.stacks.list().catch(() => [] as StackListEntry[]),
      api.backups.hosts().catch(() => [] as BackupHost[])
    ]);
    availableStacks = (Array.isArray(stks) ? stks : []).filter((s) => s.name).sort((a, b) => a.name.localeCompare(b.name));
    backupHosts = Array.isArray(hs) ? hs : [];
    resourcesLoaded = true;
    if (!editing && !hostId && hostChoices.length === 1) hostId = hostChoices[0].id;
  }

  // Hosts the caller may put this job on. An edited job keeps its host in
  // the list even when it isn't offered (e.g. after an agent was removed).
  const hostChoices = $derived.by(() => {
    const list = backupHosts.filter((h) => (editing ? h.can_update : h.can_create));
    const cur = editing?.host_id || 'local';
    if (editing && !list.some((h) => h.id === cur)) {
      list.unshift({ id: cur, name: cur, kind: cur === 'local' ? 'local' : 'agent', status: 'unknown', can_create: false, can_update: false });
    }
    return list;
  });

  function hostName(id: string): string {
    return backupHosts.find((h) => h.id === id)?.name ?? (id === 'local' ? m.common_local() : id);
  }

  function hostOffered(id: string): boolean {
    return hostChoices.some((h) => h.id === id);
  }

  // Volumes of the picked host, loaded when a volume job picks a host.
  let availableVolumes = $state<string[]>([]);
  let volumesFor = $state('');
  let volumesError = $state('');
  let volumesLoading = $state(false);
  $effect(() => {
    if (!open || sourceKind !== 'volume' || !hostId || volumesFor === hostId) return;
    const h = hostId;
    volumesFor = h;
    volumesLoading = true;
    volumesError = '';
    availableVolumes = [];
    api.volumes.list(h)
      .then((vols) => {
        if (volumesFor !== h) return;
        availableVolumes = (Array.isArray(vols) ? vols : []).map((v: any) => v.Name).filter(Boolean).sort();
      })
      .catch((err) => {
        if (volumesFor !== h) return;
        volumesError = err instanceof ApiError ? err.message : (err as Error).message;
      })
      .finally(() => {
        if (volumesFor === h) volumesLoading = false;
      });
  });

  function pickHost(id: string) {
    if (id === hostId) return;
    hostId = id;
    sourceRef = '';
  }

  const pickedStack = $derived(sourceKind === 'stack' ? availableStacks.find((s) => s.name === sourceRef) : undefined);
  const stackHost = $derived(pickedStack?.deployment?.host_id || undefined);

  function stackUnavailable(s: StackListEntry): string {
    const h = s.deployment?.host_id;
    if (!h) return m.backups_jwiz_not_deployed();
    if (!hostOffered(h) && !(editing && editing.sources[0]?.name === s.name)) return m.backups_jwiz_no_backup_access({ host: hostName(h) });
    return '';
  }

  const jobHost = $derived(sourceKind === 'system' ? 'local' : sourceKind === 'stack' ? (stackHost ?? '') : hostId);

  // ─── Initialise from `editing` whenever the wizard is opened. We use
  // an explicit "did-init" guard so toggling the open prop while a draft
  // is dirty doesn't blow it away.
  let initialised = $state(false);
  $effect(() => {
    if (!open) { initialised = false; return; }
    if (initialised) return;
    initialised = true;
    step = 0;
    volumesFor = '';
    if (editing) {
      name = editing.name;
      hostId = editing.host_id || 'local';
      const src = editing.sources[0];
      if (!src) { sourceKind = 'system'; sourceRef = ''; }
      else if ((src.type as string) === 'system') { sourceKind = 'system'; sourceRef = ''; }
      else { sourceKind = src.type as 'stack' | 'volume'; sourceRef = src.name; }
      cronExpr = editing.schedule;
      const pc = parseCron(editing.schedule);
      freq = pc.freq;
      hour = pc.hour;
      minute = pc.minute;
      weekday = pc.weekday;
      targetType = editing.target_type as typeof targetType;
      targetConfig = { ...(editing.target_config ?? {}) };
      preTargetId = editing.target_id ?? -1;
      encrypt = editing.encrypt;
      keepN = editing.retention_count;
      keepDays = editing.retention_days;
      preHooks = (editing.pre_hooks ?? []).map(hookToRow);
      postHooks = (editing.post_hooks ?? []).map(hookToRow);
    } else {
      name = 'new-backup-job';
      sourceKind = 'system';
      sourceRef = '';
      hostId = '';
      freq = 'daily';
      hour = 3;
      minute = 0;
      weekday = 0;
      cronExpr = '0 3 * * *';
      targetType = 'local';
      targetConfig = { path: defaultPath };
      encrypt = true;
      keepN = 14;
      keepDays = 30;
      preHooks = [];
      postHooks = [];
      selectPreTarget(targets[0]?.id ?? -1);
    }
    loadResources();
  });

  function parseCron(c: string): { freq: Freq; hour: number; minute: number; weekday: number } {
    // Best-effort parse for common patterns we generated. Anything else
    // falls back to "cron" mode with the string preserved.
    const mm = c.trim().match(/^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)$/);
    if (!mm) return { freq: 'cron', hour: 3, minute: 0, weekday: 0 };
    const [, mn, hr, dom, , dow] = mm;
    if (mn.startsWith('*') || hr.startsWith('*/')) {
      if (hr.includes('*/6')) return { freq: 'every-6h', hour: 0, minute: 0, weekday: 0 };
      return { freq: 'hourly', hour: 0, minute: parseInt(mn) || 0, weekday: 0 };
    }
    if (dow !== '*') return { freq: 'weekly', hour: parseInt(hr) || 0, minute: parseInt(mn) || 0, weekday: parseInt(dow) || 0 };
    if (dom !== '*') return { freq: 'cron', hour: parseInt(hr) || 0, minute: parseInt(mn) || 0, weekday: 0 };
    return { freq: 'daily', hour: parseInt(hr) || 0, minute: parseInt(mn) || 0, weekday: 0 };
  }

  // Reactive cron rebuild when freq / hour / minute / weekday change
  // (only when not in raw-cron mode).
  $effect(() => {
    if (freq === 'cron') return;
    if (freq === 'hourly') cronExpr = `${minute} * * * *`;
    else if (freq === 'every-6h') cronExpr = `0 */6 * * *`;
    else if (freq === 'daily') cronExpr = `${minute} ${hour} * * *`;
    else if (freq === 'weekly') cronExpr = `${minute} ${hour} * * ${weekday}`;
  });

  // ─── Validation per step
  const step0Valid = $derived.by(() => {
    if (!name.trim()) return false;
    if (sourceKind === 'system') return true;
    if (sourceKind === 'stack') return !!sourceRef && !!stackHost;
    return !!hostId && sourceRef.trim().length > 0;
  });
  const step1Valid = $derived(cronExpr.trim().length > 0);
  const step2Valid = $derived(true); // Target step always passes; defaults are fine
  const step3Valid = $derived(keepN >= 1);
  const stepValidities = [step0Valid, step1Valid, step2Valid, step3Valid];
  const canContinue = $derived(stepValidities[step]);

  function selectPreTarget(id: number) {
    preTargetId = id;
    if (id === -1) {
      // back to inline-mode defaults
      targetType = 'local';
      targetConfig = { path: defaultPath };
    } else {
      const t = targets.find((x) => x.id === id);
      if (t) targetType = t.type as typeof targetType;
    }
  }

  function nextFires(cron: string): string {
    // Cheap human preview — we don't ship a full cron evaluator. The
    // backend has the real schedule; this is hint-only.
    if (cron === '0 3 * * *') return m.backups_jwiz_next_0300();
    if (cron === '0 0 * * *') return m.backups_jwiz_next_0000();
    if (cron === '0 */6 * * *') return m.backups_jwiz_next_6h();
    if (cron.endsWith(' * * 0')) return m.backups_jwiz_next_sunday();
    return m.backups_jwiz_next_unknown();
  }

  function targetTypeMeta(type: string): { label: string; glyph: string; blurb: string } {
    if (type === 's3') return { label: m.backups_ttype_s3_label(), glyph: 'S3', blurb: m.backups_jwiz_s3_blurb() };
    if (type === 'sftp') return { label: m.backups_ttype_sftp_label(), glyph: 'FTP', blurb: m.backups_jwiz_sftp_blurb() };
    if (type === 'smb') return { label: m.backups_ttype_smb_label(), glyph: 'NAS', blurb: m.backups_ttype_smb_blurb() };
    if (type === 'webdav') return { label: 'WebDAV', glyph: 'DAV', blurb: m.backups_jwiz_webdav_blurb() };
    return { label: m.backups_ttype_local_label(), glyph: 'LOC', blurb: m.backups_jwiz_local_blurb() };
  }

  // ─── Submit
  async function finish() {
    busy = true;
    try {
      const sources: BackupSource[] = [];
      if (sourceKind === 'system') {
        sources.push({ type: 'system' as any, name: '*' });
      } else {
        sources.push({ type: sourceKind, name: sourceRef.trim() });
      }
      const payload: BackupJobInput = {
        name: name.trim(),
        host_id: jobHost || undefined,
        target_id: preTargetId !== -1 ? preTargetId : undefined,
        target_type: targetType,
        target_config: preTargetId !== -1 ? targetConfig : inlineTargetConfig(targetType, targetConfig),
        sources,
        schedule: cronExpr,
        retention_count: keepN,
        retention_days: keepDays,
        encrypt,
        pre_hooks: rowsToHooks(preHooks),
        post_hooks: rowsToHooks(postHooks),
        enabled: editing?.enabled ?? true
      };
      await onsave(payload, !!editing, editing?.id);
    } catch (err) {
      toast.error(m.backups_save_failed(), err instanceof ApiError ? err.message : (err as Error).message);
    } finally {
      busy = false;
    }
  }

  // Derived helpers for the summary line on step 3.
  const sourceSummary = $derived.by(() => {
    if (sourceKind === 'system') return m.backups_jwiz_summary_system();
    return m.backups_jwiz_summary_source({ kind: sourceKind === 'stack' ? m.backups_jwiz_kind_stack() : m.backups_jwiz_kind_volume(), ref: sourceRef || '—', host: jobHost ? hostName(jobHost) : '—' });
  });
  const scheduleSummary = $derived.by(() => {
    if (freq === 'cron') return cronExpr;
    if (freq === 'hourly') return m.backups_jwiz_sched_hourly({ minute: String(minute).padStart(2, '0') });
    if (freq === 'every-6h') return m.backups_jwiz_sched_6h();
    if (freq === 'daily') return m.backups_jwiz_sched_daily({ time: `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}` });
    if (freq === 'weekly') {
      return m.backups_jwiz_sched_weekly({ day: WEEKDAYS[weekday], time: `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}` });
    }
    return cronExpr;
  });
  const targetSummary = $derived.by(() => {
    if (preTargetId !== -1) {
      const t = targets.find((x) => x.id === preTargetId);
      if (t) return t.name;
    }
    return m.backups_jwiz_target_new({ label: targetTypeMeta(targetType).label });
  });
</script>

<WizardShell
  {open}
  title={editing ? m.backups_jwiz_edit_title() : m.backups_jwiz_new_title()}
  subtitle={m.backups_jwiz_subtitle()}
  steps={STEPS}
  stepIdx={step}
  {busy}
  finishLabel={editing ? m.backups_save_changes() : m.backups_jwiz_create_job()}
  nextDisabled={!canContinue}
  {onclose}
  onstep={(i) => (step = i)}
  onback={() => (step = Math.max(0, step - 1))}
  onnext={() => (step = Math.min(STEPS.length - 1, step + 1))}
  onfinish={finish}
>
  <!-- ============================================================== -->
  <!--  Step 0 — Source                                                -->
  <!-- ============================================================== -->
  {#if step === 0}
    <div class="wiz-stack">
      <div class="wiz-field">
        <label class="wiz-field-label" for="job-name">{m.backups_jwiz_name_label()}</label>
        <span class="wiz-field-hint">{m.backups_jwiz_name_hint()}</span>
        <input id="job-name" class="wiz-input wiz-input-mono" bind:value={name} />
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label">{m.common_source()}</label>
        <span class="wiz-field-hint">{m.backups_jwiz_source_hint()}</span>
        <div class="wiz-radio-grid">
          {#each [
            { id: 'system', label: m.backups_jwiz_src_system(), meta: 'DB · stacks/ · data/', icon: LayoutDashboard },
            { id: 'stack',  label: m.backups_jwiz_src_stack(),  meta: m.backups_jwiz_src_stack_meta(),  icon: Layers },
            { id: 'volume', label: m.backups_jwiz_src_volume(), meta: m.backups_jwiz_src_volume_meta(), icon: HardDrive }
          ] as opt (opt.id)}
            {@const Icn = opt.icon}
            <button type="button" class="wiz-radio-card" class:active={sourceKind === opt.id} onclick={() => { if (sourceKind !== opt.id) { sourceKind = opt.id as typeof sourceKind; sourceRef = ''; } }}>
              <Icn size={14} strokeWidth={1.5} />
              <span class="wiz-radio-card-label">{opt.label}</span>
              <span class="wiz-radio-card-meta font-mono">{opt.meta}</span>
            </button>
          {/each}
        </div>
      </div>

      {#if sourceKind === 'system'}
        <div class="wiz-info-box">
          <HardDrive size={12} strokeWidth={1.5} class="wiz-info-icon" />
          <span>{m.backups_jwiz_system_info()}</span>
        </div>
      {:else if sourceKind === 'stack'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="src-stack">{m.backups_jwiz_pick_stack()}</label>
          <select id="src-stack" class="wiz-input wiz-input-mono" bind:value={sourceRef}>
            <option value="" disabled>{m.backups_jwiz_pick_one()}</option>
            {#each availableStacks as s (s.name)}
              {@const why = stackUnavailable(s)}
              <option value={s.name} disabled={!!why}>{s.name}{why ? ` (${why})` : ''}</option>
            {/each}
          </select>
        </div>
        {#if sourceRef}
          {#if stackHost}
            <div class="wiz-field">
              <span class="wiz-field-label">{m.common_host()}</span>
              <span class="wiz-static font-mono" data-testid="stack-host">{hostName(stackHost)}</span>
              <span class="wiz-field-hint">{m.backups_jwiz_stack_host_hint()}</span>
            </div>
          {:else}
            <div class="wiz-info-box wiz-info-warn">
              <Layers size={12} strokeWidth={1.5} class="wiz-info-icon" />
              <span>{m.backups_jwiz_stack_not_deployed_before()} <em>{sourceRef}</em> {m.backups_jwiz_stack_not_deployed_after()}</span>
            </div>
          {/if}
        {/if}
      {:else if sourceKind === 'volume'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="src-host">{m.common_host()}</label>
          <span class="wiz-field-hint">{m.backups_jwiz_volume_host_hint()}</span>
          <select id="src-host" class="wiz-input wiz-input-mono" value={hostId} onchange={(e) => pickHost((e.currentTarget as HTMLSelectElement).value)}>
            <option value="" disabled>{m.backups_jwiz_pick_host()}</option>
            {#each hostChoices as h (h.id)}
              <option value={h.id}>{h.name}{h.status !== 'online' ? ` (${h.status})` : ''}</option>
            {/each}
          </select>
          {#if resourcesLoaded && hostChoices.length === 0}
            <span class="wiz-field-hint">{m.backups_jwiz_no_hosts()}</span>
          {/if}
        </div>
        {#if hostId}
          <div class="wiz-field">
            <label class="wiz-field-label" for="src-vol">{m.backups_jwiz_pick_volume()}</label>
            {#if volumesError}
              <span class="wiz-field-hint">{m.backups_jwiz_volumes_error({ host: hostName(hostId), error: volumesError })}</span>
              <input id="src-vol" class="wiz-input wiz-input-mono" bind:value={sourceRef} placeholder={m.backups_jwiz_volume_placeholder()} />
            {:else}
              <select id="src-vol" class="wiz-input wiz-input-mono" bind:value={sourceRef} disabled={volumesLoading}>
                <option value="" disabled>{volumesLoading ? m.backups_jwiz_loading_lc() : availableVolumes.length ? m.backups_jwiz_pick_one() : m.backups_jwiz_no_volumes()}</option>
                {#if sourceRef && !volumesLoading && !availableVolumes.includes(sourceRef)}
                  <option value={sourceRef}>{m.backups_jwiz_not_found({ name: sourceRef })}</option>
                {/if}
                {#each availableVolumes as v (v)}
                  <option value={v}>{v}</option>
                {/each}
              </select>
            {/if}
          </div>
        {/if}
      {/if}
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  Step 1 — Schedule                                              -->
  <!-- ============================================================== -->
  {#if step === 1}
    <div class="wiz-stack">
      <div class="wiz-field">
        <label class="wiz-field-label">{m.backups_jwiz_frequency()}</label>
        <div class="wiz-segctrl">
          {#each [
            ['hourly', m.backups_jwiz_freq_hourly()],
            ['every-6h', m.backups_jwiz_freq_6h()],
            ['daily', m.backups_jwiz_freq_daily()],
            ['weekly', m.backups_jwiz_freq_weekly()],
            ['cron', 'cron']
          ] as [id, label] (id)}
            <button type="button" class="wiz-segctrl-btn" class:active={freq === id} onclick={() => (freq = id as Freq)}>
              {label}
            </button>
          {/each}
        </div>
      </div>

      {#if freq === 'hourly'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="sch-min">{m.backups_jwiz_at_minute()}</label>
          <span class="wiz-field-hint">{m.backups_jwiz_at_minute_hint()}</span>
          <input
            id="sch-min"
            type="number"
            class="wiz-input wiz-input-mono wiz-input-narrow"
            min="0"
            max="59"
            bind:value={minute}
          />
        </div>
      {/if}

      {#if freq === 'daily' || freq === 'weekly'}
        {#if freq === 'weekly'}
          <div class="wiz-field">
            <label class="wiz-field-label">{m.backups_jwiz_weekday()}</label>
            <div class="wiz-segctrl">
              {#each WEEKDAYS as d, i (i)}
                <button type="button" class="wiz-segctrl-btn" class:active={weekday === i} onclick={() => (weekday = i)}>{d}</button>
              {/each}
            </div>
          </div>
        {/if}
        <div class="wiz-field">
          <label class="wiz-field-label" for="sch-time">{m.backups_jwiz_at_time()}</label>
          <span class="wiz-field-hint">{m.backups_jwiz_at_time_hint()}</span>
          <input
            id="sch-time"
            type="time"
            class="wiz-input wiz-input-mono wiz-input-narrow"
            value={`${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`}
            onchange={(e) => {
              const parts = ((e.currentTarget as HTMLInputElement).value || '00:00').split(':');
              hour = parseInt(parts[0], 10) || 0;
              minute = parseInt(parts[1], 10) || 0;
            }}
          />
        </div>
      {/if}

      {#if freq === 'cron'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="sch-cron">{m.backups_jwiz_cron_label()}</label>
          <span class="wiz-field-hint">{m.backups_jwiz_cron_hint()}</span>
          <input id="sch-cron" class="wiz-input wiz-input-mono" bind:value={cronExpr} placeholder="0 3 * * *" />
        </div>
      {/if}

      <div class="wiz-info-box">
        <Clock size={12} strokeWidth={1.5} class="wiz-info-icon" />
        <span>
          {m.backups_jwiz_will_run()} <em class="ed-accent">{scheduleSummary}</em>.
          Cron: <span class="font-mono">{cronExpr}</span>.
          {m.backups_jwiz_next()} <span class="font-mono">{nextFires(cronExpr)}</span>.
          {#if timezone}{m.backups_jwiz_times_in()} <span class="font-mono">{timezone}</span>.{/if}
        </span>
      </div>
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  Step 2 — Target                                                -->
  <!-- ============================================================== -->
  {#if step === 2}
    <div class="wiz-stack">
      <div class="wiz-field">
        <label class="wiz-field-label">{m.backups_jwiz_write_to_target()}</label>
        <span class="wiz-field-hint">{m.backups_jwiz_write_to_target_hint()}</span>
        <div class="wiz-target-pick">
          {#each targets as t (t.id)}
            {@const tm = targetTypeMeta(t.type)}
            {@const usedPct = t.total_bytes > 0 ? (t.used_bytes / t.total_bytes) * 100 : 0}
            <button type="button" class="wiz-target-pick-row" class:active={preTargetId === t.id} onclick={() => selectPreTarget(t.id)}>
              <span class="wiz-target-glyph">{tm.glyph}</span>
              <span class="wiz-target-pick-text">
                <span class="font-mono wiz-target-pick-name">{t.name}</span>
                <span class="font-mono wiz-target-pick-meta">{tm.label}</span>
              </span>
              {#if t.total_bytes > 0}
                <span class="wiz-target-pick-fill">
                  <span class="wiz-target-pick-bar"><span class="wiz-target-pick-bar-fill" style="width: {usedPct}%"></span></span>
                  <span class="font-mono wiz-target-pick-free">
                    {m.backups_jwiz_free({ free: fmtBytes(t.free_bytes) })}
                  </span>
                </span>
              {/if}
            </button>
          {/each}
          <button type="button" class="wiz-target-pick-row wiz-target-pick-inline" class:active={preTargetId === -1} onclick={() => selectPreTarget(-1)}>
            <span class="wiz-target-glyph">+</span>
            <span class="wiz-target-pick-text">
              <span class="wiz-target-pick-name">{m.backups_new_target()}</span>
              <span class="font-mono wiz-target-pick-meta">{m.backups_jwiz_new_target_meta()}</span>
            </span>
          </button>
        </div>
      </div>

      {#if preTargetId === -1}
        <div class="wiz-field">
          <label class="wiz-field-label">{m.backups_twiz_type_label()}</label>
          <div class="wiz-segctrl">
            {#each ['local', 'sftp', 'smb', 'webdav', 's3'] as t (t)}
              <button type="button" class="wiz-segctrl-btn" class:active={targetType === t} onclick={() => (targetType = t as typeof targetType)}>
                {t}
              </button>
            {/each}
          </div>
        </div>

        {#if targetType === 'local'}
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-path">{m.common_path()}</label>
            <span class="wiz-field-hint">{m.backups_jwiz_local_path_hint()}</span>
            <input id="t-path" class="wiz-input wiz-input-mono" placeholder={defaultPath || '/srv/backups'} value={targetConfig.path ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, path: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        {:else if targetType === 'sftp'}
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-host">{m.common_host()}</label>
              <input id="t-host" class="wiz-input wiz-input-mono" value={targetConfig.host ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, host: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-port">{m.common_port()}</label>
              <input id="t-port" class="wiz-input wiz-input-mono" type="number" min="1" max="65535" placeholder="22" value={targetConfig.port ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, port: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-user">{m.backups_username()}</label>
              <input id="t-user" class="wiz-input wiz-input-mono" value={targetConfig.username ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, username: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-pass">{m.backups_cfg_password()}</label>
              <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={targetConfig.password ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, password: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-key">{m.backups_twiz_private_key_label()}</label>
            <span class="wiz-field-hint">{m.backups_twiz_private_key_hint()}{targetConfig.private_key_set && !targetConfig.private_key ? ` ${m.backups_twiz_private_key_saved()}` : ''}</span>
            <textarea id="t-key" class="wiz-input wiz-input-mono wiz-textarea" rows="4" spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" value={targetConfig.private_key ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, private_key: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-rpath">{m.backups_twiz_remote_path()}</label>
            <input id="t-rpath" class="wiz-input wiz-input-mono" placeholder="/backups" value={targetConfig.path ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, path: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        {:else if targetType === 's3'}
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-ep">{m.backups_cfg_endpoint()}</label>
              <input id="t-ep" class="wiz-input wiz-input-mono" placeholder="s3.amazonaws.com" value={targetConfig.endpoint ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, endpoint: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-region">{m.backups_cfg_region()}</label>
              <input id="t-region" class="wiz-input wiz-input-mono" placeholder="us-east-1" value={targetConfig.region ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, region: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-bucket">{m.backups_cfg_bucket()}</label>
            <input id="t-bucket" class="wiz-input wiz-input-mono" value={targetConfig.bucket ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, bucket: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-ak">{m.backups_cfg_access_key()}</label>
              <input id="t-ak" class="wiz-input wiz-input-mono" value={targetConfig.access_key ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, access_key: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-sk">{m.backups_cfg_secret_key()}</label>
              <input id="t-sk" class="wiz-input wiz-input-mono" type="password" value={targetConfig.secret_key ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, secret_key: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-prefix">{m.backups_twiz_prefix_label()}</label>
            <span class="wiz-field-hint">{m.backups_twiz_prefix_hint()}</span>
            <input id="t-prefix" class="wiz-input wiz-input-mono" placeholder="backups/dockmesh" value={targetConfig.prefix ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, prefix: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <label class="wiz-check">
            <input type="checkbox" checked={!!targetConfig.use_ssl} onchange={(e) => (targetConfig = { ...targetConfig, use_ssl: (e.currentTarget as HTMLInputElement).checked })} />
            <span>{m.backups_twiz_use_tls()}</span>
          </label>
        {:else if targetType === 'smb'}
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-host">{m.backups_cfg_server()}</label>
              <input id="t-host" class="wiz-input wiz-input-mono" value={targetConfig.host ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, host: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-share">{m.backups_cfg_share()}</label>
              <input id="t-share" class="wiz-input wiz-input-mono" value={targetConfig.share ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, share: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-user">{m.backups_username()}</label>
              <input id="t-user" class="wiz-input wiz-input-mono" value={targetConfig.username ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, username: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-pass">{m.backups_cfg_password()}</label>
              <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={targetConfig.password ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, password: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
        {:else if targetType === 'webdav'}
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-url">{m.backups_twiz_webdav_url()}</label>
            <input id="t-url" class="wiz-input wiz-input-mono" placeholder="https://nextcloud.example.com/remote.php/dav/files/user/" value={targetConfig.url ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, url: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-user">{m.backups_username()}</label>
              <input id="t-user" class="wiz-input wiz-input-mono" value={targetConfig.username ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, username: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-pass">{m.backups_twiz_app_password()}</label>
              <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={targetConfig.password ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, password: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
        {/if}
      {/if}

      <div class="wiz-field">
        <label class="wiz-toggle">
          <input type="checkbox" bind:checked={encrypt} />
          <span class="wiz-toggle-text">
            <Lock size={11} strokeWidth={1.5} class="wiz-toggle-icon" />
            <span class="wiz-toggle-label">{encrypt ? m.backups_jwiz_encrypt_on() : m.backups_jwiz_encrypt_off()}</span>
            <span class="wiz-toggle-hint">{m.backups_jwiz_encrypt_hint()}</span>
          </span>
        </label>
      </div>
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  Step 3 — Retention + hooks                                     -->
  <!-- ============================================================== -->
  {#if step === 3}
    <div class="wiz-stack">
      <div class="wiz-field">
        <label class="wiz-field-label" for="ret-n">{m.backups_jwiz_keep_n()}</label>
        <span class="wiz-field-hint">{m.backups_jwiz_keep_n_hint()}</span>
        <div class="wiz-slider">
          <input id="ret-n" type="range" min="1" max="60" bind:value={keepN} />
          <span class="wiz-slider-val font-mono">{m.backups_n_runs({ count: keepN })}</span>
        </div>
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label" for="ret-d">{m.backups_jwiz_max_age()}</label>
        <span class="wiz-field-hint">{m.backups_jwiz_max_age_hint()}</span>
        <div class="wiz-slider">
          <input id="ret-d" type="range" min="0" max="365" bind:value={keepDays} />
          <span class="wiz-slider-val font-mono">{keepDays === 0 ? m.backups_jwiz_no_limit() : m.backups_jwiz_days_short({ n: keepDays })}</span>
        </div>
      </div>

      {#if hookContainers.length}
        <datalist id="hook-containers">
          {#each hookContainers as c (c)}<option value={c}></option>{/each}
        </datalist>
      {/if}
      {#each HOOK_SECTIONS as sec (sec.kind)}
        {@const rows = sec.kind === 'pre' ? preHooks : postHooks}
        <div class="wiz-field">
          <span class="wiz-field-label">{sec.label}</span>
          <span class="wiz-field-hint">{sec.hint} {m.backups_jwiz_hooks_sh_before()} <code>sh -c</code> {m.backups_jwiz_hooks_sh_after()}</span>
          {#each rows as row, i (i)}
            <div class="wiz-hook-row" data-testid="hook-row-{sec.kind}">
              <input
                class="wiz-input wiz-input-mono wiz-hook-container"
                placeholder={m.backups_jwiz_hook_container_ph()}
                aria-label={m.common_container()}
                list="hook-containers"
                value={row.container}
                oninput={(e) => setHooks(sec.kind, rows.map((r, j) => (j === i ? { ...r, container: (e.currentTarget as HTMLInputElement).value } : r)))}
              />
              <input
                class="wiz-input wiz-input-mono wiz-hook-command"
                placeholder={m.backups_jwiz_hook_command_ph()}
                aria-label={m.backups_jwiz_hook_command()}
                value={row.command}
                oninput={(e) => setHooks(sec.kind, rows.map((r, j) => (j === i ? { ...r, command: (e.currentTarget as HTMLInputElement).value } : r)))}
              />
              <button type="button" class="wiz-hook-btn" aria-label={m.backups_jwiz_remove_hook()} onclick={() => setHooks(sec.kind, rows.filter((_, j) => j !== i))}>{m.common_remove()}</button>
            </div>
          {/each}
          <div class="wiz-hook-actions">
            <button type="button" class="wiz-hook-btn" onclick={() => setHooks(sec.kind, [...rows, { container: defaultHookContainer(), command: '' }])}>{m.backups_jwiz_add_command()}</button>
            {#if sec.kind === 'pre'}
              <span class="wiz-hook-presets-label">{m.backups_jwiz_presets()}</span>
              {#each HOOK_PRESETS as p (p.id)}
                <button type="button" class="wiz-hook-btn" title={p.command} onclick={() => addPreset(p.id)}>{p.label}</button>
              {/each}
            {/if}
          </div>
        </div>
      {/each}
      {#if preHooks.some((r) => r.command.includes('dockmesh-dump'))}
        <div class="wiz-info-box">
          <HardDrive size={12} strokeWidth={1.5} class="wiz-info-icon" />
          <span>{m.backups_jwiz_dump_info()}</span>
        </div>
      {/if}

      <div class="wiz-info-box wiz-info-success">
        <Check size={12} strokeWidth={1.8} class="wiz-info-icon" />
        <span>
          {m.backups_jwiz_summary_job()} <em class="ed-accent">{name || m.backups_jwiz_unnamed()}</em> {m.backups_jwiz_summary_will_back_up()}
          <em>{sourceSummary}</em> {m.backups_jwiz_summary_to()} <em class="ed-accent">{targetSummary}</em>,
          {m.backups_jwiz_summary_retaining({ n: keepN, age: keepDays === 0 ? m.backups_no_age_limit() : m.backups_jwiz_n_days({ n: keepDays }) })}.
        </span>
      </div>
    </div>
  {/if}
</WizardShell>

<style>
  .wiz-stack { display: flex; flex-direction: column; gap: 18px; }
  .wiz-grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }

  .wiz-field { display: flex; flex-direction: column; gap: 4px; }
  .wiz-field-label {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .wiz-field-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-bottom: 4px;
    line-height: 1.5;
  }
  .wiz-input {
    height: 32px;
    padding: 0 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-elevated);
    color: var(--fg);
    font-size: 13px;
    transition: border-color 120ms;
  }
  .wiz-input:focus {
    outline: none;
    border-color: var(--accent);
  }
  .wiz-input-mono { font-family: var(--font-mono); }
  .wiz-input-narrow { width: auto; min-width: 110px; max-width: 160px; align-self: flex-start; }
  /* Native time/number inputs render a tiny picker icon on Webkit —
     invert to match the dark theme. */
  .wiz-input::-webkit-calendar-picker-indicator { filter: invert(0.7); cursor: pointer; }

  /* Source-type radio cards */
  .wiz-radio-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 10px;
  }
  .wiz-radio-card {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    cursor: pointer;
    text-align: left;
    color: var(--fg-muted);
    transition: border-color 120ms, background 120ms, color 120ms;
  }
  .wiz-radio-card:hover { border-color: var(--border-strong); color: var(--fg); }
  .wiz-radio-card.active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 8%, transparent);
    color: var(--accent-fg);
  }
  .wiz-radio-card-label { font-size: 13px; color: var(--fg); }
  .wiz-radio-card.active .wiz-radio-card-label { color: var(--accent-fg); }
  .wiz-radio-card-meta { font-size: 11.5px; color: var(--fg-subtle); }

  /* Segmented control */
  .wiz-segctrl {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
    align-self: flex-start;
  }
  .wiz-segctrl-btn {
    padding: 6px 14px;
    border: 0;
    border-right: 1px solid var(--border);
    background: transparent;
    cursor: pointer;
    font-size: 11.5px;
    color: var(--fg-subtle);
    transition: color 120ms, background 120ms;
  }
  .wiz-segctrl-btn:last-child { border-right: 0; }
  .wiz-segctrl-btn:hover { color: var(--fg-muted); }
  .wiz-segctrl-btn.active {
    background: var(--bg-elevated);
    color: var(--fg);
  }

  /* Slider field */
  .wiz-slider {
    display: flex;
    align-items: center;
    gap: 14px;
  }
  .wiz-slider input[type="range"] {
    flex: 1;
    accent-color: var(--accent);
  }
  .wiz-slider-val {
    font-size: 13px;
    color: var(--fg);
    min-width: 80px;
    text-align: right;
  }

  /* Info-box (callout under schedule + step-3 summary) */
  .wiz-info-box {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    color: var(--fg-muted);
    font-size: 12px;
    line-height: 1.55;
  }
  :global(.wiz-info-icon) { color: var(--fg-muted); flex-shrink: 0; margin-top: 3px; }
  .wiz-info-box em { color: var(--fg); font-style: normal; }
  .wiz-info-success {
    border-color: color-mix(in srgb, var(--color-success-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 8%, transparent);
  }
  .wiz-info-success :global(.wiz-info-icon) { color: var(--color-success-400); }
  .wiz-info-warn {
    border-color: color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
  }
  .wiz-info-warn :global(.wiz-info-icon) { color: var(--color-warning-400); }
  .wiz-static { font-size: 13px; color: var(--fg); padding: 6px 0; }

  /* Target picker */
  .wiz-target-pick {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .wiz-target-pick-row {
    display: grid;
    grid-template-columns: 32px minmax(0, 1.2fr) minmax(0, 1fr) minmax(120px, 180px);
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    cursor: pointer;
    text-align: left;
    color: var(--fg);
    transition: border-color 120ms, background 120ms;
  }
  .wiz-target-pick-row:hover { border-color: var(--border-strong); }
  .wiz-target-pick-row.active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 6%, transparent);
  }
  .wiz-target-pick-inline {
    border-style: dashed;
    color: var(--fg-muted);
  }
  .wiz-target-glyph {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
  }
  .wiz-target-pick-text { display: flex; flex-direction: column; min-width: 0; }
  .wiz-target-pick-name {
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .wiz-target-pick-meta { font-size: 11.5px; color: var(--fg-subtle); }
  .wiz-target-pick-fill {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .wiz-target-pick-bar {
    height: 4px;
    background: var(--bg-elevated);
    border-radius: 2px;
    overflow: hidden;
    position: relative;
  }
  .wiz-target-pick-bar-fill {
    position: absolute;
    inset: 0;
    right: auto;
    background: var(--accent);
    border-radius: 2px;
  }
  .wiz-target-pick-free {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }

  /* Toggle (encryption + similar) */
  .wiz-toggle {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    cursor: pointer;
  }
  .wiz-toggle:hover { border-color: var(--border-strong); }
  .wiz-toggle input[type="checkbox"] {
    accent-color: var(--accent);
    margin-top: 2px;
    flex-shrink: 0;
  }
  .wiz-toggle-text {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .wiz-toggle-label {
    font-size: 12.5px;
    color: var(--fg);
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  :global(.wiz-toggle-icon) { color: var(--accent-fg); }
  .wiz-toggle-hint {
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.5;
  }

  /* Hook editor */
  .wiz-hook-row { display: grid; grid-template-columns: minmax(140px, 1fr) 3fr auto; gap: 8px; margin-bottom: 6px; }
  .wiz-hook-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .wiz-hook-presets-label { font-size: 11.5px; color: var(--fg-subtle); margin-left: 6px; }
  .wiz-hook-btn {
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-elevated);
    color: var(--fg);
    font-size: 12px;
    cursor: pointer;
  }
  .wiz-hook-btn:hover { border-color: var(--border-strong); }
  .wiz-textarea { height: auto; padding: 8px 12px; resize: vertical; }
  .wiz-check { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--fg-muted, inherit); }
</style>
