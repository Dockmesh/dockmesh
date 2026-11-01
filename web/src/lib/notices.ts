import type { Notice, NoticeSeverity } from '$lib/api';

export const noticeLabel: Record<NoticeSeverity, string> = {
  critical: 'Critical',
  warning: 'Action required',
  info: 'Info',
};

const rank: Record<NoticeSeverity, number> = { critical: 0, warning: 1, info: 2 };

/** Most severe first, oldest first within a severity. */
export function sortNotices(list: Notice[]): Notice[] {
  return [...list].sort((a, b) => rank[a.severity] - rank[b.severity] || a.created_at.localeCompare(b.created_at));
}

/** Notices the banner carries for this user: warnings and critical ones
 * the user has not hidden. Info notices never are. */
export function bannerNotices(list: Notice[]): Notice[] {
  return sortNotices(list).filter((n) => n.severity !== 'info' && !n.dismissed);
}

/** The one notice the app shell shows as a banner. */
export function bannerNotice(list: Notice[]): Notice | undefined {
  return bannerNotices(list)[0];
}

/** Drops the attention items the banner already shows, so one notice is
 * never in both places; a notice hidden from the banner comes back here. */
export function withoutBanner<T extends { kind: string; object?: { id: string } }>(items: T[], list: Notice[]): T[] {
  const inBanner = new Set(bannerNotices(list).map((n) => String(n.id)));
  return items.filter((a) => a.kind !== 'notice' || !inBanner.has(a.object?.id ?? ''));
}

/** Info notices only explain a change, so their link shows details; the
 * others link to the place where the problem is fixed. */
export function noticeAction(n: Notice): string {
  return n.severity === 'info' ? 'Details' : 'Fix it';
}

export type NoticeBlock = { kind: 'text'; text: string } | { kind: 'list'; items: string[] };

/** Splits a notice body into paragraphs and lists: every line is its own
 * paragraph, consecutive lines starting with "- " form one list. */
export function noticeBlocks(body: string | undefined): NoticeBlock[] {
  const blocks: NoticeBlock[] = [];
  for (const raw of (body ?? '').split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    if (line.startsWith('- ')) {
      const last = blocks[blocks.length - 1];
      if (last?.kind === 'list') last.items.push(line.slice(2));
      else blocks.push({ kind: 'list', items: [line.slice(2)] });
    } else {
      blocks.push({ kind: 'text', text: line });
    }
  }
  return blocks;
}

/** One-line summary for the banner: the body's first paragraph. */
export function noticeSummary(body: string | undefined): string {
  const first = noticeBlocks(body)[0];
  if (!first) return '';
  return first.kind === 'text' ? first.text : first.items.join(', ');
}
