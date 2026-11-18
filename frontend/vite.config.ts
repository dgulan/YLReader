import { fileURLToPath, URL } from 'node:url'
import { createHash } from 'node:crypto'
import { readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'

import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'

function appShellServiceWorkerPlugin(): Plugin {
  return {
    name: 'ylreader-app-shell-sw',
    apply: 'build',
    async writeBundle(options, bundle) {
      const outDir = options.dir
      if (!outDir) return
      const swPath = path.join(outDir, 'sw.js')
      let swSource: string
      try {
        swSource = await readFile(swPath, 'utf8')
      } catch {
        return
      }
      const assets = Object.keys(bundle)
        .filter(name => name.startsWith('assets/') && !name.endsWith('.map'))
        .sort()
        .map(name => `/${name}`)
      const hash = createHash('sha256')
      hash.update(assets.join('\n'))
      for (const key of Object.keys(bundle).sort()) {
        const entry = bundle[key]
        if (entry && entry.type === 'asset') {
          hash.update(typeof entry.source === 'string' ? entry.source : Buffer.from(entry.source))
        } else if (entry && entry.type === 'chunk') {
          hash.update(entry.code)
        }
      }
      const version = hash.digest('hex').slice(0, 12)
      const updated = swSource
        .replace("'__YLREADER_SW_VERSION__'", JSON.stringify(version))
        .replace("'__YLREADER_PRECACHE_ASSETS__'", JSON.stringify(assets))
      await writeFile(swPath, updated, 'utf8')
    },
  }
}

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue(), appShellServiceWorkerPlugin()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    // 开发期将 /api 代理到后端，规避 CORS
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
