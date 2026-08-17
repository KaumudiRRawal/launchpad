// Imported from vitest/config rather than vite so the `test` block below is
// type-checked alongside the rest of the config.
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The dev server proxies /v1 to the control plane so the browser only ever
// talks to one origin. The alternative — pointing the client straight at
// :8080 — would mean adding CORS to the API purely to support local
// development, and a permissive Access-Control-Allow-Origin is a bad thing to
// have to remember to tighten before production.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // The control plane's own default address. A different one is a one-line
      // change here, which is cheaper than a configuration layer nobody uses.
      '/v1': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
  },
})
