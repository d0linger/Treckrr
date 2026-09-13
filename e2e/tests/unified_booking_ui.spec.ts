import { test, expect, Page, Locator } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

const web = path.resolve(__dirname, "../../internal/web");

/** Supplies deterministic master-data values to the actual booking template. */
function fixture(): string {
  const template = readFileSync(path.join(web, "templates/neighbor.html"), "utf8");
  const start = template.indexOf('<form method="post" action="/entries"');
  let form = template.slice(start, template.indexOf("</form>", start) + 7);
  form = form.replace(/{{if \.HasInvoice}}disabled{{end}}/g, "")
    .replace(/{{if not \.Persons}}[\s\S]*?{{end}}/g, "")
    .replace(/{{range \.Gespanne}}[\s\S]*?{{end}}/g, '<option value="1">E2E Gespann</option>')
    .replace(/{{range \.Tractors}}[\s\S]*?{{end}}/g, '<option value="1">E2E Traktor</option>')
    .replace(/{{range \.Loads}}[\s\S]*?{{end}}/g, '<option value="1">mittel</option>')
    .replace(/{{range \.Persons}}[\s\S]*?{{end}}/g, '<option value="1" data-person-rate="18">E2E Helfer (18,00 €/h)</option>')
    .replace(/{{template "machine-checks"[\s\S]*?}}/g, '<div class="checkgroup" data-machine-picker><label class="check" data-category="Ernte"><input type="checkbox" name="machine_ids" value="1" data-machine> E2E Mähwerk</label><label class="check" data-category="Ernte"><input type="checkbox" name="machine_ids" value="2" data-machine> E2E Schwader</label></div>');
  const values: Record<string, string> = { ".Year.ID": "11", ".Neighbor.ID": "22", ".Base.ID": "1", ".Remaining": "72", ".Neighbor.Name": "E2E Nachbar", ".Today": "2026-09-13" };
  form = form.replace(/{{([^}]+)}}/g, (_, expression: string) => {
    if (!(expression in values)) throw new Error(`Unhandled booking fixture expression: ${expression}`);
    return values[expression];
  });
  form = form.replace("</form>", '<input type="hidden" name="csrf_token" value="fixture-csrf"></form>');
  return `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="user-id" content="7"><link rel="stylesheet" href="/static/css/app.css"></head><body><main class="main"><h1>Buchung erfassen</h1>${form}</main>
    <script src="/static/js/app.js"></script><script src="/static/js/form-ux.js"></script><script src="/static/js/offline.js"></script><script src="/static/js/capture.js"></script><script src="/static/js/unified-booking.js"></script><script src="/static/js/entry-form.js"></script></body></html>`;
}

/** Intercepts every request so GUI unit scenarios never reach a real app or database. */
async function mockPage(page: Page, failedPricing = false) {
  page.on("pageerror", error => { throw error; });
  await page.route("**/*", route => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/fixture") return route.fulfill({ contentType: "text/html", body: fixture() });
    if (pathname.startsWith("/static/js/") || pathname === "/static/css/app.css") {
      return route.fulfill({ contentType: pathname.endsWith(".css") ? "text/css" : "application/javascript", body: readFileSync(path.join(web, pathname), "utf8") });
    }
    if (pathname === "/api/base/1/pricing") return failedPricing ? route.fulfill({ status: 503 }) : route.fulfill({ json: {
      tractors: [{ id: 1, ps: 100 }], loads: [{ id: 1, cost: 0.36 }],
      machines: [{ id: 1, rate: 10 }, { id: 2, rate: 5 }],
      gespanne: [{ id: 1, tractor: 1, load: 1, machines: [1] }],
    } });
    if (pathname === "/api/entries/precheck") return route.fulfill({ json: {} });
    return route.fulfill({ status: 404 });
  });
  await page.goto("http://booking-ui.test/fixture");
  return page.locator("[data-unified-booking]");
}

/** Captures browser-successful controls, including every repeated machine ID. */
async function successful(form: Locator) {
  return new URLSearchParams(await form.evaluate(element => [...new FormData(element as HTMLFormElement)].map(([key, value]) => [key, String(value)])));
}

