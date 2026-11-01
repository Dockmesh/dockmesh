import { page } from '$app/state';
import { setTabParam } from './tabs';

// The open tab of a page, mirrored in ?tab=. replaceState does not update
// page.url, so the tab lives in state and follows the URL only when a real
// navigation (a link to ?tab=…, back/forward) changes it.
export function urlTab<T extends string>(tabs: readonly T[], defaultTab: T) {
  const read = (): T => {
    const t = page.url.searchParams.get('tab') as T | null;
    return t && tabs.includes(t) ? t : defaultTab;
  };
  let current = $state<T>(read());
  $effect(() => {
    current = read();
  });
  return {
    get current() {
      return current;
    },
    set(t: string) {
      if (!tabs.includes(t as T)) return;
      current = t as T;
      setTabParam(t, defaultTab);
    }
  };
}
