import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  // Relative base so ONE dist/ artifact can be deployed under both "/"
  // (production) and "/<env>/" (preview path-prefix). NOTE: relative
  // refs alone break SPA deep routes / refresh / the /oauth/callback
  // return URL (./assets resolves against the browser path → 404 →
  // blank screen). scripts/deploy-frontend.sh therefore REWRITES the
  // emitted ./asset refs in index.html to the absolute served base
  // per env at deploy time — that is the real pitfall-#17 mitigation.
  // Do not "fix" this to base:'/' (it breaks the shared-artifact
  // preview path-prefix); the deploy-time rewrite is the contract.
  base: './',
  server: { port: 5173 },
  build: {
    outDir: 'dist',
    sourcemap: true,
    target: 'es2022',
  },
});
