import { test, expect, Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

// Real application scripts and IndexedDB, with HTTP isolated from the app DB.
// In particular, keep all three submit handlers in their production order.
const web = path.resolve(__dirname, "../../internal/web");
const layout = readFileSync(path.join(web, "templates/layout.html"), "utf8");
const panel = layout.slice(layout.indexOf('<div class="offlineq"'), layout.indexOf('<script src="/static/js/app.js'));
const fixture = `<!doctype html><html lang="de"><head>
  <meta charset="utf-8"><meta name="user-id" content="7">
  <link rel="stylesheet" href="/static/css/app.css">
  </head><body><button data-offline-badge hidden></button>
  <form method="post" action="/entries" data-entry-form data-pricing-url="/api/pricing">
    <input name="csrf_token" value="fresh-csrf" type="hidden">
    <input name="neighbor_id" value="11" type="hidden"><input name="year_id" value="22" type="hidden">
    <input name="entry_date" value="2026-09-10"><input name="task_label" value="Mähen">
    <input name="hours" data-hours value="2"><input name="unit" data-unit value="h">
    <select name="gespann_id" data-gespann-select><option value="33">Mähwerk</option></select>
    <select name="person_id"><option value="44">Helfer</option></select>
    <span data-rate></span><span data-cost></span>
    <button type="submit" class="btn btn--primary">Buchung speichern</button>
  </form>
  <form method="post" action="/entries/quick" data-quick-form>
    <input name="csrf_token" value="fresh-csrf" type="hidden">
    <input name="neighbor_id" value="11" type="hidden"><input name="year_id" value="22" type="hidden">
    ${[1, 2].map(i => `<input name="q_date" value="2026-09-0${i}">
      <input name="q_gespann" value="33"><input name="q_hours" value="${i}">
      <input name="q_person" value="${i === 1 ? "44" : "55"}">
      <input name="q_key" data-quick-key type="hidden">`).join("")}
    <button type="submit">Schnell speichern</button>
  </form>${panel}
  <script src="/static/js/app.js"></script><script src="/static/js/offline.js"></script>
  <script src="/static/js/entry-form.js"></script></body></html>`;

async function queue(page: Page) {
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

/** Reads a successful control from both historical objects and lossless pairs. */
function queuedValue(item: any, name: string): string | undefined {
  return item?.data.__pairs ? item.data.__pairs.find(([key]: string[]) => key === name)?.[1] : item?.data[name];
}

test.beforeEach(async ({ page }) => {
  page.on("pageerror", error => { throw error; });
  await page.route("**/*", async route => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/fixture") return route.fulfill({ contentType: "text/html", body: fixture });
    if (["/static/js/app.js", "/static/js/offline.js", "/static/js/entry-form.js", "/static/css/app.css"].includes(pathname)) {
      return route.fulfill({
        contentType: pathname.endsWith(".css") ? "text/css" : "application/javascript",
        body: readFileSync(path.join(web, pathname), "utf8"),
      });
    }
    if (pathname === "/api/pricing" || pathname === "/api/entries/precheck") {
      return route.fulfill({ json: {} });
    }
    return route.fulfill({ status: 404 });
  });
  await page.goto("http://offline.test/fixture");
});

test("rejected quick batch survives reload, can be exported and retries with the original row keys", async ({ page, context }) => {
  let status = 422;
  const submissions: URLSearchParams[] = [];
  const reason = "1 Buchung(en) gespeichert, 1 Zeile(n) ungültig: Person braucht einen Stundensatz. <b>kein HTML</b>";
  await page.route("**/entries/quick", async route => {
    submissions.push(new URLSearchParams(route.request().postData()!));
    await route.fulfill({ status, contentType: "text/plain", body: status === 422 ? reason : "" });
  });
  await context.setOffline(true);
  await page.getByRole("button", { name: "Schnell speichern" }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  const original = (await queue(page))[0];
  expect(original.data.__pairs.filter(([key]: string[]) => key === "q_key")).toHaveLength(2);
  expect(original.data.__pairs.some(([key]: string[]) => key === "csrf_token")).toBe(false);
  await context.setOffline(false);
  await expect.poll(() => submissions.length).toBe(1);
  await expect.poll(async () => (await queue(page))[0]?.rejection?.status).toBe(422);
  expect((await queue(page))[0].data).toEqual(original.data);

  await page.reload();
  await page.locator("[data-offline-badge]").click();
  await expect(page.locator("[data-offline-list]")).toContainText(reason);
  expect(submissions).toHaveLength(1); // A rejected batch needs a deliberate retry.
  await expect(page.locator("[data-offline-list] b")).toHaveCount(0);
  await page.setViewportSize({ width: 375, height: 812 });
  await page.getByText("Erfasste Daten anzeigen", { exact: true }).click();
  await expect(page.locator(".offlineq__data")).toContainText("q_hours: 2");
  expect(await page.locator(".offlineq__sheet").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("offline-recovery-mobile.png") });
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Daten sichern" }).click();
  const download = await downloadPromise;
  const backup = JSON.parse(readFileSync((await download.path())!, "utf8"));
  expect(backup.data).toEqual(original.data);
  expect(backup.rejection.message).toBe(reason);
  expect((await queue(page)).length).toBe(1); // Export does not discard.

  status = 204; // Master data corrected; an already-saved row must not duplicate.
  await page.locator("[data-offline-flush]").click();
  await expect.poll(async () => (await queue(page)).length).toBe(0);
  expect(submissions).toHaveLength(2);
  expect([...submissions[1]]).toEqual([...submissions[0]]);
  expect(submissions[1].get("csrf_token")).toBe("fresh-csrf");
  await expect(page.locator("[data-offline-list]")).toContainText("Nichts in der Warteschlange");
});

test("400 keeps a single booking until the user explicitly discards it", async ({ page, context }) => {
  await page.route("**/entries", route => route.fulfill({ status: 400, contentType: "text/plain", body: "Jahr gesperrt." }));
  await context.setOffline(true);
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  await context.setOffline(false);
  await expect.poll(async () => (await queue(page))[0]?.rejection?.status).toBe(400);
  await page.locator("[data-offline-badge]").click();
  await expect(page.locator("[data-offline-list]")).toContainText("Jahr gesperrt.");
  page.once("dialog", dialog => dialog.dismiss());
  await page.getByRole("button", { name: "Verwerfen", exact: true }).click();
  expect((await queue(page)).length).toBe(1);
  page.once("dialog", dialog => dialog.accept());
  await page.getByRole("button", { name: "Verwerfen", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(0);
});

test("a rejected single booking can be corrected without losing its original data or key", async ({ page, context }) => {
  const submissions: URLSearchParams[] = [];
  await page.route("**/entries", route => {
    const data = new URLSearchParams(route.request().postData()!);
    submissions.push(data);
    return route.fulfill({ status: data.get("person_id") === "55" ? 204 : 422, body: "" });
  });
  await context.setOffline(true);
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  const original = (await queue(page))[0];
  await context.setOffline(false);
  await expect.poll(async () => (await queue(page))[0]?.rejection?.status).toBe(422);
  await page.locator("[data-offline-badge]").click();
  await page.getByText("Buchungsdaten korrigieren", { exact: true }).click();
  await page.getByLabel("Person-ID (leer = ohne Helfer)", { exact: true }).fill("55");
  await page.getByRole("button", { name: "Korrektur speichern", exact: true }).click();
  await expect.poll(async () => queuedValue((await queue(page))[0], "person_id")).toBe("55");
  const corrected = (await queue(page))[0];
  expect(corrected.originalData).toEqual(original.data);
  expect(queuedValue(corrected, "idempotency_key")).toBe(queuedValue(original, "idempotency_key"));
  expect(queuedValue(corrected, "neighbor_id")).toBe(queuedValue(original, "neighbor_id"));
  expect(queuedValue(corrected, "year_id")).toBe(queuedValue(original, "year_id"));
  await page.reload();
  expect(submissions).toHaveLength(1); // Saving/reloading never silently retries an edit.
  await page.locator("[data-offline-badge]").click();
  await page.locator("[data-offline-flush]").click();
  await expect.poll(async () => (await queue(page)).length).toBe(0);
  expect(submissions[1].get("person_id")).toBe("55");
  expect(submissions[1].get("idempotency_key")).toBe(submissions[0].get("idempotency_key"));
});

test("correcting a partially saved quick batch retains every key and deduplicates accepted rows", async ({ page, context }) => {
  const accepted = new Map<string, string>();
  const submissions: URLSearchParams[] = [];
  await page.route("**/entries/quick", route => {
    const data = new URLSearchParams(route.request().postData()!);
    submissions.push(data);
    const keys = data.getAll("q_key"), persons = data.getAll("q_person");
    keys.forEach((key, i) => { if (persons[i] !== "55" && !accepted.has(key)) accepted.set(key, persons[i]); });
    return route.fulfill({ status: persons.includes("55") ? 422 : 204, body: "" });
  });
  await context.setOffline(true);
  await page.getByRole("button", { name: "Schnell speichern" }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  const original = (await queue(page))[0];
  await context.setOffline(false);
  await expect.poll(async () => (await queue(page))[0]?.rejection?.status).toBe(422);
  expect(accepted.size).toBe(1);
  await page.locator("[data-offline-badge]").click();
  await page.getByText("Buchungsdaten korrigieren", { exact: true }).click();
  await page.getByLabel("Zeile 2 · Person-ID (leer = ohne Helfer)", { exact: true }).fill("66");
  await page.setViewportSize({ width: 375, height: 812 });
  expect(await page.locator(".offlineq__sheet").evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("offline-correction-mobile.png") });
  await page.getByRole("button", { name: "Korrektur speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page))[0]?.originalData).toEqual(original.data);
  const corrected = (await queue(page))[0];
  expect(corrected.data.__pairs.filter(([key]: string[]) => key === "q_key"))
    .toEqual(original.data.__pairs.filter(([key]: string[]) => key === "q_key"));
  await page.locator("[data-offline-flush]").click();
  await expect.poll(async () => (await queue(page)).length).toBe(0);
  expect(submissions).toHaveLength(2);
  expect(accepted.size).toBe(2);
  expect([...accepted.values()]).toEqual(["44", "66"]);
});

test("offline capture coordinates all submit handlers and releases the save button", async ({ page, context }) => {
  let prechecks = 0;
  await page.route("**/api/entries/precheck?*", route => { prechecks++; return route.fulfill({ json: {} }); });
  await context.setOffline(true);
  const save = page.getByRole("button", { name: "Buchung speichern", exact: true });
  await save.click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  await expect(save).toBeEnabled();
  await save.click();
  await expect.poll(async () => (await queue(page)).length).toBe(2);
  await expect(save).toBeEnabled();
  const items = await queue(page);
  expect(new Set(items.map(item => queuedValue(item, "idempotency_key"))).size).toBe(2);
  expect(items.every(item => item.user === "7" && queuedValue(item, "person_id") === "44")).toBe(true);
  expect(prechecks).toBe(0);
});

test("online save still reaches exactly one POST after the precheck", async ({ page }) => {
  let prechecks = 0, posts = 0;
  await page.route("**/api/entries/precheck?*", route => { prechecks++; return route.fulfill({ json: {} }); });
  await page.route("**/entries", route => {
    posts++;
    expect(route.request().method()).toBe("POST");
    return route.fulfill({ contentType: "text/html", body: "Buchung gespeichert." });
  });
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect(page.locator("body")).toHaveText("Buchung gespeichert.");
  expect(prechecks).toBe(1);
  expect(posts).toBe(1);
});

test("a late rejection does not resurrect a booking explicitly discarded during sending", async ({ page, context }) => {
  let reply!: () => void;
  const release = new Promise<void>(resolve => { reply = resolve; });
  let started = false;
  await page.route("**/entries", async route => {
    started = true;
    await release;
    await route.fulfill({ status: 422, body: "Person fehlt." });
  });
  await context.setOffline(true);
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  await page.locator("[data-offline-badge]").click();
  await context.setOffline(false);
  await expect.poll(() => started).toBe(true);
  page.once("dialog", dialog => dialog.accept());
  await page.getByRole("button", { name: "Verwerfen", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(0);
  const response = page.waitForResponse("**/entries");
  reply();
  await response;
  // Manual flush joins the in-flight promise, then redraws the emptied panel.
  await page.locator("[data-offline-flush]").click();
  await expect(page.locator("[data-offline-list]")).toContainText("Nichts in der Warteschlange");
  expect(await queue(page)).toEqual([]);
});

test("other owners and unstamped legacy items remain quarantined", async ({ page, context }) => {
  let posts = 0;
  await page.route("**/entries", route => { posts++; return route.fulfill({ status: 204 }); });
  await context.setOffline(true);
  await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
  await expect.poll(async () => (await queue(page)).length).toBe(1);
  const item = (await queue(page))[0];
  await page.evaluate(async item => {
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("treckrr-offline", 1);
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction("queue", "readwrite");
        tx.objectStore("queue").put({ ...item, id: "foreign", user: "8", rejection: { status: 422, message: "Private Fremdbuchung" } });
        tx.objectStore("queue").put({ ...item, id: "legacy", user: undefined });
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onerror = () => { db.close(); reject(tx.error); };
      };
      request.onerror = () => reject(request.error);
    });
  }, item);
  await page.locator("[data-offline-badge]").click();
  await expect(page.locator(".offlineq__row")).toHaveCount(1);
  await expect(page.locator("[data-offline-list]")).not.toContainText("Private Fremdbuchung");
  await context.setOffline(false);
  await expect.poll(() => posts).toBe(1);
  await expect.poll(async () => (await queue(page)).length).toBe(2);
  await page.locator("[data-offline-flush]").click();
  await expect(page.locator("[data-offline-list]")).toContainText("Nichts in der Warteschlange");
  expect(posts).toBe(1);
  expect((await queue(page)).map(item => item.id).sort()).toEqual(["foreign", "legacy"]);
});

for (const status of [401, 403, 500]) {
  test(`transient ${status} stays queued and retries automatically`, async ({ page, context }) => {
    let responseStatus = status;
    let posts = 0;
    await page.route("**/entries", route => {
      posts++;
      return route.fulfill({ status: responseStatus });
    });
    await context.setOffline(true);
    await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
    await expect.poll(async () => (await queue(page)).length).toBe(1);
    await context.setOffline(false);
    await expect.poll(() => posts).toBe(1);
    expect((await queue(page))[0].rejection).toBeUndefined();
    responseStatus = 204;
    await page.reload();
    await expect.poll(async () => (await queue(page)).length).toBe(0);
    expect(posts).toBe(2);
  });
}
