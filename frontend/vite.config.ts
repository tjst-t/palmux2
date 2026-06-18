import { fileURLToPath } from 'node:url'

import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// @novnc/novnc 1.7 declares `"exports": "./core/rfb.js"` (a bare string), so the
// package exposes ONLY its root specifier. We need noVNC's own keysym-mapping
// helpers (getKeysym / getKeycode) from core/input/util.js so the Browser tab can
// forward raw keys via rfb.sendKey (S8fe0cb) without forking node_modules. Alias
// a stable specifier to the real (shipped) util.js file — explicit config, not a
// node_modules edit.
const novncUtil = fileURLToPath(
  new URL('./node_modules/@novnc/novnc/core/input/util.js', import.meta.url),
)

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = { ...process.env, ...loadEnv(mode, process.cwd(), '') }
  const apiPort = env.PALMUX2_API_PORT ?? '8080'
  const apiTarget = `http://127.0.0.1:${apiPort}`
  return {
    plugins: [react()],
    resolve: {
      alias: {
        '@novnc/novnc/util': novncUtil,
      },
    },
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
