<script lang="ts">
  import { api, ApiError, isFanOut } from '$lib/api';
  import { Modal, Stepper } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { operations, opRef } from '$lib/stores/operations.svelte';
  import { hosts } from '$lib/stores/host.svelte';
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

  const STEPS = ['Name', 'Destination', 'Certificate', 'Advanced'];
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

  // Caddy reaches containers over the Docker network by name and
  // internal port.
  async function loadTargets() {
    try {
      const res = await api.containers.list(false, hosts.id === 'all' ? 'local' : hosts.id);
      const items: any[] = isFanOut(res) ? (res.items as any[]) : (res as any[]);
      const out: Target[] = [];
      for (const c of items) {
        const name = (c.Names?.[0] ?? '').replace(/^\//, '');
        if (!name) continue;
        const ports = [...new Set((c.Ports ?? []).map((p: any) => Number(p?.PrivatePort)).filter((n: number) => n > 0))] as number[];
        out.push({ container: name, stack: c.Labels?.['com.docker.compose.project'] ?? '', ports: ports.length ? ports.sort((a, b) => a - b) : [80] });
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
  const upstream = $derived(kind === 'container' ? (container && port ? `${container}:${port}` : '') : address.trim());

  const HOST_RE = /^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]*[a-z0-9])?$/i;
  const UPSTREAM_RE = /^(\[[0-9a-f:.]+\]|[a-z0-9.-]+):\d{1,5}$/i;

  function next() {
    error = '';
    if (step === 0) {
      const h = host.trim().toLowerCase();
      if (!h || !HOST_RE.test(h)) {
        error = 'Enter one hostname, e.g. photos.example.org.';
        return;
      }
      host = h;
    }
    if (step === 1 && !UPSTREAM_RE.test(upstream)) {
      error = kind === 'container' ? 'Pick a container and port.' : 'Enter host:port, e.g. 192.168.10.40:5001.';
      return;
    }
    step++;
  }

  async function save() {
    error = '';
    fieldErrors = {};
    if (editing && !UPSTREAM_RE.test(upstream)) {
      error = 'Enter host:port, e.g. grafana:3000.';
      return;
    }
    saving = true;
    try {
      if (editing) {
        await api.proxy.updateRoute(editing.id, upstream, tls);
        toast.success('Site updated', editing.host);
      } else {
        const since = Date.now();
        const custom = changedGroups(options, toForm(null)).length > 0;
        await api.proxy.createRoute(host, upstream, tls, custom ? toPayload(options) : undefined);
        const op = await operations.find(opRef.proxySite(host), 'proxy.site', since);
        if (op) await operations.open(op.id);
        else toast.success('Site created', host);
      }
      open = false;
      onsaved?.();
    } catch (e) {
      error = e instanceof ApiError ? e.message : 'Saving failed';
      const fe = editing ? null : parseFieldError(error);
      if (fe) {
        fieldErrors = { [fe.field]: fe.message };
        error = 'Check the advanced options.';
        step = ADVANCED;
      }
    } finally {
      saving = false;
    }
  }

  const TLS_OPTS: Array<{ id: TLSMode; hint: string }> = [
    { id: 'auto', hint: 'public name; DNS must point to this server' },
    { id: 'internal', hint: 'names only used at home, browsers warn until the CA is trusted' },
    { id: 'none', hint: 'no encryption' }
  ];
</script>

<Modal bind:open title={editing ? `Edit ${editing.host}` : 'New site'} maxWidth={step === ADVANCED && !editing ? 'max-w-3xl' : 'max-w-xl'}>
  {#if !editing}
    <Stepper steps={STEPS} current={step} />
  {/if}

  <div class="ns">
    {#if editing || step === 1}
      {#if !editing}
        <div class="opts" role="radiogroup" aria-label="Where should {host} go?">
          <span class="lbl">Where should <b class="dm-mono">{host}</b> go?</span>
          <label class="opt" class:on={kind === 'container'}>
            <input type="radio" bind:group={kind} value="container" disabled={!targets.length} />
            <span><b>A container</b> <span class="dm-dim">on {hosts.id === 'all' || hosts.id === 'local' ? 'the server' : hosts.selected?.name}</span></span>
          </label>
          <label class="opt" class:on={kind === 'address'}>
            <input type="radio" bind:group={kind} value="address" />
            <span><b>Another address</b> <span class="dm-dim">NAS, router, any host:port</span></span>
          </label>
        </div>
      {/if}
      {#if kind === 'container' && !editing}
        <div class="grid">
          <div class="dm-field">
            <label for="ns-ct">Container</label>
            <select id="ns-ct" class="dm-input" bind:value={container}>
              <option value="" disabled>Pick a container</option>
              {#each targets as t (t.container)}
                <option value={t.container}>{t.container}{t.stack ? ` · ${t.stack}` : ''}</option>
              {/each}
            </select>
          </div>
          <div class="dm-field">
            <label for="ns-port">Port</label>
            <select id="ns-port" class="dm-input" bind:value={port} disabled={!picked}>
              {#each picked?.ports ?? [] as p (p)}<option value={String(p)}>{p}</option>{/each}
            </select>
          </div>
        </div>
      {:else}
        <div class="dm-field">
          <label for="ns-addr">Goes to</label>
          <input id="ns-addr" class="dm-input dm-mono" bind:value={address} placeholder="192.168.10.40:5001 or grafana:3000" autocomplete="off" list="ns-targets" />
          <datalist id="ns-targets">
            {#each targets as t (t.container)}{#each t.ports as p (p)}<option value="{t.container}:{p}"></option>{/each}{/each}
          </datalist>
          <span class="hint">host:port; Caddy reaches containers by name over the Docker network.</span>
        </div>
      {/if}
    {/if}

    {#if !editing && step === 0}
      <div class="dm-field">
        <label for="ns-host">Name</label>
        <input id="ns-host" class="dm-input dm-mono" bind:value={host} placeholder="photos.example.org" autocomplete="off"
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); next(); } }} />
        <span class="hint">One hostname per site; it must point to this server in DNS. *.example.org works with a DNS challenge only.</span>
      </div>
    {/if}

    {#if editing || step === 2}
      <div class="opts" role="radiogroup" aria-label="Certificate">
        <span class="lbl">Certificate</span>
        {#each TLS_OPTS as o (o.id)}
          <label class="opt" class:on={tls === o.id}>
            <input type="radio" bind:group={tls} value={o.id} />
            <span><b>{TLS_LABEL[o.id]}</b> <span class="dm-dim">{o.hint}</span></span>
          </label>
        {/each}
      </div>
      {#if !editing}
        <p class="result"><b class="dm-mono">{host}</b> → <span class="dm-mono">{upstream}</span> with {TLS_LABEL[tls]}. Caddy defaults for everything else unless you change them under Advanced; you can see each step after creating it.</p>
      {/if}
    {/if}

    {#if !editing && step === ADVANCED}
      <p class="result">Optional. Open a group to change it; everything you leave closed keeps Caddy's defaults. You can change all of this later on the site's Options tab.</p>
      <OptionsEditor bind:form={options} {tls} {host} errors={fieldErrors} collapsible />
    {/if}

    {#if error}<p class="err" role="alert">{error}</p>{/if}
  </div>

  {#snippet footer()}
    {#if editing}
      <button type="button" class="dm-btn" onclick={() => (open = false)}>Cancel</button>
      <button type="button" class="dm-btn dm-btn-primary" disabled={saving} onclick={save}>Save</button>
    {:else}
      {#if step > 0}<button type="button" class="dm-btn" onclick={() => { error = ''; step--; }}>Back</button>
      {:else}<button type="button" class="dm-btn" onclick={() => (open = false)}>Cancel</button>{/if}
      {#if step < 2}<button type="button" class="dm-btn dm-btn-primary" onclick={next}>Next</button>
      {:else}
        {#if step === 2}<button type="button" class="dm-btn" onclick={next}>Advanced</button>{/if}
        <button type="button" class="dm-btn dm-btn-primary" disabled={saving} onclick={save}>Create site</button>
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
