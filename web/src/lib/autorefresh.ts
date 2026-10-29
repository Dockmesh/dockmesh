// Tiny helper for "poll this page every N seconds". Use in a component
// mount effect and return its cleanup function so the interval stops
// when the component unmounts.
//
// We deliberately do NOT use the browser's Page Visibility API: on a
// long-running ops dashboard, operators tab away and tab back, and
// they expect the data to be current both during and after the tab
// switch. A background tab that stopped refreshing would show stale
// state for minutes after focus returns, which is the opposite of
// what a live fleet view should do.
//
// If a page has a more specific signal (WebSocket events for docker /
// stacks filesystem, a running migration poll, …) it should prefer
// that. Polling is the fallback for endpoints that don't emit events
// (backups, alerts, system metrics, …).
//
// Usage in a Svelte 5 component:
//
//     $effect(() => autoRefresh(load, 10_000));
//
// The returned function is called when the effect re-runs or the
// component unmounts.

// A tick is skipped while the previous call is still pending, so slow
// fan-out requests never pile up.
export function autoRefresh(fn: () => void | Promise<void>, intervalMs: number): () => void {
	let pending = false;
	const id = setInterval(async () => {
		if (pending) return;
		pending = true;
		try {
			await fn();
		} catch {
			/* the page's own load reports errors */
		} finally {
			pending = false;
		}
	}, intervalMs);
	return () => clearInterval(id);
}
