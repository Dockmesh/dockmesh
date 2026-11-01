<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { api, ApiError, type AuditWebhookConfig, type AuditRetentionPreview, type AuditRetentionResult } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { fmtAgo } from '$lib/format';
  import { Skeleton, Segmented, Modal, Tabs, EmptyState, StatusChip } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Download, ShieldCheck, Settings2, X } from 'lucide-svelte';
  import { auditTargetHref, csvLine, CATEGORY_LABEL, type AuditCategory } from './_links';

  interface AuditEntry {
    id: number;
    ts: string;
    user_id?: string;
    username?: string;
    action: string;
    category?: string;
    target?: string;
    details?: string;
    prev_hash?: string;
    row_hash?: string;
  }

  const CATS: AuditCategory[] = ['changes', 'access', 'auth', 'system'];
  const canWrite = $derived(allowed('audit.write'));

  let entries = $state<AuditEntry[]>([]);
  let loading = $state(true);
  let limit = $state('500');
  let category = $state<string>(page.url.searchParams.get('category') ?? 'changes');
  let userId = $state(page.url.searchParams.get('user') ?? '');
  let search = $state('');
  let detail = $state<AuditEntry | null>(null);
  let detailOpen = $state(false);

  async function load() {
    loading = true;
    try {
      entries = await api.audit.list(Number(limit), '', userId);
    } catch (err) {
      toast.error('Could not load the audit log', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void limit;
    void userId;
    untrack(load);
  });

  const catOf = (e: AuditEntry) => (CATS.includes(e.category as AuditCategory) ? (e.category as AuditCategory) : 'system');
  const counts = $derived.by(() => {
    const c: Record<string, number> = { all: entries.length, changes: 0, access: 0, auth: 0, system: 0 };
    for (const e of entries) c[catOf(e)]++;
    return c;
  });
  const users = $derived.by(() => {
    const m = new Map<string, string>();
    for (const e of entries) if (e.user_id) m.set(e.user_id, e.username || e.user_id);
    return [...m.entries()].sort((a, b) => a[1].localeCompare(b[1]));
  });
  const shown = $derived(
    entries.filter((e) => {
      if (category !== 'all' && catOf(e) !== category) return false;
      const q = search.trim().toLowerCase();
      if (!q) return true;
      return (e.username ?? '').toLowerCase().includes(q) || e.action.toLowerCase().includes(q) || (e.target ?? '').toLowerCase().includes(q);
    })
  );

  function setUser(id: string) {
    userId = id;
    const u = new URL(page.url.href);
    if (id) u.searchParams.set('user', id);
    else u.searchParams.delete('user');
    replaceState(u, page.state);
  }
  function openDetail(e: AuditEntry) {
    detail = e;
    detailOpen = true;
  }
  function detailsText(s: string | undefined): string {
    if (!s) return '';
    try {
      return JSON.stringify(JSON.parse(s), null, 2);
    } catch {
      return s;
    }
  }

  function exportCSV() {
    const rows = [csvLine(['Time', 'User', 'Action', 'Category', 'Target', 'Details'])].concat(
      shown.map((e) => csvLine([e.ts, e.username ?? e.user_id ?? 'system', e.action, CATEGORY_LABEL[catOf(e)], e.target, e.details]))
    );
    const blob = new Blob([rows.join('\n')], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `dockmesh-audit-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  let verifying = $state(false);
  let verifyOpen = $state(false);
  let verifyResult = $state<null | { verified: number; broken: number; first_break?: number; break_reason?: string; genesis: string; warnings?: string[] }>(null);
  async function verify(show: boolean) {
    verifying = true;
    if (show) verifyOpen = true;
    try {
      verifyResult = await api.audit.verify();
    } catch (err) {
      if (show) toast.error('Verify failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      verifying = false;
    }
  }
  $effect(() => {
    untrack(() => verify(false));
  });

  let settingsOpen = $state(false);
  let settingsTab = $state('retention');

  let webhookCfg = $state<AuditWebhookConfig | null>(null);
  let webhookEnabled = $state(false);
  let webhookURL = $state('');
  let webhookSecret = $state('');
  let webhookClearSecret = $state(false);
  let webhookFilter = $state<string[]>([]);
  let webhookFilterDraft = $state('');
  let webhookBusy = $state(false);
  async function loadWebhook() {
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
      toast.success('Forwarding saved');
    } catch (err) {
      toast.error('Saving forwarding failed', err instanceof ApiError ? err.message : undefined);
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

  let retentionPreview = $state<AuditRetentionPreview | null>(null);
  let retentionMode = $state<'forever' | 'days' | 'archive_local' | 'archive_target'>('forever');
  let retentionDays = $state(365);
  let retentionLocalDir = $state('/var/lib/dockmesh/audit-archive');
  let retentionTargetID = $state(0);
  let retentionTargets = $state<Array<{ id: number; name: string; type: string }>>([]);
  let retentionBusy = $state(false);
  let retentionLastResult = $state<AuditRetentionResult | null>(null);
  async function loadRetention() {
    try {
      const res = await api.audit.getRetention();
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
      retentionPreview = res.preview;
      toast.success('Retention policy saved');
    } catch (err) {
      toast.error('Saving retention failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      retentionBusy = false;
    }
  }
  async function runRetentionNow() {
    if (retentionMode === 'forever') return;
    if (!(await confirm.ask({
      title: 'Run retention now',
      message: 'Remove old audit entries now?',
      body: `${retentionPreview?.would_prune ?? 'Some'} entries are removed for good. The hash chain stays verifiable.`,
      confirmLabel: 'Remove entries', danger: true
    }))) return;
    retentionBusy = true;
    try {
      retentionLastResult = await api.audit.runRetention();
      toast.success(`Removed ${retentionLastResult.pruned} entries`);
      await loadRetention();
    } catch (err) {
      toast.error('Run failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      retentionBusy = false;
    }
  }

  $effect(() => {
    if (!canWrite) return;
    untrack(() => {
      loadRetention();
      loadWebhook();
    });
  });

  const broken = $derived((verifyResult?.broken ?? 0) > 0);
  const COLS = $derived(category === 'all' ? '96px 150px 210px minmax(0, 1fr) 150px' : '96px 150px 210px minmax(0, 1fr)');
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Audit log</h1>
    <span class="dm-sub">
      {entries.length.toLocaleString()} entries ·
      {#if verifyResult === null}checking the hash chain…{:else if broken}<span class="bad">hash chain broken</span>{:else}hash chain verified{/if}
    </span>
    <span class="dm-sp"></span>
    {#if canWrite}<button type="button" class="dm-btn dm-btn-ghost" onclick={() => (settingsOpen = true)}><Settings2 size={14} /> Audit settings</button>{/if}
    <button type="button" class="dm-btn" onclick={exportCSV} disabled={!shown.length}><Download size={14} /> Export CSV</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={() => verify(true)} disabled={verifying}><ShieldCheck size={14} /> Verify chain</button>
  </div>

  {#if broken && verifyResult}
    <div class="fail" role="alert">
      <b>The hash chain is broken at entry #{verifyResult.first_break}.</b>
      <span>Entries were changed or removed outside Dockmesh. {verifyResult.break_reason ?? ''}</span>
    </div>
  {/if}

  <div class="dm-toolbar">
    <Segmented
      label="Category"
      size="sm"
      bind:value={category}
      options={[
        ...CATS.map((c) => ({ key: c, label: CATEGORY_LABEL[c], count: counts[c] })),
        { key: 'all', label: 'All', count: counts.all }
      ]}
    />
    <select class="dm-filter" value={userId} onchange={(e) => setUser((e.target as HTMLSelectElement).value)} aria-label="User">
      <option value="">Any user</option>
      {#each users as [id, name] (id)}<option value={id}>{name}</option>{/each}
      {#if userId && !users.some(([id]) => id === userId)}<option value={userId}>{userId}</option>{/if}
    </select>
    <input class="dm-filter" type="search" placeholder="Filter user, action, target…" bind:value={search} aria-label="Filter entries" />
    <span class="dm-sp"></span>
    <Segmented label="Entries to load" size="sm" bind:value={limit} options={[{ key: '100', label: '100' }, { key: '500', label: '500' }, { key: '1000', label: '1000' }]} />
  </div>

  {#if loading && entries.length === 0}
    <div class="dm-tbl">{#each Array(6) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="70%" height="0.9rem" /></div>{/each}</div>
  {:else if entries.length === 0}
    <EmptyState title="No entries yet" description="Every change, sign-in and file access is recorded here." />
  {:else}
    <div class="dm-tbl" role="table" aria-label="Audit entries">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
        <span role="columnheader">Time</span>
        <span role="columnheader">User</span>
        <span role="columnheader">Action</span>
        <span role="columnheader">Target</span>
        {#if category === 'all'}<span role="columnheader">Category</span>{/if}
      </div>
      {#each shown as e (e.id)}
        {@const href = auditTargetHref(e.action, e.target)}
        <div
          class="dm-tr dm-click"
          role="row"
          tabindex="0"
          style="grid-template-columns: {COLS}"
          onclick={() => openDetail(e)}
          onkeydown={(ev) => { if (ev.key === 'Enter' && ev.target === ev.currentTarget) openDetail(e); }}
        >
          <span role="cell" class="dm-num dm-dim" title={new Date(e.ts).toLocaleString()}>{fmtAgo(e.ts)}</span>
          <span role="cell" class="dm-trunc">
            {#if e.user_id}
              <button type="button" class="lnk" title="Show only {e.username || e.user_id}" onclick={(ev) => { ev.stopPropagation(); setUser(e.user_id!); }}>{e.username || e.user_id}</button>
            {:else}<span class="dm-dim">System</span>{/if}
          </span>
          <span role="cell" class="dm-mono dm-trunc" title={e.action}>{e.action}</span>
          <span role="cell" class="dm-trunc" title={e.target ?? ''}>
            {#if href}<a {href} onclick={(ev) => ev.stopPropagation()}>{e.target}</a>{:else}<span class="dm-dim">{e.target || '—'}</span>{/if}
          </span>
          {#if category === 'all'}<span role="cell" class="dm-dim">{CATEGORY_LABEL[catOf(e)]}</span>{/if}
        </div>
      {:else}
        <div class="dm-empty">No entries match. <button type="button" class="lnk acc" onclick={() => { search = ''; category = 'all'; setUser(''); }}>Clear filters</button></div>
      {/each}
    </div>
    {#if entries.length >= Number(limit)}<p class="dm-sub">Showing the latest {limit} entries. Load more with the selector above.</p>{/if}
  {/if}
</section>

<Modal bind:open={detailOpen} title={detail ? `Entry #${detail.id}` : 'Entry'} maxWidth="max-w-2xl">
  {#if detail}
    {@const href = auditTargetHref(detail.action, detail.target)}
    <dl class="dm-kv">
      <dt>Time</dt><dd>{new Date(detail.ts).toLocaleString()} <span class="dm-dim">· {fmtAgo(detail.ts)}</span></dd>
      <dt>User</dt><dd>{detail.username || detail.user_id || 'System'}</dd>
      <dt>Action</dt><dd class="dm-mono">{detail.action}</dd>
      <dt>Category</dt><dd>{CATEGORY_LABEL[catOf(detail)]}</dd>
      <dt>Target</dt><dd>{#if href}<a {href}>{detail.target}</a>{:else}{detail.target || '—'}{/if}</dd>
      {#if detail.row_hash}<dt>Row hash</dt><dd class="dm-mono hash" title={detail.row_hash}>{detail.row_hash}</dd>{/if}
    </dl>
    <h3 class="dm-h2 sp">Details</h3>
    {#if detail.details}<pre class="json dm-mono">{detailsText(detail.details)}</pre>{:else}<p class="dm-sub">No details recorded.</p>{/if}
  {/if}
  {#snippet footer()}
    {#if detail}
      {@const d = detail}
      <button type="button" class="dm-btn dm-btn-ghost" onclick={() => copyWithToast(JSON.stringify({ ...d, details: detailsText(d.details) || undefined }, null, 2), 'Copied')}>Copy as JSON</button>
      <span class="dm-sp"></span>
      <button type="button" class="dm-btn" onclick={() => (detailOpen = false)}>Close</button>
    {/if}
  {/snippet}
</Modal>

<Modal bind:open={verifyOpen} title="Verify the hash chain" maxWidth="max-w-lg">
  {#if verifying}
    <p>Checking every entry against the one before it…</p>
  {:else if verifyResult && !broken}
    <p class="res"><StatusChip tone="ok" label="Intact" /> {verifyResult.verified.toLocaleString()} entries verified, none changed or missing.</p>
    {#if verifyResult.warnings?.length}<p class="dm-sub">{verifyResult.warnings.length} entries from older versions carry no hash and were skipped.</p>{/if}
  {:else if verifyResult}
    <p class="res"><StatusChip tone="bad" label="Broken" /> First break at entry #{verifyResult.first_break}, {verifyResult.broken} entries affected.</p>
    {#if verifyResult.break_reason}<p class="dm-sub">{verifyResult.break_reason}</p>{/if}
  {/if}
  {#snippet footer()}
    <button type="button" class="dm-btn" onclick={() => (verifyOpen = false)}>Close</button>
  {/snippet}
</Modal>

<Modal bind:open={settingsOpen} title="Audit settings" maxWidth="max-w-2xl">
  <Tabs tabs={[{ key: 'retention', label: 'Retention' }, { key: 'forwarding', label: 'Forwarding' }]} active={settingsTab} onchange={(k) => (settingsTab = k)} label="Audit settings" />
  {#if settingsTab === 'retention'}
    <div class="opts" role="radiogroup" aria-label="Retention">
      {#each [
        ['forever', 'Keep forever', 'Nothing is removed.'],
        ['days', 'Remove old entries', retentionPreview ? `${retentionPreview.would_prune} entries are older than the limit now.` : 'Entries older than the limit are deleted.'],
        ['archive_local', 'Archive to a folder', 'Old entries move to a file on this server.'],
        ['archive_target', 'Archive to a backup target', 'Old entries move to a saved backup target.']
      ] as [id, label, hint] (id)}
        <label class="opt" class:on={retentionMode === id}>
          <input type="radio" name="retention" checked={retentionMode === id} onchange={() => (retentionMode = id as typeof retentionMode)} />
          <span><b>{label}</b><small>{hint}</small></span>
        </label>
      {/each}
    </div>
    {#if retentionMode !== 'forever'}
      <div class="dm-field"><label for="ret-days">Older than (days)</label><input id="ret-days" type="number" min="1" class="dm-input w" bind:value={retentionDays} /></div>
    {/if}
    {#if retentionMode === 'archive_local'}
      <div class="dm-field"><label for="ret-dir">Folder</label><input id="ret-dir" class="dm-input dm-mono" bind:value={retentionLocalDir} /></div>
    {/if}
    {#if retentionMode === 'archive_target'}
      <div class="dm-field">
        <label for="ret-target">Target</label>
        <select id="ret-target" class="dm-input" bind:value={retentionTargetID}>
          <option value={0}>Choose a target</option>
          {#each retentionTargets as t (t.id)}<option value={t.id}>{t.name} ({t.type})</option>{/each}
        </select>
      </div>
    {/if}
    {#if retentionLastResult}<p class="dm-sub">Last run removed {retentionLastResult.pruned} entries{#if retentionLastResult.archived}, archived to <span class="dm-mono">{retentionLastResult.archive_path}</span>{/if}.</p>{/if}
  {:else}
    <label class="chk"><input type="checkbox" bind:checked={webhookEnabled} /> Send every new entry to a webhook (SIEM)</label>
    <fieldset class="wh" disabled={!webhookEnabled}>
      <div class="dm-field"><label for="wh-url">URL</label><input id="wh-url" class="dm-input dm-mono" placeholder="https://siem.example.com/collector" bind:value={webhookURL} /></div>
      <div class="dm-field">
        <label for="wh-secret">Signing secret {#if webhookCfg?.has_secret && !webhookClearSecret}<span class="dm-dim">· saved, leave empty to keep</span>{/if}</label>
        <input id="wh-secret" type="password" class="dm-input" placeholder={webhookCfg?.has_secret ? '••••••••' : 'Optional'} bind:value={webhookSecret} />
        {#if webhookCfg?.has_secret}<label class="chk"><input type="checkbox" bind:checked={webhookClearSecret} /> Remove the saved secret</label>{/if}
      </div>
      <div class="dm-field">
        <label for="wh-filter">Only these actions <span class="dm-dim">· empty sends all</span></label>
        <div class="chips">
          {#each webhookFilter as f (f)}
            <span class="dm-pill dm-mono">{f}<button type="button" class="x" onclick={() => removeFilter(f)} aria-label="Remove {f}"><X size={10} /></button></span>
          {/each}
          <input id="wh-filter" class="dm-input dm-mono add" placeholder="e.g. stack.deploy" bind:value={webhookFilterDraft} onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addFilter(); } }} />
        </div>
      </div>
    </fieldset>
  {/if}
  {#snippet footer()}
    {#if settingsTab === 'retention'}
      <button type="button" class="dm-btn dm-btn-ghost" onclick={runRetentionNow} disabled={retentionBusy || retentionMode === 'forever'}>Run now…</button>
      <span class="dm-sp"></span>
      <button type="button" class="dm-btn" onclick={() => (settingsOpen = false)}>Close</button>
      <button type="button" class="dm-btn dm-btn-primary" onclick={saveRetention} disabled={retentionBusy}>{retentionBusy ? 'Saving…' : 'Save'}</button>
    {:else}
      <button type="button" class="dm-btn dm-btn-ghost" onclick={testWebhook} disabled={webhookBusy || !webhookCfg?.url}>Send test</button>
      <span class="dm-sp"></span>
      <button type="button" class="dm-btn" onclick={() => (settingsOpen = false)}>Close</button>
      <button type="button" class="dm-btn dm-btn-primary" onclick={saveWebhook} disabled={webhookBusy}>{webhookBusy ? 'Saving…' : 'Save'}</button>
    {/if}
  {/snippet}
</Modal>

<style>
  .bad { color: var(--bad); }
  .fail { border: 1px solid var(--bad-bd); background: var(--bad-bg); border-radius: var(--r); padding: 10px 14px; display: flex; gap: 8px; flex-wrap: wrap; font-size: var(--fs-sm); }
  .fail b { color: var(--bad); }
  .lnk { background: none; border: 0; padding: 0; font: inherit; color: var(--fg); cursor: pointer; font-weight: 500; }
  .lnk:hover { text-decoration: underline; }
  .lnk.acc { color: var(--accent); }
  .sp { margin-top: 16px; }
  .hash { font-size: 11.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .json { max-height: 300px; overflow: auto; background: var(--field); padding: 10px 12px; border-radius: var(--r); font-size: 12px; margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .res { display: flex; align-items: center; gap: 10px; margin: 0 0 8px; }
  .opts { display: flex; flex-direction: column; gap: 6px; margin: 14px 0; }
  .opt { display: flex; gap: 10px; align-items: flex-start; padding: 8px 10px; border: 1px solid var(--border); border-radius: var(--r); cursor: pointer; }
  .opt.on { border-color: var(--accent); background: var(--accent-soft); }
  .opt span { display: flex; flex-direction: column; gap: 2px; }
  .opt small { color: var(--subtle); font-size: var(--fs-xs); }
  .dm-field { margin-top: 10px; }
  .w { max-width: 140px; }
  .chk { display: flex; align-items: center; gap: 8px; margin: 12px 0 4px; font-size: var(--fs-sm); }
  .wh { border: 0; padding: 0; margin: 0; }
  .wh:disabled { opacity: 0.55; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .chips .x { background: none; border: 0; color: inherit; cursor: pointer; padding: 0 0 0 4px; display: inline-flex; }
  .chips .add { flex: 1; min-width: 180px; }
</style>
