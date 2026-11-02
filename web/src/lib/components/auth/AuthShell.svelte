<script lang="ts">
  // Frame for the pages shown without a session (sign-in, setup): brand,
  // one card, a quiet footer. These pages render outside the app chrome,
  // so the theme toggle lives here.
  import type { Snippet } from 'svelte';
  import { Sun, Moon } from 'lucide-svelte';
  import Logo from '$lib/components/shell/Logo.svelte';

  interface Props {
    width?: number;
    children: Snippet;
    footer?: Snippet;
  }

  let { width = 400, children, footer }: Props = $props();

  let theme = $state<'light' | 'dark'>('dark');
  $effect(() => {
    const stored = localStorage.getItem('dockmesh.theme');
    if (stored === 'light' || stored === 'dark') theme = stored;
    else theme = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  });
  $effect(() => {
    document.documentElement.dataset.theme = theme;
  });
  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem('dockmesh.theme', theme);
  }
</script>

<div class="auth">
  <button
    type="button"
    class="theme-toggle auth-theme"
    onclick={toggleTheme}
    aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
    title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
  >
    {#if theme === 'dark'}<Sun size={13} strokeWidth={1.5} />{:else}<Moon size={13} strokeWidth={1.5} />{/if}
  </button>

  <div class="auth-col" style:--auth-w="{width}px">
    <div class="auth-brand">
      <span class="auth-mark"><Logo width={78} height={32} /></span>
      <span class="auth-name">Dockmesh</span>
    </div>

    <section class="auth-card">
      {@render children()}
    </section>

    <footer class="auth-foot">
      {#if footer}{@render footer()}{:else}Dockmesh · self-hosted · AGPL-3.0{/if}
    </footer>
  </div>
</div>

<style>
  .auth {
    position: relative;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
    padding: 56px 16px 32px;
    background: var(--bg);
    color: var(--fg);
  }
  .auth-theme {
    position: absolute;
    top: 20px;
    right: 24px;
  }
  .auth-col {
    width: 100%;
    max-width: var(--auth-w);
    margin: auto;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 24px;
  }
  .auth-brand {
    display: inline-flex;
    align-items: center;
    gap: 12px;
  }
  .auth-mark {
    color: var(--accent);
    display: inline-flex;
  }
  .auth-name {
    font-size: var(--fs-lg);
    font-weight: 700;
    letter-spacing: -0.015em;
  }
  .auth-card {
    width: 100%;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    padding: 28px 32px 32px;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04), 0 12px 32px -16px rgba(0, 0, 0, 0.18);
  }
  .auth-foot {
    font-size: var(--fs-xs);
    color: var(--subtle);
    text-align: center;
  }

  /* Shared content classes for the pages rendered inside the card. */
  .auth-card :global(.auth-form) {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .auth-card :global(.auth-title) {
    margin: 0;
    font-size: var(--fs-xl);
    font-weight: 700;
    letter-spacing: -0.02em;
    line-height: 1.25;
  }
  .auth-card :global(.auth-sub) {
    margin: -10px 0 0;
    color: var(--muted);
    font-size: var(--fs-base);
  }
  .auth-card :global(.auth-notice),
  .auth-card :global(.auth-error) {
    margin: 0;
    padding: 9px 12px;
    border-radius: var(--r);
    font-size: var(--fs-sm);
    line-height: 1.5;
  }
  .auth-card :global(.auth-notice) {
    background: var(--band);
    border: 1px solid var(--border);
    color: var(--muted);
  }
  .auth-card :global(.auth-error) {
    background: var(--bad-bg);
    border: 1px solid var(--bad-bd);
    color: var(--fg);
  }
  .auth-card :global(.auth-error-detail) {
    display: block;
    margin-top: 2px;
    color: var(--muted);
    font-size: var(--fs-xs);
    overflow-wrap: anywhere;
  }
  .auth-card :global(.auth-label-row) {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
  }
  .auth-card :global(.auth-label-row label) {
    font-size: 12px;
    color: var(--muted);
    font-weight: 500;
  }
  .auth-card :global(.auth-link) {
    all: unset;
    cursor: pointer;
    color: var(--accent);
    font-size: var(--fs-xs);
    border-radius: 3px;
  }
  .auth-card :global(.auth-link:hover) { text-decoration: underline; }
  .auth-card :global(.auth-link:focus-visible) { outline: 2px solid var(--accent); outline-offset: 2px; }
  .auth-card :global(.auth-hint) {
    margin: 2px 0 0;
    color: var(--subtle);
    font-size: var(--fs-xs);
    line-height: 1.5;
  }
  .auth-card :global(.auth-cmd) {
    display: block;
    margin-top: 4px;
    padding: 6px 8px;
    border-radius: var(--r-sm);
    background: var(--field);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 12px;
    overflow-wrap: anywhere;
    user-select: all;
  }
  .auth-card :global(.auth-block) {
    width: 100%;
    min-height: 38px;
  }
  .auth-card :global(.auth-or) {
    display: flex;
    align-items: center;
    gap: 12px;
    color: var(--subtle);
    font-size: var(--fs-xs);
  }
  .auth-card :global(.auth-or::before),
  .auth-card :global(.auth-or::after) {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border);
  }

  @media (max-width: 480px) {
    .auth { padding: 56px 12px 24px; }
    .auth-theme { top: 14px; right: 14px; }
    .auth-card { padding: 22px 18px 24px; border-radius: var(--r-lg); }
  }
</style>
