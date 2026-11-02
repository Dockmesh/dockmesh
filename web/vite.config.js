/// <reference types="vitest/config" />
import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import { paraglideVitePlugin } from '@inlang/paraglide-js';

export default defineConfig({
  plugins: [
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
