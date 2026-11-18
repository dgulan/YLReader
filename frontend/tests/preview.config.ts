import { defineConfig } from 'vite'

// Synthetic browser tests must never inherit the development /api proxy.
// Unmocked API requests remain on this static preview and cannot reach a backend.
export default defineConfig({ preview: { proxy: {} } })