/** Selects a manual own machine combination with a known 46 EUR hourly rate. */
async function ownEquipment(form: Locator) {
  await form.getByText("Frei zusammenstellen", { exact: true }).click();
  await expect(form.locator('[name="mode"][value="manual"]')).toBeChecked();
  await form.locator('[name="tractor_id"]').selectOption("1");
  await form.locator('[name="load_level_id"]').selectOption("1");
  await form.locator('[name="machine_ids"][value="1"]').check();
  await form.locator('[name="hours"]').fill("2");
}

/** Confirms hidden branches cannot contribute stale fields or hidden required errors. */
test("all eight variants expose only their successful controls", async ({ page }) => {
  const form = await mockPage(page);
  for (const direction of ["out", "in"]) {
    await form.locator(`[name="booking_direction"][value="${direction}"]`).check();
    for (const kind of ["equipment", "labor", "quantity", "fixed"]) {
      await form.locator('[name="booking_kind"]').selectOption(kind);
      const values = await successful(form);
      expect(values.get("booking_kind")).toBe(kind);
      expect(values.get("booking_direction")).toBe(direction);
      expect(values.has("hours")).toBe(kind === "equipment" || kind === "labor");
      expect(values.has("quantity")).toBe(kind === "quantity");
      expect(values.has("amount")).toBe(kind === "fixed");
      expect(values.has("partner_rate")).toBe(kind === "equipment" && direction === "in");
      expect(values.has("person_id")).toBe((kind === "equipment" || kind === "labor") && direction === "out");
      if (kind === "quantity") {
        expect(values.get("unit")).not.toBe("h");
        await expect(form.locator('[name="unit"] option[value="h"]')).toHaveAttribute("hidden", "");
      }
      expect(await form.evaluate(element => [...element.querySelectorAll<HTMLInputElement>("input[required], select[required]")].filter(control => !control.disabled && !!control.closest("[hidden]")).length)).toBe(0);
    }
  }
});

/** Keeps checked search-hidden machines successful through subsequent field edits and POST. */
test("filtering selected machines cannot remove them from a submitted booking", async ({ page }) => {
  const form = await mockPage(page);
  const submissions: URLSearchParams[] = [];
  await page.route("**/entries", route => {
    submissions.push(new URLSearchParams(route.request().postData()!));
    return route.fulfill({ contentType: "text/html", body: "Beide Maschinen gespeichert" });
  });
  await ownEquipment(form);
  await form.locator('[name="machine_ids"][value="2"]').check();
  await form.getByRole("searchbox", { name: "Maschinen suchen", exact: true }).fill("keine-treffer");
  await expect(form.locator('[name="machine_ids"][value="1"]')).toBeHidden();
  await expect(form.locator('[name="machine_ids"][value="2"]')).toBeHidden();
  await form.locator('[name="hours"]').fill("3");
  await form.locator('[name="task_label"]').fill("Gefilterte Maschinen");
  await expect(form.locator("[data-cost]")).toHaveText(/153,00/);
  expect((await successful(form)).getAll("machine_ids")).toEqual(["1", "2"]);
  await expect(form.locator('[name="machine_ids"][value="1"]')).toBeEnabled();
  await expect(form.locator('[name="machine_ids"][value="2"]')).toBeEnabled();
  await form.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect(page.locator("body")).toHaveText("Beide Maschinen gespeichert");
  expect(submissions).toHaveLength(1);
  expect(submissions[0].getAll("machine_ids")).toEqual(["1", "2"]);
});

