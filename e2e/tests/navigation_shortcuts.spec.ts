import { expect, test, type Page } from "@playwright/test";

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

/** Signs into the disposable fixture and waits for the authenticated app shell. */
async function login(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Benutzername").fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

test("theme and reporting shortcuts stay direct on desktop and mobile", async ({ page }) => {
  await login(page);

  const themeToggle = page.getByRole("button", { name: "Hell/Dunkel umschalten" });
  const statistics = page.getByRole("link", { name: /Statistik 2025/ });
  const comparison = page.getByRole("link", { name: "Jahresvergleich" });

  await expect(themeToggle).toBeVisible();
  await expect(statistics).toBeVisible();
  await expect(comparison).toBeVisible();
  await expect(page.locator('main a[href^="/stats"]')).toHaveCount(0);

  const previousTheme = await page.locator("html").getAttribute("data-theme");
  await themeToggle.click();
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", previousTheme || "auto");

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(themeToggle).toBeVisible();
  await expect(statistics).toBeVisible();
  await expect(comparison).toBeVisible();
  const shortcutSizes = await page.locator(".yearquick:visible").evaluateAll((links) =>
    links.map((link) => ({
      width: link.getBoundingClientRect().width,
      height: link.getBoundingClientRect().height,
    })),
  );
  expect(shortcutSizes.every(({ width, height }) => width >= 43.5 && height >= 43.5)).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("neighbor account exposes the receipt without opening a disclosure", async ({ page }) => {
  await login(page);
  await page.goto("/neighbors/1?year=1");

  const accountTabs = page.getByRole("navigation", { name: /Bereiche für/ });
  await expect(accountTabs.getByRole("link", { name: "Beleg", exact: true })).toHaveAttribute(
    "href",
    "/neighbors/1/beleg?year=1",
  );
  await expect(page.getByText("Verlauf und Export", { exact: true })).toBeVisible();
  await expect(page.locator("details.page-more").getByRole("link", { name: /Beleg/ })).toHaveCount(0);
});
