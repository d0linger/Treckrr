import { test, expect, Page, Locator } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

const web = path.resolve(__dirname, "../../internal/web");

/** Supplies the complete DOM contract required by the shared booking controllers. */
function fixture(): string {
  const personRow = `<fieldset data-person-row><legend>Person</legend>
    <input type="hidden" name="person_row_id" value="">
    <label data-person-name-field>Name der Person<input name="person_name"></label>
    <label>Gepflegte Person<select name="person_id"><option value="">Freien Namen eingeben</option><option value="1" data-person-rate="18">E2E Helfer</option></select></label>
    <label>Mannstunden<input name="person_hours" type="number" step="0.0001"></label>
    <label>Vereinbarter Personensatz<input name="person_rate" type="number" step="0.0001"></label>
    <input type="hidden" name="person_state" value="active">
    <button type="button" data-person-use-rate hidden>Stammsatz übernehmen</button>
    <button type="button" data-person-remove hidden>Person entfernen</button>
  </fieldset>`;
  const form = `<form method="post" action="/entries" data-entry-form data-entry-create data-entry-defaults data-unified-booking data-balance="72" data-pricing-url="/api/base/1/pricing" data-neighbor-name="E2E Nachbar">
    <input type="hidden" name="csrf_token" value="fixture-csrf"><input type="hidden" name="booking_form_version" value="2">
    <input type="hidden" name="year_id" value="11"><input type="hidden" name="neighbor_id" value="22">
    <fieldset><legend>Wer verrechnet wem?</legend>
      <label><input type="radio" name="booking_direction" value="out" checked>Ich verrechne</label>
      <label><input type="radio" name="booking_direction" value="in">Nachbar verrechnet</label>
    </fieldset>
    <label>Was wird verrechnet?<select name="booking_kind" data-booking-kind><option value="equipment">Traktor / Gespann / Gefährt</option><option value="labor">Arbeitszeit / Mannstunden</option><option value="quantity">Mengenleistung</option><option value="fixed">Freie Position / Kosten</option></select></label>
    <label>Datum<input type="date" name="entry_date" value="2026-09-13" required></label>
    <p data-booking-direction-note></p>
    <div data-booking-panel="equipment:out equipment:in">
      <select name="mode" data-mode-select><option value="gespann">Fixes Gespann</option><option value="manual">Frei zusammenstellen</option><option value="free">Anderes Gefährt / Freitext</option></select>
      <div data-mode-panel="gespann"><select name="gespann_id" data-gespann-select><option value="">— wählen —</option><option value="1">E2E Gespann</option></select></div>
      <div data-mode-panel="manual"><select name="tractor_id" data-tractor-select><option value="">— keiner —</option><option value="1">E2E Traktor</option></select><select name="load_level_id" data-load-select><option value="">— keine —</option><option value="1">mittel</option></select>
        <div class="checkgroup" data-machine-picker><label class="check" data-category="Ernte"><input type="checkbox" name="machine_ids" value="1" data-machine>E2E Mähwerk</label><label class="check" data-category="Ernte"><input type="checkbox" name="machine_ids" value="2" data-machine>E2E Schwader</label></div>
      </div>
      <label data-mode-panel="free">Gespann / Fahrzeug beschreiben<input name="partner_label" data-booking-required></label>
      <label data-agreed-equipment-rate>Vereinbarter Maschinensatz<input name="partner_rate" type="number" data-booking-required></label>
      <p data-no-tractor-note hidden></p>
    </div>
    <div data-booking-panel="equipment:out equipment:in labor:out labor:in"><label><span data-booking-hours-label>Stunden</span><input name="hours" type="number" data-hours data-booking-required></label><div data-rate-preview data-booking-catalog-rate><span data-cost>–</span><span data-rate>–</span></div></div>
    <div data-booking-panel="quantity:out quantity:in"><select name="unit" data-unit><option value="h" hidden>Stunden</option><option value="ha">Hektar</option><option value="Ballen">Ballen</option><option value="__custom">Andere Einheit</option></select><label data-unit-custom>Eigene Einheit<input name="unit_custom" data-unit-custom-input></label><input name="quantity" type="number" data-qty data-booking-required><input name="unit_price" type="number" data-unit-price data-booking-required><span data-unit-label></span><strong data-qty-cost></strong></div>
    <label data-booking-panel="fixed:out fixed:in">Betrag<input name="amount" type="number" data-booking-required></label>
    <details data-person-details><summary><span data-person-heading>Personen mitbuchen</span> <span data-person-summary>optional</span></summary><p data-person-help></p><div data-person-rows>${personRow}${personRow}${personRow}</div><button type="button" data-person-add hidden>Person hinzufügen</button><p data-person-status></p></details>
    <label>Tätigkeit / Beschreibung<input name="task_label" data-booking-task><span data-booking-task-help></span></label><input name="note">
    <div data-booking-preview></div><p data-pricing-status></p>
    <button class="btn btn--primary" type="submit">Buchung speichern</button><button type="button" data-defaults-reset>Gemerkte Vorgaben löschen</button>
    <template data-person-template>${personRow}</template>
  </form>`;
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

/** Returns one visible aligned person row without relying on globally unique names. */
function person(form: Locator, index = 0) {
  return form.locator("[data-person-row]:visible").nth(index);
}

/** Selects a manual own machine combination with a known 46 EUR hourly rate. */
async function ownEquipment(form: Locator) {
  await form.locator('[name="mode"]').selectOption("manual");
  await expect(form.locator('[name="mode"]')).toHaveValue("manual");
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
      expect(values.has("partner_rate")).toBe(false);
      expect(values.getAll("person_id")).toHaveLength(1);
      expect(values.getAll("person_name")).toHaveLength(1);
      expect(values.getAll("person_hours")).toHaveLength(1);
      expect(values.getAll("person_rate")).toHaveLength(1);
      expect(values.getAll("person_state")).toEqual(["active"]);
      if (kind === "quantity") {
        expect(values.get("unit")).not.toBe("h");
        await expect(form.locator('[name="unit"] option[value="h"]')).toHaveAttribute("hidden", "");
      }
      await expect(form.locator('[name="task_label"]')).toHaveJSProperty("required", kind === "quantity" || kind === "fixed");
      await expect(form.locator("[data-booking-task-help]")).toContainText(kind === "equipment" ? "Gespann oder Gefährt" : kind === "labor" ? "gewählte Person" : "Leistung oder Kostenposition");
      expect(await form.evaluate(element => [...element.querySelectorAll<HTMLInputElement>("input[required], select[required]")].filter(control => !control.disabled && !!control.closest("[hidden]")).length)).toBe(0);
    }
  }
});

