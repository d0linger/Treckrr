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
    // Compact, actionable log: rule id, impact, and the first offending selector.
    console.log(
      "a11y serious/critical violations:\n" +
        blocking
          .map(
            (v) =>
              `  [${v.impact}] ${v.id}: ${v.help} — e.g. ${v.nodes[0]?.target?.join(" ")}`
          )
          .join("\n")
    );
  }
  return blocking;
}

test("login page has no serious accessibility violations", async ({ page }) => {
  await page.goto("/login");
  await expect(page.locator('input[name="username"]')).toBeVisible();
  expect(await seriousViolations(page)).toEqual([]);
});

test("core authenticated pages have no serious accessibility violations", async ({
  page,
}) => {
  await login(page);
  const pages: Array<[string, string]> = [
    ["/?year=1", "dashboard"],
    ["/neighbors/1?year=1", "neighbor detail (booking form)"],
    ["/neighbors/1/beleg?year=1", "Beleg"],
    ["/stats?year=1", "statistics"],
    ["/neighbors", "neighbor management"],
    ["/mahnwesen?year=1", "Mahnwesen"],
  ];
  for (const [path, name] of pages) {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
    const violations = await seriousViolations(page);
    expect(violations, `${name} (${path}) has serious a11y violations`).toEqual([]);
  }
});
