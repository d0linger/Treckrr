import { expect, Page, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

/** Seeds a fresh disposable account-year with own work and two counterclaims. */
async function filterAccount(page: Page) {
  await page.goto("/login");
  await page.locator('[name="username"]').fill(USER);
  await page.locator('[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
  await page.goto("/years");
  const create = page.locator('form[action="/years"]');
  const year = await create.locator('[name="year"]').inputValue();
  const csrf = await create.locator('[name="csrf_token"]').inputValue();
  const response = await page.request.post("/years", { form: {
    csrf_token: csrf, year, base_id: "1", label: `Filter E2E ${year}`,
  } });
  expect(response.ok()).toBeTruthy();
  const yearID = new URL(response.url()).searchParams.get("year");
  expect(yearID).toBeTruthy();
  const assigned = await page.request.post("/years/add-neighbor", { form: {
    csrf_token: csrf, year_id: yearID!, neighbor_id: "1",
  } });
  expect(assigned.ok()).toBeTruthy();
  const common = { csrf_token: csrf, booking_form_version: "2", year_id: yearID!, neighbor_id: "1", entry_date: `${year}-05-01` };
  const bookings: Array<Record<string, string>> = [
    { booking_kind: "quantity", booking_direction: "out", task_label: "Eigene Pressarbeit", note: "Eigene Ballen am Nordfeld", unit: "Ballen", quantity: "10", unit_price: "10" },
    { booking_kind: "equipment", booking_direction: "in", mode: "free", task_label: "Fremde Arbeit 50%", note: "Gegenleistung am Südhang", hours: "2", partner_label: "Fremdes Gespann", partner_rate: "45", person_row_id: "", person_id: "", person_name: "Franz Filter", person_hours: "1.5", person_rate: "20", person_state: "active" },
    { booking_kind: "labor", booking_direction: "in", task_label: "Mithilfe Filter", hours: "1.5", person_row_id: "", person_id: "", person_name: "Hans Filter", person_hours: "", person_rate: "20", person_state: "active", entry_date: `${year}-05-02` },
  ];
  for (const fields of bookings) {
    const saved = await page.request.post("/entries", { form: { ...common, ...fields } });
    expect(saved.ok()).toBeTruthy();
    expect(await saved.text()).toContain(fields.task_label);
  }
  return { year, yearID: yearID! };
}

for (const colorScheme of ["light", "dark"] as const) {
  /** Checks real populated task cells so adjacent notes cannot hide a color-only link regression. */
  test(`booking task links remain distinguishable beside notes and partner details in ${colorScheme} mode`, async ({ page }) => {
    await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
    const account = await filterAccount(page);
    await page.goto(`/buchungen?year=${account.yearID}`);
    const rows = page.locator(".dtable tbody tr");
    await expect(rows).toHaveCount(3);

    const own = rows.filter({ hasText: "Eigene Pressarbeit" });
    const incoming = rows.filter({ hasText: "Fremde Arbeit 50%" });
    const ownTask = own.getByRole("link", { name: "Eigene Pressarbeit", exact: true });
    const incomingTask = incoming.getByRole("link", { name: "Fremde Arbeit 50%", exact: true });
    await expect(ownTask).toBeVisible();
    await expect(ownTask).toHaveAttribute("href", /\/entries\/\d+\/edit$/);
    await expect(ownTask.locator("..")).toContainText("Eigene Ballen am Nordfeld");
    await expect(incomingTask).toBeVisible();
    await expect(incomingTask).toHaveAttribute("href", /\/ledger\/\d+\/edit$/);
    for (const detail of ["Fremdes Gespann", "Franz Filter", "Gegenleistung am Südhang"]) {
      await expect(incomingTask.locator("..")).toContainText(detail);
    }

    const results = await new AxeBuilder({ page }).include(".dtable")
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    const violations = results.violations.filter(result => result.impact === "serious" || result.impact === "critical");
    expect(violations, JSON.stringify(violations.map(result => ({
      id: result.id, impact: result.impact, targets: result.nodes.map(node => node.target),
    })), null, 2)).toEqual([]);
  });
}

/** Finds the overview from the dashboard and exercises combined filters and a real CSV download. */
test("dashboard booking shortcut opens both directions with working filters and complete CSV details", async ({ page }) => {
  const account = await filterAccount(page);
  await page.goto(`/?year=${account.yearID}`);
  const shortcut = page.locator("main").getByRole("link", { name: "Buchungen & Filter", exact: true });
  await expect(shortcut).toBeVisible();
  await expect(shortcut).toHaveAttribute("href", `/buchungen?year=${account.yearID}`);
  await shortcut.click();
  await expect(page).toHaveURL(new RegExp(`/buchungen\\?year=${account.yearID}$`));
  const filter = page.locator('form[action="/buchungen"]');
  const rows = page.locator(".dtable tbody tr");
  await expect(rows).toHaveCount(3);
  await filter.getByRole("combobox", { name: "Verrechnungsrichtung", exact: true }).selectOption("in");
  await filter.getByRole("button", { name: "Filtern", exact: true }).click();
  await expect(rows).toHaveCount(2);
  await expect(page.locator('input[name="booking_id"]')).toHaveCount(2);
  await expect(page.locator('input[name="entry_id"]')).toHaveCount(0);
  await filter.getByRole("combobox", { name: "Leistungsart", exact: true }).selectOption("equipment");
  await filter.locator("details > summary").click();
  await filter.getByLabel("Von", { exact: true }).fill(`${account.year}-05-01`);
  await filter.getByLabel("Bis", { exact: true }).fill(`${account.year}-05-01`);
  await filter.getByLabel("Tätigkeit / Notiz", { exact: true }).fill("50%");
  await filter.getByRole("button", { name: "Filtern", exact: true }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Fremde Arbeit 50%");
  await expect(rows.first()).toContainText("Franz Filter");
  await expect(rows.first()).toContainText("-120,00");
  await expect(filter.locator("details")).toHaveAttribute("open", "");

  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("link", { name: "Gefilterte Liste als CSV", exact: true }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe(`treckrr_buchungen_${account.year}.csv`);
  const stream = await download.createReadStream();
  expect(stream).not.toBeNull();
  let csv = "";
  for await (const chunk of stream!) csv += chunk.toString("utf8");
  expect(csv).toContain("Fremde Arbeit 50%");
  expect(csv).toContain("-120,00");
  expect(csv).toContain("Franz Filter: 1,5 Mannstunden × 20 €");
  expect(csv).not.toContain("Eigene Pressarbeit");
  expect(csv).not.toContain("Mithilfe Filter");

  await page.getByRole("link", { name: "Betrag", exact: true }).click();
  const sorted = new URL(page.url());
  for (const [key, value] of Object.entries({ year: account.yearID, direction: "in", kind: "equipment", task: "50%", from: `${account.year}-05-01`, to: `${account.year}-05-01` })) {
    expect(sorted.searchParams.get(key)).toBe(value);
  }
  await expect(rows).toHaveCount(1);
  await filter.getByRole("link", { name: "Zurücksetzen", exact: true }).click();
  await expect(rows).toHaveCount(3);
  await expect(filter.getByRole("combobox", { name: "Verrechnungsrichtung", exact: true })).toHaveValue("");
  await expect(filter.getByRole("combobox", { name: "Leistungsart", exact: true })).toHaveValue("");

  await filter.getByRole("combobox", { name: "Verrechnungsrichtung", exact: true }).selectOption("in");
  await filter.getByRole("combobox", { name: "Leistungsart", exact: true }).selectOption("labor");
  await filter.getByLabel("Tätigkeit / Notiz", { exact: true }).fill("Hans Filter");
  await filter.getByRole("button", { name: "Filtern", exact: true }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Mithilfe Filter");
  await filter.getByLabel("Tätigkeit / Notiz", { exact: true }).fill("Kein solcher Einsatz");
  await filter.getByRole("button", { name: "Filtern", exact: true }).click();
  await expect(page.getByText("Keine Buchungen gefunden", { exact: true })).toBeVisible();
});
