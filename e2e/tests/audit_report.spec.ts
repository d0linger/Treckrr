import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import path from "node:path";
import { pathToFileURL } from "node:url";

const reportURL = pathToFileURL(path.resolve(__dirname, "../../docs/reviews/2026-09-23-security-audit-interactive.html")).href;

test("interactive audit filters, expands, and remains usable on mobile", async ({ page }) => {
  await page.goto(reportURL);

  await expect(page.getByRole("heading", { level: 1 })).toContainText("Find the failure path");
  await expect(page.locator(".finding")).toHaveCount(15);
  await expect(page.locator(".finding:not([hidden])")).toHaveCount(15);

  const accessibility = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
  const blocking = accessibility.violations.filter(({ impact }) => impact === "serious" || impact === "critical");
  expect(blocking, JSON.stringify(blocking, null, 2)).toEqual([]);

  await page.emulateMedia({ colorScheme: "dark" });
  const darkAccessibility = await new AxeBuilder({ page }).withRules(["color-contrast"]).analyze();
  expect(darkAccessibility.violations, JSON.stringify(darkAccessibility.violations, null, 2)).toEqual([]);
  await page.emulateMedia({ colorScheme: "light" });

  await page.locator("#severity").selectOption("High");
  await expect(page.locator(".finding:not([hidden])")).toHaveCount(4);
  await expect(page.locator("#result-count")).toContainText("4 findings shown");

  await page.getByRole("button", { name: "Reset" }).click();
  await page.locator("#search").fill("SEC-03");
  await expect(page.locator(".finding:not([hidden])")).toHaveCount(1);
  await expect(page.locator(".finding:not([hidden])")).toContainText("Bootstrap and break-glass");

  await page.getByRole("button", { name: "Expand all" }).click();
  await expect(page.locator(".finding:not([hidden])")).toHaveAttribute("open", "");
  await expect(page.getByText("ADMIN_PASSWORD=a", { exact: false })).toBeVisible();

  await page.setViewportSize({ width: 390, height: 844 });
  const sizes = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
  expect(sizes.scroll).toBeLessThanOrEqual(sizes.width);
});
