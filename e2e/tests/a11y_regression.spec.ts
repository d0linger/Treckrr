import { test, expect, type BrowserContext, type Page } from "@playwright/test";

// Run with the populated CI fixture, before booking/invoice/session specs write
// to it. The latter deliberately void all bookings and close the billing year.
const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

/** Signs into the seeded admin account within the supplied page's isolated browser context. */
async function login(page: Page) {
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

/**
 * Exercises keyboard revocation through the collapsed list and checks that other sessions survive.
 * Creates and cleans up its own auxiliary sessions without revoking sessions owned by other tests.
 */
test("collapsed sessions remain keyboard-accessible and revoke only the selected session", async ({ page, browser, baseURL }) => {
  test.setTimeout(90_000);
  const sessions: { context: BrowserContext; page: Page; label: string }[] = [];
  const sessionGroup = Date.now();
  try {
    // Own every auxiliary session. Do not rely on sessions left by earlier specs
    // or use the global revoke-others action to clean up another test's session.
    for (let i = 0; i < 5; i++) {
      const label = `Treckrr disclosure regression ${sessionGroup}-${i}`;
      const context = await browser.newContext({ baseURL, userAgent: label });
      const auxiliary = await context.newPage();
      sessions.push({ context, page: auxiliary, label });
      await login(auxiliary);
    }
    await login(page);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/profile");
    const disclosure = page.locator(".session-disclosure");
    await expect(disclosure).toBeVisible();
    await expect(disclosure).not.toHaveAttribute("open", "");
    const selected = disclosure.locator(".list__row").filter({ hasText: sessions[0].label });
    await expect(selected).toBeHidden();
    const summary = disclosure.locator("summary");
    await summary.focus();
    await page.keyboard.press("Enter");
    await expect(selected).toBeVisible();

    const revoke = selected.getByRole("button", { name: "Beenden", exact: true });
    await revoke.focus();
    await expect(revoke).toBeFocused();
    await Promise.all([
      page.waitForResponse(response => response.url().endsWith("/account/sessions/revoke") && response.request().method() === "POST"),
      page.keyboard.press("Enter"),
    ]);
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".appbar")).toBeVisible();
    await expect(page.getByText(sessions[0].label, { exact: false })).toHaveCount(0);
    await sessions[0].page.goto("/profile");
    await expect(sessions[0].page).toHaveURL(/\/login/);
    await sessions[1].page.goto("/profile");
    await expect(sessions[1].page.locator(".appbar")).toBeVisible();
  } finally {
    for (const session of sessions) {
      try {
        await session.page.goto("/profile");
        const logout = session.page.locator('main form[action="/logout"] button');
        if (await logout.count()) {
          await logout.click();
          await session.page.waitForURL(/\/login/);
        }
      } finally {
        await session.context.close();
      }
    }
  }
});

/** Keeps browser recovery links usable while preserving the plain 404 contract for image requests. */
test("missing records offer recovery without changing image-client responses", async ({ page }) => {
  await login(page);
  const response = await page.goto("/neighbors/invalid?year=1");
  expect(response?.status()).toBe(404);
  await expect(page.getByRole("heading", { name: "Seite nicht gefunden" })).toBeVisible();
  await page.getByRole("link", { name: "Zur Übersicht", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();

  const image = await page.request.get("/entries/invalid/photos/1", {
    headers: { Accept: "image/avif,image/webp,*/*" },
  });
  expect(image.status()).toBe(404);
  expect(image.headers()["content-type"]).toContain("text/plain");
  expect(await image.text()).toBe("404 page not found\n");
});

/** Checks labeled native file selection and clearing without submitting or persisting an upload. */
test("booking photo selection has a usable label and preserves native input behavior", async ({ page }) => {
  await login(page);
  await page.goto("/entries/1/edit");
  const photo = page.getByLabel("Fotos auswählen", { exact: false });
  await expect(photo).toHaveAttribute("type", "file");
  await expect(photo).toHaveAttribute("accept", /image/);
  await photo.setInputFiles({
    name: "regression-photo.png",
    mimeType: "image/png",
    buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO6Z3Y8AAAAASUVORK5CYII=", "base64"),
  });
  expect(await photo.evaluate((input: HTMLInputElement) => input.files?.[0]?.name)).toBe("regression-photo.png");
  await photo.setInputFiles([]);
  expect(await photo.evaluate((input: HTMLInputElement) => input.files?.length)).toBe(0);
  // This checks native file selection only. Image decoding and persistence are
  // covered by the server integration tests; no upload is made by this test.
});

/** Guards chart touch targets, page overflow, and keyboard navigation at 320px and 390px in both themes. */
test("chart links remain reachable at narrow mobile widths in both themes", async ({ page }) => {
  await login(page);
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await page.goto("/stats?year=1");
      const labels = page.locator("a.barchart__label");
      expect(await labels.count()).toBeGreaterThan(0);
      expect(await labels.evaluateAll(nodes => nodes.every(node => node.getBoundingClientRect().height >= 43.5))).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      const first = labels.first();
      await first.focus();
      await expect(first).toBeFocused();
      const href = await first.getAttribute("href");
      await Promise.all([
        page.waitForURL(url => url.pathname + url.search === href),
        page.keyboard.press("Enter"),
      ]);
      await expect(page.locator(".appbar")).toBeVisible();
    }
  }
});

/** Verifies server-rendered authentication and profile navigation with browser scripting disabled. */
test("login, profile, and navigation remain usable without JavaScript", async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL, javaScriptEnabled: false });
  try {
    const page = await context.newPage();
    await login(page);
    await page.goto("/profile");
    await expect(page.getByRole("link", { name: "Passwort ändern", exact: true })).toBeVisible();
    const disclosure = page.locator(".session-disclosure");
    if (await disclosure.count()) {
      await disclosure.locator("summary").click();
      await expect(disclosure.locator(".list__row").first()).toBeVisible();
    }
    await page.getByRole("link", { name: "Passwort ändern", exact: true }).click();
    await expect(page).toHaveURL(/\/account\/password/);
  } finally {
    await context.close();
  }
});
