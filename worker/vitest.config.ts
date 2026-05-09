import { defineWorkersConfig } from "@cloudflare/vitest-pool-workers/config";

// NOTE: no `coverage` block — @vitest/coverage-v8 requires `node:inspector`
// which isn't available in the workerd runtime that vitest-pool-workers uses.
// We rely on test count + assertions for confidence on the Worker; coverage
// of `worker/src/**` would have to come from a separate pool (off the worker
// runtime), which costs us realism. Acceptable tradeoff for now.

export default defineWorkersConfig({
  test: {
    poolOptions: {
      workers: {
        wrangler: { configPath: "./wrangler.toml" },
      },
    },
  },
});
