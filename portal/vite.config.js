import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development the portal talks to the API gateway (never directly to MMT).
const gateway = process.env.GATEWAY_URL || 'http://localhost:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': gateway,
      '/ussd': gateway,
    },
  },
})
