<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { Skeleton, Switch } from '$lib/components/ui';
  import { createSettingsForm } from './settingsForm.svelte';

  const form = createSettingsForm();
  let systemInfo = $state<{ version: string; commit: string; build_date: string; go_version: string; os: string; arch: string; uptime_seconds: number } | null>(null);

  onMount(() => {
    if (!allowed('system.update')) {
      goto(allowed('registries.view') ? '/registries' : '/', { replace: true });
      return;
    }
    // Legacy ?tab= deep links.
    const search = new URLSearchParams(page.url.search);
    const tab = search.get('tab');
    const redirectMap: Record<string, string> = {
      account: '/account',
      users: '/users',
      audit: '/audit',
      sso: '/authentication',
      roles: '/users',
      api_tokens: '/tokens',
      registries: '/registries',
      data: '/backups',
      updates: '/settings/updates',
      encryption: '/settings/encryption'
    };
    if (tab && tab in redirectMap) {
      goto(redirectMap[tab], { replace: true });
      return;
    }
    if (search.get('sub') === 'data') {
      goto('/backups', { replace: true });
      return;
    }
    form.load();
    api.system
      .info()
      .then((i) => (systemInfo = i))
      .catch((err) => toast.error('Failed to load system info', err instanceof ApiError ? err.message : undefined));
  });

  function fmtUptime(secs?: number): string {
    if (!secs) return '—';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }
</script>

<div class="dm-page">
  <div class="dm-ph">
    <div>
      <h1>General</h1>
      <div class="dm-sub">Instance address, proxies in front of Dockmesh and the image scanner</div>
    </div>
  </div>

  <div>
    <div class="dm-form-sec">
      <div>
        <h4>Dockmesh</h4>
        <p>The running build of this server.</p>
      </div>
      <div>
        {#if systemInfo}
          <dl class="dm-kv">
            <dt>Version</dt><dd class="dm-mono">{systemInfo.version}</dd>
            <dt>Commit</dt><dd class="dm-mono">{systemInfo.commit.slice(0, 7)}</dd>
            <dt>Built</dt><dd class="dm-mono">{systemInfo.build_date.slice(0, 10)}</dd>
            <dt>Platform</dt><dd class="dm-mono">{systemInfo.os}/{systemInfo.arch} · {systemInfo.go_version}</dd>
            <dt>Uptime</dt><dd class="dm-mono">{fmtUptime(systemInfo.uptime_seconds)}</dd>
          </dl>
        {:else}
          <Skeleton width="320px" height="6rem" />
        {/if}
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>Base URL</h4>
        <p>Used for OIDC callbacks, links in notifications and agent enrollment.</p>
      </div>
      <div>
        <input
          class="dm-input set-in"
          aria-label="Base URL"
          placeholder="https://dockmesh.example.com"
          value={form.get('base_url')}
          oninput={(e) => form.set('base_url', e.currentTarget.value)}
        />
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>Agent public URL</h4>
        <p>The wss:// address agents connect to. Empty derives it from the base URL.</p>
      </div>
      <div>
        <input
          class="dm-input set-in"
          aria-label="Agent public URL"
          placeholder="wss://dockmesh.example.com:8443/connect"
          value={form.get('agent_public_url')}
          oninput={(e) => form.set('agent_public_url', e.currentTarget.value)}
        />
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>Trusted proxies</h4>
        <p>Only these addresses may set the client address and HTTPS headers. Empty means localhost only.</p>
      </div>
      <div>
        <input
          class="dm-input set-in"
          aria-label="Trusted proxies"
          placeholder="10.0.0.5, 172.16.0.0/12"
          value={form.get('trusted_proxies')}
          oninput={(e) => form.set('trusted_proxies', e.currentTarget.value)}
        />
        <span class="dm-sub">Comma-separated IPs or CIDRs (X-Forwarded-For, X-Real-IP, X-Forwarded-Proto).</span>
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>Vulnerability scanner</h4>
        <p>Scan images for known CVEs with Grype.</p>
      </div>
      <div>
        <Switch
          label="Scan images with Grype"
          checked={form.get('scanner_enabled') === 'true'}
          onchange={(v) => form.set('scanner_enabled', v ? 'true' : 'false')}
        />
      </div>
    </div>
  </div>

  <div class="set-bar">
    <button type="button" class="dm-btn" onclick={form.discard} disabled={!form.dirty || form.busy}>Discard</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={form.save} disabled={!form.dirty || form.busy}>
      {form.busy ? 'Saving…' : 'Save'}
    </button>
  </div>
</div>

<style>
  .set-in { width: min(460px, 100%); }
  .set-bar { display: flex; justify-content: flex-end; gap: 8px; border-top: 1px solid var(--border); padding-top: 14px; }
</style>
