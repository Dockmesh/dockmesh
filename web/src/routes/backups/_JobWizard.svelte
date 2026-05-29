<script lang="ts">
  // 4-step backup-job wizard. Step 1 picks Source, step 2 the Schedule,
  // step 3 the Target, step 4 retention + hooks. Mockup reference:
  // `Dockmesh Wizard (6)/backup-wizards.jsx::JobWizard`. Owns its
  // form state internally — the parent passes `editing` (or null) and
  // a `save` callback, the wizard packages the final BackupJobInput.
  import { api, ApiError, type BackupJob, type BackupJobInput, type BackupSource, type BackupHook, type BackupTarget } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Eyebrow } from '$lib/components/editorial';
  import WizardShell from './_WizardShell.svelte';
  import {
    LayoutDashboard, Layers, HardDrive, Lock, Clock, Database,
    RefreshCw, Check
  } from 'lucide-svelte';

  interface Props {
    open: boolean;
    editing: BackupJob | null;
    targets: BackupTarget[];
    onclose: () => void;
    onsave: (payload: BackupJobInput, isEdit: boolean, originalId?: number) => Promise<void>;
  }
  let { open, editing, targets, onclose, onsave }: Props = $props();

  const STEPS = ['Source', 'Schedule', 'Target', 'Retention'];
  let step = $state(0);
  let busy = $state(false);

  // ─── Form state
  let name = $state('new-backup-job');
  let sourceKind = $state<'system' | 'stack' | 'volume'>('system');
  let sourceRef = $state('');

  // Schedule
  type Freq = 'hourly' | 'every-6h' | 'daily' | 'weekly' | 'cron';
  let freq = $state<Freq>('daily');
  let hour = $state(3);
  let minute = $state(0);
  let weekday = $state(0);
  let cronExpr = $state('0 3 * * *');

  // Target
  let targetType = $state<'local' | 's3' | 'sftp' | 'smb' | 'webdav'>('local');
  let targetConfig = $state<Record<string, any>>({ path: './data/backups' });
  let encrypt = $state(true);
  // Selected pre-configured target (-1 = inline / build-on-the-fly).
  let preTargetId = $state<number>(-1);

  // Retention + hooks
  let keepN = $state(14);
  let keepDays = $state(30);
  const HOOK_PRESETS = [
    { id: 'pg',     label: 'PostgreSQL dump',  container: 'postgres', cmd: 'pg_dumpall -U postgres -f /tmp/dump.sql' },
    { id: 'mysql',  label: 'MySQL dump',       container: 'mysql',    cmd: 'mysqldump -u root --all-databases > /tmp/dump.sql' },
    { id: 'maria',  label: 'MariaDB dump',     container: 'mariadb',  cmd: 'mariadb-dump -u root --all-databases > /tmp/dump.sql' },
    { id: 'redis',  label: 'Redis save',       container: 'redis',    cmd: 'redis-cli BGSAVE' },
    { id: 'mongo',  label: 'MongoDB dump',     container: 'mongo',    cmd: 'mongodump --out /tmp/dump' }
  ];
  let pickedHooks = $state<Set<string>>(new Set());

  // ─── Available resources for the source picker (loaded lazily on
  // step 0 so the wizard opens fast even on a fleet with thousands of
  // resources).
  let availableStacks = $state<string[]>([]);
  let availableVolumes = $state<string[]>([]);
  let resourcesLoaded = $state(false);
  async function loadResources() {
    if (resourcesLoaded) return;
    try {
      const [stks, vols] = await Promise.all([
        api.stacks.list().catch(() => []),
        api.volumes.list('local').catch(() => [])
      ]);
      availableStacks = (Array.isArray(stks) ? stks : []).map((s: any) => s.name).filter(Boolean).sort();
      availableVolumes = (Array.isArray(vols) ? vols : []).map((v: any) => v.Name).filter(Boolean).sort();
      resourcesLoaded = true;
    } catch { /* ignore */ }
  }

  // ─── Initialise from `editing` whenever the wizard is opened. We use
  // an explicit "did-init" guard so toggling the open prop while a draft
  // is dirty doesn't blow it away.
  let initialised = $state(false);
  $effect(() => {
    if (!open) { initialised = false; return; }
    if (initialised) return;
    initialised = true;
    step = 0;
    if (editing) {
      name = editing.name;
      const src = editing.sources[0];
      if (!src) { sourceKind = 'system'; sourceRef = ''; }
      else if ((src.type as string) === 'system') { sourceKind = 'system'; sourceRef = ''; }
      else { sourceKind = src.type as 'stack' | 'volume'; sourceRef = src.name; }
      cronExpr = editing.schedule;
      const m = parseCron(editing.schedule);
      freq = m.freq;
      hour = m.hour;
      minute = m.minute;
      weekday = m.weekday;
      targetType = editing.target_type as typeof targetType;
      targetConfig = { ...(editing.target_config ?? {}) };
      encrypt = editing.encrypt;
      keepN = editing.retention_count;
      keepDays = editing.retention_days;
      pickedHooks = new Set();
      for (const h of editing.pre_hooks ?? []) {
        const cmdStr = h.cmd.join(' ');
        const preset = HOOK_PRESETS.find((p) => p.cmd === cmdStr || (h.container.includes(p.container) && cmdStr.includes(p.cmd.split(' ')[0])));
        if (preset) pickedHooks.add(preset.id);
      }
    } else {
      name = 'new-backup-job';
      sourceKind = 'system';
      sourceRef = '';
      freq = 'daily';
      hour = 3;
      minute = 0;
      weekday = 0;
      cronExpr = '0 3 * * *';
      targetType = 'local';
      targetConfig = { path: './data/backups' };
      encrypt = true;
      keepN = 14;
      keepDays = 30;
      pickedHooks = new Set();
      preTargetId = targets[0]?.id ?? -1;
    }
    loadResources();
  });

  function parseCron(c: string): { freq: Freq; hour: number; minute: number; weekday: number } {
    // Best-effort parse for common patterns we generated. Anything else
    // falls back to "cron" mode with the string preserved.
    const m = c.trim().match(/^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)$/);
    if (!m) return { freq: 'cron', hour: 3, minute: 0, weekday: 0 };
    const [, mn, hr, dom, , dow] = m;
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
    return sourceRef.trim().length > 0;
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
      targetConfig = { path: './data/backups' };
    } else {
      const t = targets.find((x) => x.id === id);
      if (t) {
        targetType = t.type as typeof targetType;
        targetConfig = { ...(t.config ?? {}) };
      }
    }
  }

  function nextFires(cron: string): string {
    // Cheap human preview — we don't ship a full cron evaluator. The
    // backend has the real schedule; this is hint-only.
    if (cron === '0 3 * * *') return 'tomorrow 03:00, +1d 03:00, +2d 03:00';
    if (cron === '0 0 * * *') return 'tomorrow 00:00, +1d 00:00, +2d 00:00';
    if (cron === '0 */6 * * *') return 'every 6 hours from the next 0 / 6 / 12 / 18 boundary';
    if (cron.endsWith(' * * 0')) return 'next Sunday';
    return 'see the cron expression — check after save';
  }

  function targetTypeMeta(type: string): { label: string; glyph: string; blurb: string } {
    if (type === 's3') return { label: 'S3 / object store', glyph: 'S3', blurb: 'AWS S3, MinIO, Wasabi, Backblaze B2, …' };
    if (type === 'sftp') return { label: 'SFTP over SSH', glyph: 'FTP', blurb: 'Any SSH server with chrooted user.' };
    if (type === 'smb') return { label: 'SMB / NAS share', glyph: 'NAS', blurb: 'Synology, QNAP, TrueNAS, Windows shares.' };
    if (type === 'webdav') return { label: 'WebDAV', glyph: 'DAV', blurb: 'Nextcloud, ownCloud, generic WebDAV.' };
    return { label: 'Local directory', glyph: 'LOC', blurb: 'On-host path. Survives crashes but not fires.' };
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
      const preHooks: BackupHook[] = [];
      for (const id of pickedHooks) {
        const p = HOOK_PRESETS.find((x) => x.id === id);
        if (p) preHooks.push({ container: p.container, cmd: p.cmd.split(/\s+/) });
      }
      const payload: BackupJobInput = {
        name: name.trim(),
        target_type: targetType,
        target_config: targetConfig,
        sources,
        schedule: cronExpr,
        retention_count: keepN,
        retention_days: keepDays,
        encrypt,
        pre_hooks: preHooks,
        post_hooks: [],
        enabled: editing?.enabled ?? true
      };
      await onsave(payload, !!editing, editing?.id);
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : (err as Error).message);
    } finally {
      busy = false;
    }
  }

  // Derived helpers for the summary line on step 3.
  const sourceSummary = $derived.by(() => {
    if (sourceKind === 'system') return 'whole dockmesh (DB + stacks + data)';
    return `${sourceKind} · ${sourceRef || '—'}`;
  });
  const scheduleSummary = $derived.by(() => {
    if (freq === 'cron') return cronExpr;
    if (freq === 'hourly') return `every hour at minute ${String(minute).padStart(2, '0')}`;
    if (freq === 'every-6h') return 'every 6 hours';
    if (freq === 'daily') return `daily at ${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
    if (freq === 'weekly') {
      const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
      return `weekly · ${days[weekday]} at ${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
    }
    return cronExpr;
  });
  const targetSummary = $derived.by(() => {
    if (preTargetId !== -1) {
      const t = targets.find((x) => x.id === preTargetId);
      if (t) return t.name;
    }
    const m = targetTypeMeta(targetType);
    return `${m.label} (inline)`;
  });
