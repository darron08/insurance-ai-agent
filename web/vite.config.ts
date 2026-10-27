import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1', port: 5173, strictPort: true,
    proxy: { '/api': { target: 'http://127.0.0.1:8080', timeout: 130_000, proxyTimeout: 130_000 } },
  },
  build: { outDir: '../bin/web', emptyOutDir: true },
  test: { environment: 'jsdom', setupFiles: './src/test-setup.ts', include: ['src/**/*.test.tsx'] },
})
