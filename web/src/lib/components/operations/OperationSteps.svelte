<script lang="ts">
  import type { Operation } from '$lib/api';
  import { elapsed } from '$lib/operationsProgress';
  import { Check, X, Minus, TriangleAlert, Circle, LoaderCircle } from 'lucide-svelte';

  // Ordered steps of an operation as the backend reports them. A running
  // step without a measurable progress shows as running, never as a guess.
  let { op }: { op: Operation } = $props();
</script>

<ol class="steps">
  {#each op.steps as s (s.key)}
    <li class="st {s.status}">
      <span class="ic" aria-hidden="true">
        {#if s.status === 'succeeded'}<Check size={13} strokeWidth={2.4} />
        {:else if s.status === 'failed'}<X size={13} strokeWidth={2.4} />
        {:else if s.status === 'warning'}<TriangleAlert size={13} strokeWidth={2.2} />
        {:else if s.status === 'skipped' || s.status === 'cancelled'}<Minus size={13} strokeWidth={2.2} />
        {:else if s.status === 'running'}<LoaderCircle size={13} strokeWidth={2.2} class="spin" />
        {:else}<Circle size={9} strokeWidth={2} />{/if}
      </span>
      <div class="body">
        <div class="row">
          <span class="lbl">{s.label}</span>
          <span class="meta dm-num">
            {#if s.status === 'running' && s.progress != null}{Math.round(s.progress * 100)} %{:else if s.status !== 'pending' && s.status !== 'skipped'}{elapsed(s.started_at, s.finished_at)}{/if}
          </span>
        </div>
        {#if s.status === 'running'}
          <div class="bar" class:indet={s.progress == null} role="progressbar" aria-label={s.label}
            aria-valuemin={0} aria-valuemax={100} aria-valuenow={s.progress != null ? Math.round(s.progress * 100) : undefined}>
            <i style={s.progress != null ? `width:${Math.round(s.progress * 100)}%` : ''}></i>
          </div>
        {/if}
        {#if s.detail}<div class="detail" title={s.detail}>{s.detail}</div>{/if}
      </div>
    </li>
  {/each}
</ol>

<style>
  .steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
  .st { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 10px; padding: 8px 0; border-bottom: 1px solid var(--border); }
  .st:last-child { border-bottom: 0; }
  .ic { width: 20px; height: 20px; display: inline-flex; align-items: center; justify-content: center; color: var(--subtle); }
  .succeeded .ic { color: var(--ok); }
  .failed .ic { color: var(--bad); }
  .warning .ic { color: var(--warn); }
  .running .ic { color: var(--accent); }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  .lbl { flex: 1; min-width: 0; font-weight: 500; }
  .pending .lbl, .skipped .lbl { color: var(--subtle); font-weight: 400; }
  .cancelled .ic, .cancelled .lbl { color: var(--muted); }
  .failed .lbl { color: var(--bad); }
  .meta { color: var(--subtle); }
  .detail { color: var(--subtle); font-size: var(--fs-xs); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .failed .detail { color: var(--bad); white-space: normal; overflow-wrap: anywhere; }
  .bar { height: 4px; background: var(--field); margin-top: 6px; overflow: hidden; position: relative; }
  .bar i { display: block; height: 100%; background: var(--accent); transition: width 0.3s ease; }
  .bar.indet i { width: 30%; position: absolute; animation: slide 1.4s ease-in-out infinite; }
  @keyframes slide { 0% { left: -30%; } 100% { left: 100%; } }
  :global(.spin) { animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) {
    .bar.indet i { animation: none; left: 0; width: 100%; opacity: 0.4; }
    :global(.spin) { animation: none; }
  }
</style>
