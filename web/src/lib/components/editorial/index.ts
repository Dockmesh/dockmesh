/**
 * Editorial primitives — Slice 1 foundation.
 *
 * The visual language is documented in the install-wizard slice
 * (a8bcf47, May 2026) and the claude-design mockups under
 * `Dockmesh Wizard (2)/` — line-art, hairline borders, single cyan
 * brand-500 accent, italic-serif accent words, mono uppercase eyebrows.
 *
 * These components do NOT replace `lib/components/ui/*` — they live
 * alongside it. Pages opt in by wrapping content in `<EditorialPage>`
 * and using the editorial components.
 */

export { default as EditorialPage } from './EditorialPage.svelte';
export { default as Eyebrow } from './Eyebrow.svelte';
export { default as StatusPill } from './StatusPill.svelte';
export { default as Sparkline } from './Sparkline.svelte';
export { default as Field } from './Field.svelte';
export { default as EdRow } from './EdRow.svelte';
export { default as EdMetric } from './EdMetric.svelte';
export { default as EditorialModal } from './EditorialModal.svelte';
