import { replaceState } from '$app/navigation';
import { page } from '$app/state';

// Keeps the open tab in ?tab= without a navigation, so reloads and shared
// links land on it; the default tab leaves the parameter out.
export function setTabParam(tab: string, defaultTab: string) {
  const url = new URL(page.url.href);
  if (tab === defaultTab) url.searchParams.delete('tab');
  else url.searchParams.set('tab', tab);
  replaceState(url, page.state);
}
