import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

/** Opens the CI fixture with the same cookie/CSRF login flow used by operators. */
async function login(page: Page) {
  await page.goto("/login");
  await page.getByRole("textbox", { name: "Benutzername" }).fill(process.env.E2E_ADMIN_USER || "admin");
  await page.locator('input[name="password"]').fill(process.env.E2E_ADMIN_PASS || "e2e-admin-password-123");
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

test.beforeEach(async ({ page }) => { await login(page); });

/** Checks every quantity unit and both hourly modes against the original POST controls. */
test("combined billing preserves canonical units, modes and quantity calculation", async ({ page }) => {
  await page.goto("/neighbors/1?year=1");
  const form = page.locator("[data-entry-form]");
  const kind = form.locator("[data-booking-kind]");
  await kind.selectOption("quantity");
  const billing = form.locator("[data-unit]");
  for (const unit of ["ha", "Ballen", "m³", "Fuhre", "t", "__custom"]) {
    await billing.selectOption(unit);
    await expect(form.locator('[name="unit"]')).toHaveValue(unit);
    await expect(form.locator('[data-booking-panel="quantity:out quantity:in"]')).toBeVisible();
    await expect(form.locator('[data-booking-panel="equipment:out"]').first()).toBeHidden();
    await expect(form.locator("[data-hours]")).not.toHaveAttribute("required");
    await expect(form.locator("[data-unit-custom]")).toBeVisible({ visible: unit === "__custom" });
    await form.locator("[data-qty]").fill("10");
    await form.locator("[data-unit-price]").fill("3.20");
    await expect(form.locator("[data-qty-cost]")).toHaveText(/32,00/);
  }
  await kind.selectOption("equipment");
  await form.getByText("Frei zusammenstellen", { exact: true }).click();
  await expect(form.locator('[name="unit"]')).toHaveValue("h");
  await expect(form.locator('[name="mode"][value="manual"]')).toBeChecked();
  await expect(form.locator('[data-mode-panel="manual"]')).toBeVisible();
  await expect(form.locator("[data-hours]")).toHaveAttribute("required");
  await form.getByText("Fixes Gespann", { exact: true }).click();
  await expect(form.locator('[name="mode"][value="gespann"]')).toBeChecked();
  await expect(form.locator('[data-mode-panel="gespann"]')).toBeVisible();
});

/** Verifies that search/selection filtering cannot drop IDs or accidentally submit a booking. */
test("machine filtering preserves selected IDs and Enter never saves the form", async ({ page }) => {
  await page.goto("/neighbors/1?year=1");
  const form = page.locator("[data-entry-form]");
  await form.getByText("Frei zusammenstellen", { exact: true }).click();
  const machine = form.locator("[data-machine]").first();
  const id = await machine.inputValue();
  await machine.check();
  const search = form.getByRole("searchbox", { name: "Maschinen suchen" });
  await search.fill("no-such-machine");
  await expect(machine).toBeHidden();
  await expect(machine).toBeChecked();
  expect(await form.evaluate(el => new FormData(el as HTMLFormElement).getAll("machine_ids"))).toEqual([id]);
  let submissions = 0;
  page.on("request", request => { if (request.method() === "POST") submissions++; });
  await search.press("Enter");
  await expect(form.locator("[data-machine-picker] + [role=status]")).toContainText("Keine Treffer");
  expect(submissions).toBe(0);
  await search.fill("");
  await form.getByRole("button", { name: "Nur ausgewählte (1)", exact: true }).click();
  await expect(machine).toBeVisible();
  await machine.uncheck();
  await expect(form.locator("[data-machine-picker] + [role=status]")).toContainText("Keine Treffer");
  await form.getByRole("button", { name: "Nur ausgewählte (0)", exact: true }).click();
  await expect(machine).toBeVisible();
});

/** Pins the rate and proves isolated quantity drafts cannot submit a hidden linked person. */
test("hourly preview and optional person stay explicit across isolated quantity drafts", async ({ page }) => {
  await page.goto("/neighbors/1?year=1");
  const form = page.locator("[data-entry-form]");
  await form.getByText("Frei zusammenstellen", { exact: true }).click();
  await form.locator('[name="tractor_id"]').selectOption("1");
  await form.locator('[name="load_level_id"]').selectOption("1");
  await form.locator("[data-machine]").first().check();
  await form.locator("[data-hours]").fill("2");
  await expect(form.locator("[data-rate]")).toHaveText(/46,00/);
  await expect(form.locator("[data-cost]")).toHaveText(/92,00/);
  const details = form.locator("[data-person-details]");
  await details.locator("summary").click();
  await form.locator('[name="person_id"]').selectOption("1");
  await details.locator("summary").click();
  await expect(details.locator("summary")).toContainText("36,00");
  await form.locator("[data-booking-kind]").selectOption("quantity");
  await expect(form.locator('[name="person_id"]')).toHaveValue("");
  await expect(form.locator('[name="person_id"]')).toBeDisabled();
  await form.locator("[data-booking-kind]").selectOption("equipment");
  await expect(form.locator('[name="person_id"]')).toHaveValue("1");
  await expect(details.locator("summary")).toContainText("36,00");
});

/** Ensures hidden optional email errors reveal their field instead of silently blocking a save. */
test("invalid fields open closed disclosures and receive keyboard focus", async ({ page }) => {
  await page.goto("/admin/company");
  const field = page.locator('[name="mail_cc"]');
  const details = page.locator("details").filter({ has: field });
  await details.locator("summary").click();
  await field.fill("invalid-email");
  await details.locator("summary").click();
  await page.getByRole("button", { name: "Speichern", exact: true }).click();
  await expect(details).toHaveAttribute("open");
  await expect(field).toBeFocused();
  await expect(field).toHaveAttribute("aria-invalid", "true");
  await expect(page).toHaveURL(/\/admin\/company$/);
});

/** Round-trips all twenty settings through the real CSRF-protected save with details collapsed. */
test("collapsed company settings retain every field through an actual save", async ({ page }) => {
  await page.goto("/admin/company");
  const form = page.locator('form[action="/admin/company"]');
  const before = await form.evaluate(el => Array.from(new FormData(el as HTMLFormElement).entries()).filter(([key]) => !key.includes("csrf")).sort());
  expect(before).toHaveLength(20);
  const saved = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith("/admin/company"));
  await form.getByRole("button", { name: "Speichern", exact: true }).click();
  expect((await saved).status()).toBe(303);
  const after = await form.evaluate(el => Array.from(new FormData(el as HTMLFormElement).entries()).filter(([key]) => !key.includes("csrf")).sort());
  expect(after).toEqual(before);
});

