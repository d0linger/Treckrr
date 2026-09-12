import { test, expect } from "@playwright/test";

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

/** Signs into the seeded admin account and waits for the shell before checking shared UI controls. */
async function login(page) {
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

/** Guards the script-loading boundary between login, authenticated pages, and passkey-enabled profiles. */
test("loads page-specific enhancement scripts only where needed", async ({ page }) => {
  await page.goto("/login");
  await expect(page.locator('script[src*="/passkey.js"]')).toHaveCount(1);
  await expect(page.locator('script[src*="/login-bg.js"]')).toHaveCount(1);
  await expect(page.locator('script[src*="/offline.js"]')).toHaveCount(0);
  await expect(page.locator('script[src*="/capture.js"]')).toHaveCount(0);
  await expect(page.locator('script[src*="/app-bg.js"]')).toHaveCount(0);

  await login(page);
  await expect(page.locator('script[src*="/passkey.js"]')).toHaveCount(0);
  await expect(page.locator('script[src*="/offline.js"]')).toHaveCount(1);
  await expect(page.locator('script[src*="/capture.js"]')).toHaveCount(1);
  await expect(page.locator('script[src*="/app-bg.js"]')).toHaveCount(1);

  await page.goto("/profile");
  await expect(page.locator('script[src*="/passkey.js"]')).toHaveCount(1);
});

/**
 * Enforces the mobile touch-target floor for live controls and temporary contextual-control probes.
 * Removes the probes after measurement so they cannot affect subsequent interactions.
 */
test("mobile chrome keeps primary controls comfortably tappable", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);

  await expect(page.locator(".appbar__theme")).toBeHidden();
  await expect(page.locator('.tabbar__item[aria-current="page"]')).toHaveCount(1);

  const controls = page.locator(
    ".appbar__brand:visible, .appbar__icon:visible, .yearpill:visible, .btn--sm:visible, .btn--xs:visible, .iconact:visible, .backbtn:visible, .backlink:visible, .workspace__back:visible, .barchart__label:visible, .switch:visible"
  );
  expect(await controls.count()).toBeGreaterThan(2);
  const undersized = await controls.evaluateAll((nodes) =>
    nodes
      .map((node) => ({
        label: node.getAttribute("aria-label") || node.textContent?.trim() || node.tagName,
        height: node.getBoundingClientRect().height,
      }))
      .filter((item) => item.height < 43.5)
  );
  expect(undersized, "controls below the 44px touch-target floor").toEqual([]);

  // These contextual controls are not guaranteed to exist in seeded data, so
  // render harmless probes under their real parent selectors and measure them.
  const contextualTargets = await page.evaluate(() => {
    const fixture = document.createElement("div");
    fixture.innerHTML = [
      '<div class="toast"><button class="toast__close">x</button></div>',
      '<div class="photogrid"><div class="photogrid__item"><button class="photogrid__del">x</button></div></div>',
      '<ul class="bkp__files bkp__files--compact"><li><a class="btn btn--sm btn--ghost bkp__dl">Download</a></li></ul>',
    ].join("");
    document.body.appendChild(fixture);
    const sizes = Array.from(fixture.querySelectorAll("button, a")).map((node) => ({
      className: node.className,
      height: node.getBoundingClientRect().height,
      width: node.getBoundingClientRect().width,
    }));
    fixture.remove();
    return sizes;
  });
  expect(
    contextualTargets.filter((target) => target.height < 43.5 || target.width < 43.5),
    "contextual controls below the 44px touch-target floor"
  ).toEqual([]);
});

/** Checks keyboard focus containment and Escape dismissal across the drawer, command palette, and help. */
test("custom dialogs contain focus and return it to their trigger", async ({ page }) => {
  await login(page);

  const menu = page.locator("[data-drawer-open]");
  await menu.focus();
  await menu.click();
  await expect(page.locator("#drawer")).toHaveAttribute("aria-hidden", "false");
  await page.keyboard.press("Shift+Tab");
  expect(await page.evaluate(() => document.getElementById("drawer")?.contains(document.activeElement))).toBe(true);
  await page.keyboard.press("Escape");
  await expect(menu).toBeFocused();

  const search = page.locator("[data-cmdk-open]");
  await search.focus();
  await search.click();
  const commandInput = page.locator('.cmdk input[role="combobox"]');
  await expect(commandInput).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(commandInput).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(search).toBeFocused();

  await page.keyboard.press("?");
  const help = page.locator("#kbd-help");
  await expect(help).toHaveAttribute("aria-modal", "true");
  await expect(help.getByRole("button", { name: "Schließen" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(help).toHaveCount(0);
});