/** Uses the same maintained tractor and machine controls for both account directions. */
test("both directions use the shared machine pool", async ({ page }) => {
  const form = await mockPage(page);
  for (const direction of ["out", "in"]) {
    await form.locator(`[name="booking_direction"][value="${direction}"]`).check();
    await form.locator('[name="booking_kind"]').selectOption("equipment");
    await form.locator('[name="mode"]').selectOption("manual");
    await form.locator('[name="tractor_id"]').selectOption("1");
    await form.locator('[name="load_level_id"]').selectOption("1");
    await form.locator('[name="machine_ids"][value="1"]').check();
    await form.locator('[name="hours"]').fill("2");

    const values = await successful(form);
    expect(values.get("tractor_id")).toBe("1");
    expect(values.get("load_level_id")).toBe("1");
    expect(values.getAll("machine_ids")).toEqual(["1"]);
    expect(values.has("partner_rate")).toBe(false);
    expect(values.has("neighbor_equipment_id")).toBe(false);
    await expect(form.locator("[data-booking-catalog-rate]")).toBeVisible();
    await expect(form.locator("[data-rate]")).toHaveText(/46,00/);
    await expect(form.locator("[data-cost]")).toHaveText(/92,00/);
  }
  await expect(form).not.toContainText("Fremdgerät");

  await page.setViewportSize({ width: 390, height: 844 });
  const mobileTile = form.locator("[data-booking-catalog-rate]");
  await expect(mobileTile).toBeVisible();
  const box = await mobileTile.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x + box!.width).toBeLessThanOrEqual(390);
});

/** Uses the maintained person rate automatically in both account directions. */
test("both directions use the maintained person rate", async ({ page }) => {
  const form = await mockPage(page);
  for (const direction of ["out", "in"]) {
    await form.locator(`[name="booking_direction"][value="${direction}"]`).check();
    await form.locator('[name="booking_kind"]').selectOption("labor");
    await form.locator('[name="hours"]').fill("2");
    await person(form).locator('[name="person_id"]').selectOption("1");
    await expect(person(form).locator('[name="person_rate"]')).toHaveValue("18");
    await expect(form.locator("[data-booking-preview]")).toContainText("Mannstunden · E2E Helfer");
    await expect(form.locator(".booking-preview__total")).toContainText("36,00");
    expect((await successful(form)).get("person_rate")).toBe("18");
  }
});

