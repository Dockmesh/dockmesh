<script lang="ts">
  // Audit Log — promoted from a Settings-tab to a top-level route.
  //
  // Rationale: the audit log is a first-class compliance surface, not a
  // configuration knob. Portainer Business, Grafana Enterprise, and
  // most enterprise tools give audit its own nav item. Burying it as
  // tab 8 of Settings hid the one page most compliance users open daily.
  //
  // Three sub-sections stacked vertically — admins want all three visible
  // at once: retention policy (when prune), webhook receiver (where stream),
  // entries list (what happened).
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { Activity, Link2, ShieldCheck, ShieldOff } from 'lucide-svelte';

  let auditEntries = $state<Array<any>>([]);
  let auditLoading = $state(false);
  let auditLimit = $state(100);
  let auditActionFilter = $state('');
  let auditSearch = $state('');
  let verifyResult = $state<null | {
    verified: number;
    broken: number;
    first_break?: number;
    break_reason?: string;
    genesis: string;
    warnings?: string[];
  }>(null);
  let verifying = $state(false);

  async function runVerify() {
    verifying = true;
    try {
      verifyResult = await api.audit.verify();
      if (verifyResult.broken === 0) {
        toast.success('Chain intact', `${verifyResult.verified} entries verified`);
      } else {
        toast.error('Chain broken', verifyResult.break_reason ?? 'see report');
      }
    } catch (err) {
      toast.error('Verify failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      verifying = false;
    }
  }

  async function loadAudit() {
    auditLoading = true;
    try {
      auditEntries = await api.audit.list(auditLimit, auditActionFilter);
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      auditLoading = false;
    }
  }

  const filteredAudit = $derived(
    auditEntries.filter(e => {
      if (!auditSearch.trim()) return true;
      const q = auditSearch.toLowerCase();
      return (e.username ?? e.user_id ?? '').toLowerCase().includes(q)
        || e.action.toLowerCase().includes(q)
        || (e.target ?? '').toLowerCase().includes(q);
    })
  );

  function actionVariant(action: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (action.includes('delete') || action.includes('remove') || action.includes('failed')) return 'danger';
    if (action.includes('create') || action.includes('deploy')) return 'success';
    if (action.includes('update') || action.includes('start') || action.includes('restart')) return 'info';
    return 'default';
  }

  function fmtTs(ts: string): string {
    return ts.slice(0, 19).replace('T', ' ');
  }

  // Webhook state
  let webhookCfg = $state<import('$lib/api').AuditWebhookConfig | null>(null);
  let webhookURL = $state('');
  let webhookSecret = $state('');
  let webhookClearSecret = $state(false);
  let webhookFilter = $state('');
  let webhookBusy = $state(false);

  async function loadWebhook() {
    if (!allowed('user.manage')) return;
    try {
      webhookCfg = await api.audit.getWebhook();
      webhookURL = webhookCfg.url ?? '';
      webhookFilter = (webhookCfg.filter_actions ?? []).join(', ');
      webhookSecret = '';
      webhookClearSecret = false;
    } catch { /* ignore */ }
  }

  async function saveWebhook() {
    webhookBusy = true;
    try {
      const filter = webhookFilter
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean);
      webhookCfg = await api.audit.setWebhook({
        url: webhookURL,
        secret: webhookSecret || undefined,
        clear_secret: webhookClearSecret || undefined,
        filter_actions: filter.length > 0 ? filter : undefined
      });
      webhookSecret = '';
      webhookClearSecret = false;
      toast.success('Webhook config saved');
    } catch (err) {
      toast.error('Failed to save', err instanceof ApiError ? err.message : undefined);
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

  // Retention state
  let retentionCfg = $state<import('$lib/api').AuditRetentionConfig | null>(null);
  let retentionPreview = $state<import('$lib/api').AuditRetentionPreview | null>(null);
  let retentionMode = $state<'forever' | 'days' | 'archive_local' | 'archive_target'>('forever');
  let retentionDays = $state(90);
  let retentionLocalDir = $state('');
  let retentionTargetID = $state(0);
  let retentionTargets = $state<Array<{ id: number; name: string; type: string }>>([]);
  let retentionBusy = $state(false);
  let retentionLastResult = $state<import('$lib/api').AuditRetentionResult | null>(null);

  async function loadRetention() {
    if (!allowed('user.manage')) return;
    try {
      const res = await api.audit.getRetention();
      retentionCfg = res.config;
      retentionPreview = res.preview;
      retentionMode = res.config.mode;
      retentionDays = res.config.days || 90;
      retentionLocalDir = res.config.local_dir || '';
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
      toast.error('Failed to save', err instanceof ApiError ? err.message : undefined);
    } finally {
      retentionBusy = false;
    }
  }

  async function runRetentionNow() {
    if (retentionMode === 'forever') return;
    if (!(await confirm.ask({ title: 'Run retention now', message: `Run the retention policy now?`, body: `This will prune ${retentionPreview?.would_prune ?? 'some'} audit rows. Pruned rows cannot be recovered; the chain-bridge entry stays intact.`, confirmLabel: 'Prune', danger: true }))) return;
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

  $effect(() => {
    loadAudit();
    loadRetention();
    loadWebhook();
  });
</script>

<section class="space-y-6">
  <div>
    <h2 class="text-2xl font-semibold tracking-tight">Audit Log</h2>
    <p class="text-sm text-[var(--fg-muted)] mt-0.5">
      Hash-chained record of every user action. Tamper-evident, exportable, and retention-configurable.
    </p>
  </div>

  <section class="space-y-4">
    {#if allowed('user.manage')}
      <Card class="p-4 space-y-3">
        <div class="flex items-start justify-between gap-3 flex-wrap">
          <div>
            <h3 class="text-sm font-semibold">Retention</h3>
            <p class="text-xs text-[var(--fg-muted)]">
              Older audit entries can be kept forever, pruned after N days, or archived before pruning.
            </p>
          </div>
          {#if retentionCfg?.mode !== 'forever' && retentionPreview}
            <div class="text-xs text-[var(--fg-muted)] text-right">
              {retentionPreview.total_rows} total rows
              {#if retentionPreview.would_prune > 0}
                · <span class="text-[var(--color-warning-400)]">{retentionPreview.would_prune} would prune</span>
              {:else}
                · nothing due
              {/if}
            </div>
          {/if}
        </div>
        <div class="grid sm:grid-cols-[160px_1fr] gap-3 items-start">
          <label class="text-xs text-[var(--fg-muted)]" for="ret-mode">Mode</label>
          <select id="ret-mode" class="dm-input max-w-xs" bind:value={retentionMode}>
            <option value="forever">Forever (default)</option>
            <option value="days">Keep last N days</option>
            <option value="archive_local">Archive locally, then prune</option>
            <option value="archive_target">Archive to backup target, then prune</option>
          </select>
          {#if retentionMode !== 'forever'}
            <label class="text-xs text-[var(--fg-muted)]" for="ret-days">Retention (days)</label>
            <input id="ret-days" type="number" min="1" class="dm-input max-w-xs" bind:value={retentionDays} />
          {/if}
          {#if retentionMode === 'archive_local'}
            <label class="text-xs text-[var(--fg-muted)]" for="ret-local-dir">Local directory</label>
            <input id="ret-local-dir" class="dm-input max-w-xs" placeholder="./data/audit-archive" bind:value={retentionLocalDir} />
          {/if}
          {#if retentionMode === 'archive_target'}
            <label class="text-xs text-[var(--fg-muted)]" for="ret-target">Backup target</label>
            <select id="ret-target" class="dm-input max-w-xs" bind:value={retentionTargetID}>
              <option value={0}>— pick one —</option>
              {#each retentionTargets as t}
                <option value={t.id}>{t.name} ({t.type})</option>
              {/each}
            </select>
          {/if}
        </div>
        <div class="flex items-center gap-2 flex-wrap">
          <Button variant="primary" onclick={saveRetention} disabled={retentionBusy}>
            {retentionBusy ? 'Saving…' : 'Save'}
          </Button>
          <Button variant="secondary" onclick={runRetentionNow} disabled={retentionBusy || retentionMode === 'forever'}>
            Run now
          </Button>
          {#if retentionLastResult}
            <span class="text-xs text-[var(--fg-muted)]">
              Last run: pruned {retentionLastResult.pruned}
              {#if retentionLastResult.archived} · archived to <code>{retentionLastResult.archive_path}</code>{/if}
            </span>
          {/if}
        </div>
      </Card>

      <Card class="p-4 space-y-3">
        <div>
          <h3 class="text-sm font-semibold">Webhook</h3>
          <p class="text-xs text-[var(--fg-muted)]">
            Stream every audit entry to an external URL. Payload is JSON; body is signed with HMAC-SHA256 on <code>X-Audit-Signature</code> when a secret is set.
          </p>
        </div>
        <div class="grid sm:grid-cols-[160px_1fr] gap-3 items-start">
          <label class="text-xs text-[var(--fg-muted)]" for="wh-url">Receiver URL</label>
          <input id="wh-url" class="dm-input" placeholder="https://siem.example.com/hook" bind:value={webhookURL} />
          <label class="text-xs text-[var(--fg-muted)]" for="wh-secret">
            HMAC secret
            {#if webhookCfg?.has_secret && !webhookClearSecret}<span class="font-normal normal-case block">— stored; leave blank to keep</span>{/if}
          </label>
          <div class="space-y-1">
            <input id="wh-secret" type="password" class="dm-input" placeholder={webhookCfg?.has_secret ? '••••••••' : 'Optional shared secret'} bind:value={webhookSecret} />
            {#if webhookCfg?.has_secret}
              <label class="flex items-center gap-1 text-xs text-[var(--fg-muted)]">
                <input type="checkbox" bind:checked={webhookClearSecret} /> Remove stored secret
              </label>
            {/if}
          </div>
          <label class="text-xs text-[var(--fg-muted)]" for="wh-filter">Filter actions</label>
          <input id="wh-filter" class="dm-input" placeholder="stack.*, user.manage (empty = all)" bind:value={webhookFilter} />
        </div>
        <div class="flex items-center gap-2 flex-wrap">
          <Button variant="primary" onclick={saveWebhook} disabled={webhookBusy}>
            {webhookBusy ? 'Saving…' : 'Save'}
          </Button>
          <Button variant="secondary" onclick={testWebhook} disabled={webhookBusy || !(webhookCfg?.url)}>
            Send test event
          </Button>
        </div>
      </Card>
    {/if}

    <div class="flex items-center gap-3 flex-wrap">
      <div class="relative flex-1 min-w-[180px] max-w-xs">
        <input type="search" placeholder="Search user, action, target…" bind:value={auditSearch} class="dm-input pl-3 pr-3 py-1.5 text-xs w-full" />
      </div>
      <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={auditActionFilter} onchange={loadAudit}>
        <option value="">All actions</option>
        <option value="auth">auth</option>
        <option value="stack">stack</option>
        <option value="container">container</option>
        <option value="image">image</option>
        <option value="user">user</option>
        <option value="oidc">oidc</option>
        <option value="network">network</option>
        <option value="volume">volume</option>
      </select>
      <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={auditLimit} onchange={loadAudit}>
        <option value={50}>50</option>
        <option value={100}>100</option>
        <option value={500}>500</option>
      </select>
      <Button size="sm" variant="secondary" onclick={loadAudit}>Refresh</Button>
      <Button size="sm" variant="secondary" loading={verifying} onclick={runVerify}>
        <Link2 class="w-3.5 h-3.5" />
        Verify chain
      </Button>
      <button
        class="dm-btn dm-btn-secondary dm-btn-sm"
        onclick={() => {
          const csv = ['Timestamp,Action,Target,User,Details']
            .concat(filteredAudit.map(e => `"${e.ts}","${e.action}","${e.target ?? ''}","${e.username ?? e.user_id ?? ''}","${(e.details ?? '').replace(/"/g, '""')}"`))
            .join('\n');
          const blob = new Blob([csv], { type: 'text/csv' });
          const a = document.createElement('a');
          a.href = URL.createObjectURL(blob);
          a.download = `dockmesh-audit-${new Date().toISOString().slice(0, 10)}.csv`;
          a.click();
        }}
      >Export CSV</button>
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">{filteredAudit.length} / {auditEntries.length}</span>
    </div>

    {#if verifyResult}
      <div class="dm-card p-4 {verifyResult.broken === 0 ? 'border-[color-mix(in_srgb,var(--color-success-500)_40%,transparent)]' : 'border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)]'}">
        <div class="flex items-center gap-2 text-sm font-medium">
          {#if verifyResult.broken === 0}
            <ShieldCheck class="w-4 h-4 text-[var(--color-success-400)]" />
            <span class="text-[var(--color-success-400)]">Chain intact</span>
          {:else}
            <ShieldOff class="w-4 h-4 text-[var(--color-danger-400)]" />
            <span class="text-[var(--color-danger-400)]">Chain broken</span>
          {/if}
        </div>
        <div class="text-xs text-[var(--fg-muted)] mt-2 space-y-1 font-mono">
          <div>verified: <span class="text-[var(--fg)]">{verifyResult.verified}</span></div>
          <div>broken: <span class="text-[var(--fg)]">{verifyResult.broken}</span></div>
          {#if verifyResult.first_break}
            <div>first break: row <span class="text-[var(--color-danger-400)]">{verifyResult.first_break}</span></div>
            <div>reason: <span class="text-[var(--color-danger-400)]">{verifyResult.break_reason}</span></div>
          {/if}
          <div class="pt-1">genesis: <span class="text-[var(--fg-subtle)] break-all">{verifyResult.genesis}</span></div>
          {#if verifyResult.warnings && verifyResult.warnings.length > 0}
            <div class="pt-1 text-[var(--color-warning-400)]">
              {verifyResult.warnings.length} legacy entries without chain
            </div>
          {/if}
        </div>
      </div>
    {/if}

    {#if auditLoading && auditEntries.length === 0}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each Array(6) as _}
            <div class="px-5 py-3 flex gap-3">
              <Skeleton width="10rem" height="0.85rem" />
              <Skeleton width="8rem" height="0.85rem" />
            </div>
          {/each}
        </div>
      </Card>
    {:else if auditEntries.length === 0}
      <Card>
        <EmptyState icon={Activity} title="No audit entries yet" description="Actions like login, deploy and delete will appear here." />
      </Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="text-left text-xs text-[var(--fg-muted)] uppercase tracking-wider bg-[var(--bg-elevated)]">
              <tr>
                <th class="px-5 py-3 font-medium">Timestamp</th>
                <th class="px-5 py-3 font-medium">Action</th>
                <th class="px-5 py-3 font-medium">Target</th>
                <th class="px-5 py-3 font-medium">User</th>
                <th class="px-5 py-3 font-medium">Details</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each filteredAudit as e}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-5 py-3 font-mono text-xs whitespace-nowrap text-[var(--fg-muted)]">{fmtTs(e.ts)}</td>
                  <td class="px-5 py-3">
                    <Badge variant={actionVariant(e.action)}>{e.action}</Badge>
                  </td>
                  <td class="px-5 py-3 font-mono text-xs truncate max-w-[200px]">{e.target ?? '—'}</td>
                  <td class="px-5 py-3 text-xs">{e.username || e.user_id?.slice(0, 8) || '—'}</td>
                  <td class="px-5 py-3 font-mono text-xs text-[var(--fg-subtle)] truncate max-w-[300px]">{e.details ?? ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}
  </section>
</section>