</script>

<WizardShell
  {open}
  eyebrow={editing ? 'Backups · edit job' : 'Backups · new job'}
  title={editing ? 'Edit backup job' : 'Schedule a new backup.'}
  subtitle="A job picks one source, runs on a schedule, writes to one target."
  steps={STEPS}
  stepIdx={step}
  {busy}
  finishLabel={editing ? 'Save changes' : 'Create job'}
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
        <label class="wiz-field-label" for="job-name">Job name</label>
        <span class="wiz-field-hint">Lowercase, dashes ok. Used in logs and target paths.</span>
        <input id="job-name" class="wiz-input wiz-input-mono" bind:value={name} />
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label">Source</label>
        <span class="wiz-field-hint">What to back up. Each kind dumps differently.</span>
        <div class="wiz-radio-grid">
          {#each [
            { id: 'system', label: 'Whole dockmesh', meta: 'DB · stacks/ · data/', icon: LayoutDashboard },
            { id: 'stack',  label: 'Single stack',   meta: 'compose + volumes',    icon: Layers },
            { id: 'volume', label: 'Single volume',  meta: 'tar of volume mount',  icon: HardDrive }
          ] as opt (opt.id)}
            {@const Icn = opt.icon}
            <button type="button" class="wiz-radio-card" class:active={sourceKind === opt.id} onclick={() => (sourceKind = opt.id as typeof sourceKind)}>
              <Icn size={14} strokeWidth={1.5} />
              <span class="wiz-radio-card-label">{opt.label}</span>
              <span class="wiz-radio-card-meta font-mono">{opt.meta}</span>
            </button>
          {/each}
        </div>
      </div>

      {#if sourceKind === 'stack'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="src-stack">Pick a stack</label>
          <select id="src-stack" class="wiz-input wiz-input-mono" bind:value={sourceRef}>
            <option value="" disabled>— pick one —</option>
            {#each availableStacks as s (s)}
              <option value={s}>{s}</option>
            {/each}
          </select>
        </div>
      {:else if sourceKind === 'volume'}
        <div class="wiz-field">
          <label class="wiz-field-label" for="src-vol">Pick a volume</label>
          <select id="src-vol" class="wiz-input wiz-input-mono" bind:value={sourceRef}>
            <option value="" disabled>— pick one —</option>
            {#each availableVolumes as v (v)}
              <option value={v}>{v}</option>
            {/each}
          </select>
        </div>
      {/if}
    </div>
  {/if}

  <!-- ============================================================== -->
  <!--  Step 1 — Schedule                                              -->
  <!-- ============================================================== -->
  {#if step === 1}
    <div class="wiz-stack">
      <div class="wiz-field">
        <label class="wiz-field-label">Frequency</label>
        <div class="wiz-segctrl">
          {#each [
            ['hourly', 'hourly'],
            ['every-6h', 'every 6h'],
            ['daily', 'daily'],
            ['weekly', 'weekly'],
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
          <label class="wiz-field-label" for="sch-min">At minute</label>
          <span class="wiz-field-hint">Within each hour. 0 = on the hour.</span>
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
            <label class="wiz-field-label">Weekday</label>
            <div class="wiz-segctrl">
              {#each ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as d, i (d)}
                <button type="button" class="wiz-segctrl-btn" class:active={weekday === i} onclick={() => (weekday = i)}>{d}</button>
              {/each}
            </div>
          </div>
        {/if}
        <div class="wiz-field">
          <label class="wiz-field-label" for="sch-time">At time</label>
          <span class="wiz-field-hint">Local server time. Backups run a few minutes earlier than typical traffic peak.</span>
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
          <label class="wiz-field-label" for="sch-cron">Cron expression</label>
          <span class="wiz-field-hint">Standard 5-field cron. Backend validates on save.</span>
          <input id="sch-cron" class="wiz-input wiz-input-mono" bind:value={cronExpr} placeholder="0 3 * * *" />
        </div>
      {/if}

      <div class="wiz-info-box">
        <Clock size={12} strokeWidth={1.5} class="wiz-info-icon" />
        <span>
          Will run <em class="ed-accent">{scheduleSummary}</em>.
          Cron: <span class="font-mono">{cronExpr}</span>.
          Next: <span class="font-mono">{nextFires(cronExpr)}</span>.
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
        <label class="wiz-field-label">Write to target</label>
        <span class="wiz-field-hint">Pick a pre-configured target — or build one inline below.</span>
        <div class="wiz-target-pick">
          {#each targets as t (t.id)}
            {@const m = targetTypeMeta(t.type)}
            {@const usedPct = t.total_bytes > 0 ? (t.used_bytes / t.total_bytes) * 100 : 0}
            <button type="button" class="wiz-target-pick-row" class:active={preTargetId === t.id} onclick={() => selectPreTarget(t.id)}>
              <span class="wiz-target-glyph">{m.glyph}</span>
              <span class="wiz-target-pick-text">
                <span class="font-mono wiz-target-pick-name">{t.name}</span>
                <span class="font-mono wiz-target-pick-meta">{m.label}</span>
              </span>
              {#if t.total_bytes > 0}
                <span class="wiz-target-pick-fill">
                  <span class="wiz-target-pick-bar"><span class="wiz-target-pick-bar-fill" style="width: {usedPct}%"></span></span>
                  <span class="font-mono wiz-target-pick-free">
                    {(t.free_bytes / 1024 / 1024 / 1024).toFixed(1)} GB free
                  </span>
                </span>
              {/if}
            </button>
          {/each}
          <button type="button" class="wiz-target-pick-row wiz-target-pick-inline" class:active={preTargetId === -1} onclick={() => selectPreTarget(-1)}>
            <span class="wiz-target-glyph">+</span>
            <span class="wiz-target-pick-text">
              <span class="wiz-target-pick-name">Inline target</span>
              <span class="font-mono wiz-target-pick-meta">build below — won't be reusable</span>
            </span>
          </button>
        </div>
      </div>

      {#if preTargetId === -1}
        <div class="wiz-field">
          <label class="wiz-field-label">Inline target type</label>
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
            <label class="wiz-field-label" for="t-path">Path</label>
            <span class="wiz-field-hint">Absolute path, must be writable by dockmesh.</span>
            <input id="t-path" class="wiz-input wiz-input-mono" placeholder="./data/backups" value={targetConfig.path ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, path: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        {:else if targetType === 'sftp'}
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-host">Host</label>
              <input id="t-host" class="wiz-input wiz-input-mono" value={targetConfig.host ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, host: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-port">Port</label>
              <input id="t-port" class="wiz-input wiz-input-mono" placeholder="22" value={targetConfig.port ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, port: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-user">Username</label>
              <input id="t-user" class="wiz-input wiz-input-mono" value={targetConfig.username ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, username: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-pass">Password</label>
              <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={targetConfig.password ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, password: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-rpath">Remote path</label>
            <input id="t-rpath" class="wiz-input wiz-input-mono" placeholder="/backups" value={targetConfig.path ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, path: (e.currentTarget as HTMLInputElement).value })} />
          </div>
        {:else if targetType === 's3'}
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-ep">Endpoint</label>
              <input id="t-ep" class="wiz-input wiz-input-mono" placeholder="s3.amazonaws.com" value={targetConfig.endpoint ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, endpoint: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-region">Region</label>
              <input id="t-region" class="wiz-input wiz-input-mono" placeholder="us-east-1" value={targetConfig.region ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, region: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-bucket">Bucket</label>
            <input id="t-bucket" class="wiz-input wiz-input-mono" value={targetConfig.bucket ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, bucket: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-ak">Access key</label>
              <input id="t-ak" class="wiz-input wiz-input-mono" value={targetConfig.access_key ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, access_key: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-sk">Secret key</label>
              <input id="t-sk" class="wiz-input wiz-input-mono" type="password" value={targetConfig.secret_key ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, secret_key: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
        {:else if targetType === 'smb'}
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-host">Server</label>
              <input id="t-host" class="wiz-input wiz-input-mono" value={targetConfig.host ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, host: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-share">Share</label>
              <input id="t-share" class="wiz-input wiz-input-mono" value={targetConfig.share ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, share: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-user">Username</label>
              <input id="t-user" class="wiz-input wiz-input-mono" value={targetConfig.username ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, username: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-pass">Password</label>
              <input id="t-pass" class="wiz-input wiz-input-mono" type="password" value={targetConfig.password ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, password: (e.currentTarget as HTMLInputElement).value })} />
            </div>
          </div>
        {:else if targetType === 'webdav'}
          <div class="wiz-field">
            <label class="wiz-field-label" for="t-url">WebDAV URL</label>
            <input id="t-url" class="wiz-input wiz-input-mono" placeholder="https://nextcloud.example.com/remote.php/dav/files/user/" value={targetConfig.url ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, url: (e.currentTarget as HTMLInputElement).value })} />
          </div>
          <div class="wiz-grid-2">
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-user">Username</label>
              <input id="t-user" class="wiz-input wiz-input-mono" value={targetConfig.username ?? ''} oninput={(e) => (targetConfig = { ...targetConfig, username: (e.currentTarget as HTMLInputElement).value })} />
            </div>
            <div class="wiz-field">
              <label class="wiz-field-label" for="t-pass">App password</label>
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
            <span class="wiz-toggle-label">{encrypt ? 'Encrypt at rest with age' : 'No encryption (not recommended)'}</span>
            <span class="wiz-toggle-hint">Recipient key is configured globally in Settings → Backups.</span>
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
        <label class="wiz-field-label" for="ret-n">Keep at least N runs</label>
        <span class="wiz-field-hint">The newest N runs are always retained, even if older than max age.</span>
        <div class="wiz-slider">
          <input id="ret-n" type="range" min="1" max="60" bind:value={keepN} />
          <span class="wiz-slider-val font-mono">{keepN} runs</span>
        </div>
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label" for="ret-d">Max age (days)</label>
        <span class="wiz-field-hint">Older runs are deleted after a successful new run. 0 = no limit.</span>
        <div class="wiz-slider">
          <input id="ret-d" type="range" min="0" max="365" bind:value={keepDays} />
          <span class="wiz-slider-val font-mono">{keepDays === 0 ? 'no limit' : keepDays + 'd'}</span>
        </div>
      </div>

      <div class="wiz-field">
        <label class="wiz-field-label">Pre-backup hooks</label>
        <span class="wiz-field-hint">Run inside source containers before snapshot. Pick presets that match your stack.</span>
        <div class="wiz-hook-grid">
          {#each HOOK_PRESETS as h (h.id)}
            {@const checked = pickedHooks.has(h.id)}
            <label class="wiz-hook-card" class:active={checked}>
              <input
                type="checkbox"
                {checked}
                onchange={(e) => {
                  const next = new Set(pickedHooks);
                  if ((e.currentTarget as HTMLInputElement).checked) next.add(h.id); else next.delete(h.id);
                  pickedHooks = next;
                }}
              />
              <span class="wiz-hook-text">
                <span class="wiz-hook-label">{h.label}</span>
                <span class="font-mono wiz-hook-cmd">{h.cmd}</span>
              </span>
            </label>
          {/each}
        </div>
      </div>

      <div class="wiz-info-box wiz-info-success">
        <Check size={12} strokeWidth={1.8} class="wiz-info-icon" />
        <span>
          Job <em class="ed-accent">{name || 'unnamed'}</em> will back up
          <em>{sourceSummary}</em> to <em class="ed-accent">{targetSummary}</em>,
          retaining {keepN} runs / {keepDays === 0 ? 'no age limit' : `${keepDays} days`}.
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
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
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
  .wiz-radio-card-meta { font-size: 10.5px; color: var(--fg-subtle); }

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
    font-family: var(--font-mono);
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
    font-family: var(--font-mono);
    font-size: 10.5px;
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
  .wiz-target-pick-meta { font-size: 10.5px; color: var(--fg-subtle); }
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
    font-size: 10px;
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

  /* Hook picker grid */
  .wiz-hook-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 8px;
  }
  .wiz-hook-card {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-elevated);
    cursor: pointer;
    transition: border-color 120ms, background 120ms;
  }
  .wiz-hook-card:hover { border-color: var(--border-strong); }
  .wiz-hook-card.active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 6%, transparent);
  }
  .wiz-hook-card input { accent-color: var(--accent); margin-top: 3px; flex-shrink: 0; }
  .wiz-hook-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .wiz-hook-label { font-size: 12.5px; color: var(--fg); }
  .wiz-hook-cmd {
    font-size: 10.5px;
    color: var(--fg-subtle);
    word-break: break-all;
    line-height: 1.4;
  }
</style>
