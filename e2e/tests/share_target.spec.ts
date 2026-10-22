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
	return route.fulfill({ contentType: "application/json", body: JSON.stringify({ uploaded: true }) });
  });
  await page.getByLabel("Buchung 7").check();
  await page.getByRole("button", { name: "Fotos anhängen" }).click();
  await expect.poll(() => target).toBe("/entries/7/photos");
  expect(body).toContain('name="photo"; filename="beleg-1.png"');
  expect(body).toContain('name="photo"; filename="beleg-2.png"');
  expect(body).toContain('name="csrf_token"');

  await expect(page).toHaveURL("http://share-target.test/entries/7/edit");
  await page.goto("http://share-target.test/fixture");
  await expect(page.locator("[data-share-empty]")).toBeVisible();
  await expect(page.locator("[data-share-form]")).toBeHidden();
});

for (const failure of ["network", "rejected", "partial", "login"]) {
	test(`parked photos survive ${failure} upload failures and can be retried`, async ({ page }) => {
		await openFixture(page);
		await park(page, 2);
		await page.route("**/entries/7/photos", route => {
			if (failure === "network") return route.abort();
			if (failure === "login") return route.fulfill({ contentType: "text/html", body: "Anmelden" });
			return route.fulfill({ status: 422, contentType: "application/json", body: JSON.stringify({ uploaded: false }) });
		});
		await page.getByLabel("Buchung 7").check();
		await page.getByRole("button", { name: "Fotos anhängen" }).click();
		await expect(page.getByRole("status")).toContainText("Fotos bleiben auf diesem Gerät erhalten");
		await page.reload();
		await expect(page.locator("[data-share-count]")).toHaveText("2 Fotos bereit");
		await page.route("**/entries/7/photos", route => route.fulfill({ contentType: "application/json", body: JSON.stringify({ uploaded: true }) }));
		await page.getByLabel("Buchung 7").check();
		await page.getByRole("button", { name: "Fotos anhängen" }).click();
		await expect(page).toHaveURL("http://share-target.test/entries/7/edit");
		await page.goto("http://share-target.test/fixture");
		await expect(page.locator("[data-share-empty]")).toBeVisible();
	});
}

for (const replacement of [
  { name: "another booking", value: "/entries/8/photos" },
  { name: "a script URL", value: "javascript:void(window.__shareTargetScriptRan=true)" },
  { name: "an external URL", value: "https://attacker.invalid/entries/7/photos" },
]) {
  test(`upload stays bound to its original booking when the target changes to ${replacement.name}`, async ({ page }) => {
    await openFixture(page);
    await park(page, 1);
    await page.route("**/entries/7/photos", async route => {
      await page.locator('input[name="target"]').evaluate((input, value) => {
        (input as HTMLInputElement).value = value;
      }, replacement.value);
      return route.fulfill({ contentType: "application/json", body: JSON.stringify({ uploaded: true }) });
    });
    await page.getByLabel("Buchung 7").check();
    await page.getByRole("button", { name: "Fotos anhängen" }).click();
    await expect(page).toHaveURL("http://share-target.test/entries/7/edit");
    await page.goto("http://share-target.test/fixture");
    await expect(page.locator("[data-share-empty]")).toBeVisible();
  });
}

for (const invalid of [
  { name: "script URLs", value: "javascript:void(0)" },
  { name: "data URLs", value: "data:text/html,test" },
  { name: "external origins", value: "https://attacker.invalid/entries/7/photos" },
  { name: "protocol-relative URLs", value: "//attacker.invalid/entries/7/photos" },
  { name: "zero IDs", value: "/entries/0/photos" },
  { name: "negative IDs", value: "/entries/-1/photos" },
  { name: "query strings", value: "/entries/7/photos?next=/other" },
  { name: "trailing newlines", value: "/entries/7/photos\n" },
  { name: "path traversal", value: "/entries/7/photos/../photos" },
  { name: "encoded path separators", value: "/entries/7%2fedit/photos" },
]) {
  test(`invalid share targets reject ${invalid.name} without uploading or losing parked photos`, async ({ page }) => {
    await openFixture(page);
    await park(page, 1);
    const posts: string[] = [];
    page.on("request", request => { if (request.method() === "POST") posts.push(request.url()); });
    await page.locator('input[name="target"]').evaluate((input, value) => {
      (input as HTMLInputElement).value = value;
    }, invalid.value);
    await page.getByLabel("Buchung 7").check();
    await page.getByRole("button", { name: "Fotos anhängen" }).click();
    await expect(page.getByRole("status")).toBeEmpty();
    await expect(page).toHaveURL("http://share-target.test/fixture");
    await page.reload();
    await expect(page.locator("[data-share-count]")).toHaveText("1 Foto bereit");
    expect(posts).toEqual([]);
  });
}
