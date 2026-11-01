// Host tags: lowercase letters, digits and hyphens, at most 20 per host.
export const MAX_HOST_TAGS = 20;

// Why tag cannot be added to current, or null when it can.
export function tagProblem(tag: string, current: string[]): { title: string; detail?: string } | null {
  if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(tag)) return { title: 'Invalid tag', detail: 'lowercase letters, digits, hyphens, 1-32 chars' };
  if (current.length >= MAX_HOST_TAGS) return { title: `Max ${MAX_HOST_TAGS} tags` };
  return null;
}
