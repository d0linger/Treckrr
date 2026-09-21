import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// Route-injected UI probes must reach Playwright rather than the offline worker.
test.use({ serviceWorkers: "block" });

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

/** Logs into the disposable CI fixture without changing its financial records. */
async function login(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Benutzername").fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

/** Pins accessible field names in the initially rendered and cloned quick rows. */
test("quick entry identifies every field and newly added row", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  await page.goto("/neighbors/1?year=1");
  await page.getByText("Schnellerfassung (mehrere Zeilen)", { exact: true }).click();
  for (const row of [1, 6]) {
    for (const label of ["Datum", "Gespann", "Stunden", "Person"]) {
      await expect(page.getByLabel(`${label} · Zeile ${row}`, { exact: true })).toHaveCount(1);
    }
  }
  await page.locator("[data-quick-add]").click();
  await expect(page.getByLabel("Datum · Zeile 7", { exact: true })).toBeFocused();
  await expect(page.getByLabel("Stunden · Zeile 7", { exact: true })).toHaveValue("");
  const results = await new AxeBuilder({ page }).include("[data-quick-form]").withTags(["wcag2a", "wcag2aa"]).analyze();
  expect(results.violations.filter((v) => ["serious", "critical"].includes(v.impact || ""))).toEqual([]);
});

/** A missing invoice detail must lead directly to an open editor, not the ledger. */
test("invoice correction opens the matching neighbour details", async ({ page }) => {
  await login(page);
  await page.goto("/neighbors/1/invoice/confirm?year=1");
  await page.getByRole("link", { name: "Nachbardaten ergänzen" }).click();
  await expect(page).toHaveURL(/\/neighbors\?scope=alle&edit=1#neighbor-1$/);
  const editor = page.locator("#neighbor-1");
  await expect(editor.locator('textarea[name="address"]')).toBeVisible();
  await expect(editor.locator('input[name="name"]')).toHaveValue("E2E Nachbar");
});

/** Keeps consequential closing explanations readable at a phone width. */
test("year closing wraps complete explanations", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  await page.goto("/years/1/abschluss");
  const details = page.locator(".year-closing .list__sub");
  expect(await details.count()).toBeGreaterThan(0);
  expect(await details.evaluateAll((nodes) => nodes.filter((node) =>
    getComputedStyle(node).whiteSpace === "nowrap" || node.scrollWidth > node.clientWidth + 1
  ).length)).toBe(0);
});

/** Exercises transient UI through the real shared script without writing bookings. */
test("Undo remains available and field errors preserve useful instructions", async ({ page }) => {
  await login(page);
  await page.clock.install();
  await page.route("**/profile", async (route) => {
    const response = await route.fetch();
    const body = (await response.text()).replace("</main>", `
      <div class="toast" role="status"><span>Test saved</span>
        <form class="toast__undo"><button type="button">Rückgängig</button></form>
        <button type="button" data-toast-dismiss aria-label="Meldung schließen">×</button>
      </div>
      <label class="field"><span>Testmenge</span>
        <input id="polish-number" type="number" min="1" max="10" step="0.5" aria-describedby="polish-help">
        <span id="polish-help">Eine halbe Stunde ist 0,5.</span>
      </label>
      <label class="field"><span>Testadresse</span><input id="polish-email" type="email"></label>
      <label class="segmented__item"><input id="polish-radio" type="radio" name="polish-direction"><span>Ich schulde</span></label>
    </main>`);
    await route.fulfill({ response, body });
  });
  await page.goto("/profile");
  await expect(page.locator("#polish-number")).toBeVisible();
  await page.clock.fastForward(6_000);
  await expect(page.getByRole("button", { name: "Rückgängig", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Meldung schließen", exact: true }).click();
  await page.clock.fastForward(400);
  await expect(page.locator(".toast")).toHaveCount(0);

  const number = page.locator("#polish-number");
  for (const [value, message] of [
    ["0", "Bitte einen Wert ab 1 eingeben."],
    ["11", "Bitte höchstens 10 eingeben."],
    ["1.1", "Bitte einen Wert in Schritten von 0,5 eingeben."],
  ]) {
    await number.fill(value);
    await number.evaluate((node: HTMLInputElement) => node.reportValidity());
    await expect(page.locator("#polish-number-err")).toHaveText(message);
    await expect(number).toHaveAttribute("aria-describedby", "polish-help polish-number-err");
  }
  await number.fill("2");
  await expect(number).toHaveAttribute("aria-describedby", "polish-help");
  await number.evaluate((node: HTMLInputElement) => { node.setCustomValidity("Bitte den vereinbarten Satz prüfen."); node.reportValidity(); });
  await expect(page.locator("#polish-number-err")).toHaveText("Bitte den vereinbarten Satz prüfen.");

  const email = page.locator("#polish-email");
  await email.fill("not-an-address");
  await email.evaluate((node: HTMLInputElement) => node.reportValidity());
  await expect(page.locator("#polish-email-err")).toContainText("gültige E-Mail-Adresse");
  await email.focus();
  await page.keyboard.press("Tab");
  await expect(page.locator("#polish-radio")).toBeFocused();
  await expect(page.locator("#polish-radio + span")).toHaveCSS("outline-style", "solid");
});

for (const scheme of ["light", "dark"] as const) {
  /** Measures hidden/contextual shared chrome in both palettes, including a focused skip link. */
  test(`contextual status markers and skip link have contrast in ${scheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await login(page);
    await page.goto("/?year=1");
    await page.evaluate(() => {
      const fixture = document.createElement("div");
      fixture.id = "polish-contrast";
      fixture.innerHTML = '<div class="step step--done"><span class="step__n">1</span></div><span class="appbar__offline">2</span>';
      document.querySelector("main")!.appendChild(fixture);
    });
    await page.locator(".skip").focus();
    const result = await new AxeBuilder({ page }).include(".skip").include("#polish-contrast").withRules(["color-contrast"]).analyze();
    expect(result.violations).toEqual([]);
  });
}
