import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  base: './',
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      // Backend origin; override with INSIGHTS_PROXY for a non-default port.
      '/api': process.env.INSIGHTS_PROXY ?? 'http://localhost:4318',
      '/v1': process.env.INSIGHTS_PROXY ?? 'http://localhost:4318',
      // Personal-usage endpoints live under /api/personal (see RegisterPersonal);
      // /companion is proxied too since it's kept as a back-compat alias.
      '/companion': process.env.INSIGHTS_PROXY ?? 'http://localhost:4318',
    },
  },
})
