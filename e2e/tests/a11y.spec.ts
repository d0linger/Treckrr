import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// Automated accessibility regression guard. The app's a11y is maintained by hand
// (aria/role/label, focus-visible, reduced-motion); this pins it so a future change
// can't silently regress. We scan the key pages against WCAG 2.1 A/AA and fail on
// serious/critical violations. Minor/moderate findings are logged (via the report)
// but don't fail the build, to avoid blocking on subjective best-practice noise.
//
// The CI job seeds base/year/neighbor 1/1/1 and clears the admin password flag.

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";
const WCAG = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];

/** Signs into the seeded admin account and waits for the authenticated shell before scanning. */
async function login(page) {
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

// scan runs axe on the current page and returns only the serious/critical violations.
async function seriousViolations(page) {
  const results = await new AxeBuilder({ page }).withTags(WCAG).analyze();
  const blocking = results.violations.filter(
    (v) => v.impact === "serious" || v.impact === "critical"
  );
  if (blocking.length) {
    // Actionable log: rule, impact, the offending selectors and axe's own
    // failure summary (which carries the measured contrast ratio) — a CI
    // failure should say WHAT to change, not just that something is wrong.
    const lines = ["a11y serious/critical violations:"];
    for (const v of blocking) {
      lines.push("  [" + v.impact + "] " + v.id + ": " + v.help);
      for (const n of v.nodes.slice(0, 3)) {
        lines.push("      " + (n.target || []).join(" "));
        for (const part of (n.failureSummary || "").split("\n")) {
          if (part.trim()) lines.push("        " + part.trim());
        }
      }
    }
    console.log(lines.join("\n"));
  }
  return blocking;
}

// Every stable rendered page a logged-in operator can reach with the CI seed,
// plus the light/dark pair
// (Ausbaukarte 90). Contrast rules only fire against the colours actually
// rendered, so a single-theme run checks half the palette — and the app has a
// full dark theme with its own token set.
const PAGES: Array<[string, string]> = [
  ["/?year=1", "dashboard"],
  ["/neighbors/1?year=1", "neighbor detail (booking form)"],
  ["/neighbors/1/beleg?year=1", "Beleg"],
  ["/neighbors/1/overview", "neighbor history"],
  ["/buchungen?year=1", "bookings list"],
  ["/stats?year=1", "statistics"],
  ["/stats/all", "statistics, all years"],
  ["/neighbors", "neighbor management"],
  ["/personen", "person master data"],
  ["/mahnwesen?year=1", "Mahnwesen"],
  ["/rechnungsjournal?year=1", "invoice journal"],
  ["/years", "billing years"],
  ["/years/1/abschluss", "year closing checklist"],
  ["/years/1/issue-all", "batch invoicing"],
  ["/years/1/recalc", "year recalculation"],
  ["/prices?base=1", "price master"],
  ["/prices/compare?base=1", "price comparison"],
  ["/gespanne?base=1", "rigs"],
  ["/bases", "assessment bases"],
  ["/recurring", "recurring bookings"],
  ["/entries/1/edit", "booking edit"],
  ["/entries/1/copy", "booking copy"],
  ["/payments/1/edit", "payment edit"],
  ["/payments/1/copy", "payment copy"],
  ["/neighbors/1/recalc?year=1", "neighbor recalculation"],
  ["/neighbors/1/invoice/confirm?year=1", "invoice confirmation"],
  ["/entries/import?year=1", "CSV import"],
  ["/payments/import", "bank import"],
  ["/admin/users", "user administration"],
  ["/admin/backup", "backup"],
  ["/admin/company", "company data"],
  ["/admin/audit", "audit log"],
  ["/profile", "profile"],
  ["/account/password", "password change"],
  ["/account/2fa", "two-factor setup"],
];

test("login page has no serious accessibility violations", async ({ page }) => {
  await page.goto("/login");
  await expect(page.locator('input[name="username"]')).toBeVisible();
  expect(await seriousViolations(page)).toEqual([]);
});

test("login page passes in dark mode too", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto("/login");
  await expect(page.locator('input[name="username"]')).toBeVisible();
  expect(await seriousViolations(page)).toEqual([]);
});

/** Keeps recovery pages in the accessibility gate even though they do not require a session. */
test("offline and branded error pages have no serious accessibility violations", async ({ page }) => {
  for (const path of ["/offline", "/missing-page"]) {
    await page.goto(path);
    await expect(page.locator("h1")).toHaveCount(1);
    expect(await seriousViolations(page)).toEqual([]);
  }
});

/**
 * Checks mobile labels, keyboard access to overflowing data, and current-section semantics.
 * Requires the initial seed, before the write-based specs close the year.
 */
test("page semantics remain explicit on mobile and data-heavy views", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);

  await expect(page.getByRole("link", { name: "Treckrr – zur Übersicht" })).toBeVisible();

  await page.goto("/entries/1/edit");
  await expect(page.getByLabel("Fotos auswählen")).toHaveCount(1);
  await expect(page.locator("h1")).toHaveCount(1);

  // The CI seed always includes a payment, but not every stats table.
  await page.goto("/neighbors/1/overview");
  const scrollRegions = page.locator('.tablewrap[role="region"]');
  expect(await scrollRegions.count()).toBeGreaterThan(0);
  const unfocusable = await scrollRegions.evaluateAll((nodes) =>
    nodes.filter((node) => node.scrollWidth > node.clientWidth && node.tabIndex < 0).length
  );
  expect(unfocusable, "overflowing data regions must be keyboard focusable").toBe(0);

  await page.goto("/prices?base=1");
  await expect(page.getByRole("navigation", { name: "Bereich der Bemessungsgrundlage" })).toBeVisible();
  await expect(page.locator('.subtabs [aria-current="page"]')).toHaveText("Kosten");
});

for (const scheme of ["light", "dark"] as const) {
  /** Rejects failed page loads, ambiguous page headings, and blocking axe findings in each palette. */
  test(`all authenticated pages pass in ${scheme} mode`, async ({ page }) => {
    test.slow(); // The full authenticated page matrix is well past the default timeout.
    await page.emulateMedia({ colorScheme: scheme });
    await login(page);
    const failures: string[] = [];
    for (const [path, name] of PAGES) {
      const resp = await page.goto(path);
      // A page that 404s is a broken link in the list, not an a11y pass.
      expect(resp?.status(), `${name} (${path}) did not load`).toBeLessThan(400);
      await page.waitForLoadState("networkidle");
      await expect(page.locator("h1"), `${name} (${path}) needs one page-level heading`).toHaveCount(1);
      const violations = await seriousViolations(page);
      if (violations.length) {
        failures.push(`${name} (${path}, ${scheme}): ${violations.map((v) => v.id).join(", ")}`);
      }
    }
    expect(failures, "serious a11y violations").toEqual([]);
  });
}

