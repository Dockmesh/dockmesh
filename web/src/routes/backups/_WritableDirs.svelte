<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError } from '$lib/api';
  import type { UnitRewriteState } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { AlertTriangle } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  let st = $state<UnitRewriteState | null>(null);
  let restarting = $state(false);

  const readOnly = $derived(st?.dirs.filter((d) => d.state === 'read_only') ?? []);
  // A default root nobody writes to may well not exist; only report the
  // ones a target needs.
  const blocked = $derived(st?.dirs.filter((d) => d.in_use && (d.state === 'missing' || d.state === 'permission_denied')) ?? []);
  const canRewrite = $derived(!!st && !st.unavailable && !st.restart_pending);

  // refresh re-checks the directories; with prompt it offers the restart
  // right away, as after saving a local target that can't be written yet.
  export async function refresh(prompt = false) {
    if (!allowed('system.upgrade')) return;
    try {
      st = await api.system.unitRewriteState();
    } catch {
      st = null;
      return;
    }
    if (prompt && readOnly.length > 0 && canRewrite) await allowWriting();
  }

  async function allowWriting() {
    const paths = readOnly.map((d) => d.path).join(', ');
    if (!(await confirm.ask({
      title: m.backups_unit_confirm_title(),
      message: m.backups_unit_confirm_message({ paths }),
      body: m.backups_unit_confirm_body(),
      confirmLabel: m.backups_unit_confirm_action()
    }))) return;
    try {
      st = await api.system.startUnitRewrite();
    } catch (err) {
      toast.error(m.backups_unit_failed(), err instanceof ApiError ? err.message : undefined);
      return;
    }
    restarting = true;
    toast.info(m.backups_unit_restarting());
    await waitForRestart();
    restarting = false;
  }

  // The server goes away for the restart; the answer after it tells
  // whether root rewrote the unit or put the old one back.
  async function waitForRestart() {
    const deadline = Date.now() + 5 * 60_000;
    let wentAway = false;
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 2000));
      let next: UnitRewriteState;
      try {
        next = await api.system.unitRewriteState();
      } catch {
        wentAway = true;
        continue;
      }
      st = next;
      if (next.phase === 'failed') {
        toast.error(m.backups_unit_failed(), next.error);
        return;
      }
      if (next.restart_pending) continue;
      if (next.last_result && next.last_result.status !== 'done') {
        toast.error(m.backups_unit_failed(), next.last_result.reason);
        return;
      }
      if (wentAway || next.last_result) {
        if (next.dirs.some((d) => d.state === 'read_only')) toast.error(m.backups_unit_still_read_only());
        else toast.success(m.backups_unit_done());
        return;
      }
    }
    toast.error(m.backups_unit_failed(), m.backups_unit_timeout());
  }

  onMount(() => { void refresh(); });
</script>

{#if readOnly.length > 0 || blocked.length > 0}
  <div class="attn" role="status" data-testid="backup-dirs-not-writable">
    <div class="attn-h">
      <AlertTriangle size={14} strokeWidth={1.8} />
      <b>{m.backups_unit_title()}</b>
      <span class="dm-dim">· {m.backups_unit_hint()}</span>
      <span class="dm-sp"></span>
      {#if readOnly.length > 0 && canRewrite}
        <button type="button" class="dm-btn dm-btn-sm" onclick={allowWriting} disabled={restarting}>
          {restarting ? m.backups_unit_restarting_short() : m.backups_unit_allow()}
        </button>
      {/if}
    </div>
    {#each readOnly as d (d.path)}
      <div class="attn-row">
        <span class="dm-mono dm-trunc" title={d.path}>{d.path}</span>
        <span class="dm-sp"></span>
        <span class="dm-dim">
          {#if st?.unavailable}{m.backups_unit_read_only_manual()}{:else}{m.backups_unit_read_only()}{/if}
        </span>
      </div>
    {/each}
    {#each blocked as d (d.path)}
      <div class="attn-row">
        <span class="dm-mono dm-trunc" title={d.path}>{d.path}</span>
        <span class="dm-sp"></span>
        <span class="dm-dim dm-mono">
          {d.state === 'missing'
            ? m.backups_unit_missing({ path: d.path })
            : m.backups_unit_permission({ path: d.path })}
        </span>
      </div>
    {/each}
  </div>
{/if}

<style>
  .attn { border: 1px solid color-mix(in srgb, var(--warn) 40%, transparent); background: color-mix(in srgb, var(--warn) 6%, transparent); border-radius: var(--r); padding: 10px 12px; display: flex; flex-direction: column; gap: 6px; }
  .attn-h { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: var(--fs-sm); }
  .attn-h :global(svg) { color: var(--warn); }
  .attn-row { display: flex; align-items: center; gap: 10px; min-height: 32px; font-size: var(--fs-sm); min-width: 0; }
</style>
