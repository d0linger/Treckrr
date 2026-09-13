import { test, expect, Locator, Page } from "@playwright/test";

// Run against the disposable E2E service, never a real account. Each test makes
// its own open year so another spec's invoice/year lock cannot affect its data.
// The standard fixture supplies base/neighbor/tractor/load/machine/person ID 1.
const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";
type Kind = "equipment" | "labor" | "quantity" | "fixed";
type Direction = "out" | "in";
type Account = { yearID: string; year: string; url: string };

/** Creates a fresh account-year while retaining the standard pricing fixture. */
async function freshAccount(page: Page): Promise<Account> {
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
    csrf_token: csrf, year, base_id: "1", label: `Unified E2E ${year}`,
  } });
  expect(response.ok()).toBeTruthy();
  const yearID = new URL(response.url()).searchParams.get("year");
  expect(yearID).toBeTruthy();
  const assigned = await page.request.post("/years/add-neighbor", { form: {
    csrf_token: csrf, year_id: yearID!, neighbor_id: "1",
  } });
  expect(assigned.ok()).toBeTruthy();
  const account = { yearID: yearID!, year, url: `/neighbors/1?year=${yearID}` };
  await page.goto(account.url);
  await expect(page.locator("[data-unified-booking]")).toBeVisible();
  return account;
}

/** Opens a native details element only when its requested control is hidden. */
async function reveal(details: Locator) {
  if (await details.getAttribute("open") === null) await details.locator(":scope > summary").click();
}

/** Selects an actual compact-dialog variant and fills only that visible path. */
async function fillBooking(page: Page, account: Account, kind: Kind, direction: Direction, task: string) {
  const form = page.locator("[data-unified-booking]");
  await form.locator(`[name="booking_direction"][value="${direction}"]`).check();
  await form.locator('[name="booking_kind"]').selectOption(kind);
  await form.locator('[name="entry_date"]').fill(`${account.year}-05-01`);
  await form.locator('[name="task_label"]').fill(task);
  if (kind === "equipment" || kind === "labor") await form.locator('[name="hours"]').fill("2");
  if (kind === "equipment" && direction === "out") {
    await form.getByText("Frei zusammenstellen", { exact: true }).click();
    await expect(form.locator('[name="mode"][value="manual"]')).toBeChecked();
    await form.locator('[name="tractor_id"]').selectOption("1");
    await form.locator('[name="load_level_id"]').selectOption("1");
    await form.locator('[name="machine_ids"][value="1"]').check();
    await expect(form.locator("[data-cost]")).toHaveText(/92,00/);
  } else if (kind === "equipment") {
    await form.locator('[name="partner_label"]').fill("Nachbartraktor mit Schwader");
    await form.locator('[name="partner_rate"]').fill("45");
  } else if (kind === "labor" && direction === "out") {
    await reveal(form.locator("[data-person-details]"));
    await form.locator('[name="person_id"]').selectOption("1");
    await form.locator('[name="person_rate"]').fill("20");
  } else if (kind === "labor") {
    await reveal(form.locator("[data-partner-person-details]"));
    await form.locator('[name="partner_person"]').fill("Franz Nachbar");
    await form.locator('[name="partner_person_rate"]').fill("20");
  } else if (kind === "quantity") {
    await form.locator('[name="unit"]').selectOption("Ballen");
    await form.locator('[name="quantity"]').fill("3");
    await form.locator('[name="unit_price"]').fill("12.50");
  } else {
    await form.locator('[name="amount"]').fill("37.50");
  }
  return form;
}

/** Saves through all normal submit handlers and waits for the actual POST redirect. */
async function saveBooking(page: Page, form: Locator, task: string) {
  const ledger = await form.locator('[name="booking_direction"]:checked').inputValue() === "in" ||
    await form.locator('[name="booking_kind"]').inputValue() === "fixed";
  const posted = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/entries");
  await form.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  expect((await posted).status()).toBe(303);
  // A linked helper also mentions the source task in its backlink. Match the
  // primary task heading, not any descendant text, for an own-service record.
  const card = ledger ? page.locator(".bcard").filter({ hasText: task }) :
    page.locator(".bcard").filter({ has: page.locator(".bcard__task", { hasText: task }) });
  await expect(card).toHaveCount(1);
  await page.waitForLoadState("load");
}

