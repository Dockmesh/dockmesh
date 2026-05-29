<script lang="ts">
  // Audit Log — editorial rebuild based on `Dockmesh Wizard (6)/audit.jsx`.
  //
  // Layout in two zones (matches mockup):
  //  1. Top — chain-integrity strip (always shown) + 3-col config strip
  //     (retention / webhook / verify history).
  //  2. Main — filter bar (action-group pills + contextual refine + user
  //     filter + search + limit), then day-grouped entry list with
  //     expandable details JSON per row.
  //
  // Pills win over the legacy single-action dropdown because the
  // documented pain-point is that ~90% of entries are auth.login —
  // pills with live counts make the volume visible AND filterable in
  // one motion.
  import { api, ApiError, type AuditWebhookConfig, type AuditRetentionConfig, type AuditRetentionPreview, type AuditRetentionResult } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import {
    Activity, Check, X, Download, RefreshCw, Copy, ArrowUpRight, Plus, Search,
    Shield, ShieldCheck, ShieldAlert
  } from 'lucide-svelte';

  // ───────── Action vocabulary
  // Mirror of the mockup. Groups roll up the dotted action namespace
  // (auth.*, stack.*) into a coarser pill-set so the operator can land
  // on "all stack mutations" without typing wildcards. Falling-through
  // anything we don't recognise into "system" keeps the UI honest when
  // the backend ships a new action class.
  type GroupKey = 'auth' | 'user' | 'stack' | 'container' | 'image' | 'network' | 'volume' | 'system';
  const ACTION_GROUPS: Record<GroupKey, { label: string; actions: string[] }> = {
    auth:      { label: 'Auth',       actions: ['auth.login', 'auth.login_failed', 'auth.logout', 'auth.refresh'] },
    user:      { label: 'User',       actions: ['user.create', 'user.update', 'user.delete', 'user.password'] },
    stack:     { label: 'Stack',      actions: ['stack.create', 'stack.update', 'stack.delete', 'stack.deploy', 'stack.stop', 'stack.adopt'] },
    container: { label: 'Container',  actions: ['container.start', 'container.stop', 'container.restart', 'container.remove', 'container.update', 'container.rollback'] },
    image:     { label: 'Image',      actions: ['image.pull', 'image.remove', 'image.prune', 'image.scan'] },
    network:   { label: 'Network',    actions: ['network.create', 'network.remove'] },
    volume:    { label: 'Volume',     actions: ['volume.create', 'volume.remove', 'volume.prune', 'volume.browse', 'volume.read_file'] },
    system:    { label: 'System',     actions: ['audit.genesis'] }
  };
  function actionGroup(action: string): GroupKey {
    const head = action.split('.')[0];
    if (head === 'audit') return 'system';
    return (head in ACTION_GROUPS ? head : 'system') as GroupKey;
  }

  // ───────── Data + filter state
  interface AuditEntry {
    id: number;
    ts: string;
    user_id?: string;
    username?: string;
    action: string;
    target?: string;
    details?: string;
    prev_hash?: string;
    row_hash?: string;
  }
  let auditEntries = $state<AuditEntry[]>([]);
  let auditLoading = $state(true);

  let pillFilter = $state<'all' | GroupKey>('all');
  let actionFilter = $state<string>('any'); // 'any' or full action name
  let userFilter = $state<string>('any');
  let auditSearch = $state('');
  let auditLimit = $state(500);
  let expanded = $state<Set<number>>(new Set());

  async function loadAudit() {
    auditLoading = true;
    try {
      // Backend "action" filter is a substring; the mockup pills filter
      // client-side by group, so we always fetch with no action filter
      // and let the $derived chain do the slicing.
      auditEntries = await api.audit.list(auditLimit, '', userFilter === 'any' ? '' : userFilter);
    } catch (err) {
      toast.error('Failed to load audit', err instanceof ApiError ? err.message : undefined);
    } finally {
      auditLoading = false;
    }
  }
  $effect(() => { auditLimit; userFilter; loadAudit(); });

  // Counts per pill — computed against the unfiltered set so volumes
  // stay honest when a pill is selected (the mockup makes a point of
  // showing how many auth.login events exist even while you're filtered
  // away from auth).
  const pillCounts = $derived.by(() => {
    const c: Record<string, number> = { all: auditEntries.length };
    for (const k of Object.keys(ACTION_GROUPS)) c[k] = 0;
    for (const e of auditEntries) c[actionGroup(e.action)]++;
    return c;
  });
  const usersList = $derived.by(() => {
    const s = new Set<string>();
    for (const e of auditEntries) {
      const name = e.username ?? e.user_id;
      if (name) s.add(name);
    }
    return [...s].sort();
  });
  const filtered = $derived(
    auditEntries
      .filter((e) => pillFilter === 'all' ? true : actionGroup(e.action) === pillFilter)
      .filter((e) => actionFilter === 'any' ? true : e.action === actionFilter)
      .filter((e) => userFilter === 'any' ? true : (e.username ?? e.user_id ?? '') === userFilter)
      .filter((e) => {
        if (!auditSearch.trim()) return true;
        const q = auditSearch.toLowerCase();
        return (e.username ?? e.user_id ?? '').toLowerCase().includes(q)
          || e.action.toLowerCase().includes(q)
          || (e.target ?? '').toLowerCase().includes(q);
      })
  );
  // Group by ISO day (UTC). Newest day first, newest entry within a day
  // first. Backend already returns newest-first so we don't re-sort the
  // entries inside a group.
  const byDay = $derived.by(() => {
    const groups = new Map<string, AuditEntry[]>();
    for (const e of filtered) {
      const day = e.ts.slice(0, 10);
      if (!groups.has(day)) groups.set(day, []);
      groups.get(day)!.push(e);
    }
    return [...groups.entries()];
  });

  function toggleExpand(id: number) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id); else next.add(id);
    expanded = next;
  }
  function clearAll() {
    pillFilter = 'all';
    actionFilter = 'any';
    userFilter = 'any';
    auditSearch = '';
  }

  // ───────── Verify state + modal
  // The mockup shows a full progress modal during verify. Our backend
  // verifies synchronously (no streaming endpoint), so the "progress"
  // is fake-stepped while waiting for the response. Verify is fast
  // enough (<1s for typical fleets) that this is fine.
  let verifyOpen = $state(false);
  let verifying = $state(false);
  let verifyResult = $state<null | {
    verified: number; broken: number; first_break?: number;
    break_reason?: string; genesis: string; warnings?: string[];
  }>(null);
  let verifyProgress = $state(0);
  let verifyTimer: ReturnType<typeof setInterval> | null = null;

  async function runVerify() {
    verifyOpen = true;
    verifying = true;
    verifyResult = null;
    verifyProgress = 0;
    if (verifyTimer) clearInterval(verifyTimer);
    verifyTimer = setInterval(() => {
      if (verifyProgress < 90) verifyProgress = Math.min(90, verifyProgress + 8);
    }, 80);
    try {
      verifyResult = await api.audit.verify();
      verifyProgress = 100;
      if (verifyResult.broken === 0) {
        toast.success('Chain intact', `${verifyResult.verified} entries verified`);
      } else {
        toast.error('Chain broken', verifyResult.break_reason ?? 'see report');
      }
    } catch (err) {
      toast.error('Verify failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      verifying = false;
      if (verifyTimer) { clearInterval(verifyTimer); verifyTimer = null; }
    }
  }
  function closeVerify() { verifyOpen = false; }

  // Auto-run a quiet verify on first mount so the chain strip starts
  // populated. We don't open the modal for it — chain strip is enough.
  let bootedVerify = false;
  $effect(() => {
    if (bootedVerify) return;
    bootedVerify = true;
    api.audit.verify().then((r) => { verifyResult = r; }).catch(() => { /* ignore */ });
  });

  // ───────── Webhook
  let webhookCfg = $state<AuditWebhookConfig | null>(null);
  let webhookEnabled = $state(false);
  let webhookURL = $state('');
  let webhookSecret = $state('');
  let webhookClearSecret = $state(false);
  let webhookFilter = $state<string[]>([]);
  let webhookFilterDraft = $state('');
  let webhookBusy = $state(false);
  async function loadWebhook() {
    if (!allowed('audit.write')) return;
    try {
      webhookCfg = await api.audit.getWebhook();
      webhookURL = webhookCfg.url ?? '';
      webhookEnabled = !!webhookCfg.url;
      webhookFilter = webhookCfg.filter_actions ?? [];
      webhookSecret = '';
      webhookClearSecret = false;
    } catch { /* ignore */ }
  }
  async function saveWebhook() {
    webhookBusy = true;
    try {
      webhookCfg = await api.audit.setWebhook({
        url: webhookEnabled ? webhookURL : '',
        secret: webhookSecret || undefined,
        clear_secret: webhookClearSecret || undefined,
        filter_actions: webhookFilter.length > 0 ? webhookFilter : undefined
      });
      webhookSecret = '';
      webhookClearSecret = false;
      toast.success('Webhook config saved');
    } catch (err) {
      toast.error('Failed to save webhook', err instanceof ApiError ? err.message : undefined);
    } finally {
      webhookBusy = false;
    }
  }
  async function testWebhook() {
    webhookBusy = true;
    try {
      await api.audit.testWebhook();
      toast.success('Test event delivered');
    } catch (err) {
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      webhookBusy = false;
    }
  }
  function addFilter() {
    const v = webhookFilterDraft.trim();
    if (!v) return;
    if (!webhookFilter.includes(v)) webhookFilter = [...webhookFilter, v];
    webhookFilterDraft = '';
  }
  function removeFilter(v: string) {
    webhookFilter = webhookFilter.filter((f) => f !== v);
  }

  // ───────── Retention
  let retentionCfg = $state<AuditRetentionConfig | null>(null);
  let retentionPreview = $state<AuditRetentionPreview | null>(null);
  let retentionMode = $state<'forever' | 'days' | 'archive_local' | 'archive_target'>('forever');
  let retentionDays = $state(365);
  let retentionLocalDir = $state('/var/lib/dockmesh/audit-archive');
  let retentionTargetID = $state(0);
  let retentionTargets = $state<Array<{ id: number; name: string; type: string }>>([]);
  let retentionBusy = $state(false);
  let retentionLastResult = $state<AuditRetentionResult | null>(null);
  async function loadRetention() {
    if (!allowed('audit.write')) return;
    try {
      const res = await api.audit.getRetention();
      retentionCfg = res.config;
      retentionPreview = res.preview;
      retentionMode = res.config.mode;
      retentionDays = res.config.days || 365;
      retentionLocalDir = res.config.local_dir || '/var/lib/dockmesh/audit-archive';
      retentionTargetID = res.config.target_id || 0;
    } catch { /* ignore */ }
    try {
      const list = await api.backups.listTargets();
      retentionTargets = list.map((t) => ({ id: t.id, name: t.name, type: t.type }));
    } catch { /* ignore */ }
  }
  async function saveRetention() {
    retentionBusy = true;
    try {
      const res = await api.audit.setRetention({
        mode: retentionMode,
        days: retentionMode === 'forever' ? undefined : retentionDays,
        local_dir: retentionMode === 'archive_local' ? retentionLocalDir || undefined : undefined,
        target_id: retentionMode === 'archive_target' ? retentionTargetID || undefined : undefined
      });
      retentionCfg = res.config;
      retentionPreview = res.preview;
      toast.success('Retention policy saved');
    } catch (err) {
      toast.error('Failed to save retention', err instanceof ApiError ? err.message : undefined);
    } finally {
      retentionBusy = false;
    }
  }
  async function runRetentionNow() {
    if (retentionMode === 'forever') return;
    if (!(await confirm.ask({
      title: 'Run retention now',
      message: 'Run the retention policy now?',
      body: `This will prune ${retentionPreview?.would_prune ?? 'some'} audit rows. Pruned rows cannot be recovered; the chain-bridge entry stays intact.`,
      confirmLabel: 'Prune', danger: true
    }))) return;
    retentionBusy = true;
    try {
      retentionLastResult = await api.audit.runRetention();
      toast.success(`Pruned ${retentionLastResult.pruned} rows`);
      await loadRetention();
    } catch (err) {
      toast.error('Run failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      retentionBusy = false;
    }
  }

  $effect(() => { loadRetention(); loadWebhook(); });

  // ───────── Helpers
  function fmtDayHeading(iso: string): string {
    const today = new Date();
    const todayIso = today.toISOString().slice(0, 10);
    if (iso === todayIso) return 'Today';
    const d = new Date(iso + 'T00:00:00Z');
    const diff = (Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate()) - d.getTime()) / 86400000;
    if (diff === 1) return 'Yesterday';
    if (diff < 7) return d.toLocaleDateString('en-US', { weekday: 'long' });
    return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
  }
  function fmtTime(iso: string): string {
    return iso.slice(11, 19);
  }
  function fmtAgo(iso: string): string {
    if (!iso) return '—';
    const t = Date.parse(iso);
    if (!t) return '—';
    const s = Math.floor((Date.now() - t) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }
  function copyText(s: string) {
    void copyWithToast(s, 'Copied');
  }
  function exportCSV() {
    const rows = ['Timestamp,Action,Target,User,Details']
      .concat(filtered.map((e) => `"${e.ts}","${e.action}","${e.target ?? ''}","${e.username ?? e.user_id ?? ''}","${(e.details ?? '').replace(/"/g, '""')}"`))
      .join('\n');
    const blob = new Blob([rows], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `dockmesh-audit-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
  }
  // Try parsing details as JSON for the expanded panel; fall back to
  // raw string when the backend logged something that isn't structured
  // (older entries, system-emitted notes).
  function detailsAsJson(s: string | undefined): any {
    if (!s) return null;
    try { return JSON.parse(s); } catch { return s; }
  }
</script>

<section class="audit-page">
  <!-- ─── Header ─── -->
  <header class="audit-header">
    <div class="audit-header-text">
      <h1 class="ed-title audit-title">Audit log</h1>
      <p class="audit-subtitle">
        {auditEntries.length} entr{auditEntries.length === 1 ? 'y' : 'ies'}{(verifyResult?.broken ?? 0) > 0 ? ' · chain BROKEN' : verifyResult ? ' · chain ok' : ''}
      </p>
    </div>
    <div class="ed-actions">
      <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={exportCSV}>
        <Download size={12} strokeWidth={1.5} /> Export CSV
      </button>
      <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={runVerify}>
        <Check size={12} strokeWidth={1.5} /> Verify chain
      </button>
    </div>
  </header>

  <!-- ─── Chain integrity strip ─── -->
  <div class="audit-chain" class:audit-chain-broken={(verifyResult?.broken ?? 0) > 0}>
    <div class="audit-chain-mark" data-state={(verifyResult?.broken ?? 0) > 0 ? 'broken' : 'ok'}>
      {#if (verifyResult?.broken ?? 0) > 0}
        <X size={18} strokeWidth={1.8} />
      {:else}
        <Check size={18} strokeWidth={1.8} />
      {/if}
    </div>
    <div class="audit-chain-text">
      {#if (verifyResult?.broken ?? 0) > 0}
        <span class="audit-chain-eyebrow-danger"><Eyebrow>Tamper detected</Eyebrow></span>
        <h3 class="audit-chain-title audit-chain-title-danger">
          Hash chain broken at row #{verifyResult?.first_break}
        </h3>
        <p class="audit-chain-meta">
          <span class="font-mono">{verifyResult?.break_reason}</span> — investigate immediately.
        </p>
      {:else if verifyResult}
        <Eyebrow>Chain integrity</Eyebrow>
        <h3 class="audit-chain-title">
          Verified · <em class="ed-accent">{verifyResult.verified.toLocaleString()}</em> rows in sequence
        </h3>
        <p class="audit-chain-meta">
          last run <em>just now</em> · genesis <span class="font-mono">{verifyResult.genesis.slice(0, 19).replace('T', ' ')}</span> · sha-256 hash chain
          {#if verifyResult.warnings && verifyResult.warnings.length > 0}
            · <span class="audit-chain-warn">{verifyResult.warnings.length} legacy entries without chain</span>
          {/if}
        </p>
      {:else}
        <Eyebrow>Chain integrity</Eyebrow>
        <h3 class="audit-chain-title">Verifying…</h3>
        <p class="audit-chain-meta">running initial verification</p>
      {/if}
    </div>
    <div class="audit-chain-strip" aria-hidden="true">
      {#each Array(32) as _, i (i)}
        <span class:audit-chain-strip-accent={i % 7 === 6} class:audit-chain-strip-broken={(verifyResult?.broken ?? 0) > 0}></span>
      {/each}
    </div>
    <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={runVerify}>
      <RefreshCw size={11} strokeWidth={1.5} class={verifying ? 'ed-spin' : ''} /> Run again
    </button>
  </div>

  <!-- ─── Config strip — retention / webhook / verify history ─── -->
  {#if allowed('audit.write')}
    <section class="audit-config-grid">
      <!-- Retention card -->
      <div class="dm-card audit-cfg-card">
        <Eyebrow>Retention policy</Eyebrow>
        <div class="audit-cfg-options">
          {#each [
            ['forever', 'Keep forever', 'WORM-style — never prune'],
            ['days', `Prune after ${retentionDays} days`, retentionPreview ? `${retentionPreview.would_prune} rows would prune now` : 'set retention window'],
            ['archive_local', `Archive after ${retentionDays} days`, retentionLocalDir],
            ['archive_target', 'Archive to backup target', retentionTargets.find((t) => t.id === retentionTargetID)?.name ?? 'pick a target']
          ] as [id, label, hint] (id)}
            <label class="audit-cfg-radio" class:active={retentionMode === id}>
              <input type="radio" name="retention" checked={retentionMode === id} onchange={() => (retentionMode = id as typeof retentionMode)} />
              <div class="audit-cfg-radio-text">
                <div class="audit-cfg-radio-label">{label}</div>
                <div class="audit-cfg-radio-hint">{hint}</div>
              </div>
            </label>
          {/each}
        </div>
        {#if retentionMode === 'days' || retentionMode === 'archive_local' || retentionMode === 'archive_target'}
          <div class="audit-cfg-inline">
            <label class="audit-field-label" for="ret-days">days</label>
            <input id="ret-days" type="number" min="1" class="dm-input audit-cfg-input" bind:value={retentionDays} />
          </div>
        {/if}
        {#if retentionMode === 'archive_local'}
          <div class="audit-cfg-inline">
            <label class="audit-field-label" for="ret-localdir">local dir</label>
            <input id="ret-localdir" class="dm-input audit-cfg-input audit-cfg-input-mono" bind:value={retentionLocalDir} />
          </div>
        {/if}
        {#if retentionMode === 'archive_target'}
          <div class="audit-cfg-inline">
            <label class="audit-field-label" for="ret-target">target</label>
            <select id="ret-target" class="dm-input audit-cfg-input" bind:value={retentionTargetID}>
              <option value={0}>— pick one —</option>
              {#each retentionTargets as t (t.id)}
                <option value={t.id}>{t.name} ({t.type})</option>
              {/each}
            </select>
          </div>
        {/if}
        <div class="audit-cfg-actions">
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={saveRetention} disabled={retentionBusy}>
            {retentionBusy ? 'Saving…' : 'Save'}
          </button>
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={runRetentionNow} disabled={retentionBusy || retentionMode === 'forever'}>
            Run now
          </button>
        </div>
        {#if retentionLastResult}
          <p class="audit-cfg-hint font-mono">
            last run pruned {retentionLastResult.pruned}
            {#if retentionLastResult.archived} · archived to <span class="ed-accent">{retentionLastResult.archive_path}</span>{/if}
          </p>
        {/if}
      </div>

      <!-- Webhook card -->
      <div class="dm-card audit-cfg-card">
        <div class="audit-cfg-card-head">
          <Eyebrow>Webhook receiver</Eyebrow>
          <label class="audit-cfg-toggle font-mono">
            <input type="checkbox" bind:checked={webhookEnabled} />
            enabled
          </label>
        </div>
        <div class="audit-cfg-fields" class:disabled={!webhookEnabled}>
          <div>
            <div class="audit-field-label">URL</div>
            <input type="text" class="dm-input audit-cfg-input audit-cfg-input-mono" placeholder="https://splunk.haus.lan/services/collector/event" bind:value={webhookURL} />
          </div>
          <div>
            <div class="audit-field-label">
              HMAC secret
              {#if webhookCfg?.has_secret && !webhookClearSecret}<span class="audit-cfg-hint-inline">stored — leave blank to keep</span>{/if}
            </div>
            <input type="password" class="dm-input audit-cfg-input audit-cfg-input-mono" placeholder={webhookCfg?.has_secret ? '••••••••••••••••' : 'optional shared secret'} bind:value={webhookSecret} />
            {#if webhookCfg?.has_secret}
              <label class="audit-cfg-clear">
                <input type="checkbox" bind:checked={webhookClearSecret} /> remove stored secret
              </label>
            {/if}
          </div>
          <div>
            <div class="audit-field-label">Forward only</div>
            <div class="audit-cfg-chips">
              {#each webhookFilter as f (f)}
                <span class="dm-pill dm-pill-neutral audit-cfg-chip">
                  {f}
                  <button type="button" class="audit-cfg-chip-x" onclick={() => removeFilter(f)} aria-label="Remove {f}">
                    <X size={9} strokeWidth={2} />
                  </button>
                </span>
              {/each}
              <input
                type="text"
                placeholder="add pattern…"
                class="audit-cfg-chip-add font-mono"
                bind:value={webhookFilterDraft}
                onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addFilter(); } }}
              />
            </div>
          </div>
        </div>
        <div class="audit-cfg-foot">
          <span class="audit-cfg-hint font-mono">
            {#if webhookCfg?.url}last delivery <em class="ed-accent">200 OK</em>{:else}not configured{/if}
          </span>
          <span class="audit-cfg-spacer"></span>
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={saveWebhook} disabled={webhookBusy}>
            {webhookBusy ? 'Saving…' : 'Save'}
          </button>
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={testWebhook} disabled={webhookBusy || !(webhookCfg?.url)}>
            Send test
          </button>
        </div>
      </div>

      <!-- Verify history card -->
      <div class="dm-card audit-cfg-card">
        <div class="audit-cfg-card-head">
          <Eyebrow>Verify history</Eyebrow>
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={runVerify}>
            <Check size={11} strokeWidth={1.5} /> Run now
          </button>
        </div>
        {#if verifyResult}
          <div class="audit-verify-hist">
            <div class="audit-verify-row">
              <div class="audit-verify-mark" class:broken={verifyResult.broken > 0}>
                {#if verifyResult.broken > 0}
                  <X size={11} strokeWidth={2} />
                {:else}
                  <Check size={11} strokeWidth={2} />
                {/if}
              </div>
              <div class="audit-verify-text">
                <div class="font-mono">just now</div>
                <div class="audit-verify-meta">{verifyResult.verified} rows · sha-256 chain</div>
              </div>
              {#if verifyResult.broken === 0}
                <span class="dm-pill dm-pill-success audit-cfg-pill"><span class="dm-pill-dot"></span> ok</span>
              {:else}
                <span class="dm-pill dm-pill-danger audit-cfg-pill"><span class="dm-pill-dot"></span> broken</span>
              {/if}
            </div>
          </div>
        {:else}
          <div class="audit-verify-hist">
            <div class="audit-verify-empty font-mono">no verify run yet</div>
          </div>
        {/if}
        <p class="audit-cfg-hint">
          Verify replays every <code>row_hash</code> from genesis using
          <code>sha-256(prev_hash + canonical_json(row))</code>.
        </p>
      </div>
    </section>
  {/if}

  <!-- ─── Filter bar ─── -->
  <div class="audit-entries-head">
    <Eyebrow>Entries</Eyebrow>
    <span class="audit-entries-meta">
      showing <em class="ed-accent">{filtered.length.toLocaleString()}</em> of {auditEntries.length.toLocaleString()}
    </span>
  </div>

  <div class="audit-filterbar">
    <div class="audit-pills">
      <button type="button" class="audit-pill" class:active={pillFilter === 'all'} onclick={() => { pillFilter = 'all'; actionFilter = 'any'; }}>
        All <span class="audit-pill-count">{(pillCounts.all ?? 0).toLocaleString()}</span>
      </button>
      {#each Object.entries(ACTION_GROUPS) as [k, g] (k)}
        <button type="button" class="audit-pill" class:active={pillFilter === k} class:muted={k === 'auth'} onclick={() => { pillFilter = k as GroupKey; actionFilter = 'any'; }}>
          {g.label} <span class="audit-pill-count">{(pillCounts[k] ?? 0).toLocaleString()}</span>
        </button>
      {/each}
    </div>

    <div class="audit-refines">
      {#if pillFilter !== 'all'}
        <select bind:value={actionFilter} class="audit-refine-select">
          <option value="any">any {ACTION_GROUPS[pillFilter as GroupKey].label.toLowerCase()} action</option>
          {#each ACTION_GROUPS[pillFilter as GroupKey].actions as a (a)}
            <option value={a}>{a}</option>
          {/each}
        </select>
      {/if}

      <select bind:value={userFilter} class="audit-refine-select">
        <option value="any">any user</option>
        {#each usersList as u (u)}
          <option value={u}>{u}</option>
        {/each}
      </select>

      <div class="audit-refine-search">
        <Search size={12} strokeWidth={1.5} class="audit-refine-search-icon" />
        <input type="text" placeholder="user, action, target…" bind:value={auditSearch} class="ed-underline-input audit-refine-search-input" />
      </div>

      <div class="audit-limit">
        {#each [100, 500, 1000] as n (n)}
          <button type="button" class="audit-limit-btn" class:active={auditLimit === n} onclick={() => (auditLimit = n)}>
            {n}
          </button>
        {/each}
      </div>
    </div>
  </div>

  <!-- ─── Entry list ─── -->
  {#if auditLoading && auditEntries.length === 0}
    <div class="dm-card audit-card-pad"><Skeleton width="80%" height="8rem" /></div>
  {:else if byDay.length === 0}
    <div class="audit-empty">
      <Eyebrow>No matches</Eyebrow>
      <h3 class="audit-empty-title">
        {#if auditSearch}
          No entries match <em class="ed-accent">"{auditSearch}"</em>
        {:else if pillFilter !== 'all'}
          No <em class="ed-accent">{ACTION_GROUPS[pillFilter as GroupKey].label.toLowerCase()}</em> entries in the current window
        {:else}
          No entries in the current window
        {/if}
      </h3>
      <p class="audit-empty-body">Try widening the limit, or clear filters.</p>
      <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm audit-empty-cta" onclick={clearAll}>
        Clear filters
      </button>
    </div>
  {:else}
    <div class="audit-list">
      {#each byDay as [day, items] (day)}
        <section class="audit-day">
          <header class="audit-day-head">
            <span class="audit-day-label">{fmtDayHeading(day)}</span>
            <span class="audit-day-meta">{items.length} {items.length === 1 ? 'entry' : 'entries'}</span>
            <span class="audit-day-rule"></span>
            <span class="audit-day-iso font-mono">{day}</span>
          </header>
          <ol class="audit-rows">
            {#each items as e (e.id)}
              {@const grp = actionGroup(e.action)}
              {@const isOpen = expanded.has(e.id)}
              {@const detailsObj = detailsAsJson(e.details)}
              <li class="audit-row" class:audit-row-open={isOpen} data-group={grp}>
                <button type="button" class="audit-row-head" onclick={() => toggleExpand(e.id)}>
                  <span class="audit-row-time font-mono">{fmtTime(e.ts)}</span>
                  <span class="audit-row-stripe"></span>
                  <span class="audit-row-actor font-mono">
                    {#if e.username || e.user_id}{e.username ?? e.user_id?.slice(0, 8)}{:else}<em class="audit-row-system">system</em>{/if}
                  </span>
                  <span class="audit-row-action font-mono">{e.action}</span>
                  <span class="audit-row-target font-mono">{e.target ?? '—'}</span>
                  <span class="audit-row-id font-mono">#{e.id}</span>
                  <span class="audit-row-chevron">{isOpen ? '▾' : '▸'}</span>
                </button>
                {#if isOpen}
                  <div class="audit-row-body">
                    <div class="audit-row-body-grid">
                      <div>
                        <div class="audit-field-label">details</div>
                        {#if detailsObj === null}
                          <pre class="audit-json">no details recorded</pre>
                        {:else if typeof detailsObj === 'string'}
                          <pre class="audit-json">{detailsObj}</pre>
                        {:else}
                          <pre class="audit-json">{JSON.stringify(detailsObj, null, 2)}</pre>
                        {/if}
                      </div>
                      <div class="audit-row-body-meta">
                        <dl class="audit-row-kv">
                          <dt>entry id</dt>
                          <dd class="font-mono">#{e.id}</dd>
                          <dt>timestamp</dt>
                          <dd class="font-mono">{e.ts}</dd>
                          <dt>actor</dt>
                          <dd class="font-mono">{e.username ?? e.user_id ?? 'system'}</dd>
                          <dt>action</dt>
                          <dd class="font-mono">{e.action}</dd>
                          <dt>target</dt>
                          <dd class="font-mono audit-row-kv-break">{e.target ?? '—'}</dd>
                          {#if e.row_hash}
                            <dt>row hash</dt>
                            <dd class="font-mono audit-row-kv-hash">{e.row_hash.slice(0, 32)}…</dd>
                          {/if}
                          {#if e.prev_hash}
                            <dt>prev hash</dt>
                            <dd class="font-mono audit-row-kv-hash">{e.prev_hash.slice(0, 32)}…</dd>
                          {/if}
                        </dl>
                        <div class="audit-row-actions">
                          <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => copyText(JSON.stringify({ id: e.id, ts: e.ts, action: e.action, target: e.target, user: e.username ?? e.user_id, details: detailsObj, row_hash: e.row_hash, prev_hash: e.prev_hash }, null, 2))}>
                            <Copy size={10} strokeWidth={1.5} /> Copy as JSON
                          </button>
                          {#if e.target}
                            <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => copyText(e.target!)}>
                              <ArrowUpRight size={10} strokeWidth={1.5} /> Copy target
                            </button>
                          {/if}
                        </div>
                      </div>
                    </div>
                  </div>
                {/if}
              </li>
            {/each}
          </ol>
        </section>
      {/each}
    </div>
  {/if}
</section>

<!-- ─── Verify modal ─── -->
{#if verifyOpen}
  <button type="button" class="audit-modal-backdrop" onclick={closeVerify} aria-label="Close"></button>
  <div class="audit-modal" role="dialog" aria-modal="true">
    <header class="audit-modal-head">
      <div>
        <Eyebrow>Verify chain</Eyebrow>
        <h3 class="audit-modal-title">
          {#if verifying}Replaying hashes from genesis{:else}Result{/if}
        </h3>
      </div>
      <button type="button" onclick={closeVerify} class="audit-modal-close" aria-label="Close">
        <X size={14} strokeWidth={1.5} />
      </button>
    </header>
    <div class="audit-modal-body">
      {#if verifying}
        <div class="audit-modal-progress">
          <div class="audit-modal-progress-bar">
            <div class="audit-modal-progress-fill" style="width: {verifyProgress}%"></div>
          </div>
          <div class="audit-modal-progress-meta font-mono">
            <span>verifying chain…</span>
            <span>{verifyProgress}%</span>
          </div>
        </div>
      {:else if verifyResult && verifyResult.broken === 0}
        <div class="audit-modal-result audit-modal-result-ok">
          <div class="audit-modal-result-mark">
            <ShieldCheck size={22} strokeWidth={1.6} />
          </div>
          <h4 class="audit-modal-result-title">Chain intact</h4>
          <p class="audit-modal-result-body">
            <em class="ed-accent">{verifyResult.verified.toLocaleString()}</em> rows verified ·
            <span class="font-mono">0</span> breaks · genesis sha matches
          </p>
        </div>
      {:else if verifyResult}
        <div class="audit-modal-result audit-modal-result-broken">
          <div class="audit-modal-result-mark broken">
            <ShieldAlert size={22} strokeWidth={1.6} />
          </div>
          <h4 class="audit-modal-result-title broken">Chain broken</h4>
          <p class="audit-modal-result-body">
            first break at row #{verifyResult.first_break} · {verifyResult.broken} broken row{verifyResult.broken === 1 ? '' : 's'}
          </p>
          <p class="audit-modal-result-reason font-mono">{verifyResult.break_reason}</p>
        </div>
      {/if}
    </div>
    <footer class="audit-modal-foot">
      <span class="audit-modal-foot-meta font-mono">verified at {new Date().toISOString().slice(11, 19)} UTC</span>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={closeVerify}>Close</button>
        {#if !verifying && verifyResult}
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={() => copyText(JSON.stringify(verifyResult, null, 2))}>
            <Copy size={11} strokeWidth={1.5} /> Copy attestation
          </button>
        {/if}
      </div>
    </footer>
  </div>
{/if}

<style>
  .audit-page { display: block; }

  /* ─── Header ─── */
  .audit-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
    margin-bottom: 20px;
  }
  .audit-header-text { min-width: 0; max-width: 70ch; }
  .audit-title { font-size: 26px; }
  .audit-subtitle {
    margin-top: 6px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }

  /* ─── Chain integrity strip ─── */
  .audit-chain {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 14px 18px;
    border: 1px solid var(--border);
    border-left: 3px solid var(--color-success-500);
    border-radius: 6px;
    background: var(--surface);
  }
  .audit-chain-broken {
    border-left-color: var(--color-danger-500);
    background: color-mix(in srgb, var(--color-danger-500) 6%, var(--bg));
  }
  .audit-chain-mark {
    width: 36px; height: 36px;
    border-radius: 18px;
    display: inline-flex; align-items: center; justify-content: center;
    flex-shrink: 0;
  }
  .audit-chain-mark[data-state="ok"] {
    background: color-mix(in srgb, var(--color-success-500) 18%, transparent);
    color: var(--color-success-400);
  }
  .audit-chain-mark[data-state="broken"] {
    background: color-mix(in srgb, var(--color-danger-500) 22%, transparent);
    color: var(--color-danger-400);
  }
  .audit-chain-text { flex: 1; min-width: 0; }
  .audit-chain-title {
    margin-top: 4px;
    font-size: 15px;
    color: var(--fg);
    font-weight: 500;
  }
  .audit-chain-title-danger { color: var(--color-danger-400); font-weight: 600; }
  .audit-chain-eyebrow-danger :global(.ed-eyebrow) { color: var(--color-danger-400); }
  .audit-chain-meta {
    margin-top: 4px;
    font-size: 11px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .audit-chain-meta em { color: var(--fg-muted); font-style: normal; }
  .audit-chain-warn { color: var(--color-warning-400); }
  .audit-chain-strip {
    display: flex;
    gap: 2px;
    flex-shrink: 0;
  }
  .audit-chain-strip span {
    display: inline-block;
    width: 4px; height: 18px;
    border-radius: 1px;
    background: var(--color-success-500);
    opacity: 0.7;
  }
  .audit-chain-strip-accent { background: var(--accent) !important; }
  .audit-chain-strip-broken { background: var(--color-danger-500) !important; }
  .audit-chain-broken .audit-chain-strip-accent { background: var(--color-danger-500) !important; }

  /* ─── Config strip ─── */
  .audit-config-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 14px;
    margin-top: 18px;
  }
  @media (max-width: 1100px) {
    .audit-config-grid { grid-template-columns: 1fr; }
  }
  .audit-cfg-card { padding: 16px 18px 18px; }
  .audit-cfg-card-head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 10px;
  }
  .audit-cfg-options {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 12px;
  }
  .audit-cfg-radio {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 8px 10px;
    border-radius: 5px;
    border: 1px solid var(--border);
    cursor: pointer;
    transition: border-color 120ms, background 120ms;
  }
  .audit-cfg-radio:hover { border-color: var(--border-strong); }
  .audit-cfg-radio.active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent) 8%, transparent);
  }
  .audit-cfg-radio input[type="radio"] {
    margin-top: 2px;
    accent-color: var(--accent);
  }
  .audit-cfg-radio-text { min-width: 0; }
  .audit-cfg-radio-label { font-size: 12.5px; color: var(--fg); }
  .audit-cfg-radio-hint {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }
  .audit-cfg-inline {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 10px;
  }
  .audit-cfg-input {
    height: 28px;
    font-size: 12px;
    flex: 1;
    min-width: 0;
  }
  .audit-cfg-input-mono { font-family: var(--font-mono); }
  .audit-cfg-actions {
    display: flex;
    gap: 6px;
    margin-top: 12px;
  }
  .audit-cfg-hint {
    font-size: 10.5px;
    color: var(--fg-subtle);
    line-height: 1.55;
    margin-top: 8px;
  }
  .audit-cfg-hint code {
    font-family: var(--font-mono);
    color: var(--accent-fg);
    background: var(--bg-elevated);
    padding: 0 4px;
    border-radius: 3px;
  }
  .audit-cfg-hint-inline {
    font-weight: 400;
    text-transform: none;
    letter-spacing: 0;
    margin-left: 6px;
    font-size: 10px;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
  }
  .audit-cfg-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--fg-subtle);
    cursor: pointer;
  }
  .audit-cfg-toggle input { accent-color: var(--accent); }
  .audit-cfg-fields {
    margin-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .audit-cfg-fields.disabled { opacity: 0.5; pointer-events: none; }
  .audit-cfg-clear {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 4px;
    cursor: pointer;
  }
  .audit-cfg-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 4px;
  }
  .audit-cfg-chip {
    font-size: 10px;
    padding: 1px 6px;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .audit-cfg-chip-x {
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 0;
    display: inline-flex;
    align-items: center;
  }
  .audit-cfg-chip-x:hover { color: var(--fg); }
  .audit-cfg-chip-add {
    background: transparent;
    border: 0;
    border-bottom: 1px dashed var(--border);
    padding: 1px 4px;
    font-size: 10.5px;
    color: var(--fg-muted);
    outline: none;
    min-width: 100px;
  }
  .audit-cfg-chip-add::placeholder { color: var(--fg-subtle); }
  .audit-cfg-chip-add:focus { border-bottom-color: var(--accent); }
  .audit-cfg-foot {
    display: flex;
    align-items: center;
    gap: 6px;
    padding-top: 12px;
    margin-top: 14px;
    border-top: 1px solid var(--border);
  }
  .audit-cfg-spacer { flex: 1; }
  .audit-cfg-pill {
    font-size: 10px;
    padding: 1px 6px;
  }

  /* Verify history */
  .audit-verify-hist {
    margin-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .audit-verify-row {
    display: grid;
    grid-template-columns: auto 1fr auto;
    gap: 10px;
    align-items: center;
  }
  .audit-verify-mark {
    width: 18px; height: 18px;
    border-radius: 3px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: color-mix(in srgb, var(--color-success-500) 18%, transparent);
    color: var(--color-success-400);
  }
  .audit-verify-mark.broken {
    background: color-mix(in srgb, var(--color-danger-500) 22%, transparent);
    color: var(--color-danger-400);
  }
  .audit-verify-text { min-width: 0; font-size: 12px; color: var(--fg); }
  .audit-verify-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 1px;
  }
  .audit-verify-empty {
    padding: 12px 0;
    font-size: 11px;
    color: var(--fg-subtle);
    text-align: center;
  }

  /* ─── Filter bar ─── */
  .audit-entries-head {
    margin-top: 36px;
    display: flex;
    flex-wrap: wrap;
    gap: 14px;
    align-items: center;
  }
  .audit-entries-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .audit-filterbar {
    margin-top: 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 14px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-elevated);
  }
  .audit-pills {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .audit-pill {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 5px 11px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: transparent;
    color: var(--fg-muted);
    font-size: 12px;
    font-weight: 500;
    cursor: pointer;
    transition: background 120ms, color 120ms, border-color 120ms;
  }
  .audit-pill:hover {
    color: var(--fg);
    border-color: var(--border-strong);
  }
  .audit-pill.active {
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    border-color: color-mix(in srgb, var(--accent) 60%, var(--border));
    color: var(--accent-fg);
  }
  .audit-pill.muted .audit-pill-count { color: var(--fg-subtle); }
  .audit-pill-count {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    font-weight: 400;
    letter-spacing: 0.02em;
  }
  .audit-pill.active .audit-pill-count { color: var(--accent-fg); }

  .audit-refines {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    align-items: center;
  }
  /* Compact select: hairline border, sans, 13px so the trigger text is
     legible even on dense filter rows. The native option list still
     renders with the OS theme — there's no cross-browser way to style
     <option>; we just make the trigger readable. */
  .audit-refine-select {
    height: 30px;
    padding: 0 28px 0 10px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg-elevated);
    color: var(--fg);
    font-family: var(--font-sans);
    font-size: 13px;
    min-width: 170px;
    cursor: pointer;
    appearance: none;
    background-image: linear-gradient(45deg, transparent 50%, var(--fg-muted) 50%),
                      linear-gradient(-45deg, transparent 50%, var(--fg-muted) 50%);
    background-position: calc(100% - 14px) 13px, calc(100% - 9px) 13px;
    background-size: 5px 5px;
    background-repeat: no-repeat;
    transition: border-color 120ms;
  }
  .audit-refine-select:hover { border-color: var(--border-strong); }
  .audit-refine-select:focus {
    outline: none;
    border-color: var(--accent);
  }

  /* Search uses the global underline pattern — same as /hosts,
     /resources, /topology toolbars. Icon sits at left:0 inside the
     wrapper; padding-left clears it. */
  .audit-refine-search {
    position: relative;
    flex: 1 1 200px;
    min-width: 200px;
    max-width: 280px;
  }
  :global(.audit-refine-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
  }
  .audit-refine-search-input { padding-left: 20px; }
  .audit-limit {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }
  .audit-limit-btn {
    padding: 5px 10px;
    background: transparent;
    border: 0;
    border-right: 1px solid var(--border);
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .audit-limit-btn:last-child { border-right: 0; }
  .audit-limit-btn:hover { color: var(--fg-muted); }
  .audit-limit-btn.active {
    background: var(--bg-elevated);
    color: var(--fg);
  }

  .audit-field-label {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
    margin-bottom: 4px;
  }

  /* ─── Day group + entries ─── */
  .audit-card-pad { padding: 22px; margin-top: 16px; }
  .audit-list {
    display: flex;
    flex-direction: column;
    gap: 16px;
    margin-top: 16px;
  }
  .audit-day {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-elevated);
    overflow: hidden;
  }
  .audit-day-head {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 14px;
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
  }
  .audit-day-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--fg);
  }
  .audit-day-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .audit-day-rule { flex: 1; height: 1px; background: var(--border); }
  .audit-day-iso { font-size: 10.5px; color: var(--fg-subtle); }

  .audit-rows {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .audit-row { border-top: 1px solid var(--border); }
  .audit-row:first-child { border-top: 0; }
  .audit-row-head {
    display: grid;
    grid-template-columns: 64px 4px minmax(110px, 140px) minmax(180px, 240px) minmax(140px, 1fr) auto 16px;
    gap: 14px;
    align-items: center;
    width: 100%;
    padding: 10px 14px;
    background: transparent;
    border: 0;
    text-align: left;
    cursor: pointer;
    color: var(--fg);
    font-size: 12.5px;
    transition: background 100ms;
  }
  .audit-row-head:hover { background: var(--surface-hover); }
  .audit-row-open .audit-row-head { background: var(--surface-hover); }
  .audit-row-time { font-size: 11.5px; color: var(--fg-subtle); }
  .audit-row-stripe {
    width: 3px;
    height: 18px;
    border-radius: 1.5px;
    background: var(--border-strong);
  }
  .audit-row[data-group="auth"]      .audit-row-stripe { background: var(--fg-subtle); }
  .audit-row[data-group="user"]      .audit-row-stripe { background: var(--color-brand-400); }
  .audit-row[data-group="stack"]     .audit-row-stripe { background: var(--accent); }
  .audit-row[data-group="container"] .audit-row-stripe { background: var(--color-warning-500); }
  .audit-row[data-group="image"]     .audit-row-stripe { background: var(--color-brand-300); }
  .audit-row[data-group="network"]   .audit-row-stripe { background: var(--color-brand-500); }
  .audit-row[data-group="volume"]    .audit-row-stripe { background: var(--color-brand-600); }
  .audit-row[data-group="system"]    .audit-row-stripe { background: var(--fg-subtle); }
  .audit-row-actor {
    font-size: 12px;
    color: var(--fg);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .audit-row-system { color: var(--fg-subtle); font-style: normal; }
  .audit-row-action {
    font-size: 11.5px;
    color: var(--fg-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .audit-row[data-group="auth"]      .audit-row-action { color: var(--fg-subtle); }
  .audit-row[data-group="stack"]     .audit-row-action { color: var(--accent-fg); }
  .audit-row[data-group="container"] .audit-row-action { color: var(--color-warning-400); }
  .audit-row-target {
    font-size: 12px;
    color: var(--fg-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .audit-row-id {
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.02em;
  }
  .audit-row-chevron {
    color: var(--fg-subtle);
    font-size: 11px;
    text-align: center;
  }

  .audit-row-body {
    padding: 4px 14px 18px 88px;
    border-top: 1px dashed var(--border);
    background: color-mix(in srgb, var(--accent) 3%, var(--bg));
  }
  .audit-row-body-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr);
    gap: 24px;
    padding-top: 14px;
  }
  @media (max-width: 720px) {
    .audit-row-body { padding-left: 14px; }
    .audit-row-body-grid { grid-template-columns: 1fr; }
  }
  .audit-row-body-meta {
    border-left: 1px solid var(--border);
    padding-left: 20px;
  }
  @media (max-width: 720px) {
    .audit-row-body-meta { border-left: 0; padding-left: 0; }
  }
  .audit-json {
    margin: 0;
    padding: 12px 14px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 5px;
    font-size: 11.5px;
    line-height: 1.6;
    color: var(--fg);
    white-space: pre-wrap;
    word-break: break-word;
    overflow-x: auto;
    max-height: 360px;
  }
  .audit-row-kv {
    display: grid;
    grid-template-columns: 76px 1fr;
    row-gap: 6px;
    column-gap: 12px;
    align-items: baseline;
    margin: 0;
  }
  .audit-row-kv dt {
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
    margin: 0;
  }
  .audit-row-kv dd {
    margin: 0;
    font-size: 12px;
    color: var(--fg);
    line-height: 1.55;
  }
  .audit-row-kv-break { word-break: break-all; }
  .audit-row-kv-hash {
    font-size: 10.5px;
    color: var(--fg-subtle);
    word-break: break-all;
  }
  .audit-row-actions {
    display: flex;
    gap: 6px;
    margin-top: 14px;
    flex-wrap: wrap;
  }

  /* ─── Empty results ─── */
  .audit-empty {
    margin-top: 28px;
    padding: 44px 24px;
    text-align: center;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-elevated);
  }
  .audit-empty-title {
    margin-top: 10px;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .audit-empty-body {
    margin-top: 6px;
    font-size: 13px;
    color: var(--fg-muted);
    max-width: 44ch;
    margin-inline: auto;
    line-height: 1.55;
  }
  .audit-empty-cta { margin-top: 16px; }

  /* ─── Verify modal ─── */
  .audit-modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.66);
    z-index: 100;
    border: 0;
    cursor: default;
  }
  .audit-modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(560px, calc(100vw - 48px));
    max-height: calc(100vh - 48px);
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    overflow: auto;
    z-index: 101;
    box-shadow: 0 20px 60px rgba(0, 0, 0, 0.45);
  }
  .audit-modal-head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    padding: 20px 22px 12px;
  }
  .audit-modal-title {
    font-size: 18px;
    color: var(--fg);
    margin-top: 4px;
    font-weight: 500;
  }
  .audit-modal-close {
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 4px;
    border-radius: 3px;
  }
  .audit-modal-close:hover { color: var(--fg); background: var(--surface-hover); }
  .audit-modal-body { padding: 0 22px 16px; }
  .audit-modal-progress-bar {
    height: 4px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 2px;
    overflow: hidden;
  }
  .audit-modal-progress-fill {
    height: 100%;
    background: var(--accent);
    transition: width 80ms linear;
  }
  .audit-modal-progress-meta {
    display: flex;
    justify-content: space-between;
    margin-top: 10px;
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .audit-modal-result { padding: 22px 0; text-align: center; }
  .audit-modal-result-mark {
    width: 48px;
    height: 48px;
    border-radius: 24px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: color-mix(in srgb, var(--color-success-500) 22%, transparent);
    color: var(--color-success-400);
    margin-bottom: 12px;
  }
  .audit-modal-result-mark.broken {
    background: color-mix(in srgb, var(--color-danger-500) 22%, transparent);
    color: var(--color-danger-400);
  }
  .audit-modal-result-title {
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .audit-modal-result-title.broken { color: var(--color-danger-400); }
  .audit-modal-result-body {
    margin-top: 6px;
    font-size: 12.5px;
    color: var(--fg-muted);
    max-width: 40ch;
    margin-inline: auto;
    line-height: 1.55;
  }
  .audit-modal-result-reason {
    margin-top: 10px;
    font-size: 11px;
    color: var(--color-danger-400);
  }
  .audit-modal-foot {
    border-top: 1px solid var(--border);
    padding: 14px 22px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
  }
  .audit-modal-foot-meta {
    font-size: 10.5px;
    color: var(--fg-subtle);
  }

  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
