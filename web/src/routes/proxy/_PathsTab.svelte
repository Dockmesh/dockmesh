<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, ApiError } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { TriangleAlert } from 'lucide-svelte';
  import UpstreamTLSFields from './_UpstreamTLSFields.svelte';
  import FallbackFields from './_FallbackFields.svelte';
  import HeaderRows from './_HeaderRows.svelte';
  import type { Site } from './_site';
  import { toForm, toPayload, toFallbackForm, parseFieldError, fromLines, REDIRECT_CODES, type FallbackForm } from './_options';
  import {
    MAX_PATHS, newPathForm, toPathForm, toPathPayload, sortForms, pathError, cleanPath,
    actionLabel, detailChips, parsePathError, type PathForm
  } from './_paths';

  let {
    site,
    readonly = false,
    onsaved,
    onoptions
  }: { site: Site; readonly?: boolean; onsaved: (s: Site) => void; onoptions: () => void } = $props();

  // Rows carry a stable key so the expanded row survives re-sorting.
  type Row = { key: number; form: PathForm };
  let nextKey = 0;
  const rowsOf = (s: Site): Row[] => (s.paths ?? []).map((p) => ({ key: nextKey++, form: toPathForm(p) }));

  let rows = $state<Row[]>([]);
  let fallback = $state<FallbackForm>(toFallbackForm(null));
  let open = $state<number | null>(null);
  let fbOpen = $state(false);
  let errors = $state<Record<number, Record<string, string>>>({});
  let fbErrors = $state<Record<string, string>>({});
  let banner = $state('');
  let saving = $state(false);
  let baseline = $state('');

  const snapshot = () => JSON.stringify([sortForms(rows).map((r) => toPathPayload(r.form)), fallback]);
  function reset() {
    rows = rowsOf(site);
    fallback = toFallbackForm(site.options?.routing?.fallback);
    errors = {};
    fbErrors = {};
    banner = '';
    open = null;
    fbOpen = false;
    baseline = snapshot();
  }
  // Reload from the site only while nothing is edited.
  let seen = '';
  $effect(() => {
    const key = JSON.stringify([site.paths ?? [], site.options?.routing?.fallback ?? null]);
    if (key !== seen && (!baseline || snapshot() === baseline)) {
      seen = key;
      reset();
    }
  });

  const sorted = $derived(sortForms(rows));
  const dirty = $derived(baseline !== '' && snapshot() !== baseline);
  const forms = $derived(rows.map((r) => r.form));
  const full = $derived(rows.length >= MAX_PATHS);

  const siteTLS = $derived(!!site.options?.routing?.upstream_tls?.tls);
  const fwdPort = $derived(!!site.options?.headers?.forwarded_port);
  const siteRespNames = $derived.by(() => {
    const h = site.options?.headers;
    const names = new Set((h?.response_set ?? []).map((x) => x.name.toLowerCase()));
    if (h?.security) for (const n of ['x-content-type-options', 'x-frame-options', 'referrer-policy']) names.add(n);
    if (site.options?.https?.hsts && site.tls_mode !== 'none') names.add('strict-transport-security');
    return names;
  });
  const replacesSite = (name: string) => (siteRespNames.has(name.trim().toLowerCase()) ? m.proxy_path_replaces_site() : '');

  const fbLabel = $derived(
    fallback.mode === 'redirect'
      ? m.proxy_path_act_redirect({ code: fallback.code, to: fallback.to })
      : fallback.mode === '404'
        ? m.proxy_path_act_respond({ status: 404 })
        : m.proxy_path_act_proxy({ to: site.upstream })
  );

  function add() {
    const r = { key: nextKey++, form: newPathForm() };
    rows.push(r);
    open = r.key;
  }

  function remove(key: number) {
    rows = rows.filter((r) => r.key !== key);
    if (open === key) open = null;
  }

  async function save() {
    errors = {};
    fbErrors = {};
    banner = '';
    const order = sortForms(rows);
    for (const r of order) {
      r.form.path = cleanPath(r.form.path);
      const e = pathError(r.form, forms);
      if (e) {
        errors = { [r.key]: { 'match.path': e } };
        open = r.key;
        return;
      }
    }
    const fbChanged = JSON.stringify(fallback) !== JSON.stringify(toFallbackForm(site.options?.routing?.fallback));
    saving = true;
    try {
      // The fallback lives in the options, which go back whole: read them
      // fresh, so a change saved meanwhile on the Options tab is kept.
      let options: object | undefined;
      if (fbChanged) {
        const fresh = ((await api.proxy.listRoutes()) as Site[]).find((s) => s.id === site.id) ?? site;
        const opts = toForm(fresh.options);
        options = toPayload({ ...opts, routing: { ...opts.routing, fallback } });
      }
      const updated = (await api.proxy.updateRoute(site.id, site.upstream, site.tls_mode, undefined, options, order.map((r) => toPathPayload(r.form)))) as Site;
      baseline = '';
      onsaved(updated);
      toast.success(m.proxy_paths_saved(), site.host);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : String(e);
      const pe = parsePathError(msg);
      const fieldErr = parseFieldError(msg);
      const fe = fieldErr?.field.startsWith('routing.fallback') ? fieldErr : null;
      if (pe && order[pe.index]) {
        const key = order[pe.index].key;
        errors = { [key]: { [pe.field]: pe.message } };
        open = key;
      } else if (fe) {
        fbErrors = { [fe.field]: fe.message };
        fbOpen = true;
      } else {
        banner = e instanceof ApiError && e.status >= 500 ? m.proxy_path_rejected() : msg;
      }
      toast.error(m.proxy_paths_not_saved(), pe?.message ?? fe?.message ?? banner);
    } finally {
      saving = false;
    }
  }

  const err = (key: number, field: string) => errors[key]?.[field] ?? '';
