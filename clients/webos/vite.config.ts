import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, Plugin } from 'vite';
import preact from '@preact/preset-vite';

const root = dirname(fileURLToPath(import.meta.url));

// Same static-emission pattern as clients/tv/vite.config.ts, but the boot
// scripts are read from clients/tv so the Tizen copies stay the single source
// and are never forked. The local index drops only the Samsung $WEBAPIS
// script; the LG SDK script is inserted after diagnostics in Task 3.
function packageStaticFiles(): Plugin {
  return {
    name: 'package-webos-static-files',
    generateBundle() {
      for (const name of ['index.html', 'startup.js', 'fatal-error.js']) {
        const source = name === 'index.html'
          ? readFileSync(resolve(root, name))
          : readFileSync(resolve(root, '../tv', name));
        this.emitFile({ type: 'asset', fileName: name, source });
      }
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [preact(), packageStaticFiles()],
  build: {
    // webOS 4.x ships Chromium 53: esbuild must transpile all syntax down to
    // that engine, and authored CSS declarations stay verbatim because
    // esbuild's minifier re-merges longhands into the `inset` shorthand,
    // which Chromium 53 does not support (same ticket #73 failure as Tizen).
    target: 'chrome53',
    cssTarget: 'chrome53',
    cssMinify: false,
    outDir: 'dist',
    emptyOutDir: true,
    lib: {
      entry: resolve(root, 'src/entry-webos.ts'),
      name: 'TorrentTV',
      formats: ['iife'],
      fileName: () => 'app.js',
      cssFileName: 'app',
    },
  },
});