/** Checks state-aware filter disclosure and proves exported CSV honors an empty-result filter. */
test("secondary booking and audit filters reopen only while active", async ({ page }) => {
  await page.goto("/buchungen?year=1");
  await expect(page.locator('form[action="/buchungen"] details')).not.toHaveAttribute("open");
  await page.goto("/buchungen?year=1&from=2025-01-01&unit=Ballen&voided=hide");
  await expect(page.locator('form[action="/buchungen"] details')).toHaveAttribute("open");
  await expect(page.locator('[name="from"]')).toHaveValue("2025-01-01");
  await page.getByRole("link", { name: "Zurücksetzen", exact: true }).click();
  await expect(page.locator('form[action="/buchungen"] details')).not.toHaveAttribute("open");
  await page.goto("/admin/audit?username=admin&from=2025-01-01&q=no-matching-ui-audit-event");
  await expect(page.locator('form[action="/admin/audit"] details')).toHaveAttribute("open");
  await expect(page.getByRole("link", { name: "CSV Export", exact: true })).toHaveAttribute("href", /username=admin/);
  const target = await page.getByRole("link", { name: "CSV Export", exact: true }).getAttribute("href");
  const csv = await page.request.get(target!);
  expect(csv.ok()).toBe(true);
  expect((await csv.text()).trim().split(/\r?\n/)).toHaveLength(1);
});

/** Exercises native billing controls and keyboard disclosures when enhancement scripts cannot run. */
test("native form controls and optional sections remain usable without JavaScript", async ({ browser, page }) => {
  const context = await browser.newContext({ baseURL: new URL(page.url()).origin, storageState: await page.context().storageState(), javaScriptEnabled: false });
  try {
    const native = await context.newPage();
    await native.goto("/neighbors/1?year=1");
    await expect(native.locator('select[name="unit"]')).toBeVisible();
    await expect(native.getByRole("radiogroup", { name: "Zusammenstellung" })).toBeVisible();
    await native.goto("/admin/company");
    const numbering = native.locator("details").filter({ has: native.locator('[name="invoice_start"]') });
    await numbering.locator("summary").focus();
    await native.keyboard.press("Enter");
    await expect(native.locator('[name="invoice_start"]')).toBeVisible();
    await expect(native.getByRole("button", { name: "Speichern", exact: true })).toBeVisible();
  } finally {
    await context.close();
  }
});

/** Reproduces poisoned remembered defaults while protecting both edit and copy source values. */
test("remembered new-booking defaults never overwrite an edited or copied record", async ({ page }) => {
  await page.evaluate(() => {
    for (const key of ["global", "1"]) localStorage.setItem("treckrr:entry-defaults:" + key,
      JSON.stringify({ unit: "Ballen", task_label: "Unrelated remembered task", mode: "gespann" }));
  });
  for (const action of ["edit", "copy"]) {
    await page.goto("/entries/1/" + action);
    await expect(page.locator('[name="task_label"]')).toHaveValue("Mähen");
    await expect(page.locator('[name="unit"]')).toHaveValue("h");
  }
  await page.goto("/neighbors/1?year=1");
  await expect(page.locator("[data-booking-kind]")).toHaveValue("quantity");
  await expect(page.locator("[data-unit]")).toHaveValue("Ballen");
  await expect(page.locator('[name="task_label"]')).toHaveValue("Unrelated remembered task");
});

/** Measures the shared inactive-card presentation using populated labels, badges and links. */
test("inactive record content keeps sufficient contrast in both themes", async ({ page }) => {
  await page.goto("/neighbors");
  // Exercise the shared inactive class on a populated fixture without archiving
  // business records used by later tests. The real archived route is also audited.
  await page.locator("[data-nb-item]").first().evaluate(card => {
    card.classList.add("is-inactive");
    const status = document.createElement("span");
    status.className = "pill-off";
    status.textContent = "inaktiv";
    card.querySelector(".list__title")!.append(status);
  });
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    const result = await new AxeBuilder({ page }).include("[data-nb-item]")
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.filter(v => v.impact === "serious" || v.impact === "critical")).toEqual([]);
  }
});

/** Guards against later base rules shrinking touch actions or creating viewport overflow. */
test("booking actions keep 44px touch targets at narrow mobile widths", async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 844 });
  await page.goto("/neighbors/1?year=1");
  const targets = await page.locator(".bcard__acts .iconact").evaluateAll(nodes =>
    nodes.map(node => { const rect = node.getBoundingClientRect(); return { width: rect.width, height: rect.height }; }));
  expect(targets.length).toBeGreaterThan(0);
  for (const target of targets) { expect(target.width).toBeGreaterThanOrEqual(44); expect(target.height).toBeGreaterThanOrEqual(44); }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
