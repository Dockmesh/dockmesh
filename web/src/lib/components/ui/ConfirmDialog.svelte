<script lang="ts">
	// Globally-mounted dialog. Reads the current pending request from the
	// confirm store and renders it as a Modal. One-dialog-at-a-time.
	//
	// Used by `confirm.ask(opts)` in /lib/stores/confirm.svelte.ts.
	import Modal from './Modal.svelte';
	import Button from './Button.svelte';
	import { AlertTriangle } from 'lucide-svelte';
	import { confirm } from '$lib/stores/confirm.svelte';

	// Bind a local boolean to the Modal's `open` prop so the close-on-
	// backdrop behaviour works. We mirror it to the store.
	let open = $state(false);
	$effect(() => {
		open = confirm.current !== null;
	});

	// When the user dismisses via X / backdrop / Esc, Modal sets open=false.
	// Translate that into a reject() on the store.
	$effect(() => {
		if (!open && confirm.current !== null) {
			confirm.reject();
		}
	});

	function accept() {
		confirm.accept();
	}
	function reject() {
		confirm.reject();
	}
</script>

{#if confirm.current}
	{@const req = confirm.current}
	<Modal bind:open title={req.title} maxWidth="max-w-md">
		<div class="space-y-3 text-sm">
			{#if req.danger}
				<div
					class="flex items-start gap-3 p-3 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-danger-500)_8%,transparent)]"
				>
					<AlertTriangle class="w-4 h-4 text-[var(--color-danger-400)] shrink-0 mt-0.5" />
					<div class="space-y-1">
						<div>{req.message}</div>
						{#if req.body}
							<div class="text-xs text-[var(--fg-muted)]">{req.body}</div>
						{/if}
					</div>
				</div>
			{:else}
				<div>{req.message}</div>
				{#if req.body}
					<div class="text-xs text-[var(--fg-muted)]">{req.body}</div>
				{/if}
			{/if}
		</div>
		<svelte:fragment slot="footer">
			<Button variant="secondary" onclick={reject}>{req.cancelLabel ?? 'Cancel'}</Button>
			<Button variant={req.danger ? 'danger' : 'primary'} onclick={accept}>
				{req.confirmLabel ?? 'Confirm'}
			</Button>
		</svelte:fragment>
	</Modal>
{/if}