for (const kind of ["equipment", "labor", "quantity", "fixed"] as Kind[]) {
  for (const direction of ["out", "in"] as Direction[]) {
    /** Verifies each service/direction saves to the right record family and Beleg section. */
    test(`unified ${kind} ${direction} saves with the correct signed settlement`, async ({ page }) => {
      const account = await freshAccount(page);
      const task = `E2E ${kind} ${direction} ${account.year}`;
      const form = await fillBooking(page, account, kind, direction, task);
      await saveBooking(page, form, task);
      const card = page.locator(".bcard").filter({ hasText: task });
      const amount = kind === "equipment" ? (direction === "out" ? "92,00" : "90,00") : kind === "labor" ? "40,00" : "37,50";
      await expect(card.locator(".bcard__cost")).toHaveText(new RegExp(`^${direction === "in" ? "[−-]" : ""}${amount}\\s*€$`));
      const ledger = direction === "in" || kind === "fixed";
      await expect(card.locator(`a[href^="/${ledger ? "ledger" : "entries"}/"][href$="/edit"]`)).toHaveCount(1);
      await page.goto(`/neighbors/1/beleg?year=${account.yearID}`);
      const line = page.locator(".beleg__lrow").filter({ hasText: task });
      await expect(line).toHaveCount(1);
      await expect(line).toContainText(amount);
      if (ledger) await expect(line).toHaveClass(/beleg__lrow--ver/);
      else await expect(line).not.toHaveClass(/beleg__lrow--ver/);
    });
  }
}

/** Adds a distinct helper quantity and rate without changing the machine charge. */
test("own equipment can book independent helper hours as a linked position", async ({ page }) => {
  const account = await freshAccount(page), task = `E2E independent helper ${account.year}`;
  const form = await fillBooking(page, account, "equipment", "out", task);
  await reveal(form.locator("[data-person-details]"));
  await form.locator('[name="person_id"]').selectOption("1");
  await form.locator('[name="person_hours"]').fill("1.5");
  await form.locator('[name="person_rate"]').fill("20");
  await saveBooking(page, form, task);
  const main = page.locator(".bcard").filter({ has: page.locator(".bcard__task", { hasText: task }) });
  await expect(main.locator(".bcard__cost")).toContainText("92,00");
  const helper = page.locator('.bcard[id^="entry-"]').filter({ hasText: "Mannstunden E2E Helfer" }).filter({ has: page.locator('.bcard__cost', { hasText: "30,00" }) });
  await expect(helper).toHaveCount(1);
  await expect(helper).toContainText(/1,5.*Mannstunde/);
  const helperID = await helper.getAttribute("id");
  await expect(main.locator(`a[href="#${helperID}"]`)).toHaveCount(1);
  await expect(page.locator(".summary-card__value")).toContainText("122,00");
});

/** Exercises structured ledger editing/copying without flattening partner pricing or helper time. */
test("incoming equipment keeps its independent helper through edit and copy", async ({ page }) => {
  const account = await freshAccount(page), task = `E2E partner copy ${account.year}`;
  const form = await fillBooking(page, account, "equipment", "in", task);
  await reveal(form.locator("[data-partner-person-details]"));
  await form.locator('[name="partner_person"]').fill("Franz Nachbar");
  await form.locator('[name="partner_person_rate"]').fill("20");
  await form.locator('[name="partner_person_hours"]').fill("1.5");
  await saveBooking(page, form, task);
  let card = page.locator(".bcard").filter({ hasText: task });
  await expect(card.locator(".bcard__cost")).toContainText("-120,00");
  const editURL = await card.locator('a[href^="/ledger/"][href$="/edit"]').getAttribute("href");
  const copyURL = await card.locator('a[href^="/ledger/"][href$="/copy"]').getAttribute("href");
  await page.goto(editURL!);
  const edit = page.locator('form[action^="/ledger/"][action$="/update"]');
  await expect(edit.locator('[name="partner_person_hours"]')).toHaveValue("1.5");
  await edit.locator('[name="hours"]').fill("3");
  const updated = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/update"));
  await edit.getByRole("button", { name: /speichern/i }).click();
  expect((await updated).status()).toBe(303);
  await expect(page.locator(".bcard").filter({ hasText: task }).locator(".bcard__cost")).toContainText("-165,00");
  await page.goto(copyURL!);
  const copy = page.locator('form[action="/entries"]');
  await expect(copy.locator('[name="booking_direction"]')).toHaveValue("in");
  await expect(copy.locator('[name="hours"]')).toHaveValue("3");
  await expect(copy.locator('[name="partner_person_hours"]')).toHaveValue("1.5");
  await copy.locator('[name="task_label"]').fill(task + " Kopie");
  const copied = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/entries");
  await copy.getByRole("button", { name: /speichern/i }).click();
  expect((await copied).status()).toBe(303);
  await expect(page.locator(".bcard").filter({ hasText: task })).toHaveCount(2);
  card = page.locator(".bcard").filter({ hasText: task + " Kopie" });
  await expect(card.locator(".bcard__cost")).toContainText("-165,00");
  await expect(page.locator(".summary-card__value")).toContainText("-330,00");
});

