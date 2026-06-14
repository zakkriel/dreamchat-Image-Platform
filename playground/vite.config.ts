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
  const target = env.VITE_API_TARGET || 'http://localhost:8080'

  return {
    plugins: [react()],
    server: {
      port: 5173,
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
