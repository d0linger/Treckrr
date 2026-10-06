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

test("global tools and reporting shortcuts stay direct on desktop and mobile", async ({ page }) => {
  await login(page);

  const themeToggle = page.getByRole("button", { name: "Hell/Dunkel umschalten" });
  const quickSearch = page.getByRole("button", { name: "Schnellsuche (Strg+K)" });
  const notifications = page.getByRole("link", { name: /^Hinweise/ });
  const statistics = page.getByRole("link", { name: /Statistik \d{4}/ });
  const comparison = page.getByRole("link", { name: "Jahresvergleich" });

  await expect(themeToggle).toBeVisible();
  await expect(quickSearch).toBeVisible();
  await expect(notifications).toBeVisible();
  await expect(statistics).toBeVisible();
  await expect(comparison).toBeVisible();
  await expect(page.locator('main a[href^="/stats"]')).toHaveCount(0);

  await page.getByRole("button", { name: "Menü" }).click();
  const drawer = page.locator("#drawer");
  await expect(drawer).toHaveAttribute("aria-hidden", "false");
  const drawerBackground = await drawer.evaluate((node) => getComputedStyle(node).backgroundColor);
  expect(drawerBackground).not.toBe("rgba(0, 0, 0, 0)");
  for (const href of ["/", "/years", "/neighbors", "/bases"]) {
    await expect(drawer.locator(`a[href="${href}"]`)).toHaveCount(0);
  }
  await expect(drawer.getByRole("link", { name: "Mein Konto und Sicherheit" })).toBeVisible();
  await expect(drawer.getByRole("link", { name: /Hinweise/ })).toHaveCount(0);
  await expect(drawer.locator("[data-cmdk-open]")).toHaveCount(0);
  await expect(drawer.locator("summary.drawer__group-toggle").filter({ hasText: "Weitere Funktionen" })).toBeVisible();
  await expect(drawer.locator("summary.drawer__group-toggle").filter({ hasText: "Verwaltung" })).toBeVisible();
  await expect(drawer.getByText("Konto & Verwaltung", { exact: true })).toHaveCount(0);
  await expect(drawer.getByText("Erweiterte Verwaltung", { exact: true })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(drawer).toHaveAttribute("aria-hidden", "true");

  await page.emulateMedia({ colorScheme: "dark" });
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-theme", "auto");
    localStorage.setItem("treckrr-theme", "auto");
  });
  await themeToggle.click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

  await comparison.click();
  await expect(page).toHaveURL(/\/stats\/all\?year=\d+$/);
  await expect(page.locator(".yearbar")).toBeVisible();
  await expect(comparison).toHaveAttribute("aria-current", "page");
  await expect(statistics).toBeVisible();
  const yearLinks = page.locator(".yearbar__links .yearpill");
  for (const link of await yearLinks.all()) {
    await expect(link).toHaveAttribute("href", /^\/stats\?year=\d+$/);
  }

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(themeToggle).toBeVisible();
  await expect(quickSearch).toBeVisible();
  await expect(notifications).toBeVisible();
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

test("neighbor account exposes receipt and export in its summary header", async ({ page }) => {
  await login(page);
  await page.goto("/neighbors/1?year=1");

  const summary = page.locator(".summary-card");
  await expect(summary.getByRole("link", { name: "Beleg", exact: true })).toHaveAttribute(
    "href",
    "/neighbors/1/beleg?year=1",
  );
  await expect(summary.getByRole("link", { name: "CSV Export", exact: true })).toBeVisible();
  await expect(page.getByText("Verlauf und Export", { exact: true })).toBeVisible();
  await expect(page.locator("details.page-more").getByRole("link", { name: /Beleg/ })).toHaveCount(0);
});

test("dashboard neighbor cards keep totals and payment status in a stable responsive layout", async ({ page }) => {
  await login(page);

  const grid = page.locator(".neighbor-account-grid");
  const cards = grid.locator(".neighbor-account-card");
  await expect(cards.first()).toBeVisible();
  await expect(cards.first().getByText("Jahressaldo", { exact: true })).toBeVisible();
  await expect(cards.first().getByText("Zahlungsstand:", { exact: true })).toHaveCount(1);
  expect(await grid.evaluate((node) => getComputedStyle(node).alignItems)).toBe("stretch");
  expect(await cards.first().evaluate((node) => getComputedStyle(node).display)).toBe("grid");

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(cards.first()).toBeVisible();
  expect(await cards.first().locator(".neighbor-account-card__footer").evaluate(
    (node) => getComputedStyle(node).display,
  )).toBe("grid");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});
