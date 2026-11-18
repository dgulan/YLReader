import { defineConfig } from 'vite'

// Real-backend reading tests must explicitly supply their owned loopback API.
// Keep the synthetic preview configuration proxy-free.
const target = new URL(process.env.E2E_API_TARGET ?? 'invalid:')
if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1' || !target.port || target.username || target.password || target.pathname !== '/' || target.search || target.hash) {
  throw new Error('Reading preview requires an explicit HTTP loopback API origin')
}
export default defineConfig({ preview: { proxy: { '/api': { target: target.origin } } } })
