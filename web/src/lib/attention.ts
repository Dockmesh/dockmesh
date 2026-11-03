import type { AttentionItem } from '$lib/api';
import { m } from '$lib/paraglide/messages.js';
import { getLocale } from '$lib/paraglide/runtime.js';

const SEV: Record<string, number> = { critical: 0, warning: 1, info: 2 };

/** Most severe first, newest first within a severity. */
export function sortAttention(items: AttentionItem[]): AttentionItem[] {
  return items
    .slice()
    .sort((a, b) => (SEV[a.severity] ?? 3) - (SEV[b.severity] ?? 3) || Date.parse(b.since ?? '0') - Date.parse(a.since ?? '0'));
}

type Params = Record<string, string | number>;

const str = (p: Params, k: string) => String(p[k] ?? '');
const num = (p: Params, k: string) => Number(p[k] ?? 0);
const orUnknown = (p: Params, k: string) => str(p, k) || m.attn_unknown();
// The server sends the expiry as a UTC calendar date.
const utcDate = (d: string) => (d ? new Date(`${d}T00:00:00Z`).toLocaleDateString(getLocale(), { timeZone: 'UTC' }) : '');

// The server sends message keys with the names they need; an item whose
// key this build doesn't know keeps the server's English text.
export const ATTENTION_TEXT: Record<string, (p: Params) => string> = {
  agent_outdated: (p) => m.attn_agent_outdated({ name: str(p, 'name') }),
  agent_outdated_detail: (p) => m.attn_agent_outdated_detail({ agent: orUnknown(p, 'agent'), server: str(p, 'server') }),
  agents_outdated: (p) => m.attn_agents_outdated({ count: num(p, 'count') }),
  agents_outdated_detail: (p) => m.attn_agents_outdated_detail({ names: str(p, 'names'), server: str(p, 'server') }),
  alert_firing: (p) => m.attn_alert_firing({ name: str(p, 'name') }),
  backup_failed: (p) => m.attn_backup_failed({ name: str(p, 'name') }),
  backup_failed_detail: () => m.attn_backup_failed_detail(),
  cert_detail: (p) => m.attn_cert_detail({ date: utcDate(str(p, 'valid_to')), issuer: orUnknown(p, 'issuer') }),
  cert_expired: (p) => m.attn_cert_expired({ host: str(p, 'host') }),
  cert_expires_days: (p) => m.attn_cert_expires_days({ host: str(p, 'host'), days: num(p, 'days') }),
  cert_expires_today: (p) => m.attn_cert_expires_today({ host: str(p, 'host') }),
  container_detail: (p) => m.attn_container_detail({ status: str(p, 'status'), host: str(p, 'host') }),
  container_exited: (p) => m.attn_container_exited({ name: str(p, 'name') }),
  container_restarting: (p) => m.attn_container_restarting({ name: str(p, 'name') }),
  container_unhealthy: (p) => m.attn_container_unhealthy({ name: str(p, 'name') }),
  host_offline: (p) => m.attn_host_offline({ name: str(p, 'name') }),
  host_offline_detail: () => m.attn_host_offline_detail(),
  image_update: (p) => m.attn_image_update({ image: str(p, 'image') }),
  image_used_by_count: (p) => m.attn_image_used_by_count({ count: num(p, 'count') }),
  image_used_by_more: (p) => m.attn_image_used_by_more({ name: str(p, 'name'), count: num(p, 'count') }),
  image_used_by_one: (p) => m.attn_image_used_by_one({ name: str(p, 'name') })
};

function render(key: string | undefined, params: Params | undefined, fallback: string): string {
  const fn = key ? ATTENTION_TEXT[key] : undefined;
  return fn ? fn(params ?? {}) : fallback;
}

/** Title and detail of an item in the user's language. */
export function attentionText(a: AttentionItem): { title: string; detail: string } {
  return {
    title: render(a.title_key, a.params, a.title),
    detail: render(a.detail_key, a.params, a.detail ?? '')
  };
}