/** Keeps drafts separate across direction/type and restores their own exact machine/helper values. */
test("changing direction and kind preserves isolated drafts without leaking hidden prices", async ({ page }) => {
  const form = await mockPage(page);
  await ownEquipment(form);
  await form.locator('[name="task_label"]').fill("Eigene Arbeit");
  await form.locator("[data-person-details] > summary").click();
  await form.locator('[name="person_id"]').selectOption("1");
  await form.locator('[name="person_hours"]').fill("1.5");
  await form.locator('[name="booking_direction"][value="in"]').check();
  await expect(form.locator('[name="hours"]')).toHaveValue("");
  await expect(form.locator('[name="partner_rate"]')).toHaveValue("");
  await form.locator('[name="partner_rate"]').fill("45");
  await form.locator('[name="partner_label"]').fill("Fremder Schwader");
  await form.locator('[name="hours"]').fill("3");
  await form.locator('[name="task_label"]').fill("Nachbars Arbeit");
  expect((await successful(form)).has("person_id")).toBe(false);
  await form.locator('[name="booking_kind"]').selectOption("fixed");
  await form.locator('[name="amount"]').fill("17");
  expect((await successful(form)).has("partner_rate")).toBe(false);
  await form.locator('[name="booking_kind"]').selectOption("equipment");
  await expect(form.locator('[name="partner_rate"]')).toHaveValue("45");
  await expect(form.locator('[name="hours"]')).toHaveValue("3");
  await form.locator('[name="booking_direction"][value="out"]').check();
  await expect(form.locator('[name="hours"]')).toHaveValue("2");
  await expect(form.locator('[name="task_label"]')).toHaveValue("Eigene Arbeit");
  await expect(form.locator('[name="person_hours"]')).toHaveValue("1.5");
  const restored = await successful(form);
  expect(restored.getAll("machine_ids")).toEqual(["1"]);
  expect(restored.has("partner_rate")).toBe(false);
  expect(restored.has("amount")).toBe(false);
});

/** Verifies separate own helper pricing and the account-direction total. */
test("own and incoming itemized totals use independent helper hours and literal labels", async ({ page }) => {
  const form = await mockPage(page);
  await ownEquipment(form);
  await form.locator("[data-person-details] > summary").click();
  await form.locator('[name="person_id"]').selectOption("1");
  await form.locator('[name="person_hours"]').fill("1.5");
  await form.locator('[name="person_rate"]').fill("20");
  await expect(form.locator(".booking-preview__total")).toContainText("122,00");
  await expect(form.locator("[data-booking-preview]")).toContainText("194,00 € zu meinen Gunsten");
  await form.locator('[name="booking_direction"][value="in"]').check();
  await form.locator('[name="hours"]').fill("2");
  await form.locator('[name="partner_label"]').fill('<img src=x onerror="alert(1)">');
  await form.locator('[name="partner_rate"]').fill("45");
  await form.locator("[data-partner-person-details] > summary").click();
  await form.locator('[name="partner_person"]').fill("Franz");
  await form.locator('[name="partner_person_rate"]').fill("20");
  await form.locator('[name="partner_person_hours"]').fill("1.5");
  await expect(form.locator(".booking-preview__total")).toContainText("120,00");
  await expect(form.locator("[data-booking-preview]")).toContainText("48,00 € zu Gunsten des Nachbarn");
  await expect(form.locator("[data-booking-preview]")).toContainText('<img src=x onerror="alert(1)">');
  await expect(form.locator("[data-booking-preview] img")).toHaveCount(0);
});

/** Keeps a machine pricing failure informative without blocking the canonical save path. */
test("failed pricing preview still submits exactly once after the precheck", async ({ page }) => {
  const form = await mockPage(page, true);
  const submissions: URLSearchParams[] = [];
  await page.route("**/entries", route => {
    submissions.push(new URLSearchParams(route.request().postData()!));
    return route.fulfill({ contentType: "text/html", body: "Gespeichert" });
  });
  await ownEquipment(form);
  await form.locator('[name="task_label"]').fill("Preisausfall");
  await expect(form.locator("[data-pricing-status]")).toContainText("Beim Speichern");
  await expect(form.locator("[data-rate-preview]")).not.toHaveClass(/is-loading/);
  await form.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect(page.locator("body")).toHaveText("Gespeichert");
  expect(submissions).toHaveLength(1);
  expect(submissions[0].getAll("machine_ids")).toEqual(["1"]);
  expect(submissions[0].get("booking_direction")).toBe("out");
});

