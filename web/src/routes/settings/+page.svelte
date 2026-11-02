<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { api, ApiError } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { Skeleton, Switch } from '$lib/components/ui';
  import { createSettingsForm } from './settingsForm.svelte';
  import AgentReachability from '$lib/components/AgentReachability.svelte';
  import { m } from '$lib/paraglide/messages.js';

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
      .catch((err) => toast.error(m.settings_sysinfo_failed(), err instanceof ApiError ? err.message : undefined));
  });

  function fmtUptime(secs?: number): string {
    if (!secs) return '—';
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const mins = Math.floor((secs % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${mins}m`;
    return `${mins}m`;
  }
</script>

<div class="dm-page">
  <div class="dm-ph">
    <div>
      <h1>{m.settings_general_title()}</h1>
      <div class="dm-sub">{m.settings_general_sub()}</div>
    </div>
  </div>

  <div>
    <div class="dm-form-sec">
      <div>
        <h4>Dockmesh</h4>
        <p>{m.settings_build_hint()}</p>
      </div>
      <div>
        {#if systemInfo}
          <dl class="dm-kv">
            <dt>{m.settings_version()}</dt><dd class="dm-mono">{systemInfo.version}</dd>
            <dt>{m.settings_commit()}</dt><dd class="dm-mono">{systemInfo.commit.slice(0, 7)}</dd>
            <dt>{m.settings_built()}</dt><dd class="dm-mono">{systemInfo.build_date.slice(0, 10)}</dd>
            <dt>{m.settings_platform()}</dt><dd class="dm-mono">{systemInfo.os}/{systemInfo.arch} · {systemInfo.go_version}</dd>
            <dt>{m.settings_uptime()}</dt><dd class="dm-mono">{fmtUptime(systemInfo.uptime_seconds)}</dd>
          </dl>
        {:else}
          <Skeleton width="320px" height="6rem" />
        {/if}
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_base_url()}</h4>
        <p>{m.settings_base_url_before()} <span class="dm-mono">DOCKMESH_BASE_URL</span>{m.settings_base_url_after()}</p>
      </div>
      <div>
        <input
          class="dm-input set-in"
          aria-label={m.settings_base_url()}
          placeholder="https://dockmesh.example.com"
          value={form.get('base_url')}
          oninput={(e) => form.set('base_url', e.currentTarget.value)}
        />
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_agent_url()}</h4>
        <p>{m.settings_agent_url_before()} <span class="dm-mono">DOCKMESH_AGENT_PUBLIC_URL</span>{m.settings_wins_after()}</p>
      </div>
      <div>
        <input
          class="dm-input set-in"
          aria-label={m.settings_agent_url()}
          placeholder="wss://dockmesh.example.com:8443/connect"
          value={form.get('agent_public_url')}
          oninput={(e) => form.set('agent_public_url', e.currentTarget.value)}
        />
        <div class="set-reach"><AgentReachability auto={false} showSettingsLink={false} /></div>
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_trusted_proxies()}</h4>
        <p>{m.settings_trusted_proxies_hint()}</p>
      </div>
      <div>
        <input
          class="dm-input set-in"
          aria-label={m.settings_trusted_proxies()}
          placeholder="10.0.0.5, 172.16.0.0/12"
          value={form.get('trusted_proxies')}
          oninput={(e) => form.set('trusted_proxies', e.currentTarget.value)}
        />
        <span class="dm-sub">{m.settings_trusted_proxies_sub()}</span>
      </div>
    </div>

    <div class="dm-form-sec">
      <div>
        <h4>{m.settings_scanner()}</h4>
        <p>{m.settings_scanner_hint()}</p>
      </div>
      <div>
        <Switch
          label={m.settings_scanner_switch()}
          checked={form.get('scanner_enabled') === 'true'}
          onchange={(v) => form.set('scanner_enabled', v ? 'true' : 'false')}
        />
      </div>
    </div>
  </div>

  <div class="set-bar">
    <button type="button" class="dm-btn" onclick={form.discard} disabled={!form.dirty || form.busy}>{m.settings_discard()}</button>
    <button type="button" class="dm-btn dm-btn-primary" onclick={form.save} disabled={!form.dirty || form.busy}>
      {form.busy ? m.common_saving() : m.common_save()}
    </button>
  </div>
</div>

<style>
  .set-in { width: min(460px, 100%); }
  .set-reach { margin-top: 10px; width: min(560px, 100%); }
  .set-bar { display: flex; justify-content: flex-end; gap: 8px; border-top: 1px solid var(--border); padding-top: 14px; }
</style>
