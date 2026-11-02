<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, ApiError, type ProxyVersionInfo, type ProxySettings } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';

  // Caddy version and the options that apply to every site.
  let { running, onchanged }: { running: boolean; onchanged?: () => void } = $props();

  const canEdit = $derived(allowed('proxy.update'));
  let info = $state<ProxyVersionInfo | null>(null);
  let settings = $state<ProxySettings | null>(null);
  let form = $state<ProxySettings>({ acme_email: '', acme_ca: '', trusted_proxies: '' });
  let pick = $state('');
  let switching = $state(false);
  let saving = $state(false);

  async function load() {
    const [v, s] = await Promise.all([
      api.proxy.version().catch(() => null),
      api.proxy.settings().catch(() => null)
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
      title: rollback ? m.proxy_caddy_rollback_title() : m.proxy_caddy_switch_title({ version }),
      message: m.proxy_caddy_switch_message({ version }),
      body: m.proxy_caddy_switch_body(),
      confirmLabel: rollback ? m.proxy_caddy_go_back() : m.proxy_caddy_switch()
    });
    if (!ok) return;
    switching = true;
    try {
      info = rollback
        ? await api.proxy.rollbackVersion()
        : await api.proxy.setVersion(version);
      toast.success(m.proxy_caddy_runs({ version: info.current }));
      onchanged?.();
    } catch (e) {
      toast.error(m.proxy_caddy_switch_failed(), e instanceof ApiError ? e.message : undefined);
      await load();
    } finally {
      switching = false;
    }
  }

  async function saveSettings() {
    saving = true;
    try {
      settings = await api.proxy.updateSettings(form);
      form = { ...settings };
      toast.success(m.proxy_settings_saved());
    } catch (e) {
      toast.error(m.proxy_saving_failed(), e instanceof ApiError ? e.message : undefined);
    } finally {
      saving = false;
    }
  }
  const dirty = $derived(!!settings && (form.acme_email !== settings.acme_email || form.acme_ca !== settings.acme_ca || form.trusted_proxies !== settings.trusted_proxies));
</script>

<div class="cad">
  <div class="dm-form-sec">
    <div><h4>{m.proxy_caddy_version()}</h4><p>{m.proxy_caddy_version_hint()}</p></div>
    <div>
      {#if !info}
        <span class="dm-dim">{m.proxy_not_available()}</span>
      {:else if !info.managed}
        <span class="dm-mono">{info.image || m.proxy_custom_image()}</span>
        <span class="dm-sub">{info.read_only_reason ?? m.proxy_custom_image_hint()}</span>
      {:else}
        <span>{m.proxy_caddy_running()} <b class="dm-mono">{info.current || '—'}</b>{#if info.default && info.current !== info.default} · {m.proxy_caddy_recommended()} <b class="dm-mono">{info.default}</b>{/if}</span>
        {#if canEdit}
          <div class="row">
            <select class="dm-input" bind:value={pick} disabled={switching || !running} aria-label={m.proxy_caddy_version()}>
              {#each info.available as v (v.version)}<option value={v.version}>{v.version}{v.version === info.default ? ` (${m.proxy_caddy_recommended()})` : ''}</option>{/each}
            </select>
            <button type="button" class="dm-btn" disabled={switching || !running || pick === info.current} onclick={() => switchTo(pick)}>{m.proxy_caddy_switch()}</button>
            {#if info.previous}
              <button type="button" class="dm-btn dm-btn-ghost" disabled={switching || !running} onclick={() => switchTo(info!.previous!, true)}>{m.proxy_caddy_back_to({ version: info.previous })}</button>
            {/if}
          </div>
          {#if !running}<span class="dm-sub">{m.proxy_caddy_start_to_switch()}</span>{/if}
        {/if}
      {/if}
    </div>
  </div>

  {#if settings}
    <div class="dm-form-sec">
      <div><h4>{m.proxy_cert_contact()}</h4><p>{m.proxy_cert_contact_hint()}</p></div>
      <div><input class="dm-input" type="email" bind:value={form.acme_email} disabled={!canEdit} placeholder="admin@example.org" /></div>
    </div>
    <div class="dm-form-sec">
      <div><h4>{m.proxy_cert_authority()}</h4><p>{m.proxy_cert_authority_hint()}</p></div>
      <div>
        <select class="dm-input" bind:value={form.acme_ca} disabled={!canEdit}>
          <option value="">Let's Encrypt</option>
          <option value="zerossl">ZeroSSL</option>
          <option value="letsencrypt_staging">{m.proxy_le_staging()}</option>
        </select>
      </div>
    </div>
    <div class="dm-form-sec">
      <div><h4>{m.proxy_trusted_proxies()}</h4><p>{m.proxy_trusted_proxies_hint()}</p></div>
      <div>
        <select class="dm-input" bind:value={form.trusted_proxies} disabled={!canEdit}>
          <option value="">{m.common_none()}</option>
          <option value="cloudflare">Cloudflare</option>
        </select>
      </div>
    </div>
    {#if canEdit}
      <div class="save"><button type="button" class="dm-btn dm-btn-primary" disabled={!dirty || saving} onclick={saveSettings}>{m.proxy_save_settings()}</button></div>
    {/if}
  {/if}
</div>

<style>
  .cad { display: flex; flex-direction: column; }
  .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .save { display: flex; justify-content: flex-end; padding-top: 12px; }
</style>
