<script lang="ts">
  import { TriangleAlert, Plus, X } from 'lucide-svelte';
  import type { TLSMode } from './_site';
  import { GROUPS, warnings, isDefault, groupOfField, type OptionsForm, type GroupKey, type HeaderRow } from './_options';

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
  const wwwPossible = $derived(!/^(www\.|\*\.)/i.test(host) && !/^[\d.:[\]]+$/.test(host));

  const SECRET_HINT = 'set — leave empty to keep';

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

{#snippet headerRows(list: HeaderRow[], field: string, label: string)}
  <div class="dm-field wide">
    <span class="lbl">{label}</span>
    {#each list as h, i (i)}
      <div class="hrow">
        <input class="dm-input dm-mono" bind:value={h.name} placeholder="Header-Name" aria-label="{label} name" disabled={readonly} autocomplete="off" />
        {#if h.secret}
          <input class="dm-input dm-mono" type="password" bind:value={h.value} aria-label="{label} value"
            placeholder={h.valueSet ? SECRET_HINT : 'value'} disabled={readonly} autocomplete="new-password" />
        {:else}
          <input class="dm-input dm-mono" bind:value={h.value} placeholder="value" aria-label="{label} value" disabled={readonly} autocomplete="off" />
        {/if}
        <label class="chk sm" title={h.secret && h.valueSet && !h.value ? 'Enter the value again to make it visible' : 'Write-only: the value is never shown again, also not in the config preview'}>
          <input type="checkbox" bind:checked={h.secret} disabled={readonly || (h.secret && h.valueSet && !h.value)} /> Secret
        </label>
        {#if !readonly}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-label="Remove header" onclick={() => list.splice(i, 1)}><X size={13} /></button>
        {/if}
      </div>
    {/each}
    {#if !readonly}
      <button type="button" class="dm-btn dm-btn-sm" onclick={() => addHeader(list)}><Plus size={13} /> Add header</button>
    {:else if !list.length}<span class="dm-dim">None</span>{/if}
    {@render err(field)}
  </div>
{/snippet}

{#snippet body(key: GroupKey)}
  {#if key === 'routing'}
    <div class="dm-field wide">
      <label for="o-up">More upstreams</label>
      <textarea id="o-up" class="dm-input dm-mono ta" rows="2" bind:value={form.routing.extraUpstreams} disabled={readonly} placeholder="app2:8080 (one host:port per line)"></textarea>
      <span class="hint">Requests are shared between the site's destination and these.</span>
      {@render err('routing.extra_upstreams')}
    </div>
    <div class="dm-field">
      <label for="o-lb">Load balancing</label>
      <select id="o-lb" class="dm-input" bind:value={form.routing.lbPolicy} disabled={readonly}>
        <option value="">Random (Caddy default)</option>
        <option value="round_robin">Round robin</option>
        <option value="least_conn">Fewest connections</option>
        <option value="first">First available</option>
        <option value="client_ip_hash">Same client, same upstream</option>
      </select>
      <span class="hint">How Caddy picks an upstream when there is more than one.</span>
      {@render err('routing.lb_policy')}
    </div>
    <div class="dm-field">
      <label for="o-hp">Health check path</label>
      <input id="o-hp" class="dm-input dm-mono" bind:value={form.routing.healthPath} disabled={readonly} placeholder="/health" autocomplete="off" />
      {@render risk('routing.health_path', 'Upstreams that fail the check get no traffic.')}
      {@render err('routing.health_path')}
    </div>
    <div class="ck">
      <input id="o-www" type="checkbox" bind:checked={form.routing.wwwRedirect} disabled={readonly || (!wwwPossible && !form.routing.wwwRedirect)} />
      {@render check('o-www', `Redirect www.${host}`, wwwPossible ? `Also answer www.${host} and send visitors on to ${host}.` : 'Not available for wildcard, IP or www. names.')}
    </div>
    {@render err('routing.www_redirect')}
  {:else if key === 'https'}
    <div class="ck">
      <input id="o-ah" type="checkbox" bind:checked={form.https.allowHTTP} disabled={readonly || tls === 'none'} />
      <span class="ck-l">
        <label for="o-ah">Also serve plain HTTP</label>
        <span class="hint">{tls === 'none' ? 'The site is HTTP only already.' : 'Answer on http:// too instead of redirecting to https://.'}</span>
        {#if tls !== 'none'}{@render risk('https.allow_http', 'Traffic over http:// is not encrypted.')}{/if}
      </span>
    </div>
    {@render err('https.allow_http')}
    <div class="ck">
      <input id="o-hsts" type="checkbox" bind:checked={form.https.hsts} disabled={readonly || (tls === 'none' && !form.https.hsts)} />
      <span class="ck-l">
        <label for="o-hsts">HSTS</label>
        <span class="hint">{tls === 'none' ? 'Needs HTTPS.' : 'Tell browsers to use HTTPS only for this name (one year).'}</span>
        {@render risk('https.hsts', 'Hard to undo: browsers keep it for a year.')}
      </span>
    </div>
    {@render err('https.hsts')}
  {:else if key === 'performance'}
    <div class="ck">
      <input id="o-fl" type="checkbox" bind:checked={form.performance.flushImmediately} disabled={readonly} />
      {@render check('o-fl', 'Stream responses', 'Pass every chunk on at once. For server-sent events, live logs and streaming downloads.')}
    </div>
    <div class="ck">
      <input id="o-br" type="checkbox" bind:checked={form.performance.bufferRequests} disabled={readonly} />
      <span class="ck-l">
        <label for="o-br">Buffer uploads</label>
        <span class="hint">Read up to 4 MiB of a request before sending it on. Off streams uploads straight through.</span>
        {@render risk('performance.buffer_requests', 'Uses memory for every request.')}
      </span>
    </div>
    <div class="grid3">
      {#each [['o-dt', 'Connect timeout', 'dialTimeout', 'performance.dial_timeout', '10s'], ['o-rt', 'Read timeout', 'readTimeout', 'performance.read_timeout', 'no limit'], ['o-wt', 'Write timeout', 'writeTimeout', 'performance.write_timeout', 'no limit']] as [id, label, k, field, ph] (id)}
        <div class="dm-field">
          <label for={id}>{label}</label>
          <input {id} class="dm-input dm-mono" bind:value={form.performance[k as 'dialTimeout']} disabled={readonly} placeholder={ph} autocomplete="off" />
          {@render err(field)}
        </div>
      {/each}
    </div>
    <span class="hint">Durations like 30s, 5m or 1h (up to 168h). Empty keeps Caddy's default; long-running requests need a longer read timeout.</span>
    <div class="grid3">
      <div class="dm-field">
        <label for="o-mb">Largest upload</label>
        <div class="size">
          <input id="o-mb" class="dm-input dm-num" inputmode="decimal" bind:value={form.performance.maxBody} disabled={readonly} placeholder="no limit" autocomplete="off" />
          <select class="dm-input" bind:value={form.performance.maxBodyUnit} disabled={readonly} aria-label="Unit">
            <option value="B">bytes</option><option value="MB">MB</option><option value="GB">GB</option>
          </select>
        </div>
        <span class="hint">Bigger requests get 413. Empty or 0: no limit.</span>
        {@render err('performance.max_body_bytes')}
      </div>
      <div class="dm-field">
        <label for="o-sc">Keep streams on reload</label>
        <input id="o-sc" class="dm-input dm-mono" bind:value={form.performance.streamCloseDelay} disabled={readonly} placeholder="close at once" autocomplete="off" />
        <span class="hint">WebSockets stay open this long after a config change, e.g. 5m.</span>
        {@render err('performance.stream_close_delay')}
      </div>
    </div>
    <div class="ck">
      <input id="o-cz" type="checkbox" bind:checked={form.performance.compression} disabled={readonly} />
      {@render check('o-cz', 'Compress responses', 'zstd or gzip, when the browser supports it.')}
    </div>
  {:else if key === 'access'}
    <div class="dm-field wide">
      <span class="lbl">Password protection</span>
      {#each form.access.users as u, i (i)}
        <div class="hrow">
          <input class="dm-input" bind:value={u.username} placeholder="user name" aria-label="User name" disabled={readonly} autocomplete="off" />
          <input class="dm-input" type="password" bind:value={u.password} aria-label="Password for {u.username || 'new user'}"
            placeholder={u.stored ? SECRET_HINT : 'password, 8 characters or more'} disabled={readonly} autocomplete="new-password" />
          {#if !readonly}
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-label="Remove user" onclick={() => form.access.users.splice(i, 1)}><X size={13} /></button>
          {/if}
        </div>
      {/each}
      {#if !readonly}
        <button type="button" class="dm-btn dm-btn-sm" onclick={() => form.access.users.push({ username: '', password: '', stored: false })}><Plus size={13} /> Add user</button>
      {:else if !form.access.users.length}<span class="dm-dim">Off</span>{/if}
      <span class="hint">Browsers ask for one of these logins. Passwords are stored as hashes and never shown.</span>
      {#if warn['access.basic_auth']}{@render risk('access.basic_auth', '')}{/if}
      {@render err('access.basic_auth')}
    </div>
    <div class="dm-field">
      <label for="o-ipm">IP addresses</label>
      <select id="o-ipm" class="dm-input" bind:value={form.access.ipMode} disabled={readonly}>
        <option value="">Everyone</option>
        <option value="allow">Only the addresses below</option>
        <option value="deny">Everyone except the addresses below</option>
      </select>
      {#if form.access.ipMode === 'allow'}{@render risk('access.ip_mode', '')}{/if}
      {@render err('access.ip_mode')}
    </div>
    {#if form.access.ipMode}
      <div class="dm-field wide">
        <label for="o-ipr">Addresses</label>
        <textarea id="o-ipr" class="dm-input dm-mono ta" rows="3" bind:value={form.access.ipRanges} disabled={readonly} placeholder="192.168.10.0/24 (one address or range per line)"></textarea>
        <span class="hint">Others get 403. Behind a CDN, set it under Trusted proxies so the real client address is used.</span>
        {@render err('access.ip_ranges')}
      </div>
    {/if}
    <div class="ck">
      <input id="o-fa" type="checkbox" bind:checked={form.access.forwardAuth} disabled={readonly} />
      <span class="ck-l">
        <label for="o-fa">Ask an auth service first</label>
        <span class="hint">Forward auth, e.g. Authelia or Authentik: a 2xx answer lets the request through.</span>
        {@render risk('access.forward_auth', 'The site depends on the auth service.')}
      </span>
    </div>
    {#if form.access.forwardAuth}
      <div class="dm-field wide">
        <label for="o-fau">Auth service URL</label>
        <input id="o-fau" class="dm-input dm-mono" bind:value={form.access.forwardAuthURL} disabled={readonly} placeholder="http://authelia:9091/api/verify" autocomplete="off" />
        {#if form.access.querySet}
          <span class="hint">{form.access.clearQuery ? 'The stored query is removed on save.' : 'A query is set and kept; it is never shown. Type a URL with a new query to replace it.'}</span>
          {#if !readonly}<label class="chk sm"><input type="checkbox" bind:checked={form.access.clearQuery} /> Remove the stored query</label>{/if}
        {/if}
        {@render err('access.forward_auth.url')}
      </div>
      <div class="dm-field wide">
        <label for="o-fah">Copy headers from the answer</label>
        <input id="o-fah" class="dm-input dm-mono" bind:value={form.access.copyHeaders} disabled={readonly} placeholder="Remote-User, Remote-Groups" autocomplete="off" />
        {@render err('access.forward_auth.copy_headers')}
      </div>
    {/if}
  {:else if key === 'headers'}
    <div class="ck">
      <input id="o-sh" type="checkbox" bind:checked={form.headers.security} disabled={readonly} />
      {@render check('o-sh', 'Security headers', 'nosniff, SAMEORIGIN frames and a strict referrer policy.')}
    </div>
    {@render headerRows(form.headers.requestSet, 'headers.request_set', 'Set on requests to the upstream')}
    <div class="dm-field wide">
      <label for="o-rqr">Remove from requests</label>
      <input id="o-rqr" class="dm-input dm-mono" bind:value={form.headers.requestRemove} disabled={readonly} placeholder="X-Debug, Cookie" autocomplete="off" />
      {@render err('headers.request_remove')}
    </div>
    {@render headerRows(form.headers.responseSet, 'headers.response_set', 'Set on responses to visitors')}
    <div class="dm-field wide">
      <label for="o-rsr">Remove from responses</label>
      <input id="o-rsr" class="dm-input dm-mono" bind:value={form.headers.responseRemove} disabled={readonly} placeholder="Server, X-Powered-By" autocomplete="off" />
      {@render err('headers.response_remove')}
    </div>
    <span class="hint">Mark values like API keys as secret: they are stored but never shown again.</span>
  {:else if key === 'logs'}
    <div class="ck">
      <input id="o-al" type="checkbox" bind:checked={form.logs.accessLog} disabled={readonly} />
      {@render check('o-al', 'Access log', "One line per request in the proxy container's log.")}
    </div>
  {/if}
{/snippet}

<div class="oe">
  {#each GROUPS as g (g.key)}
    {@const def = isDefault(form, g.key)}
    {#if collapsible}
      <details class="grp" open={!def || errGroups.has(g.key)}>
        <summary><b>{g.label}</b> <span class="dm-dim">{def ? 'Caddy defaults' : 'changed'}</span>{#if errGroups.has(g.key)}<span class="ferr"> · check this group</span>{/if}</summary>
        <div class="fields">{@render body(g.key)}</div>
      </details>
    {:else}
      <div class="dm-form-sec" id="opt-{g.key}">
        <div>
          <h4>{g.label}{#if errGroups.has(g.key)} <span class="ferr">·  check this group</span>{/if}</h4>
          <p>{g.hint}</p>
          {#if def}<p class="dm-dim">Caddy defaults</p>{/if}
        </div>
        <div class="fields">{@render body(g.key)}</div>
      </div>
    {/if}
  {/each}
</div>

<style>
  .oe { display: flex; flex-direction: column; }
  .fields { display: flex; flex-direction: column; gap: 10px; align-items: stretch !important; max-width: 720px; width: 100%; }
  .dm-field.wide, .dm-field { max-width: 720px; }
  .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); }
  .ck { display: flex; align-items: flex-start; gap: 8px; }
  .ck input { margin-top: 3px; }
  .ck-l { display: flex; flex-direction: column; gap: 2px; }
  .ck-l label { font-size: var(--fs-sm); font-weight: 500; }
  .chk { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); white-space: nowrap; }
  .chk.sm { font-size: var(--fs-xs); color: var(--muted); }
  .hrow { display: grid; grid-template-columns: minmax(0, 220px) minmax(0, 1fr) auto auto; gap: 6px; align-items: center; }
  .grid3 { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
  .size { display: grid; grid-template-columns: minmax(0, 1fr) 84px; gap: 6px; }
  .ta { resize: vertical; min-height: 0; }
  .risk { display: inline-flex; align-items: flex-start; gap: 4px; font-size: var(--fs-xs); color: var(--subtle); }
  .risk :global(svg) { flex: none; margin-top: 2px; color: var(--warn); }
  .risk.on { color: var(--warn); }
  .ferr { color: var(--bad); font-size: var(--fs-xs); font-weight: 400; }
  .grp { border: 1px solid var(--border); border-radius: var(--r); padding: 0 12px; }
  .grp + .grp { margin-top: 6px; }
  .grp summary { cursor: pointer; padding: 9px 0; font-size: var(--fs-sm); }
  .grp[open] summary { border-bottom: 1px solid var(--border); margin-bottom: 10px; }
  .grp .fields { padding-bottom: 12px; }
  @media (max-width: 900px) { .grid3 { grid-template-columns: minmax(0, 1fr); } }
</style>
