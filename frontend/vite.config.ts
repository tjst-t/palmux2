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
      // Vite 5+ blocks unknown Host headers. portman exposes the dev server
      // through *.dev.tjstkm.net, so allow that domain explicitly. Also keep
      // localhost in case someone hits it directly.
      allowedHosts: ['.dev.tjstkm.net', 'localhost', '127.0.0.1'],
      proxy: {
        '/api': { target: apiTarget, changeOrigin: true, ws: true },
        '/auth': { target: apiTarget, changeOrigin: true },
      },
    },
  }
})
