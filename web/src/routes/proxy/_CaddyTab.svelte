<script lang="ts">
  import { apiRequest, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';

  // Caddy version and the options that apply to every site.
  let { running, onchanged }: { running: boolean; onchanged?: () => void } = $props();

  interface VersionInfo {
    current: string;
    image: string;
    default: string;
    previous?: string;
    available: Array<{ version: string; image: string }>;
    managed: boolean;
    read_only_reason?: string;
  }
  interface Settings {
    acme_email: string;
    acme_ca: string;
    trusted_proxies: string;
  }

  const canEdit = $derived(allowed('proxy.update'));
  let info = $state<VersionInfo | null>(null);
  let settings = $state<Settings | null>(null);
  let form = $state<Settings>({ acme_email: '', acme_ca: '', trusted_proxies: '' });
  let pick = $state('');
  let switching = $state(false);
  let saving = $state(false);

  async function load() {
    const [v, s] = await Promise.all([
      apiRequest<VersionInfo>('/proxy/version').catch(() => null),
      apiRequest<Settings>('/proxy/settings').catch(() => null)
    ]);
    info = v;
    settings = s;
    if (s) form = { ...s };
    if (v && !pick) pick = v.current || v.default;
  }
  $effect(() => {
    void load();
  });

  async function switchTo(version: string, rollback = false) {
    const ok = await confirm.ask({
      title: rollback ? 'Go back to the previous Caddy version' : `Switch Caddy to ${version}`,
      message: `Caddy is restarted with ${version}.`,
      body: 'Sites are briefly unavailable while the container is replaced. If the new version does not take the configuration, Dockmesh switches back on its own.',
      confirmLabel: rollback ? 'Go back' : 'Switch'
    });
    if (!ok) return;
    switching = true;
    try {
      info = rollback
        ? await apiRequest<VersionInfo>('/proxy/version/rollback', { method: 'POST' })
        : await apiRequest<VersionInfo>('/proxy/version', { method: 'PUT', body: JSON.stringify({ version }) });
      toast.success(`Caddy runs ${info.current}`);
      onchanged?.();
    } catch (e) {
      toast.error('Version switch failed', e instanceof ApiError ? e.message : undefined);
      await load();
    } finally {
      switching = false;
    }
  }

  async function saveSettings() {
    saving = true;
    try {
      settings = await apiRequest<Settings>('/proxy/settings', { method: 'PUT', body: JSON.stringify(form) });
      form = { ...settings };
      toast.success('Proxy settings saved');
    } catch (e) {
      toast.error('Saving failed', e instanceof ApiError ? e.message : undefined);
    } finally {
      saving = false;
    }
  }
  const dirty = $derived(!!settings && (form.acme_email !== settings.acme_email || form.acme_ca !== settings.acme_ca || form.trusted_proxies !== settings.trusted_proxies));
</script>

<div class="cad">
  <div class="dm-form-sec">
    <div><h4>Caddy version</h4><p>Dockmesh ships the versions it was tested with. Switching restarts Caddy and goes back if the new one fails.</p></div>
    <div>
      {#if !info}
        <span class="dm-dim">Not available</span>
      {:else if !info.managed}
        <span class="dm-mono">{info.image || 'custom image'}</span>
        <span class="dm-sub">{info.read_only_reason ?? 'A custom image is set, so the version is managed outside Dockmesh.'}</span>
      {:else}
        <span>Running <b class="dm-mono">{info.current || '—'}</b>{#if info.default && info.current !== info.default} · recommended <b class="dm-mono">{info.default}</b>{/if}</span>
        {#if canEdit}
          <div class="row">
            <select class="dm-input" bind:value={pick} disabled={switching || !running} aria-label="Caddy version">
              {#each info.available as v (v.version)}<option value={v.version}>{v.version}{v.version === info.default ? ' (recommended)' : ''}</option>{/each}
            </select>
            <button type="button" class="dm-btn" disabled={switching || !running || pick === info.current} onclick={() => switchTo(pick)}>Switch</button>
            {#if info.previous}
              <button type="button" class="dm-btn dm-btn-ghost" disabled={switching || !running} onclick={() => switchTo(info!.previous!, true)}>Back to {info.previous}</button>
            {/if}
          </div>
          {#if !running}<span class="dm-sub">Start the proxy to switch versions.</span>{/if}
        {/if}
      {/if}
    </div>
  </div>

  {#if settings}
    <div class="dm-form-sec">
      <div><h4>Certificate contact</h4><p>Email the certificate authority uses for expiry warnings. Optional.</p></div>
      <div><input class="dm-input" type="email" bind:value={form.acme_email} disabled={!canEdit} placeholder="admin@example.org" /></div>
    </div>
    <div class="dm-form-sec">
      <div><h4>Certificate authority</h4><p>Where Let's Encrypt sites get their certificates. Staging certificates are not trusted by browsers.</p></div>
      <div>
        <select class="dm-input" bind:value={form.acme_ca} disabled={!canEdit}>
          <option value="">Let's Encrypt</option>
          <option value="zerossl">ZeroSSL</option>
          <option value="letsencrypt_staging">Let's Encrypt staging (testing)</option>
        </select>
      </div>
    </div>
    <div class="dm-form-sec">
      <div><h4>Trusted proxies</h4><p>Trust the client address that a CDN in front of Caddy forwards. Only enable it when all traffic comes through that CDN.</p></div>
      <div>
        <select class="dm-input" bind:value={form.trusted_proxies} disabled={!canEdit}>
          <option value="">None</option>
          <option value="cloudflare">Cloudflare</option>
        </select>
      </div>
    </div>
    {#if canEdit}
      <div class="save"><button type="button" class="dm-btn dm-btn-primary" disabled={!dirty || saving} onclick={saveSettings}>Save settings</button></div>
    {/if}
  {/if}
</div>

<style>
  .cad { display: flex; flex-direction: column; }
  .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .save { display: flex; justify-content: flex-end; padding-top: 12px; }
</style>
