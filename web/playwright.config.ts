import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  use: { baseURL: 'http://127.0.0.1:5173', browserName: 'chromium' },
  webServer: { command: 'npm run dev', url: 'http://127.0.0.1:5173', reuseExistingServer: false },
  reporter: 'list',
})
