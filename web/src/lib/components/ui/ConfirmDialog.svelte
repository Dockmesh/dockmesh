<script lang="ts">
	// Globally-mounted dialog for `confirm.ask(opts)`. One dialog at a time.
	import Modal from './Modal.svelte';
	import { confirm } from '$lib/stores/confirm.svelte';

	let open = $state(false);
	let typed = $state('');
	$effect(() => {
		open = confirm.current !== null;
		if (confirm.current) typed = '';
	});

	// Dismissing via X, backdrop or Esc closes the Modal; treat that as cancel.
	$effect(() => {
		if (!open && confirm.current !== null) {
			confirm.reject();
		}
	});

	const locked = $derived(
		!!confirm.current?.typeToConfirm && typed.trim() !== confirm.current.typeToConfirm
	);

	function accept() {
		if (locked) return;
		confirm.accept();
	}
</script>

{#if confirm.current}
	{@const req = confirm.current}
	<Modal bind:open title={req.title} maxWidth="max-w-md" {footer}>
		<form
			class="cf"
			onsubmit={(e) => {
				e.preventDefault();
				accept();
			}}
		>
			<p>{req.message}</p>
			{#if req.body}
				<p class="cf-body">{req.body}</p>
			{/if}
			{#if req.typeToConfirm}
				<div class="dm-field">
					<label for="cf-word">Type <b class="dm-mono">{req.typeToConfirm}</b> to confirm</label>
					<input id="cf-word" class="dm-input dm-mono" autocomplete="off" spellcheck="false" bind:value={typed} data-autofocus />
				</div>
			{/if}
		</form>
	</Modal>
{/if}

{#snippet footer()}
	{#if confirm.current}
		{@const req = confirm.current}
		<button type="button" class="dm-btn" onclick={() => confirm.reject()}>{req.cancelLabel ?? 'Cancel'}</button>
		<button
			type="button"
			class="dm-btn {req.danger ? 'dm-btn-danger' : 'dm-btn-primary'}"
			disabled={locked}
			onclick={accept}
		>
			{req.confirmLabel ?? 'Confirm'}
		</button>
	{/if}
{/snippet}

<style>
	.cf { display: flex; flex-direction: column; gap: 12px; }
	.cf p { margin: 0; }
	.cf-body { color: var(--muted); font-size: var(--fs-sm); }
</style>
