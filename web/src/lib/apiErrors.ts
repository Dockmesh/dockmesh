import { m } from '$lib/paraglide/messages.js';

type MessageFn = () => string;
const messages = m as unknown as Record<string, MessageFn | undefined>;

// The server's message in the user's language. Generic http_* codes carry
// runtime detail (names, paths, Docker output) that only the server text
// has, so those stay as sent; replies without JSON fall back per status.
export function apiErrorText(code: unknown, status: number, serverText: string): string {
  if (typeof code === 'string' && code !== '') {
    if (code.startsWith('http_')) return serverText;
    const fn = messages[`apierr_${code}`];
    return typeof fn === 'function' ? fn() : serverText;
  }
  if (status === 401) return m.apierr_unauthorized();
  if (status === 403) return m.apierr_forbidden();
  return serverText;
}
