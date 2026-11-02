<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, ApiError, isFanOut } from '$lib/api';
  import { Modal, Stepper } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { operations, opRef } from '$lib/stores/operations.svelte';
  import { TLS_LABEL, type Site, type TLSMode } from './_site';
  import OptionsEditor from './_OptionsEditor.svelte';
  import { toForm, toPayload, changedGroups, parseFieldError, type OptionsForm } from './_options';

  // New site as three short questions (name, where it goes, certificate)
  // and an optional fourth with the advanced options; editing an existing
  // site is one form for its destination and HTTPS.
  let {
    open = $bindable(false),
    editing = null,
    onsaved
  }: { open: boolean; editing?: Site | null; onsaved?: () => void } = $props();

  interface Target {
    container: string;
    stack: string;
    ports: number[];
  }

  let step = $state(0);
  let host = $state('');
  let kind = $state<'container' | 'address'>('container');
  let container = $state('');
  let port = $state('');
  let address = $state('');
  let tls = $state<TLSMode>('auto');
  let saving = $state(false);
  let error = $state('');
  let targets = $state<Target[]>([]);
  let options = $state<OptionsForm>(toForm(null));
  let fieldErrors = $state<Record<string, string>>({});

  const STEPS = [m.common_name(), m.proxy_sm_step_destination(), m.proxy_sm_certificate(), m.proxy_sm_advanced()];
  const ADVANCED = 3;

  $effect(() => {
    if (!open) return;
    error = '';
    saving = false;
    fieldErrors = {};
    options = toForm(null);
    if (editing) {
      host = editing.host;
      address = editing.upstream;
      tls = editing.tls_mode;
      kind = 'address';
    } else {
      step = 0;
      host = '';
      address = '';
      container = '';
      port = '';
      kind = 'container';
      tls = 'auto';
    }
    void loadTargets();
  });

  // Caddy runs on the server's host network, so it reaches a container
  // through a port published on the server, not by container name.
  async function loadTargets() {
    try {
      const res = await api.containers.list(false, 'local');
      const items: any[] = isFanOut(res) ? (res.items as any[]) : (res as any[]);
      const out: Target[] = [];
      for (const c of items) {
        const name = (c.Names?.[0] ?? '').replace(/^\//, '');
        if (!name) continue;
        const ports = [...new Set((c.Ports ?? []).map((p: any) => Number(p?.PublicPort)).filter((n: number) => n > 0))] as number[];
        if (!ports.length) continue;
        out.push({ container: name, stack: c.Labels?.['com.docker.compose.project'] ?? '', ports: ports.sort((a, b) => a - b) });
      }
      targets = out.sort((a, b) => (a.stack || '~').localeCompare(b.stack || '~') || a.container.localeCompare(b.container));
      if (!targets.length) kind = 'address';
    } catch {
      targets = [];
      kind = 'address';
    }
  }

  const picked = $derived(targets.find((t) => t.container === container));
  $effect(() => {
    if (picked && !picked.ports.map(String).includes(port)) port = String(picked.ports[0]);
  });
  const upstream = $derived(kind === 'container' ? (container && port ? `localhost:${port}` : '') : address.trim());

  const HOST_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]*[a-z0-9])?$/i;
  const UPSTREAM_RE = /^(\[[0-9a-f:.]+\]|[a-z0-9.-]+):\d{1,5}$/i;

  function next() {
    error = '';
    if (step === 0) {
      const h = host.trim().toLowerCase();
      if (!h || !HOST_RE.test(h)) {
        error = m.proxy_sm_err_host();
        return;
      }
      host = h;
    }
    if (step === 1 && !UPSTREAM_RE.test(upstream)) {
      error = kind === 'container' ? m.proxy_sm_err_container() : m.proxy_sm_err_address();
      return;
    }
    step++;
  }

  async function save() {
    error = '';
    fieldErrors = {};
    if (editing && !UPSTREAM_RE.test(upstream)) {
      error = m.proxy_sm_err_address_edit();
      return;
    }
    saving = true;
    try {
      if (editing) {
        await api.proxy.updateRoute(editing.id, upstream, tls);
        toast.success(m.proxy_site_updated(), editing.host);
      } else {
        const since = Date.now();
        const custom = changedGroups(options, toForm(null)).length > 0;
        await api.proxy.createRoute(host, upstream, tls, custom ? toPayload(options) : undefined);
        const op = await operations.find(opRef.proxySite(host), 'proxy.site', since);
        if (op) await operations.open(op.id);
        else toast.success(m.proxy_site_created(), host);
      }
      open = false;
      onsaved?.();
    } catch (e) {
      error = e instanceof ApiError ? e.message : m.proxy_saving_failed();
      const fe = editing ? null : parseFieldError(error);
      if (fe) {
        fieldErrors = { [fe.field]: fe.message };
        error = m.proxy_sm_err_check_advanced();
        step = ADVANCED;
      }
    } finally {
      saving = false;
    }
  }

  const TLS_OPTS: Array<{ id: TLSMode; hint: string }> = [
    { id: 'auto', hint: m.proxy_sm_tls_auto_hint() },
    { id: 'internal', hint: m.proxy_sm_tls_internal_hint() },
    { id: 'none', hint: m.proxy_sm_tls_none_hint() }
  ];
</script>

