import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, Plugin } from 'vite';
import preact from '@preact/preset-vite';

const root = dirname(fileURLToPath(import.meta.url));

function packageStaticFiles(): Plugin {
  return {
    name: 'package-tizen-static-files',
    generateBundle() {
      for (const name of ['index.html', 'startup.js', 'fatal-error.js']) {
        this.emitFile({ type: 'asset', fileName: name, source: readFileSync(resolve(root, name)) });
      }
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [preact(), packageStaticFiles()],
  build: {
    target: 'es2017',
    outDir: 'dist',
    emptyOutDir: true,
    // Keep authored declarations verbatim: esbuild's CSS minifier re-merges
    // top/right/bottom/left longhands into the `inset` shorthand, which
    // Tizen 5.0-era Chromium 63 does not support (ticket #73).
    cssMinify: false,
    lib: {
      entry: resolve(root, 'src/main.tsx'),
      name: 'TorrentTV',
      formats: ['iife'],
      fileName: () => 'app.js',
      cssFileName: 'app',
    },
  },
});