/** Ensures personal counterparty drafts never overwrite remembered own booking defaults. */
test("remembered defaults ignore incoming, labor and fixed drafts", async ({ page }) => {
  let form = await mockPage(page);
  await form.locator('[name="booking_kind"]').selectOption("quantity");
  await form.locator('[name="unit"]').selectOption("Ballen");
  await form.locator('[name="task_label"]').fill("Eigene Ballen");
  await form.locator('[name="task_label"]').press("Tab");
  const read = () => page.evaluate(() => JSON.parse(localStorage.getItem("treckrr:entry-defaults:22") || "{}"));
  await expect.poll(async () => (await read()).task_label).toBe("Eigene Ballen");
  const own = await read();
  await form.locator('[name="booking_direction"][value="in"]').check();
  await form.locator('[name="task_label"]').fill("Privater Nachbarentwurf");
  await form.locator('[name="task_label"]').press("Tab");
  await form.locator('[name="booking_kind"]').selectOption("fixed");
  await form.locator('[name="booking_direction"][value="out"]').check();
  await form.locator('[name="task_label"]').fill("Fixer Sonderentwurf");
  await form.locator('[name="task_label"]').press("Tab");
  await form.locator('[name="booking_kind"]').selectOption("labor");
  await form.locator('[name="task_label"]').fill("Arbeitszeitentwurf");
  await form.locator('[name="task_label"]').press("Tab");
  expect(await read()).toEqual(own);
  await page.reload();
  form = page.locator("[data-unified-booking]");
  await expect(form.locator('[name="booking_direction"][value="out"]')).toBeChecked();
  await expect(form.locator('[name="booking_kind"]')).toHaveValue("quantity");
  await expect(form.locator('[name="unit"]')).toHaveValue("Ballen");
  await expect(form.locator('[name="task_label"]')).toHaveValue("Eigene Ballen");
});

/** Rejects preexisting nonstandard preference records instead of importing them into own work. */
test("marked counterparty, labor and fixed preferences are never restored", async ({ page }) => {
  await mockPage(page);
  for (const [booking_kind, booking_direction] of [["equipment", "in"], ["labor", "out"], ["fixed", "out"]]) {
    await page.evaluate(saved => localStorage.setItem("treckrr:entry-defaults:22", JSON.stringify(saved)), {
      booking_kind, booking_direction, unit: "Ballen", task_label: "Nicht übernehmen", mode: "manual", gespann_id: "1",
    });
    await page.reload();
    const form = page.locator("[data-unified-booking]");
    await expect(form.locator('[name="booking_direction"][value="out"]')).toBeChecked();
    await expect(form.locator('[name="booking_kind"]')).toHaveValue("equipment");
    await expect(form.locator('[name="task_label"]')).toHaveValue("");
    await expect(form.locator('[name="mode"][value="gespann"]')).toBeChecked();
  }
});

/** Keeps the native form operational without any enhancement or JavaScript validation. */
test("without JavaScript a fixed counterclaim still posts its native fields", async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false });
  try {
    const page = await context.newPage(), form = await mockPage(page);
    const submissions: URLSearchParams[] = [];
    await page.route("**/entries", route => {
      submissions.push(new URLSearchParams(route.request().postData()!));
      return route.fulfill({ contentType: "text/html", body: "Nativ gespeichert" });
    });
    await form.locator('[name="booking_direction"][value="in"]').check();
    await form.locator('[name="booking_kind"]').selectOption("fixed");
    await form.locator('[name="amount"]').fill("53");
    await form.locator('[name="task_label"]').fill("Native Gegenposition");
    await form.getByRole("button", { name: "Buchung speichern", exact: true }).click();
    await expect(page.locator("body")).toHaveText("Nativ gespeichert");
    expect(submissions).toHaveLength(1);
    expect(submissions[0].get("booking_kind")).toBe("fixed");
    expect(submissions[0].get("booking_direction")).toBe("in");
    expect(submissions[0].get("amount")).toBe("53");
  } finally { await context.close(); }
});
