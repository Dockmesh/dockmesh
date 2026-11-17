// The legacy-layout notice on a host page is hidden per user and host in
// localStorage: nothing breaks while it shows, so a server-side notice
// would be more than it needs.

export const LEGACY_NOTICE_HIDE_MS = 30 * 24 * 60 * 60 * 1000;

type KV = Pick<Storage, 'getItem' | 'setItem'>;

const key = (userId: string, hostId: string) => `dm_legacy_layout_hidden:${userId}:${hostId}`;

/** True while the user hid the notice for this host less than 30 days ago. */
export function legacyNoticeHidden(store: KV | undefined, userId: string, hostId: string, now = Date.now()): boolean {
  if (!store) return false;
  let at: number;
  try {
    at = Number(store.getItem(key(userId, hostId)));
  } catch {
    return false;
  }
  return Number.isFinite(at) && at > 0 && at <= now && now - at < LEGACY_NOTICE_HIDE_MS;
}

export function hideLegacyNotice(store: KV | undefined, userId: string, hostId: string, now = Date.now()): void {
  try {
    store?.setItem(key(userId, hostId), String(now));
  } catch {
    /* storage full or blocked: the notice stays hidden until a reload */
  }
}

/** localStorage, or undefined where the browser blocks it. */
export function browserStorage(): KV | undefined {
  try {
    return typeof localStorage === 'undefined' ? undefined : localStorage;
  } catch {
    return undefined;
  }
}