/** Confirms counterclaims reduce settlement but never the outgoing invoice or revenue base. */
test("incoming work offsets the account without reducing own invoice net or statistics", async ({ page }) => {
  const account = await freshAccount(page);
  let form = await fillBooking(page, account, "quantity", "out", `E2E own revenue ${account.year}`);
  await form.locator('[name="quantity"]').fill("10");
  await form.locator('[name="unit_price"]').fill("10");
  await saveBooking(page, form, `E2E own revenue ${account.year}`);
  form = await fillBooking(page, account, "labor", "in", `E2E counter labor ${account.year}`);
  await saveBooking(page, form, `E2E counter labor ${account.year}`);
  await expect(page.locator(".summary-card__value")).toContainText("60,00");
  await page.goto(`/stats?year=${account.yearID}`);
  await expect(page.locator(".kpi--rev .kpi__value")).toContainText("100,00");
  // Make this test independently runnable: the invoice spec need not have
  // supplied issuer prerequisites. Preserve the remaining company fields.
  await page.goto("/admin/company");
  const company = page.locator('form[action="/admin/company"]');
  await company.locator('[name="name"]').fill("Hof Bergmann");
  await company.locator('[name="address"]').fill("Feldweg 3\n4780 Schärding");
  await company.locator('[name="tax_id"]').fill("ATU12345678");
  await company.locator('[name="tax_mode"]').selectOption("regel");
  await company.locator('[name="vat_rate"]').fill("20");
  const savedCompany = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/admin/company");
  await Promise.all([
    page.waitForNavigation({ waitUntil: "load" }),
    company.getByRole("button", { name: "Speichern", exact: true }).click(),
  ]);
  expect((await savedCompany).status()).toBe(303);
  await page.goto(`/neighbors/1/invoice/confirm?year=${account.yearID}`);
  await expect(page.locator(".cfm-prev-row").filter({ has: page.getByText("Netto", { exact: true }) })).toContainText("100,00");
  await expect(page.locator(".cfm-prev-row").filter({ has: page.getByText("Brutto", { exact: true }) })).toContainText("120,00");
  await page.goto(`/neighbors/1/beleg?year=${account.yearID}`);
  await expect(page.locator(".beleg__lrow--ver").filter({ hasText: `E2E counter labor ${account.year}` })).toContainText("-40,00");
});

/** Retains the person attribution and explicit rate when editing and copying own labor. */
test("own labor remains attributed to its person after edit and copy", async ({ page }) => {
  const account = await freshAccount(page), task = `E2E labor attribution ${account.year}`;
  const form = await fillBooking(page, account, "labor", "out", task);
  await saveBooking(page, form, task);
  const card = page.locator(".bcard").filter({ hasText: task });
  const editURL = await card.locator('a[href^="/entries/"][href$="/edit"]').getAttribute("href");
  const copyURL = await card.locator('a[href^="/entries/"][href$="/copy"]').getAttribute("href");
  await page.goto(editURL!);
  const edit = page.locator('form[action^="/entries/"][action$="/update"]');
  await expect(edit.locator('[name="person_id"]')).toHaveValue("1");
  await edit.locator('[name="hours"]').fill("3");
  const updated = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/update"));
  await edit.getByRole("button", { name: /speichern/i }).click();
  expect((await updated).status()).toBe(303);
  await expect(page.locator(".bcard").filter({ hasText: task }).locator(".bcard__cost")).toContainText("60,00");
  await page.goto(copyURL!);
  const copy = page.locator('form[action="/entries"]');
  await expect(copy.locator('[name="person_id"]')).toHaveValue("1");
  await expect(copy.locator('[name="person_rate"]')).toHaveValue("20");
  await copy.locator('[name="task_label"]').fill(task + " Kopie");
  const copied = page.waitForResponse(response => response.request().method() === "POST" && new URL(response.url()).pathname === "/entries");
  await copy.getByRole("button", { name: "Buchung anlegen", exact: true }).click();
  expect((await copied).status()).toBe(303);
  await expect(page.locator(".bcard").filter({ hasText: task })).toHaveCount(2);
  const copyCard = page.locator(".bcard").filter({ hasText: task + " Kopie" });
  await expect(copyCard.locator(".bcard__cost")).toContainText("60,00");
  await copyCard.locator('a[href^="/entries/"][href$="/edit"]').click();
  await expect(page.locator('form[action$="/update"] [name="person_id"]')).toHaveValue("1");
});
