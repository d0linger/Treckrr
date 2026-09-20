import { test, expect, Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

// These tests use real IndexedDB and application scripts but intercept EVERY
// request. No local, development or production database can be reached.
const web = path.resolve(__dirname, "../../internal/web");
const layout = readFileSync(path.join(web, "templates/layout.html"), "utf8");
const panel = layout.slice(layout.indexOf('<div class="offlineq"'), layout.indexOf('<script src="/static/js/app.js'));
const fixture = `<!doctype html><html lang="de"><head><meta charset="utf-8">
  <meta name="user-id" content="7"><link rel="stylesheet" href="/static/css/app.css"></head><body>
  <button data-offline-badge hidden></button>
  <form method="post" action="/entries" data-entry-form>
    <input type="hidden" name="csrf_token" value="fresh-csrf">
    <input type="hidden" name="neighbor_id" value="11"><input type="hidden" name="year_id" value="22">
    <input name="entry_date" value="2026-09-13"><input name="task_label" value="Mähen">
    <input name="booking_kind" value="equipment"><input name="booking_direction" value="out">
    <input name="hours" value="2"><input name="unit" value="h">
    <input type="checkbox" name="machine_ids" value="31" checked>
    <input type="checkbox" name="machine_ids" value="32" checked>
    <input type="checkbox" name="machine_ids" value="33">
    <input name="person_row_id" value=""><input name="person_id" value="44"><input name="person_name" value="E2E Helfer"><input name="person_hours" value="1,25"><input name="person_rate" value="18"><input name="person_state" value="active">
    <input name="person_row_id" value=""><input name="person_id" value=""><input name="person_name" value="Franz"><input name="person_hours" value="0,75"><input name="person_rate" value="20"><input name="person_state" value="active">
    <input name="partner_label" value="Nachbartraktor"><input name="partner_rate" value="45">
    <input name="quantity" value="3"><input name="unit_price" value="12,50"><input name="amount" value="37,50">
    <input name="note" value="Gemeinsame Ernte">
    <button type="submit" class="btn btn--primary">Buchung speichern</button>
  </form>${panel}<script src="/static/js/app.js"></script><script src="/static/js/offline.js"></script></body></html>`;

/** Reads only this isolated browser context's synthetic offline queue. */
async function queue(page: Page): Promise<any[]> {
  return page.evaluate(async () => {
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open("treckrr-offline", 1);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      return await new Promise<any[]>((resolve, reject) => {
        const request = db.transaction("queue").objectStore("queue").getAll();
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
    } finally { db.close(); }
  });
}

/** Installs a deny-by-default HTTP fixture with production queue scripts. */
test.beforeEach(async ({ page }) => {
  page.on("pageerror", error => { throw error; });
  await page.route("**/*", async route => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/fixture") return route.fulfill({ contentType: "text/html", body: fixture });
    if (["/static/js/app.js", "/static/js/offline.js", "/static/css/app.css"].includes(pathname)) {
      return route.fulfill({ contentType: pathname.endsWith(".css") ? "text/css" : "application/javascript",
        body: readFileSync(path.join(web, pathname), "utf8") });
    }
    return route.fulfill({ status: 404 });
  });
  await page.goto("http://unified-offline.test/fixture");
});

for (const kind of ["equipment", "labor", "quantity", "fixed"]) {
  for (const direction of ["out", "in"]) {
    /** Preserves the complete selected service, direction and independent hours during replay. */
    test(`${kind} ${direction}: offline replay retains every field and both selected machines`, async ({ page, context }) => {
      const submissions: URLSearchParams[] = [];
      await page.route("**/entries", async route => {
        expect(route.request().headers()["x-offline-replay"]).toBe("1");
        submissions.push(new URLSearchParams(route.request().postData()!));
        return route.fulfill({ status: 204 });
      });
      await page.locator('[name="booking_kind"]').fill(kind);
      await page.locator('[name="booking_direction"]').fill(direction);
      await context.setOffline(true);
      await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
      await expect.poll(async () => (await queue(page)).length).toBe(1);
      const saved = (await queue(page))[0];
      const original = new URLSearchParams(saved.data.__pairs);
      expect(original.getAll("machine_ids")).toEqual(["31", "32"]);
      expect(original.has("csrf_token")).toBe(false);
      expect(original.getAll("person_id")).toEqual(["44", ""]);
      expect(original.getAll("person_name")).toEqual(["E2E Helfer", "Franz"]);
      expect(original.getAll("person_hours")).toEqual(["1,25", "0,75"]);
      expect(original.getAll("person_rate")).toEqual(["18", "20"]);
      expect(original.getAll("person_state")).toEqual(["active", "active"]);
      expect(original.get("partner_label")).toBe("Nachbartraktor");
      expect(original.get("booking_kind")).toBe(kind);
      expect(original.get("booking_direction")).toBe(direction);
      await page.locator("[data-offline-badge]").click();
      await expect(page.locator("[data-offline-list]")).not.toContainText("Schnellerfassung");
      await expect(page.locator("[data-offline-list]")).toContainText(direction === "in" ? "Gegenleistung · Ich schulde" : "Eigene Leistung · Nachbar schuldet");
      await context.setOffline(false);
      await expect.poll(async () => (await queue(page)).length).toBe(0);
      expect(submissions).toHaveLength(1);
      expect(submissions[0].get("csrf_token")).toBe("fresh-csrf");
      submissions[0].delete("csrf_token");
      expect([...submissions[0]]).toEqual([...original]);
    });
  }
}

/** Keeps repeated controls, scope and replay identity intact through an explicit correction. */
test("a rejected incoming service can correct independent hours without changing its two machines or key", async ({ page, context }) => {
  const submissions: URLSearchParams[] = [];
  await page.route("**/entries", route => {
    const data = new URLSearchParams(route.request().postData()!);
    submissions.push(data);
    return route.fulfill({ status: data.getAll("person_hours")[1] === "1,5" ? 204 : 422, body: "Helferstunden prüfen." });
  });
  await page.locator('[name="booking_direction"]').fill("in");
  await context.setOffline(true);
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  const original = (await queue(page))[0];
  await context.setOffline(false);
  await expect.poll(async () => (await queue(page))[0]?.rejection?.status).toBe(422);
  await page.locator("[data-offline-badge]").click();
  await page.getByText("Buchungsdaten korrigieren", { exact: true }).click();
  await page.getByLabel("Helferstunden (leer = wie Maschinenstunden)", { exact: true }).nth(1).fill("1,5");
  await page.getByRole("button", { name: "Korrektur speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page))[0]?.originalData).toEqual(original.data);
  const corrected = new URLSearchParams((await queue(page))[0].data.__pairs);
  expect(corrected.getAll("machine_ids")).toEqual(["31", "32"]);
  expect(corrected.get("booking_direction")).toBe("in");
  expect(corrected.get("idempotency_key")).toBe(new URLSearchParams(original.data.__pairs).get("idempotency_key"));
  expect(corrected.get("neighbor_id")).toBe("11");
  expect(corrected.get("year_id")).toBe("22");
  await page.locator("[data-offline-flush]").click();
  await expect.poll(async () => (await queue(page)).length).toBe(0);
  expect(submissions).toHaveLength(2);
  expect(submissions[1].getAll("person_hours")).toEqual(["1,25", "1,5"]);
  expect(submissions[1].getAll("person_id")).toEqual(["44", ""]);
  expect(submissions[1].getAll("person_name")).toEqual(["E2E Helfer", "Franz"]);
  expect(submissions[1].getAll("machine_ids")).toEqual(["31", "32"]);
});

/** Replays pre-upgrade object records without migrating or losing their identity. */
test("legacy object-shaped bookings still display and replay unchanged", async ({ page, context }) => {
  const submissions: URLSearchParams[] = [];
  await page.route("**/entries", route => {
    submissions.push(new URLSearchParams(route.request().postData()!));
    return route.fulfill({ status: 204 });
  });
  await context.setOffline(true);
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  const current = (await queue(page))[0];
  const legacy = { ...current, data: { idempotency_key: "legacy-key", neighbor_id: "11", year_id: "22",
    task_label: "Alte Buchung", hours: "2", unit: "h", person_id: "44", entry_date: "2026-09-12" } };
  delete legacy.path;
  await page.evaluate(async item => {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("treckrr-offline", 1);
      request.onsuccess = () => {
        const db = request.result, tx = db.transaction("queue", "readwrite");
        tx.objectStore("queue").put(item);
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onerror = () => { db.close(); reject(tx.error); };
      };
      request.onerror = () => reject(request.error);
    });
  }, legacy);
  await page.locator("[data-offline-badge]").click();
  await expect(page.locator("[data-offline-list]")).toContainText("Alte Buchung · 2 h");
  await context.setOffline(false);
  await expect.poll(async () => (await queue(page)).length).toBe(0);
  expect(submissions).toHaveLength(1);
  expect(submissions[0].get("idempotency_key")).toBe("legacy-key");
  expect(submissions[0].get("person_id")).toBe("44");
  submissions[0].delete("csrf_token");
  expect(Object.fromEntries(submissions[0])).toEqual(legacy.data);
});
