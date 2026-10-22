import { defineConfig, devices } from "@playwright/test";

// e2e/run.mjs starts the same disposable Docker stack locally and in CI. The
// desktop suite stays intact; four bounded mobile projects cover core surfaces,
// and one independent WebKit journey exercises billing writes on an iPhone.
// A trace on first retry makes flakes diagnosable in the local report.
//
// workers: 1 — all specs share ONE database (there is no per-test DB isolation), so
// parallel workers would race and one spec's writes (a booking, an issued invoice)
// would leak into another's assertions (e.g. the a11y scan). Serial execution keeps
// the shared state deterministic. Mobile surface checks are read-only; the
// separate billing journey creates its own neighbor and year.
// Visual regression (Ausbaukarte II #14) runs on its own fresh stack, because
// every other spec writes to the shared database and would change what the
// pages show: `node e2e/run.mjs --visual` sets TRECKRR_E2E_VISUAL=1, which
// swaps in the four visual projects. Baselines live in e2e/visual-baselines
// (mounted into the browser container); `--update-snapshots` rewrites them.
const visual = process.env.TRECKRR_E2E_VISUAL === "1";
const visualProjects = (["light", "dark"] as const).flatMap((colorScheme) => [
  {
    name: `visual-desktop-${colorScheme}`,
    testMatch: /visual\.spec\.ts/,
    use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 }, colorScheme, serviceWorkers: "block" as const },
  },
  {
    name: `visual-phone-${colorScheme}`,
    testMatch: /visual\.spec\.ts/,
    use: { ...devices["Pixel 7"], colorScheme, serviceWorkers: "block" as const },
  },
]);

export default defineConfig({
  testDir: "./tests",
  snapshotPathTemplate: "visual-baselines/{arg}-{projectName}{ext}",
  expect: { toHaveScreenshot: { maxDiffPixelRatio: 0.01, animations: "disabled", caret: "hide" } },
  timeout: 30_000,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: process.env.BASE_URL || "http://localhost:8080",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: visual ? visualProjects : [
    { name: "chromium", testIgnore: /mobile_(core|billing)\.spec\.ts|visual\.spec\.ts/, use: { ...devices["Desktop Chrome"] } },
    ...(["light", "dark"] as const).flatMap((colorScheme) => [
      {
        name: `mobile-chromium-${colorScheme}`,
        testMatch: /mobile_core\.spec\.ts/,
        use: { ...devices["Pixel 7"], colorScheme, serviceWorkers: "block" as const },
      },
      {
        name: `mobile-webkit-${colorScheme}`,
        testMatch: /mobile_core\.spec\.ts/,
        use: { ...devices["iPhone 13"], colorScheme, serviceWorkers: "block" as const },
      },
    ]),
    {
      name: "mobile-webkit-billing",
      testMatch: /mobile_billing\.spec\.ts/,
      retries: 0,
      use: { ...devices["iPhone 13"], colorScheme: "dark", serviceWorkers: "block" as const },
    },
  ],
});
