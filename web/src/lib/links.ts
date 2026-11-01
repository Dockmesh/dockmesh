/** The link if it is an in-app path, else undefined; a server-stored link
 * must never become an off-site or script URL. */
export function inAppLink(link: string | undefined | null): string | undefined {
  if (!link || !link.startsWith('/') || link.startsWith('//') || /[\\\r\n]/.test(link)) return undefined;
  return link;
}
