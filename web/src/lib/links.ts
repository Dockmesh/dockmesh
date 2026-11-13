/** The link if it is an in-app path, else undefined; a server-stored link
 * must never become an off-site or script URL. */
export function inAppLink(link: string | undefined | null): string | undefined {
  if (!link || !link.startsWith('/') || link.startsWith('//') || /[\\\r\n]/.test(link)) return undefined;
  return link;
}

/** The link if it is a Dockmesh release page on GitHub, else undefined;
 * the release URL comes from the server's cached update check. */
export function releaseLink(link: string | undefined | null): string | undefined {
  if (!link || /[\\\s]/.test(link)) return undefined;
  let u: URL;
  try {
    u = new URL(link);
  } catch {
    return undefined;
  }
  if (u.protocol !== 'https:' || u.host !== 'github.com' || u.username || u.password) return undefined;
  if (!u.pathname.startsWith('/Dockmesh/dockmesh/releases/')) return undefined;
  return u.href;
}
