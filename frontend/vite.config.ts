import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = { ...process.env, ...loadEnv(mode, process.cwd(), '') }
  const apiPort = env.PALMUX2_API_PORT ?? '8080'
  const apiTarget = `http://127.0.0.1:${apiPort}`
  return {
    plugins: [react()],
    server: {
      host: '0.0.0.0',
      proxy: {
        '/api': { target: apiTarget, changeOrigin: true, ws: true },
        '/auth': { target: apiTarget, changeOrigin: true },
      },
    },
  }
})
