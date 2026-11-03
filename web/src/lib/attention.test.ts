import { afterEach, describe, expect, it } from 'vitest';
import attentionGo from '../../../internal/api/handlers/attention.go?raw';
import { baseLocale, overwriteGetLocale } from '$lib/paraglide/runtime.js';
import type { AttentionItem } from './api';
import { ATTENTION_TEXT, attentionText } from './attention';

const item = (over: Partial<AttentionItem>): AttentionItem => ({
  id: 'x',
  kind: 'x',
  severity: 'warning',
  title: 'server title',
  link: '/',
  object: { type: 'x', id: 'x' },
  ...over
});

afterEach(() => overwriteGetLocale(() => baseLocale));

describe('attentionText', () => {
  it('knows every key the server sends', () => {
    const patterns = [
      /(?:TitleKey|DetailKey):\s*"([a-z_]+)"/g,
      /(?:title|detail)Key :?= .*"([a-z_]+)"$/gm,
      // Container items use their kind as the title key.
      /kind, severity, title = "([a-z_]+)"/g
    ];
    const keys = new Set(patterns.flatMap((re) => [...attentionGo.matchAll(re)].map((m) => m[1])));
    expect([...keys].sort()).toEqual(Object.keys(ATTENTION_TEXT).sort());
  });

  const cases: [string, Partial<AttentionItem>, string, string][] = [
    ['en', { title_key: 'host_offline', detail_key: 'host_offline_detail', params: { name: 'dm-agent1' } }, 'dm-agent1 is offline', 'The agent is not connected.'],
    ['de', { title_key: 'host_offline', detail_key: 'host_offline_detail', params: { name: 'dm-agent1' } }, 'dm-agent1 ist offline', 'Der Agent ist nicht verbunden.'],
    ['de', { title_key: 'image_update', detail_key: 'image_used_by_count', params: { image: 'nginx:1', count: 1 } }, 'Update für nginx:1 verfügbar', 'Von 1 Container genutzt.'],
    ['de', { title_key: 'cert_expires_days', params: { host: 'a.example', days: 5 } }, 'Zertifikat für a.example läuft in 5 Tagen ab', ''],
    ['en', { title_key: 'cert_expires_days', detail_key: 'cert_detail', params: { host: 'a.example', days: 1, valid_to: '2026-11-08', issuer: '' } }, 'Certificate for a.example expires in 1 day', 'Valid until 11/8/2026, issued by unknown.'],
    ['de', { title_key: 'backup_failed', detail: 'disk full', params: { name: 'nightly' } }, 'Backup nightly ist fehlgeschlagen', 'disk full'],
    ['de', { title_key: 'agent_outdated', detail_key: 'agent_outdated_detail', params: { name: 'a1', agent: '', server: '0.5.1' } }, 'a1 nutzt einen anderen Agent-Build', 'Agent unbekannt, Server 0.5.1.'],
    ['de', { title: 'Check compose.yaml', detail: 'body' }, 'Check compose.yaml', 'body'],
    ['de', { title_key: 'from_a_newer_server', title: 'Something new' }, 'Something new', '']
  ];
  for (const [locale, over, title, detail] of cases) {
    it(`${locale} ${over.title_key ?? over.title}`, () => {
      overwriteGetLocale(() => locale as typeof baseLocale);
      expect(attentionText(item(over))).toEqual({ title, detail });
    });
  }
});
