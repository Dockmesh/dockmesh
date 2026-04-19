// Global confirm-dialog store. Components call `await confirm.ask(opts)`
// and get back a boolean — same shape as the native `confirm()` API but
// rendered with a styled Modal that matches the rest of the UI.
//
// A single <ConfirmDialog> in the root layout subscribes to this store
// and renders the current request. One-dialog-at-a-time is enough for
// our needs; if the user clicks two destructive buttons in quick
// succession the second call waits until the first resolves.
//
// Introduced in the v1 UI polish pass to replace ~30 native
// window.confirm() calls scattered across the routes.

export interface ConfirmOptions {
	title: string;
	message: string;
	// Body is an optional second paragraph, typically explaining side
	// effects or what won't happen (e.g. "containers keep running").
	body?: string;
	confirmLabel?: string;
	cancelLabel?: string;
	// When true, confirm button renders with the danger variant (red
	// background, white text) and the modal header gets a warning icon.
	// Use for truly destructive actions (delete, kill).
	danger?: boolean;
}

interface PendingRequest extends ConfirmOptions {
	resolve: (v: boolean) => void;
}

function createConfirmStore() {
	let current = $state<PendingRequest | null>(null);

	async function ask(opts: ConfirmOptions): Promise<boolean> {
		// Queue: if a dialog is already open, wait for it to resolve
		// before opening another. Rare in practice.
		while (current !== null) {
			await new Promise((r) => setTimeout(r, 50));
		}
		return new Promise<boolean>((resolve) => {
			current = { ...opts, resolve };
		});
	}

	function close(result: boolean) {
		if (!current) return;
		const r = current.resolve;
		current = null;
		r(result);
	}

	return {
		get current() {
			return current;
		},
		ask,
		accept: () => close(true),
		reject: () => close(false)
	};
}

export const confirm = createConfirmStore();
