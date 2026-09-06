import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: resolve(__dirname, '../webui/static'),
    emptyOutDir: true,
    assetsDir: 'assets',
  },
  server: {
    host: '127.0.0.1',
    port: 18121,
    proxy: {
      '/api': 'http://127.0.0.1:18120',
      '/health': 'http://127.0.0.1:18120',
    },
  },
})
