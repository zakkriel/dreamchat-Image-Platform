import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// The backend (cmd/api) ships no CORS middleware, so a browser cannot call it
// cross-origin directly. For local dev we proxy `/api/*` to the real API so the
// playground can run same-origin. The proxy target defaults to the documented
// local API address and is overridable via VITE_API_TARGET in `.env`.
//
// This is a dev-server convenience only — no backend code is touched.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const target = env.VITE_API_TARGET || 'http://localhost:8081'

  return {
    plugins: [react()],
    server: {
      // Port 5174, not Vite's default 5173: the sibling `dreamchat-frontend`
      // dev server owns 5173 (`--strictPort`) in the shared dev environment,
      // and both must run concurrently. strictPort makes a clash fail loudly
      // instead of silently drifting to a port the docs don't mention.
      port: 5174,
      strictPort: true,
      proxy: {
        '/api': {
          target,
          changeOrigin: true,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
      },
    },
  }
})
