<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { TriangleAlert } from 'lucide-svelte';
  import { pemCount, type TLSForm } from './_options';

  // How the proxy talks to one backend: the site's own upstreams or a
  // path route's other backend. `field` is the API field path of the
  // block, so server errors land next to their input.
  let {
    tls = $bindable(),
    host,
    field,
    idp,
    errors = {},
    readonly = false
  }: {
    tls: TLSForm;
    host: string;
    field: string;
    idp: string;
    errors?: Record<string, string>;
    readonly?: boolean;
  } = $props();

  const certs = $derived(pemCount(tls.caPEM));
</script>

{#snippet err(f: string)}
  {#if errors[`${field}.${f}`]}<span class="ferr" role="alert">{errors[`${field}.${f}`]}</span>{/if}
{/snippet}

<div class="tlsb">
  <div class="ck">
    <input id="{idp}-tls" type="checkbox" bind:checked={tls.tls} disabled={readonly} />
    <span class="ck-l">
      <label for="{idp}-tls">{m.proxy_tls_https()}</label>
      <span class="hint">{m.proxy_tls_https_hint()}</span>
    </span>
  </div>
  {@render err('tls')}
  {#if tls.tls}
    <div class="dm-field">
      <label for="{idp}-sni">{m.proxy_tls_sni()}</label>
      <input id="{idp}-sni" class="dm-input dm-mono" bind:value={tls.serverName} disabled={readonly} placeholder="backend.example.com" autocomplete="off" />
      <span class="hint">{m.proxy_tls_sni_hint()}</span>
      {@render err('server_name')}
    </div>
  {/if}
  <fieldset class="radios">
    <legend>{m.proxy_tls_host()}</legend>
    <label><input type="radio" bind:group={tls.hostHeader} value="keep" disabled={readonly} /> {m.proxy_tls_host_keep({ host })}</label>
    <label><input type="radio" bind:group={tls.hostHeader} value="upstream" disabled={readonly} /> {m.proxy_tls_host_upstream()}</label>
    {#if tls.hostHeader === 'upstream'}<span class="hint">{m.proxy_tls_host_upstream_hint()}</span>{/if}
    {@render err('host_header')}
  </fieldset>
  {#if tls.tls}
    <fieldset class="radios">
      <legend>{m.proxy_tls_check()}</legend>
      <label><input type="radio" bind:group={tls.check} value="public" disabled={readonly} /> {m.proxy_tls_check_public()}</label>
      <label><input type="radio" bind:group={tls.check} value="ca" disabled={readonly} /> {m.proxy_tls_check_ca()}</label>
      {#if tls.check === 'ca'}
        <textarea class="dm-input dm-mono ta" rows="5" bind:value={tls.caPEM} disabled={readonly} aria-label={m.proxy_tls_check_ca()}
          placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false"></textarea>
        {#if certs > 0}<span class="hint">{m.proxy_tls_ca_count({ count: certs })}</span>{/if}
        {@render err('ca_pem')}
      {/if}
      <label><input type="radio" bind:group={tls.check} value="skip" disabled={readonly} /> {m.proxy_tls_check_skip()}</label>
      {#if tls.check === 'skip'}
        <span class="risk"><TriangleAlert size={12} strokeWidth={1.8} /> {m.proxy_tls_skip_warn()}</span>
      {/if}
      {@render err('skip_verify')}
    </fieldset>
  {/if}
</div>

<style>
  .tlsb { display: flex; flex-direction: column; gap: 10px; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); }
  .ck { display: flex; align-items: flex-start; gap: 8px; }
  .ck input { margin-top: 3px; }
  .ck-l { display: flex; flex-direction: column; gap: 2px; }
  .ck-l label { font-size: var(--fs-sm); font-weight: 500; }
  .radios { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 4px; }
  .radios legend { font-size: 12px; color: var(--muted); font-weight: 500; margin-bottom: 4px; }
  .radios label { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); }
  .ta { resize: vertical; min-height: 0; }
  .risk { display: inline-flex; align-items: flex-start; gap: 4px; font-size: var(--fs-xs); color: var(--warn); }
  .risk :global(svg) { flex: none; margin-top: 2px; }
  .ferr { color: var(--bad); font-size: var(--fs-xs); }
</style>
