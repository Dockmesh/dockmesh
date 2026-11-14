<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { TriangleAlert, Plus, X } from 'lucide-svelte';
  import type { TLSMode } from './_site';
  import { GROUPS, warnings, isDefault, groupOfField, type OptionsForm, type GroupKey, type HeaderRow } from './_options';
  import UpstreamTLSFields from './_UpstreamTLSFields.svelte';
  import FallbackFields from './_FallbackFields.svelte';

  // Per-site options, grouped. With `collapsible` each group is a closed
  // section unless it differs from Caddy's defaults or has an error.
  let {
    form = $bindable(),
    tls,
    host,
    errors = {},
    readonly = false,
    collapsible = false
  }: {
    form: OptionsForm;
    tls: TLSMode;
    host: string;
    errors?: Record<string, string>;
    readonly?: boolean;
    collapsible?: boolean;
  } = $props();

  const warn = $derived(Object.fromEntries(warnings(form, tls).map((w) => [w.field, w.text])));
  const errGroups = $derived(new Set(Object.keys(errors).map(groupOfField).filter(Boolean) as GroupKey[]));
  const ownFwdPort = $derived(form.headers.requestSet.some((h) => h.name.trim().toLowerCase() === 'x-forwarded-port'));
  const wwwPossible = $derived(!/^(www\.|\*\.)/i.test(host) && !/^[\d.:[\]]+$/.test(host));

  const SECRET_HINT = m.proxy_oe_secret_hint();
  // Keeps password managers from storing these as a Dockmesh login.
  const NO_MANAGER = { 'data-1p-ignore': '', 'data-lpignore': 'true', 'data-bwignore': '' };

  function addHeader(list: HeaderRow[]) {
    list.push({ name: '', value: '', secret: false, valueSet: false });
  }
</script>

