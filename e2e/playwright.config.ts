import { defineConfig, devices } from "@playwright/test";

// The app is started by the CI job (against a Postgres service) before Playwright
// runs; BASE_URL points at it. Kept minimal: one project, retries in CI, a trace
// on first retry so a flake is diagnosable from the uploaded report.
export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: process.env.BASE_URL || "http://localhost:8080",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
