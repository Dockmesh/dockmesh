<script lang="ts">
  import { api } from '$lib/api';
  import { Drawer, StatusChip } from '$lib/components/ui';
  import { allowed } from '$lib/rbac.svelte';
  import { TLS_LABEL, certNeedsAttention, optionSummary, type Site } from './_site';

  // Quick view of one site: where it goes, its certificate, the options
  // it sets, live traffic and the Caddy config it produces.
  let {
    site = $bindable(null),
    onedit,
    ontoggle,
    ondelete
  }: {
    site: Site | null;
    onedit?: (s: Site) => void;
    ontoggle?: (s: Site) => void;
    ondelete?: (s: Site) => void;
  } = $props();

  let open = $state(false);
  $effect(() => {
    open = !!site;
  });

  type Metrics = { requests: number; requests_per_second: number; status_buckets: Record<string, number>; p95_latency_ms: number };
  let metrics = $state<Metrics | null>(null);
  let preview = $state<string | null>(null);
  let showPreview = $state(false);
  let previewErr = $state('');

  $effect(() => {
    const s = site;
    metrics = null;
    preview = null;
    showPreview = false;
    previewErr = '';
    if (!s) return;
    api.proxy.routeMetrics(s.id).then((m) => (metrics = m), () => (metrics = null));
  });

  async function loadPreview() {
    showPreview = !showPreview;
    if (!showPreview || preview || !site) return;
    try {
      const p = await api.proxy.routeConfig(site.id);
      preview = JSON.stringify(p.config, null, 2);
      if (p.blocked) previewErr = 'The stored options fail validation, so Caddy does not serve this site.';
    } catch (e) {
      previewErr = (e as Error).message;
    }
  }

  const opts = $derived(optionSummary(site?.options));
  const certTone = $derived(
    !site || site.tls_mode === 'none' ? 'off' : certNeedsAttention(site) ? 'warn' : 'ok'
  );
</script>

<Drawer bind:open title={site?.host ?? 'Site'} width={540} onclose={() => (site = null)}>
  {#if site}
    <div class="head">
      <StatusChip tone={site.enabled ? 'ok' : 'off'} label={site.enabled ? 'live' : 'disabled'} />
      <a class="dm-sub" href="{site.tls_mode === 'none' ? 'http' : 'https'}://{site.host}" target="_blank" rel="noopener">Open {site.host} ↗</a>
    </div>

    <h3 class="dm-h2">Overview</h3>
    <dl class="dm-kv">
      <dt>Goes to</dt><dd class="dm-mono">{site.upstream}</dd>
      <dt>HTTPS</dt><dd>{TLS_LABEL[site.tls_mode]}</dd>
      {#if site.tls_mode !== 'none'}
        <dt>Certificate</dt>
        <dd>
          {#if site.cert_valid_to}
            <StatusChip tone={certTone} label={certTone === 'ok' ? 'valid' : 'renew soon'} />
            <span class="dm-num">{site.cert_days_left} days left</span> · {site.cert_issuer || 'unknown issuer'}
          {:else}
            <span class="dm-dim">Not served yet — the name may not point here, or Caddy is still getting it (see Activity)</span>
          {/if}
        </dd>
      {/if}
      <dt>Options</dt><dd>{opts.length ? opts.join(', ') : 'Caddy defaults'}</dd>
      <dt>Created</dt><dd>{site.created_at ? new Date(site.created_at).toLocaleString() : '—'}</dd>
    </dl>

    <h3 class="dm-h2">Traffic</h3>
    {#if metrics && metrics.requests > 0}
      <dl class="dm-kv">
        <dt>Requests</dt><dd class="dm-num">{metrics.requests.toLocaleString()} · {metrics.requests_per_second.toFixed(1)} req/s</dd>
        <dt>p95 latency</dt><dd class="dm-num">{metrics.p95_latency_ms.toFixed(0)} ms</dd>
        <dt>Responses</dt>
        <dd class="dm-num">{#each ['2xx', '3xx', '4xx', '5xx'] as b}<span class="bk" class:bad={b === '5xx' && (metrics.status_buckets[b] ?? 0) > 0}>{b} {(metrics.status_buckets[b] ?? 0).toLocaleString()}</span>{/each}</dd>
      </dl>
    {:else}
      <p class="dm-sub">No requests since Caddy last started.</p>
    {/if}

    <h3 class="dm-h2"><button type="button" class="lnk" onclick={loadPreview}>{showPreview ? '▾' : '▸'} Config preview</button></h3>
    {#if showPreview}
      {#if previewErr}<p class="err">{previewErr}</p>{/if}
      {#if preview}<pre class="pv dm-mono">{preview}</pre>{:else if !previewErr}<p class="dm-sub">Loading…</p>{/if}
    {/if}
  {/if}

  {#snippet footer()}
    {#if site}
      {#if allowed('proxy.delete')}<button type="button" class="dm-btn dm-btn-danger" onclick={() => site && ondelete?.(site)}>Delete…</button>{/if}
      <span class="dm-sp"></span>
      {#if allowed('proxy.update')}
        <button type="button" class="dm-btn" onclick={() => site && ontoggle?.(site)}>{site.enabled ? 'Disable' : 'Enable'}</button>
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => site && onedit?.(site)}>Edit</button>
      {/if}
    {/if}
  {/snippet}
</Drawer>

<style>
  .head { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; }
  .dm-h2 { margin-top: 18px; }
  .bk { margin-right: 10px; }
  .bk.bad { color: var(--bad); }
  .lnk { all: unset; cursor: pointer; }
  .lnk:focus-visible { outline: 2px solid var(--accent); }
  .pv { max-height: 320px; overflow: auto; background: var(--field); padding: 8px 10px; border-radius: var(--r-sm); font-size: 11.5px; margin: 0; }
  .err { color: var(--bad); font-size: var(--fs-sm); }
</style>
