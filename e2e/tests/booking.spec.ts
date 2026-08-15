import { test, expect } from "@playwright/test";

// End-to-end smoke of the core flow that Go tests can't cover: a real browser
// driving login (with CSRF + session cookies) → creating a unit-based booking →
// seeing it priced on the neighbor page and the printable Beleg. The CI job seeds
// one base/year/neighbor (ids 1/1/1) and clears the admin's forced-password flag.
const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

test("login, create a unit booking, and see it on the Beleg", async ({ page }) => {
  // --- login ---
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden" }).click();
  // Lands on an authenticated page (the app shell shows the brand).
  await expect(page.locator(".appbar")).toBeVisible();

  // --- create a Ballen booking for the seeded neighbor (id 1, year 1) ---
  await page.goto("/neighbors/1?year=1");
  await page.locator('select[name="unit"]').selectOption("Ballen");
  await page.locator('input[name="quantity"]').fill("10");
  await page.locator('input[name="unit_price"]').fill("3,20");
  await page.locator('input[name="task_label"]').fill("E2E Ballenpressen");
  await page.getByRole("button", { name: "Buchung speichern" }).click();

  // The booking is now listed and priced (10 × 3,20 = 32,00 €).
  await expect(page.getByText("E2E Ballenpressen").first()).toBeVisible();
  await expect(page.getByText("32,00").first()).toBeVisible();

  // --- the printable Beleg shows the same total ---
  await page.goto("/neighbors/1/beleg?year=1");
  await expect(page.locator("#beleg")).toBeVisible();
  await expect(page.getByText("32,00").first()).toBeVisible();
});
