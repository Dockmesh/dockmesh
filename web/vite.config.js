/// <reference types="vitest/config" />
import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import { paraglideVitePlugin } from '@inlang/paraglide-js';
import enMessages from './messages/en.json' with { type: 'json' };

// A message key that does not exist must fail the build instead of shipping
// a call to undefined; svelte-check catches it too, but not every build runs it.
function messageKeys() {
  let keys;
  return {
    name: 'dockmesh-message-keys',
    enforce: 'pre',
    buildStart() {
      keys = new Set(Object.keys(enMessages));
    },
    transform(code, id) {
      const path = id.split(String.fromCharCode(92)).join('/').split('?')[0];
      if (!/\/web\/src\/.*\.(svelte|ts|js)$/.test(path) || path.includes('/paraglide/')) return null;
      for (const [, key] of code.matchAll(/\bm\.([a-z][a-z0-9_]*)\(/g)) {
        if (!keys.has(key)) this.error(`unknown message key m.${key} in ${id}`);
      }
      return null;
    }
  };
}

export default defineConfig({
  plugins: [
    messageKeys(),
    paraglideVitePlugin({
      project: './project.inlang',
      outdir: './src/lib/paraglide',
      strategy: ['cookie', 'preferredLanguage', 'baseLocale']
    }),
    tailwindcss(),
    sveltekit({
      adapter: adapter({
        pages: 'build',
        assets: 'build',
        fallback: 'index.html',
        precompress: false,
        strict: false
      }),
      preprocess: vitePreprocess(),
      // Kit 3 doesn't generate the $lib alias.
      alias: { $lib: 'src/lib' }
    })
  ],
  build: {
    rollupOptions: {
      // A message key that does not exist must fail the build, not ship as undefined.
      onwarn(warning, warn) {
        if (warning.code === 'MISSING_EXPORT') throw new Error(warning.message);
        warn(warning);
      }
    }
  },
  test: {
    include: ['src/**/*.test.ts']
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true
      }
    }
  }
});