<Modal bind:open title={editing ? m.proxy_sm_edit_title({ host: editing.host }) : m.proxy_new_site()} maxWidth={step === ADVANCED && !editing ? 'max-w-3xl' : 'max-w-xl'}>
  {#if !editing}
    <Stepper steps={STEPS} current={step} />
  {/if}

  <div class="ns">
    {#if editing || step === 1}
      {#if !editing}
        <div class="opts" role="radiogroup" aria-label={m.proxy_sm_where_aria({ host })}>
          <span class="lbl">{m.proxy_sm_where_before()} <b class="dm-mono">{host}</b> {m.proxy_sm_where_after()}</span>
          <label class="opt" class:on={kind === 'container'}>
            <input type="radio" bind:group={kind} value="container" disabled={!targets.length} />
            <span><b>{m.proxy_sm_kind_container()}</b> <span class="dm-dim">{m.proxy_sm_kind_container_hint()}</span></span>
          </label>
          <label class="opt" class:on={kind === 'address'}>
            <input type="radio" bind:group={kind} value="address" />
            <span><b>{m.proxy_sm_kind_address()}</b> <span class="dm-dim">{m.proxy_sm_kind_address_hint()}</span></span>
          </label>
        </div>
      {/if}
      {#if kind === 'container' && !editing}
        <div class="grid">
          <div class="dm-field">
            <label for="ns-ct">{m.common_container()}</label>
            <select id="ns-ct" class="dm-input" bind:value={container}>
              <option value="" disabled>{m.proxy_sm_pick_container()}</option>
              {#each targets as t (t.container)}
                <option value={t.container}>{t.container}{t.stack ? ` · ${t.stack}` : ''}</option>
              {/each}
            </select>
          </div>
          <div class="dm-field">
            <label for="ns-port">{m.proxy_sm_published_port()}</label>
            <select id="ns-port" class="dm-input" bind:value={port} disabled={!picked}>
              {#each picked?.ports ?? [] as p (p)}<option value={String(p)}>{p}</option>{/each}
            </select>
          </div>
        </div>
      {:else}
        <div class="dm-field">
          <label for="ns-addr">{m.proxy_sm_goes_to()}</label>
          <input id="ns-addr" class="dm-input dm-mono" bind:value={address} placeholder={m.proxy_sm_goes_to_ph()} autocomplete="off" list="ns-targets" />
          <datalist id="ns-targets">
            {#each targets as t (t.container)}{#each t.ports as p (p)}<option value="localhost:{p}">{t.container}</option>{/each}{/each}
          </datalist>
          <span class="hint">{m.proxy_sm_goes_to_hint()}</span>
        </div>
      {/if}
    {/if}

    {#if !editing && step === 0}
      <div class="dm-field">
        <label for="ns-host">{m.common_name()}</label>
        <input id="ns-host" class="dm-input dm-mono" bind:value={host} placeholder="photos.example.org" autocomplete="off"
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); next(); } }} />
        <span class="hint">{m.proxy_sm_host_hint()}</span>
      </div>
    {/if}

    {#if editing || step === 2}
      <div class="opts" role="radiogroup" aria-label={m.proxy_sm_certificate()}>
        <span class="lbl">{m.proxy_sm_certificate()}</span>
        {#each TLS_OPTS as o (o.id)}
          <label class="opt" class:on={tls === o.id}>
            <input type="radio" bind:group={tls} value={o.id} />
            <span><b>{TLS_LABEL[o.id]}</b> <span class="dm-dim">{o.hint}</span></span>
          </label>
        {/each}
      </div>
      {#if !editing}
        <p class="result"><b class="dm-mono">{host}</b> → <span class="dm-mono">{upstream}</span> {m.proxy_sm_result({ tls: TLS_LABEL[tls] })}</p>
      {/if}
    {/if}

    {#if !editing && step === ADVANCED}
      <p class="result">{m.proxy_sm_advanced_hint()}</p>
      <OptionsEditor bind:form={options} {tls} {host} errors={fieldErrors} collapsible />
    {/if}

    {#if error}<p class="err" role="alert">{error}</p>{/if}
  </div>

  {#snippet footer()}
    {#if editing}
      <button type="button" class="dm-btn" onclick={() => (open = false)}>{m.common_cancel()}</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={saving} onclick={save}>{m.common_save()}</button>
    {:else}
      {#if step > 0}<button type="button" class="dm-btn" onclick={() => { error = ''; step--; }}>{m.common_back()}</button>
      {:else}<button type="button" class="dm-btn" onclick={() => (open = false)}>{m.common_cancel()}</button>{/if}
      {#if step < 2}<button type="button" class="dm-btn dm-btn-primary" onclick={next}>{m.common_next()}</button>
      {:else}
        {#if step === 2}<button type="button" class="dm-btn" onclick={next}>{m.proxy_sm_advanced()}</button>{/if}
        <button type="button" class="dm-btn dm-btn-primary" disabled={saving} onclick={save}>{m.proxy_sm_create()}</button>
      {/if}
    {/if}
  {/snippet}
</Modal>

<style>
  .ns { display: flex; flex-direction: column; gap: 14px; margin-top: 14px; }
  .grid { display: grid; grid-template-columns: minmax(0, 1fr) 110px; gap: 10px; }
  .hint { font-size: var(--fs-xs); color: var(--subtle); }
  .lbl { font-size: 12px; color: var(--muted); font-weight: 500; }
  .opts { display: flex; flex-direction: column; gap: 6px; }
  .opt { display: flex; align-items: center; gap: 10px; min-height: 40px; padding: 0 12px; border: 1px solid var(--border); border-radius: var(--r); cursor: pointer; }
  .opt.on { border-color: var(--accent); background: var(--accent-soft); }
  .opt span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .result { margin: 0; font-size: var(--fs-sm); color: var(--muted); }
  .err { margin: 0; color: var(--bad); font-size: var(--fs-sm); }
</style>
