import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  // CRITICAL: relative paths so the same dist/ works under both "/" (production)
  // and "/<branch>/" (preview path-prefix). With base: '/' the bundle requests
  // /assets/index-abc.js which the path-prefix preview cannot resolve. Pitfall #17.
  base: './',
  server: { port: 5173 },
  build: {
    outDir: 'dist',
    sourcemap: true,
    target: 'es2022',
  },
});
