import { test, expect, type Locator, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

async function login(page: Page) {
  await page.goto("/login");
  await page.locator('[name="username"]').fill(USER);
  await page.locator('[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

async function freshAccount(page: Page) {
  await page.goto("/years");
  const create = page.locator('form[action="/years"]');
  const year = await create.locator('[name="year"]').inputValue();
  const csrf = await create.locator('[name="csrf_token"]').inputValue();
  const response = await page.request.post("/years", { form: {
    csrf_token: csrf, year, base_id: "1", label: `Fremdgeräte E2E ${year}`,
  } });
  expect(response.ok()).toBeTruthy();
  const yearID = new URL(response.url()).searchParams.get("year");
  expect(yearID).toBeTruthy();
  const assigned = await page.request.post("/years/add-neighbor", { form: {
    csrf_token: csrf, year_id: yearID!, neighbor_id: "1",
  } });
  expect(assigned.ok()).toBeTruthy();
  return { year, yearID: yearID!, url: `/neighbors/1?year=${yearID}` };
}

async function createEquipment(page: Page, values: Record<string, string>) {
  await page.goto("/neighbors/1/equipment");
  const create = page.locator('form[action="/neighbors/1/equipment"]');
  const disclosure = create.locator("xpath=ancestor::details");
  if (await disclosure.getAttribute("open") === null) await disclosure.locator(":scope > summary").click();
  for (const [name, value] of Object.entries(values)) await create.locator(`[name="${name}"]`).fill(value);
  await Promise.all([
    page.waitForNavigation({ waitUntil: "load" }),
    create.getByRole("button", { name: "Anlegen", exact: true }).click(),
  ]);
  await expect(page.locator("body")).toContainText("Fremdgerät angelegt");
}

function firstPerson(form: Locator) {
  return form.locator("[data-person-row]:visible").first();
}

async function addMaintainedPerson(form: Locator, hours: string, rate: string) {
  const details = form.locator("[data-person-details]");
  if (await details.getAttribute("open") === null) await details.locator(":scope > summary").click();
  const row = firstPerson(form);
  await row.locator('[name="person_id"]').selectOption("1");
  await row.locator('[name="person_hours"]').fill(hours);
  await row.locator('[name="person_rate"]').fill(rate);
}

test("foreign equipment and multiple people work for hourly and quantity counterclaims", async ({ page }) => {
  await login(page);
  const suffix = Date.now().toString(36);
  const hourlyName = `Zwangsmischer 1000 l ${suffix}`;
  const quantityName = `Mischerfüllung ${suffix}`;
  await createEquipment(page, {
    name: hourlyName, billing_unit: "h", capacity: "1000", capacity_unit: "l",
    default_rate: "12", note: "nur Mischer, ohne Traktor",
  });
  await createEquipment(page, {
    name: quantityName, billing_unit: "Füllung", capacity: "1000", capacity_unit: "l",
    default_rate: "30", note: "Abrechnung je Füllung",
  });
  await expect(page.getByText(hourlyName, { exact: true })).toBeVisible();
  await expect(page.getByText(quantityName, { exact: true })).toBeVisible();

  const account = await freshAccount(page);
  await page.goto(account.url);
  let form = page.locator("[data-unified-booking]");
  await form.locator('[name="booking_direction"][value="in"]').check();
  const equipmentSelect = form.locator('[name="neighbor_equipment_id"]');
  const hourlyOption = equipmentSelect.locator("option", { hasText: hourlyName });
  const quantityOption = equipmentSelect.locator("option", { hasText: quantityName });
  expect(await hourlyOption.evaluate(option => (option as HTMLOptionElement).disabled)).toBe(false);
  expect(await quantityOption.evaluate(option => (option as HTMLOptionElement).disabled)).toBe(true);
  const hourlyID = await hourlyOption.getAttribute("value");
  expect(hourlyID).toBeTruthy();
  await equipmentSelect.selectOption(hourlyID!);
  await expect(form.locator('[name="mode"]')).toHaveValue("free");
  await expect(form.locator('[name="partner_rate"]')).toHaveValue("12");
  await form.locator('[name="hours"]').fill("2.5");
  await form.locator('[name="task_label"]').fill(`Nachbars Zwangsmischer ${account.year}`);
  await addMaintainedPerson(form, "2.5", "20");
  await form.locator("[data-person-add]").click();
  const second = form.locator("[data-person-row]:visible").nth(1);
  await second.locator('[name="person_name"]').fill("Freier Helfer");
  await second.locator('[name="person_hours"]').fill("1");
  await second.locator('[name="person_rate"]').fill("18");
  await expect(form.locator(".booking-preview__total")).toContainText("98,00");
  const accessibility = await new AxeBuilder({ page }).include("[data-unified-booking]")
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(accessibility.violations.filter(result => result.impact === "serious" || result.impact === "critical")).toEqual([]);
  const postedHourly = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/entries");
  await form.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  expect((await postedHourly).status()).toBe(303);
  let card = page.locator(".bcard").filter({ hasText: `Nachbars Zwangsmischer ${account.year}` });
  await expect(card.locator(".bcard__cost")).toContainText(/[-−]98,00/);
  await card.getByRole("link", { name: "Bearbeiten", exact: true }).click();
  form = page.locator('[data-unified-booking][data-entry-edit]');
  await expect(form.locator('[name="neighbor_equipment_id"]')).toHaveValue(hourlyID!);
  await expect(form.locator("[data-person-row]:visible")).toHaveCount(2);
  await expect(firstPerson(form).locator('[name="person_id"]')).toHaveValue("1");
  await expect(form.locator("[data-person-row]:visible").nth(1).locator('[name="person_name"]'))
    .toHaveValue("Freier Helfer");

  await page.goto(account.url);
  form = page.locator("[data-unified-booking]");
  await form.locator('[name="booking_direction"][value="in"]').check();
  await form.locator('[name="booking_kind"]').selectOption("quantity");
  const quantitySelect = form.locator('[name="neighbor_equipment_id"]');
  expect(await quantitySelect.locator("option", { hasText: hourlyName })
    .evaluate(option => (option as HTMLOptionElement).disabled)).toBe(true);
  const quantityOptionCurrent = quantitySelect.locator("option", { hasText: quantityName });
  expect(await quantityOptionCurrent.evaluate(option => (option as HTMLOptionElement).disabled)).toBe(false);
  const quantityID = await quantityOptionCurrent.getAttribute("value");
  expect(quantityID).toBeTruthy();
  await quantitySelect.selectOption(quantityID!);
  await expect(form.locator('[name="unit"]')).toHaveValue("__custom");
  await expect(form.locator('[name="unit_custom"]')).toHaveValue("Füllung");
  await expect(form.locator('[name="unit_price"]')).toHaveValue("30");
  await form.locator('[name="quantity"]').fill("2");
  await form.locator('[name="task_label"]').fill(`Nachbars Füllungen ${account.year}`);
  await addMaintainedPerson(form, "1.25", "20");
  await expect(form.locator(".booking-preview__total")).toContainText("85,00");
  const postedQuantity = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/entries");
  await form.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  expect((await postedQuantity).status()).toBe(303);
  card = page.locator(".bcard").filter({ hasText: `Nachbars Füllungen ${account.year}` });
  await expect(card.locator(".bcard__cost")).toContainText(/[-−]85,00/);

  await page.goto("/personen");
  const report = page.locator(".dtable tbody tr").filter({ hasText: "E2E Helfer" });
  await expect(report).toContainText("3,75");
  await expect(report).toContainText("75,00");

  await page.setViewportSize({ width: 320, height: 844 });
  await page.goto(account.url);
  const mobileForm = page.locator("[data-unified-booking]");
  await expect(mobileForm).toBeVisible();
  const formBounds = await mobileForm.boundingBox();
  expect(formBounds).not.toBeNull();
  expect(formBounds!.x).toBeGreaterThanOrEqual(0);
  expect(formBounds!.x + formBounds!.width).toBeLessThanOrEqual(320);
});