/** Keeps the incoming machine summary visible when a maintained helper is added. */
test("incoming equipment keeps its cost summary after adding a person", async ({ page }) => {
  const form = await mockPage(page);
  await form.locator('[name="booking_direction"][value="in"]').check();
  await form.locator('[name="mode"]').selectOption("manual");
  await form.locator('[name="tractor_id"]').selectOption("1");
  await form.locator('[name="load_level_id"]').selectOption("1");
  await form.locator('[name="machine_ids"][value="1"]').check();
  await form.locator('[name="hours"]').fill("5");
  await expect(form.locator("[data-cost]")).toHaveText(/230,00/);

  await form.locator("[data-person-details] > summary").click();
  await person(form).locator('[name="person_id"]').selectOption("1");
  await expect(person(form).locator('[name="person_rate"]')).toHaveValue("18");
  await expect(form.locator("[data-booking-catalog-rate]")).toBeVisible();
  await expect(form.locator("[data-cost]")).toHaveText(/230,00/);
  await expect(form.locator("[data-booking-preview]")).toContainText("Maschinenleistung · 5 × 46,00 €");
  await expect(form.locator("[data-booking-preview]")).toContainText("Mannstunden · E2E Helfer · 5 × 18,00 €");
  await expect(form.locator(".booking-preview__total")).toContainText("320,00 €");
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
  await person(form).locator('[name="person_id"]').selectOption("1");
  await person(form).locator('[name="person_hours"]').fill("1.5");
  await form.locator("[data-person-add]").click();
  await person(form, 1).locator('[name="person_name"]').fill("E2E Aushilfe");
  await person(form, 1).locator('[name="person_hours"]').fill("0.75");
  await person(form, 1).locator('[name="person_rate"]').fill("16");
  await form.locator('[name="booking_direction"][value="in"]').check();
  await expect(form.locator('[name="hours"]')).toHaveValue("");
  await expect(form.locator('[name="partner_rate"]')).toHaveValue("");
  await form.locator('[name="mode"]').selectOption("free");
  await form.locator('[name="partner_rate"]').fill("45");
  await form.locator('[name="partner_label"]').fill("Nachbars Schwader");
  await form.locator('[name="hours"]').fill("3");
  await form.locator('[name="task_label"]').fill("Nachbars Arbeit");
  expect((await successful(form)).getAll("person_id")).toEqual([""]);
  await form.locator('[name="booking_kind"]').selectOption("fixed");
  await form.locator('[name="amount"]').fill("17");
  expect((await successful(form)).has("partner_rate")).toBe(false);
  await form.locator('[name="booking_kind"]').selectOption("equipment");
  await expect(form.locator('[name="partner_rate"]')).toHaveValue("45");
  await expect(form.locator('[name="hours"]')).toHaveValue("3");
  await form.locator('[name="booking_direction"][value="out"]').check();
  await expect(form.locator('[name="hours"]')).toHaveValue("2");
  await expect(form.locator('[name="task_label"]')).toHaveValue("Eigene Arbeit");
  await expect(person(form).locator('[name="person_hours"]')).toHaveValue("1.5");
  const restored = await successful(form);
  expect(restored.getAll("machine_ids")).toEqual(["1"]);
  expect(restored.getAll("person_id")).toEqual(["1", ""]);
  expect(restored.getAll("person_name")).toEqual(["E2E Helfer", "E2E Aushilfe"]);
  expect(restored.getAll("person_hours")).toEqual(["1.5", "0.75"]);
  expect(restored.getAll("person_rate")).toEqual(["18", "16"]);
  expect(restored.getAll("person_state")).toEqual(["active", "active"]);
  expect(restored.has("partner_rate")).toBe(false);
  expect(restored.has("amount")).toBe(false);
});

/** Verifies separate own helper pricing and the account-direction total. */
test("own and incoming itemized totals use independent helper hours and literal labels", async ({ page }) => {
  const form = await mockPage(page);
  await ownEquipment(form);
  await form.locator("[data-person-details] > summary").click();
  await person(form).locator('[name="person_id"]').selectOption("1");
  await person(form).locator('[name="person_hours"]').fill("1.5");
  await person(form).locator('[name="person_rate"]').fill("20");
  await expect(form.locator(".booking-preview__total")).toContainText("122,00");
  await expect(form.locator("[data-booking-preview]")).toContainText("194,00 € zu meinen Gunsten");
  await form.locator('[name="booking_direction"][value="in"]').check();
  await form.locator('[name="mode"]').selectOption("free");
  await form.locator('[name="hours"]').fill("2");
  await form.locator('[name="partner_label"]').fill('<img src=x onerror="alert(1)">');
  await form.locator('[name="partner_rate"]').fill("45");
  await form.locator("[data-person-details] > summary").click();
  await person(form).locator('[name="person_name"]').fill("Franz");
  await person(form).locator('[name="person_rate"]').fill("20");
  await person(form).locator('[name="person_hours"]').fill("1.5");
  await expect(form.locator(".booking-preview__total")).toContainText("120,00");
  await expect(form.locator("[data-booking-preview]")).toContainText("48,00 € zu Gunsten des Nachbarn");
  await expect(form.locator("[data-booking-preview]")).toContainText('<img src=x onerror="alert(1)">');
  await expect(form.locator("[data-booking-preview] img")).toHaveCount(0);
});

