import type { AttentionItem } from '$lib/api';

const SEV: Record<string, number> = { critical: 0, warning: 1, info: 2 };

/** Most severe first, newest first within a severity. */
export function sortAttention(items: AttentionItem[]): AttentionItem[] {
  return items
    .slice()
    .sort((a, b) => (SEV[a.severity] ?? 3) - (SEV[b.severity] ?? 3) || Date.parse(b.since ?? '0') - Date.parse(a.since ?? '0'));
}