{#snippet err(field: string)}
  {#if errors[field]}<span class="ferr" role="alert">{errors[field]}</span>{/if}
{/snippet}

{#snippet risk(field: string, text: string)}
  <span class="risk" class:on={!!warn[field]}><TriangleAlert size={12} strokeWidth={1.8} /> {warn[field] ?? text}</span>
{/snippet}

{#snippet check(id: string, label: string, hint: string)}
  <span class="ck-l"><label for={id}>{label}</label><span class="hint">{hint}</span></span>
{/snippet}

{#snippet headerRows(list: HeaderRow[], field: string, label: string, secrets: boolean)}
  <div class="dm-field wide">
    <span class="lbl">{label}</span>
    {#each list as h, i (i)}
      <div class="hrow" class:nosecret={!secrets}>
        <input class="dm-input dm-mono" bind:value={h.name} placeholder={m.proxy_oe_header_name_ph()} aria-label={m.proxy_oe_row_name({ label })} disabled={readonly} autocomplete="off" />
        {#if h.secret}
          <input class="dm-input dm-mono" type="password" bind:value={h.value} aria-label={m.proxy_oe_row_value({ label })}
            placeholder={h.valueSet ? SECRET_HINT : m.proxy_oe_value_ph()} disabled={readonly} autocomplete="new-password" {...NO_MANAGER} />
        {:else}
          <input class="dm-input dm-mono" bind:value={h.value} placeholder={m.proxy_oe_value_ph()} aria-label={m.proxy_oe_row_value({ label })} disabled={readonly} autocomplete="off" />
        {/if}
        {#if secrets}<label class="chk sm" title={h.secret && h.valueSet && !h.value ? m.proxy_oe_secret_reenter() : m.proxy_oe_secret_writeonly()}>
          <input type="checkbox" bind:checked={h.secret} disabled={readonly || (h.secret && h.valueSet && !h.value)} /> {m.proxy_oe_secret()}
        </label>{/if}
        {#if !readonly}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-label={m.proxy_oe_remove_header()} onclick={() => list.splice(i, 1)}><X size={13} /></button>
        {/if}
      </div>
    {/each}
    {#if !readonly}
      <button type="button" class="dm-btn dm-btn-sm" onclick={() => addHeader(list)}><Plus size={13} /> {m.proxy_oe_add_header()}</button>
    {:else if !list.length}<span class="dm-dim">{m.common_none()}</span>{/if}
    {@render err(field)}
  </div>
{/snippet}

{#snippet body(key: GroupKey)}
  {#if key === 'routing'}
    <div class="dm-field wide">
      <label for="o-up">{m.proxy_oe_more_upstreams()}</label>
      <textarea id="o-up" class="dm-input dm-mono ta" rows="2" bind:value={form.routing.extraUpstreams} disabled={readonly} placeholder={m.proxy_oe_more_upstreams_ph()}></textarea>
      <span class="hint">{m.proxy_oe_more_upstreams_hint()}</span>
      {@render err('routing.extra_upstreams')}
    </div>
    <div class="dm-field">
      <label for="o-lb">{m.proxy_oe_lb()}</label>
      <select id="o-lb" class="dm-input" bind:value={form.routing.lbPolicy} disabled={readonly}>
        <option value="">{m.proxy_oe_lb_random()}</option>
        <option value="round_robin">{m.proxy_oe_lb_round_robin()}</option>
        <option value="least_conn">{m.proxy_oe_lb_least_conn()}</option>
        <option value="first">{m.proxy_oe_lb_first()}</option>
        <option value="client_ip_hash">{m.proxy_oe_lb_ip_hash()}</option>
      </select>
      <span class="hint">{m.proxy_oe_lb_hint()}</span>
      {@render err('routing.lb_policy')}
    </div>
    <div class="dm-field">
      <label for="o-hp">{m.proxy_oe_health_path()}</label>
      <input id="o-hp" class="dm-input dm-mono" bind:value={form.routing.healthPath} disabled={readonly} placeholder="/health" autocomplete="off" />
      {@render risk('routing.health_path', m.proxy_oe_health_risk())}
      {@render err('routing.health_path')}
    </div>
    <div class="ck">
      <input id="o-www" type="checkbox" bind:checked={form.routing.wwwRedirect} disabled={readonly || (!wwwPossible && !form.routing.wwwRedirect)} />
      {@render check('o-www', m.proxy_oe_www({ host }), wwwPossible ? m.proxy_oe_www_hint({ host }) : m.proxy_oe_www_na())}
    </div>
    {@render err('routing.www_redirect')}
    <UpstreamTLSFields bind:tls={form.routing.upstreamTLS} {host} field="routing.upstream_tls" idp="o-ut" {errors} {readonly} />
    <FallbackFields bind:fb={form.routing.fallback} idp="o-fb" {errors} warning={warn['routing.fallback'] ?? ''} {readonly} />
  {:else if key === 'https'}
    <div class="ck">
      <input id="o-ah" type="checkbox" bind:checked={form.https.allowHTTP} disabled={readonly || tls === 'none'} />
      <span class="ck-l">
        <label for="o-ah">{m.proxy_oe_allow_http()}</label>
        <span class="hint">{tls === 'none' ? m.proxy_oe_allow_http_already() : m.proxy_oe_allow_http_hint()}</span>
        {#if tls !== 'none'}{@render risk('https.allow_http', m.proxy_oe_allow_http_risk())}{/if}
      </span>
    </div>
    {@render err('https.allow_http')}
    <div class="ck">
      <input id="o-hsts" type="checkbox" bind:checked={form.https.hsts} disabled={readonly || (tls === 'none' && !form.https.hsts)} />
      <span class="ck-l">
        <label for="o-hsts">HSTS</label>
        <span class="hint">{tls === 'none' ? m.proxy_oe_hsts_needs() : m.proxy_oe_hsts_hint()}</span>
        {@render risk('https.hsts', m.proxy_oe_hsts_risk())}
      </span>
    </div>
    {@render err('https.hsts')}
  {:else if key === 'performance'}
    <div class="ck">
      <input id="o-fl" type="checkbox" bind:checked={form.performance.flushImmediately} disabled={readonly} />
      {@render check('o-fl', m.proxy_oe_stream(), m.proxy_oe_stream_hint())}
    </div>
    <div class="ck">
      <input id="o-br" type="checkbox" bind:checked={form.performance.bufferRequests} disabled={readonly} />
      <span class="ck-l">
        <label for="o-br">{m.proxy_oe_buffer()}</label>
        <span class="hint">{m.proxy_oe_buffer_hint()}</span>
        {@render risk('performance.buffer_requests', m.proxy_oe_buffer_risk())}
      </span>
    </div>
    <div class="grid3">
      {#each [['o-dt', m.proxy_oe_dial_timeout(), 'dialTimeout', 'performance.dial_timeout', '10s'], ['o-rt', m.proxy_oe_read_timeout(), 'readTimeout', 'performance.read_timeout', m.proxy_oe_no_limit()], ['o-wt', m.proxy_oe_write_timeout(), 'writeTimeout', 'performance.write_timeout', m.proxy_oe_no_limit()]] as [id, label, k, field, ph] (id)}
        <div class="dm-field">
          <label for={id}>{label}</label>
          <input {id} class="dm-input dm-mono" bind:value={form.performance[k as 'dialTimeout']} disabled={readonly} placeholder={ph} autocomplete="off" />
          {@render err(field)}
        </div>
      {/each}
    </div>
    <span class="hint">{m.proxy_oe_timeouts_hint()}</span>
    <div class="grid3">
      <div class="dm-field">
        <label for="o-mb">{m.proxy_oe_max_body()}</label>
        <div class="size">
          <input id="o-mb" class="dm-input dm-num" inputmode="decimal" bind:value={form.performance.maxBody} disabled={readonly} placeholder={m.proxy_oe_no_limit()} autocomplete="off" />
          <select class="dm-input" bind:value={form.performance.maxBodyUnit} disabled={readonly} aria-label={m.proxy_oe_unit()}>
            <option value="B">{m.proxy_oe_bytes()}</option><option value="MB">MB</option><option value="GB">GB</option>
          </select>
        </div>
        <span class="hint">{m.proxy_oe_max_body_hint()}</span>
        {@render err('performance.max_body_bytes')}
      </div>
      <div class="dm-field">
        <label for="o-sc">{m.proxy_oe_stream_close()}</label>
        <input id="o-sc" class="dm-input dm-mono" bind:value={form.performance.streamCloseDelay} disabled={readonly} placeholder={m.proxy_oe_stream_close_ph()} autocomplete="off" />
        <span class="hint">{m.proxy_oe_stream_close_hint()}</span>
        {@render err('performance.stream_close_delay')}
      </div>
    </div>
    <div class="ck">
      <input id="o-cz" type="checkbox" bind:checked={form.performance.compression} disabled={readonly} />
      {@render check('o-cz', m.proxy_oe_compress(), m.proxy_oe_compress_hint())}
    </div>
  {:else if key === 'access'}
    <div class="dm-field wide">
      <span class="lbl">{m.proxy_oe_password()}</span>
      {#each form.access.users as u, i (i)}
        <div class="hrow">
          <input class="dm-input" bind:value={u.username} placeholder={m.proxy_oe_username_ph()} aria-label={m.proxy_oe_username()} disabled={readonly} autocomplete="off" />
          <input class="dm-input" type="password" bind:value={u.password} aria-label={m.proxy_oe_password_for({ user: u.username || m.proxy_oe_new_user() })}
            placeholder={u.stored ? SECRET_HINT : m.proxy_oe_password_ph()} disabled={readonly} autocomplete="new-password" {...NO_MANAGER} />
          {#if !readonly}
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-label={m.proxy_oe_remove_user()} onclick={() => form.access.users.splice(i, 1)}><X size={13} /></button>
          {/if}
        </div>
      {/each}
      {#if !readonly}
        <button type="button" class="dm-btn dm-btn-sm" onclick={() => form.access.users.push({ username: '', password: '', stored: false })}><Plus size={13} /> {m.proxy_oe_add_user()}</button>
      {:else if !form.access.users.length}<span class="dm-dim">{m.common_off()}</span>{/if}
      <span class="hint">{m.proxy_oe_password_hint()}</span>
      {#if warn['access.basic_auth']}{@render risk('access.basic_auth', '')}{/if}
      {@render err('access.basic_auth')}
    </div>
    <div class="dm-field">
      <label for="o-ipm">{m.proxy_oe_ip()}</label>
      <select id="o-ipm" class="dm-input" bind:value={form.access.ipMode} disabled={readonly}>
        <option value="">{m.proxy_oe_ip_everyone()}</option>
        <option value="allow">{m.proxy_oe_ip_allow()}</option>
        <option value="deny">{m.proxy_oe_ip_deny()}</option>
      </select>
      {#if form.access.ipMode === 'allow'}{@render risk('access.ip_mode', '')}{/if}
      {@render err('access.ip_mode')}
    </div>
    {#if form.access.ipMode}
      <div class="dm-field wide">
        <label for="o-ipr">{m.proxy_oe_addresses()}</label>
        <textarea id="o-ipr" class="dm-input dm-mono ta" rows="3" bind:value={form.access.ipRanges} disabled={readonly} placeholder={m.proxy_oe_addresses_ph()}></textarea>
        <span class="hint">{m.proxy_oe_addresses_hint()}</span>
        {@render err('access.ip_ranges')}
      </div>
    {/if}
    <div class="ck">
      <input id="o-fa" type="checkbox" bind:checked={form.access.forwardAuth} disabled={readonly} />
      <span class="ck-l">
        <label for="o-fa">{m.proxy_oe_fa()}</label>
        <span class="hint">{m.proxy_oe_fa_hint()}</span>
        {@render risk('access.forward_auth', m.proxy_oe_fa_risk())}
      </span>
    </div>
    {#if form.access.forwardAuth}
      <div class="dm-field wide">
        <label for="o-fau">{m.proxy_oe_fa_url()}</label>
        <input id="o-fau" class="dm-input dm-mono" bind:value={form.access.forwardAuthURL} disabled={readonly} placeholder="http://authelia:9091/api/verify" autocomplete="off" />
        {#if form.access.querySet}
          <span class="hint">{form.access.clearQuery ? m.proxy_oe_fa_query_removed() : m.proxy_oe_fa_query_kept()}</span>
          {#if !readonly}<label class="chk sm"><input type="checkbox" bind:checked={form.access.clearQuery} /> {m.proxy_oe_fa_query_remove()}</label>{/if}
        {/if}
        {@render err('access.forward_auth.url')}
      </div>
      <div class="dm-field wide">
        <label for="o-fah">{m.proxy_oe_fa_copy()}</label>
        <input id="o-fah" class="dm-input dm-mono" bind:value={form.access.copyHeaders} disabled={readonly} placeholder="Remote-User, Remote-Groups" autocomplete="off" />
        {@render err('access.forward_auth.copy_headers')}
      </div>
    {/if}
  {:else if key === 'headers'}
    <div class="ck">
      <input id="o-sh" type="checkbox" bind:checked={form.headers.security} disabled={readonly} />
      {@render check('o-sh', m.proxy_oe_security(), m.proxy_oe_security_hint())}
    </div>
    <div class="ck">
      <input id="o-fp" type="checkbox" bind:checked={form.headers.forwardedPort} disabled={readonly} />
      <span class="ck-l">
        <label for="o-fp">{m.proxy_fwd_port()}</label>
        <span class="hint">{m.proxy_fwd_port_hint()}</span>
        <span class="hint">{m.proxy_fwd_port_override_hint()}</span>
        {#if form.headers.forwardedPort && ownFwdPort}<span class="note">{m.proxy_fwd_port_replaced()}</span>{/if}
      </span>
    </div>
    {@render headerRows(form.headers.requestSet, 'headers.request_set', m.proxy_oe_req_set(), true)}
    <div class="dm-field wide">
      <label for="o-rqr">{m.proxy_oe_req_remove()}</label>
      <input id="o-rqr" class="dm-input dm-mono" bind:value={form.headers.requestRemove} disabled={readonly} placeholder="X-Debug, Cookie" autocomplete="off" />
      {@render err('headers.request_remove')}
    </div>
    {@render headerRows(form.headers.responseSet, 'headers.response_set', m.proxy_oe_resp_set(), false)}
    <div class="dm-field wide">
      <label for="o-rsr">{m.proxy_oe_resp_remove()}</label>
      <input id="o-rsr" class="dm-input dm-mono" bind:value={form.headers.responseRemove} disabled={readonly} placeholder="Server, X-Powered-By" autocomplete="off" />
      {@render err('headers.response_remove')}
    </div>
    <span class="hint">{m.proxy_oe_secret_note()}</span>
  {:else if key === 'logs'}
    <div class="ck">
      <input id="o-al" type="checkbox" bind:checked={form.logs.accessLog} disabled={readonly} />
      {@render check('o-al', m.proxy_oe_access_log(), m.proxy_oe_access_log_hint())}
    </div>
  {/if}
{/snippet}

<div class="oe">
  {#each GROUPS as g (g.key)}
    {@const def = isDefault(form, g.key)}
    {#if collapsible}
      <details class="grp" open={!def || errGroups.has(g.key)}>
        <summary><b>{g.label}</b> <span class="dm-dim">{def ? m.proxy_oe_defaults() : m.proxy_oe_changed()}</span>{#if errGroups.has(g.key)}<span class="ferr"> · {m.proxy_oe_check_group()}</span>{/if}</summary>
        <div class="fields">{@render body(g.key)}</div>
      </details>
    {:else}
      <div class="dm-form-sec" id="opt-{g.key}">
        <div>
          <h4>{g.label}{#if errGroups.has(g.key)} <span class="ferr">· {m.proxy_oe_check_group()}</span>{/if}</h4>
          <p>{g.hint}</p>
          {#if def}<p class="dm-dim">{m.proxy_oe_defaults()}</p>{/if}
        </div>
        <div class="fields">{@render body(g.key)}</div>
      </div>
    {/if}
  {/each}
</div>

<style>
  .oe { display: flex; flex-direction: column; }
  .fields { display: flex; flex-direction: column; gap: 10px; align-items: stretch !important; max-width: 720px; width: 100%; }
  .dm-field { max-width: 720px; }
  .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); }
  .ck { display: flex; align-items: flex-start; gap: 8px; }
  .ck input { margin-top: 3px; }
  .ck-l { display: flex; flex-direction: column; gap: 2px; }
  .ck-l label { font-size: var(--fs-sm); font-weight: 500; }
  .chk { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); white-space: nowrap; }
  .chk.sm { font-size: var(--fs-xs); color: var(--muted); }
  .hrow { display: grid; grid-template-columns: minmax(0, 220px) minmax(0, 1fr) auto auto; gap: 6px; align-items: center; }
  .hrow.nosecret { grid-template-columns: minmax(0, 220px) minmax(0, 1fr) auto; }
  .grid3 { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
  .size { display: grid; grid-template-columns: minmax(0, 1fr) 84px; gap: 6px; }
  .ta { resize: vertical; min-height: 0; }
  .risk { display: inline-flex; align-items: flex-start; gap: 4px; font-size: var(--fs-xs); color: var(--subtle); }
  .risk :global(svg) { flex: none; margin-top: 2px; color: var(--warn); }
  .risk.on { color: var(--warn); }
  .ferr { color: var(--bad); font-size: var(--fs-xs); font-weight: 400; }
  .note { font-size: var(--fs-xs); color: var(--muted); font-style: italic; }
  .grp { border: 1px solid var(--border); border-radius: var(--r); padding: 0 12px; }
  .grp + .grp { margin-top: 6px; }
  .grp summary { cursor: pointer; padding: 9px 0; font-size: var(--fs-sm); }
  .grp[open] summary { border-bottom: 1px solid var(--border); margin-bottom: 10px; }
  .grp .fields { padding-bottom: 12px; }
  @media (max-width: 900px) { .grid3 { grid-template-columns: minmax(0, 1fr); } }
</style>