</script>

{#snippet fieldErr(key: number, field: string)}
  {#if err(key, field)}<span class="ferr" role="alert">{err(key, field)}</span>{/if}
{/snippet}

{#snippet editor(r: Row)}
  {@const f = r.form}
  {@const shown = cleanPath(f.path) || '/ords'}
  <div class="editor" data-testid="path-editor">
    <div class="grid2">
      <fieldset class="radios">
        <legend>{m.proxy_path_type()}</legend>
        <div class="seg">
          <label><input type="radio" bind:group={f.type} value="prefix" disabled={readonly} /> {m.proxy_path_prefix()}</label>
          <label><input type="radio" bind:group={f.type} value="exact" disabled={readonly} /> {m.proxy_path_exact()}</label>
        </div>
        <span class="hint">{f.type === 'prefix' ? m.proxy_path_prefix_hint({ path: shown }) : m.proxy_path_exact_hint()}</span>
      </fieldset>
      <div class="dm-field">
        <label for="p-{r.key}-path">{m.proxy_path_path()}</label>
        <input id="p-{r.key}-path" class="dm-input dm-mono" bind:value={f.path} placeholder="/ords" disabled={readonly} autocomplete="off"
          onblur={() => (f.path = cleanPath(f.path))} aria-invalid={!!err(r.key, 'match.path')} />
        {@render fieldErr(r.key, 'match.path')}{@render fieldErr(r.key, 'match.type')}
      </div>
    </div>

    <fieldset class="radios">
      <legend>{m.proxy_path_action()}</legend>
      <div class="seg">
        <label><input type="radio" bind:group={f.action} value="proxy" disabled={readonly} /> {m.proxy_path_proxy()}</label>
        <label><input type="radio" bind:group={f.action} value="redirect" disabled={readonly} /> {m.proxy_path_redirect()}</label>
        <label><input type="radio" bind:group={f.action} value="respond" disabled={readonly} /> {m.proxy_path_respond()}</label>
      </div>
      {@render fieldErr(r.key, 'action')}
    </fieldset>

    {#if f.action === 'proxy'}
      <fieldset class="radios">
        <legend>{m.proxy_path_backend()}</legend>
        <label><input type="radio" bind:group={f.backend} value="site" disabled={readonly} /> {m.proxy_path_backend_site({ upstream: site.upstream })}</label>
        <label><input type="radio" bind:group={f.backend} value="static" disabled={readonly} /> {m.proxy_path_backend_other()}</label>
        {@render fieldErr(r.key, 'target.kind')}
      </fieldset>
      {#if f.backend === 'site'}
        <span class="hint">{m.proxy_path_backend_site_tls({ tls: siteTLS ? m.proxy_path_on() : m.proxy_path_off() })} <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={onoptions}>{m.proxy_col_options()}</button></span>
      {:else}
        <div class="dm-field">
          <label for="p-{r.key}-up">{m.proxy_path_upstreams()}</label>
          <textarea id="p-{r.key}-up" class="dm-input dm-mono ta" rows="2" bind:value={f.upstreams} disabled={readonly} placeholder="10.0.20.30:9000"></textarea>
          <span class="hint">{m.proxy_path_upstreams_hint()}</span>
          {@render fieldErr(r.key, 'target.upstreams')}
        </div>
        {#if fromLines(f.upstreams).length > 1}
          <div class="grid2">
            <div class="dm-field">
              <label for="p-{r.key}-lb">{m.proxy_oe_lb()}</label>
              <select id="p-{r.key}-lb" class="dm-input" bind:value={f.lbPolicy} disabled={readonly}>
                <option value="">{m.proxy_oe_lb_random()}</option>
                <option value="round_robin">{m.proxy_oe_lb_round_robin()}</option>
                <option value="least_conn">{m.proxy_oe_lb_least_conn()}</option>
                <option value="first">{m.proxy_oe_lb_first()}</option>
                <option value="client_ip_hash">{m.proxy_oe_lb_ip_hash()}</option>
              </select>
              {@render fieldErr(r.key, 'target.lb_policy')}
            </div>
            <div class="dm-field">
              <label for="p-{r.key}-hp">{m.proxy_oe_health_path()}</label>
              <input id="p-{r.key}-hp" class="dm-input dm-mono" bind:value={f.healthPath} disabled={readonly} placeholder="/health" autocomplete="off" />
              {@render fieldErr(r.key, 'target.health_path')}
            </div>
          </div>
        {/if}
        <UpstreamTLSFields bind:tls={f.tls} host={site.host} field="target.tls" idp="p-{r.key}-tls" errors={errors[r.key] ?? {}} {readonly} />
      {/if}
      {#if f.type === 'prefix'}
        <div class="ck">
          <input id="p-{r.key}-strip" type="checkbox" bind:checked={f.stripPrefix} disabled={readonly} />
          <span class="ck-l">
            <label for="p-{r.key}-strip">{m.proxy_path_strip({ path: shown })}</label>
            {#if f.stripPrefix}<span class="hint dm-mono">{shown}/x → /x, {shown} → /</span>{/if}
          </span>
        </div>
        {@render fieldErr(r.key, 'strip_prefix')}
      {/if}
      <div class="dm-field">
        <span class="lbl">{m.proxy_oe_req_set()}</span>
        <HeaderRows bind:list={f.requestSet} label={m.proxy_oe_req_set()} secrets {readonly} />
        <input class="dm-input dm-mono" bind:value={f.requestRemove} disabled={readonly} placeholder="X-Debug, Cookie" aria-label={m.proxy_oe_req_remove()} autocomplete="off" />
        <span class="hint">{m.proxy_path_req_hint()}</span>
        {@render fieldErr(r.key, 'headers.request_set')}{@render fieldErr(r.key, 'headers.request_remove')}
      </div>
      <div class="dm-field">
        <span class="lbl">{m.proxy_oe_resp_set()}</span>
        <HeaderRows bind:list={f.responseSet} label={m.proxy_oe_resp_set()} {readonly} chip={replacesSite} />
        <input class="dm-input dm-mono" bind:value={f.responseRemove} disabled={readonly} placeholder="X-Frame-Options" aria-label={m.proxy_oe_resp_remove()} autocomplete="off" />
        <span class="hint">{m.proxy_path_resp_hint()}</span>
        {@render fieldErr(r.key, 'headers.response_set')}{@render fieldErr(r.key, 'headers.response_remove')}
      </div>
      <details class="tmo" open={!!(f.dialTimeout || f.readTimeout || f.writeTimeout || f.streamCloseDelay || f.flush || f.buffer || Object.keys(errors[r.key] ?? {}).some((k) => k.startsWith('transport')))}>
        <summary>{m.proxy_path_timeouts()} <span class="dm-dim">· {m.proxy_path_inherit()}</span></summary>
        <div class="grid3">
          {#each [['dialTimeout', 'dial_timeout', m.proxy_oe_dial_timeout()], ['readTimeout', 'read_timeout', m.proxy_oe_read_timeout()], ['writeTimeout', 'write_timeout', m.proxy_oe_write_timeout()], ['streamCloseDelay', 'stream_close_delay', m.proxy_oe_stream_close()]] as [k, field, label] (k)}
            <div class="dm-field">
              <label for="p-{r.key}-{k}">{label}</label>
              <input id="p-{r.key}-{k}" class="dm-input dm-mono" bind:value={f[k as 'dialTimeout']} disabled={readonly} placeholder={m.proxy_path_inherit()} autocomplete="off" />
              {@render fieldErr(r.key, `transport.${field}`)}
            </div>
          {/each}
          {#each [['flush', m.proxy_oe_stream()], ['buffer', m.proxy_oe_buffer()]] as [k, label] (k)}
            <div class="dm-field">
              <label for="p-{r.key}-{k}">{label}</label>
              <select id="p-{r.key}-{k}" class="dm-input" bind:value={f[k as 'flush']} disabled={readonly}>
                <option value="">{m.proxy_path_inherit()}</option>
                <option value="on">{m.proxy_path_on()}</option>
                <option value="off">{m.proxy_path_off()}</option>
              </select>
            </div>
          {/each}
        </div>
      </details>
    {:else if f.action === 'redirect'}
      <div class="grid2">
        <div class="dm-field">
          <label for="p-{r.key}-to">{m.proxy_redir_to()}</label>
          <input id="p-{r.key}-to" class="dm-input dm-mono" bind:value={f.to} disabled={readonly} placeholder={m.proxy_redir_to_ph()} autocomplete="off" />
          {@render fieldErr(r.key, 'to')}
        </div>
        <div class="dm-field">
          <label for="p-{r.key}-code">{m.proxy_redir_code()}</label>
          <select id="p-{r.key}-code" class="dm-input" bind:value={f.code} disabled={readonly}>
            {#each REDIRECT_CODES as c (c)}<option value={c}>{m.proxy_path_act_redirect({ code: c, to: '' }).trim()}</option>{/each}
          </select>
          {@render fieldErr(r.key, 'code')}
        </div>
      </div>
    {:else}
      <div class="dm-field narrow">
        <label for="p-{r.key}-st">{m.proxy_path_status()}</label>
        <select id="p-{r.key}-st" class="dm-input" bind:value={f.status} disabled={readonly}>
          <option value={404}>{m.proxy_path_404()}</option>
          <option value={403}>{m.proxy_path_403()}</option>
        </select>
        {@render fieldErr(r.key, 'status')}
      </div>
    {/if}
  </div>
{/snippet}

<section class="paths">
  <div class="sec-h">
    <span class="dm-sub">{m.proxy_paths_intro()} <a class="docs" href="https://dockmesh.dev/docs/features/reverse-proxy#path-routes" target="_blank" rel="noopener">{m.proxy_paths_docs()}</a></span>
    <span class="dm-sp"></span>
    {#if !readonly}
      {#if dirty}<span class="dm-sub">{m.proxy_paths_changed()}</span>{/if}
      <button type="button" class="dm-btn dm-btn-sm" disabled={!dirty || saving} onclick={reset}>{m.common_reset()}</button>
      <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" data-testid="paths-save" disabled={!dirty || saving} onclick={save}>{m.proxy_paths_save()}</button>
    {/if}
  </div>
  {#if banner}<p class="banner" role="alert"><TriangleAlert size={14} /> {banner}</p>{/if}
  {#if readonly}<p class="dm-sub">{m.proxy_paths_readonly()}</p>{/if}

  {#if !rows.length}
    <div class="empty" data-testid="paths-empty">
      <span>{m.proxy_paths_empty({ upstream: site.upstream })}</span>
      {#if !readonly}<button type="button" class="dm-btn dm-btn-primary dm-btn-sm" data-testid="paths-add" onclick={add}>{m.proxy_paths_add()}</button>{/if}
    </div>
  {:else}
    <p class="hint">{m.proxy_paths_sort_note()}</p>
  {/if}

  <div class="dm-tbl ptable" role="table" aria-label={m.proxy_tab_paths()}>
    <div class="dm-tr dm-th prow" role="row">
      <span role="columnheader">{m.proxy_paths_col_match()}</span>
      <span role="columnheader">{m.proxy_paths_col_action()}</span>
      <span role="columnheader">{m.proxy_paths_col_details()}</span>
      <span role="columnheader"></span>
    </div>
    {#each sorted as r (r.key)}
      {@const f = r.form}
      <div class="dm-tr prow" role="row" data-testid="path-row" class:haserr={!!errors[r.key]}>
        <span role="cell" class="dm-mono"><span data-testid="path-match">{cleanPath(f.path) || '—'}</span> <span class="mchip">{f.type === 'exact' ? m.proxy_paths_exact() : m.proxy_paths_and_below()}</span></span>
        <span role="cell" class="dm-trunc">{actionLabel(f, site.upstream)}</span>
        <span role="cell" class="chips">{#each detailChips(f) as c (c.text)}<span class="mchip" class:amb={c.warn}>{c.text}</span>{/each}</span>
        <span role="cell" class="acts">
          <button type="button" class="dm-btn dm-btn-sm" data-testid="path-edit" onclick={() => (open = open === r.key ? null : r.key)}>{open === r.key ? m.common_close() : readonly ? m.proxy_show() : m.common_edit()}</button>
          {#if !readonly}<button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" data-testid="path-delete" onclick={() => remove(r.key)}>{m.common_delete()}</button>{/if}
        </span>
      </div>
      {#if open === r.key}{@render editor(r)}{/if}
    {/each}
    <div class="dm-tr prow fb" role="row" data-testid="path-fallback">
      <span role="cell" class="dm-dim">{m.proxy_paths_everything_else()}</span>
      <span role="cell" class="dm-trunc">{fbLabel}</span>
      <span role="cell"></span>
      <span role="cell" class="acts">
        <button type="button" class="dm-btn dm-btn-sm" onclick={() => (fbOpen = !fbOpen)}>{fbOpen ? m.common_close() : readonly ? m.proxy_show() : m.common_edit()}</button>
      </span>
    </div>
    {#if fbOpen}
      <div class="editor"><FallbackFields bind:fb={fallback} idp="p-fb" errors={fbErrors} {readonly} /></div>
    {/if}
  </div>

  <div class="foot">
    <span class="dm-sub">{m.proxy_paths_count({ n: rows.length, max: MAX_PATHS })}</span>
    {#if !readonly && rows.length}
      <button type="button" class="dm-btn dm-btn-sm" data-testid="paths-add" disabled={full} onclick={add}>{m.proxy_paths_add()}</button>
      {#if full}<span class="dm-sub">{m.proxy_paths_full({ max: MAX_PATHS })}</span>{/if}
    {/if}
  </div>
  {#if fwdPort}<p class="hint">{m.proxy_paths_fwd_port()}</p>{/if}
</section>

<style>
  .paths { display: flex; flex-direction: column; gap: 10px; }
  .sec-h { display: flex; align-items: center; gap: 12px; }
  .docs { color: var(--accent); white-space: nowrap; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); margin: 0; }
  .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
  .banner { display: flex; gap: 6px; align-items: center; color: var(--bad); border: 1px solid var(--bad); border-radius: var(--r); padding: 8px 10px; margin: 0; font-size: var(--fs-sm); }
  .empty { display: flex; align-items: center; gap: 12px; padding: 12px; border: 1px dashed var(--border); border-radius: var(--r); }
  .prow { display: grid; grid-template-columns: minmax(180px, 1.2fr) minmax(180px, 1.4fr) minmax(0, 1.6fr) 150px; gap: 10px; align-items: center; }
  .prow.haserr { box-shadow: inset 3px 0 0 var(--bad); }
  .fb { background: var(--band); }
  .acts { display: flex; gap: 4px; justify-content: flex-end; }
  .chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .mchip { display: inline-block; font-family: var(--font-sans); font-size: var(--fs-xs); color: var(--muted); border: 1px solid var(--border); border-radius: 999px; padding: 0 7px; margin-left: 4px; }
  .chips .mchip { margin-left: 0; }
  .mchip.amb { color: var(--warn); border-color: var(--warn); }
  .editor { display: flex; flex-direction: column; gap: 12px; padding: 12px 14px 16px; border-bottom: 1px solid var(--border); background: var(--surface); max-width: 100%; }
  .grid2 { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; max-width: 760px; }
  .grid3 { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 8px; }
  .dm-field { max-width: 760px; }
  .narrow { max-width: 260px; }
  .radios { border: 0; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 4px; }
  .radios legend { font-size: 12px; color: var(--muted); font-weight: 500; margin-bottom: 4px; }
  .radios label { display: inline-flex; align-items: center; gap: 6px; font-size: var(--fs-sm); }
  .seg { display: flex; gap: 14px; }
  .ck { display: flex; align-items: flex-start; gap: 8px; }
  .ck input { margin-top: 3px; }
  .ck-l { display: flex; flex-direction: column; gap: 2px; font-size: var(--fs-sm); }
  .ta { resize: vertical; min-height: 0; }
  .tmo summary { cursor: pointer; font-size: var(--fs-sm); }
  .ferr { color: var(--bad); font-size: var(--fs-xs); }
  .foot { display: flex; align-items: center; gap: 12px; }
  @media (max-width: 900px) { .prow { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto; } .prow > :nth-child(3) { display: none; } .grid3, .grid2 { grid-template-columns: minmax(0, 1fr); } }
</style>
