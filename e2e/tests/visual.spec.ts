import { test, expect, type Page } from "@playwright/test";

// Visual regression (Ausbaukarte II #14). The functional specs check DOM,
// overflow and contrast; a cascade change that shifts a layout passes them all.
// These screenshots catch it. They run only through `node e2e/run.mjs --visual`
// on a fresh stack with the fixed seed, in light and dark, phone and desktop.
// Anything that depends on today or on background jobs is masked.

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

/** Logs into the seeded instance. */
async function login(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Benutzername").fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

/** Regions whose content follows the clock or background jobs, not the layout. */
function dynamic(page: Page) {
  return [
    page.locator(".appbar__notify"),
    page.locator('input[type="date"]'),
    page.locator("time"),
    page.locator("[data-visual-dynamic]"),
    page.locator(".flash"),
  ];
}

/** Waits until web fonts and the page's own scripts have settled, and hides
 * the animated background canvas, which covers the whole viewport (masking it
 * would mask the page). CSSOM, because the CSP forbids injected style tags. */
async function settle(page: Page) {
  await page.waitForLoadState("networkidle");
  await page.evaluate(async () => {
    await document.fonts.ready;
    document.querySelectorAll("canvas").forEach((canvas) => { canvas.style.visibility = "hidden"; });
  });
}

const pages: Array<{ name: string; path: string; ready: string }> = [
  { name: "uebersicht", path: "/?year=1", ready: ".appbar" },
  { name: "nachbarn", path: "/neighbors", ready: ".appbar" },
  { name: "nachbar", path: "/neighbors/1?year=1", ready: ".appbar" },
  { name: "buchung", path: "/neighbors/1?year=1&view=booking", ready: "[data-unified-booking]" },
  { name: "buchungen", path: "/buchungen?year=1", ready: ".dtable" },
  { name: "kalender", path: "/buchungen?year=1&view=kalender&month=2025-05", ready: "[data-booking-calendar]" },
  { name: "beleg", path: "/neighbors/1/beleg?year=1", ready: "#beleg" },
  { name: "statistik", path: "/stats?year=1", ready: ".appbar" },
  { name: "preise", path: "/prices?base=1", ready: ".appbar" },
];

test("login page", async ({ page }) => {
  await page.goto("/login");
  await settle(page);
  await expect(page).toHaveScreenshot("login.png", { mask: dynamic(page) });
});

for (const target of pages) {
  test(`page ${target.name}`, async ({ page }) => {
    await login(page);
    await page.goto(target.path);
    await expect(page.locator(target.ready).first()).toBeVisible();
    await settle(page);
    await expect(page).toHaveScreenshot(`${target.name}.png`, { mask: dynamic(page) });
  });
}
