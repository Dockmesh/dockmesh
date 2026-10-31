<script lang="ts">
  import { page } from '$app/state';
  import { SETTINGS_NAV, matches } from './nav';

  const links = $derived(SETTINGS_NAV.filter((l) => l.show()));
  const groups = $derived([...new Set(links.map((l) => l.group))]);

  function isOn(href: string): boolean {
    const p = page.url.pathname;
    if (href === '/settings') return p === '/settings';
    return matches(p, href);
  }
</script>

<nav class="snav" aria-label="Settings">
  {#each groups as g (g)}
    <div class="g">{g}</div>
    {#each links.filter((l) => l.group === g) as l (l.href)}
      <a href={l.href} class:on={isOn(l.href)} aria-current={isOn(l.href) ? 'page' : undefined} title={l.hint}>
        <span class="l">{l.label}</span>
      </a>
    {/each}
  {/each}
</nav>

<style>
  .snav {
    position: sticky;
    top: 64px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    align-self: start;
  }
  .g { font-size: var(--fs-sm); font-weight: 600; color: var(--subtle); padding: 12px 10px 4px; }
  .g:first-child { padding-top: 0; }
  a {
    padding: 0 10px;
    min-height: 30px;
    display: flex;
    align-items: center;
    gap: 8px;
    border-radius: 7px;
    color: var(--muted);
    text-decoration: none;
  }
  a:hover { background: var(--nav-active); color: var(--fg); }
  a.on { background: var(--nav-active); color: var(--fg); font-weight: 600; }
  .l { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
