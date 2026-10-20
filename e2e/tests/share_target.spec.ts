import { test, expect, Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

const appScript = path.resolve(__dirname, "../../internal/web/static/js/app.js");
// Mirrors share_target.html for a writer with one open booking.
const fixture = `<!doctype html><html lang="de"><head><meta charset="utf-8"></head><body>
  <section data-share-target>
    <div data-share-preview hidden><strong data-share-count></strong><div data-share-thumbs></div></div>
    <p data-share-empty>Keine geteilten Fotos</p>
    <form method="post" enctype="multipart/form-data" data-share-form hidden>
      <input type="hidden" name="csrf_token" value="tok">
      <input type="file" name="photo" multiple hidden data-share-files>
      <label><input type="radio" name="target" value="/entries/7/photos" required> Buchung 7</label>
      <button type="submit">Fotos anhängen</button>
      <button type="button" data-share-discard>Verwerfen</button>
    </form>
  </section>
  <script src="/static/js/app.js"></script>
  </body></html>`;

// A 1×1 PNG stands in for a shared photo; no real image or server is involved.
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ1sAAAAASUVORK5CYII=";

async function openFixture(page: Page) {
  page.on("pageerror", error => { throw error; });
  await page.route("**/*", async route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/fixture") return route.fulfill({ contentType: "text/html", body: fixture });
    if (url.pathname === "/static/js/app.js") {
      return route.fulfill({ contentType: "application/javascript", body: readFileSync(appScript, "utf8") });
    }
    return route.fulfill({ status: 404 });
  });
}

// Writes the parked share exactly as sw.js does.
async function park(page: Page, count: number) {
  await page.goto("http://share-target.test/fixture");
  await page.evaluate(async ({ png, count }) => {
    const bytes = Uint8Array.from(atob(png), c => c.charCodeAt(0));
    const files = Array.from({ length: count }, (_, i) => ({ name: `beleg-${i + 1}.png`, type: "image/png", blob: new Blob([bytes], { type: "image/png" }) }));
    await new Promise<void>((resolve, reject) => {
      const open = indexedDB.open("treckrr-share", 1);
      open.onupgradeneeded = () => open.result.createObjectStore("pending");
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const tx = open.result.transaction("pending", "readwrite");
        tx.objectStore("pending").put({ files, at: Date.now() }, "shared");
        tx.oncomplete = () => { open.result.close(); resolve(); };
      };
    });
  }, { png, count });
  await page.reload();
}

test("parked photos are attached and uploaded to the chosen booking once", async ({ page }) => {
  await openFixture(page);
  await park(page, 2);
  await expect(page.locator("[data-share-count]")).toHaveText("2 Fotos bereit");
  await expect(page.locator("[data-share-thumbs] img")).toHaveCount(2);

  let body = "";
  let target = "";
  await page.route("**/entries/7/photos", async route => {
    target = new URL(route.request().url()).pathname;
    body = route.request().postDataBuffer()?.toString("latin1") || "";
    return route.fulfill({ contentType: "text/html", body: "ok" });
  });
  await page.getByLabel("Buchung 7").check();
  await page.getByRole("button", { name: "Fotos anhängen" }).click();
  await expect.poll(() => target).toBe("/entries/7/photos");
  expect(body).toContain('name="photo"; filename="beleg-1.png"');
  expect(body).toContain('name="photo"; filename="beleg-2.png"');
  expect(body).toContain('name="csrf_token"');

  await page.goto("http://share-target.test/fixture");
  await expect(page.locator("[data-share-empty]")).toBeVisible();
  await expect(page.locator("[data-share-form]")).toBeHidden();
});
