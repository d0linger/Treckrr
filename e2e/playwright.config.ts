import { defineConfig, devices } from "@playwright/test";

// The app is started by the CI job (against a Postgres service) before Playwright
// runs; BASE_URL points at it. Kept minimal: one project, retries in CI, a trace
// on first retry so a flake is diagnosable from the uploaded report.
//
// workers: 1 — all specs share ONE database (there is no per-test DB isolation), so
// parallel workers would race and one spec's writes (a booking, an issued invoice)
// would leak into another's assertions (e.g. the a11y scan). Serial execution keeps
// the shared state deterministic; the suite runs in a few seconds either way.
export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: process.env.BASE_URL || "http://localhost:8080",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