/** Uses native radio arrows and disclosure keys while preserving each party's successful draft fields. */
test("keyboard direction and disclosure controls preserve isolated booking drafts without submitting", async ({ page }) => {
  const form = await mockPage(page);
  const submissions: string[] = [];
  page.on("request", request => { if (request.method() === "POST") submissions.push(request.url()); });
  await ownEquipment(form);
  await form.locator('[name="task_label"]').fill("Eigener Tastaturentwurf");
  const ownDetails = form.locator("[data-person-details]");
  const ownSummary = ownDetails.locator("summary");
  await ownSummary.focus();
  await page.keyboard.press("Enter");
  await expect(ownDetails).toHaveAttribute("open", "");
  await expect(ownSummary).toBeFocused();
  await person(form).locator('[name="person_id"]').selectOption("1");
  await person(form).locator('[name="person_hours"]').fill("1.25");
  await person(form).locator('[name="person_rate"]').fill("20");
  await ownSummary.focus();
  await page.keyboard.press("Space");
  await expect(ownDetails).not.toHaveAttribute("open");
  await expect(ownSummary).toBeFocused();
  expect((await successful(form)).get("person_hours")).toBe("1.25");

  const outgoing = form.locator('[name="booking_direction"][value="out"]');
  const incoming = form.locator('[name="booking_direction"][value="in"]');
  await outgoing.focus();
  await page.keyboard.press("ArrowRight");
  await expect(incoming).toBeChecked();
  await expect(incoming).toBeFocused();
  await expect(form.locator('[name="hours"]')).toHaveValue("");
  expect((await successful(form)).getAll("person_id")).toEqual([""]);
  await form.locator('[name="mode"]').selectOption("free");
  await form.locator('[name="partner_label"]').fill("Nachbars Tastaturgespann");
  await form.locator('[name="partner_rate"]').fill("45");
  await form.locator('[name="hours"]').fill("3");
  await form.locator('[name="task_label"]').fill("Nachbars Tastaturentwurf");
  const incomingDetails = form.locator("[data-person-details]");
  const incomingSummary = incomingDetails.locator("summary");
  await incomingSummary.focus();
  await page.keyboard.press("Space");
  await expect(incomingDetails).toHaveAttribute("open", "");
  await expect(incomingSummary).toBeFocused();
  await person(form).locator('[name="person_name"]').fill("Franz Tastatur");
  await person(form).locator('[name="person_rate"]').fill("21");
  await person(form).locator('[name="person_hours"]').fill("1.5");
  await incomingSummary.focus();
  await page.keyboard.press("Enter");
  await expect(incomingDetails).not.toHaveAttribute("open");
  await expect(incomingSummary).toBeFocused();
  expect((await successful(form)).get("person_hours")).toBe("1.5");

  await incoming.focus();
  await page.keyboard.press("ArrowLeft");
  await expect(outgoing).toBeChecked();
  await expect(outgoing).toBeFocused();
  const ownValues = await successful(form);
  expect(ownValues.get("hours")).toBe("2");
  expect(ownValues.get("task_label")).toBe("Eigener Tastaturentwurf");
  expect(ownValues.getAll("machine_ids")).toEqual(["1"]);
  expect(ownValues.get("person_id")).toBe("1");
  expect(ownValues.get("person_hours")).toBe("1.25");
  expect(ownValues.get("person_rate")).toBe("20");
  expect(ownValues.has("partner_rate")).toBe(false);

  await page.keyboard.press("ArrowRight");
  await expect(incoming).toBeChecked();
  await expect(incoming).toBeFocused();
  const incomingValues = await successful(form);
  expect(incomingValues.get("task_label")).toBe("Nachbars Tastaturentwurf");
  expect(incomingValues.get("hours")).toBe("3");
  expect(incomingValues.get("partner_label")).toBe("Nachbars Tastaturgespann");
  expect(incomingValues.get("partner_rate")).toBe("45");
  expect(incomingValues.get("person_name")).toBe("Franz Tastatur");
  expect(incomingValues.get("person_rate")).toBe("21");
  expect(incomingValues.get("person_hours")).toBe("1.5");
  expect(incomingValues.get("person_id")).toBe("");
  expect(submissions).toEqual([]);
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
  const read = () => page.evaluate(() => JSON.parse(localStorage.getItem("treckrr:booking-defaults:v2:22:quantity:out") || "{}"));
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
  await form.locator('[name="booking_kind"]').selectOption("quantity");
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
    await expect(form.locator('[name="mode"]')).toHaveValue("gespann");
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
