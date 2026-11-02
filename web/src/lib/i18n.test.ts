import { describe, expect, it } from 'vitest';
import enFile from '../../messages/en.json';
import deFile from '../../messages/de.json';
import errcodesGo from '../../../internal/api/handlers/errcodes.go?raw';

type Message = string | { declarations?: string[]; match: Record<string, string> }[];

const strip = (data: Record<string, unknown>): Record<string, Message> => {
  const { $schema: _schema, ...rest } = data;
  return rest as Record<string, Message>;
};
const en = strip(enFile);
const de = strip(deFile);
const code = Object.values(
  import.meta.glob(['/src/**/*.{svelte,ts}', '!/src/**/*.test.ts', '!/src/lib/paraglide/**'], {
    query: '?raw',
    import: 'default',
    eager: true
  }) as Record<string, string>
).join('\n');

// Variables a message uses: {name} in every variant plus declared inputs.
function vars(msg: Message): string[] {
  const out = new Set<string>();
  const scan = (s: string) => {
    for (const m of s.matchAll(/\{([a-zA-Z_][a-zA-Z0-9_]*)\}/g)) out.add(m[1]);
  };
  if (typeof msg === 'string') scan(msg);
  else
    for (const v of msg) {
      for (const d of v.declarations ?? []) {
        const input = d.match(/^input (\w+)$/);
        if (input) out.add(input[1]);
      }
      Object.values(v.match).forEach(scan);
    }
  return [...out].sort();
}

// Keys looked up by building their name at runtime, checked separately.
const DYNAMIC = /^apierr_/;
// Messages whose call passes a noun twice (as is and lower-cased) so each
// language picks the form its grammar needs.
const PLACEHOLDER_CHOICE: Record<string, string[]> = { ops_cancel_title: ['kind', 'kindLower', 'target'] };

describe('messages', () => {
  it('has the same keys in every language', () => {
    expect(Object.keys(de).sort()).toEqual(Object.keys(en).sort());
  });

  it('uses the same placeholders in every language', () => {
    const bad = Object.keys(en).filter((k) => {
      if (!(k in de)) return false;
      const want = vars(en[k]);
      const got = vars(de[k]);
      const choice = PLACEHOLDER_CHOICE[k];
      if (choice) return [...want, ...got].some((v) => !choice.includes(v));
      return want.join(',') !== got.join(',');
    });
    expect(bad).toEqual([]);
  });

  // Text around inline markup may be empty in one language.
  it('has no empty texts', () => {
    const empty = [...Object.entries(en), ...Object.entries(de)]
      .filter(([k, v]) => typeof v === 'string' && v.trim() === '' && !/_(before|after)$/.test(k))
      .map(([k]) => k);
    expect(empty).toEqual([]);
  });

  it('has no unused keys', () => {
    const used = new Set([...code.matchAll(/\bm\.([a-z][a-z0-9_]*)\b/g)].map((m) => m[1]));
    const unused = Object.keys(en).filter((k) => !used.has(k) && !DYNAMIC.test(k));
    expect(unused).toEqual([]);
  });

  it('translates every fixed server error code', () => {
    const codes = [...errcodesGo.matchAll(/^\t\{".*", "([a-z0-9_]+)"\},\r?$/gm)].map((m) => m[1]);
    expect(codes.length).toBeGreaterThan(100);
    const missing = codes.filter((c) => !(`apierr_${c}` in en));
    expect(missing).toEqual([]);
    const extra = new Set(['network', 'session_renew']);
    const stale = Object.keys(en)
      .filter((k) => k.startsWith('apierr_'))
      .map((k) => k.slice('apierr_'.length))
      .filter((c) => !codes.includes(c) && !extra.has(c));
    expect(stale).toEqual([]);
  });
});
