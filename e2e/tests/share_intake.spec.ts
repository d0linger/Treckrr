import { test, expect, type Page } from "@playwright/test";

type SharedFile = { name: string; type: string; text?: string };
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ1sAAAAASUVORK5CYII=";
const photo = (name: string): SharedFile => ({ name, type: "image/png" });

// Exercise the real worker and its IndexedDB transaction in a secure localhost
// context. Every test gets its own browser storage; business records stay unchanged.
test.beforeEach(async ({ page }) => {
  page.on("pageerror", error => { throw error; });
  await page.goto("/login");
  await page.getByLabel("Benutzername").fill(process.env.E2E_ADMIN_USER || "admin");
  await page.locator('[name="password"]').fill(process.env.E2E_ADMIN_PASS || "e2e-admin-password-123");
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
  await page.evaluate(async () => {
    await navigator.serviceWorker.register("/sw.js");
    await navigator.serviceWorker.ready;
  });
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
});

/** Sends operating-system-style multipart shares through the active worker. */
async function share(page: Page, files: SharedFile[][]) {
  return page.evaluate(async ({ batches, png }) => {
    return Promise.all(batches.map(async batch => {
      const form = new FormData();
      for (const file of batch) {
        const bytes = Uint8Array.from(atob(png), c => c.charCodeAt(0));
        form.append("photos", new File([file.text ?? bytes], file.name, { type: file.type }));
      }
      const response = await fetch("/share-target", { method: "POST", body: form });
      return new URL(response.url).pathname + new URL(response.url).search;
    }));
  }, { batches: files, png });
}

/** Includes actual file bytes so preserving a label alone cannot pass the check. */
async function parked(page: Page) {
  return page.evaluate(async () => {
    const record = await new Promise<any>((resolve, reject) => {
      const open = indexedDB.open("treckrr-share", 1);
      open.onupgradeneeded = () => open.result.createObjectStore("pending");
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const tx = db.transaction("pending", "readonly");
        const request = tx.objectStore("pending").get("shared");
        tx.oncomplete = () => { db.close(); resolve(request.result); };
        tx.onabort = () => { db.close(); reject(tx.error); };
      };
    });
    if (!record) return null;
    return {
      id: record.id, at: record.at,
      files: await Promise.all(record.files.map(async (file: any) => ({
        name: file.name, type: file.type, bytes: Array.from(new Uint8Array(await file.blob.arrayBuffer())),
      }))),
    };
  });
}

test("a second share preserves all pending photos and shows a recovery message", async ({ page }) => {
  const imagePolicyErrors: string[] = [];
  page.on("console", message => {
    if (message.type() === "error" && message.text().includes("img-src")) imagePolicyErrors.push(message.text());
  });
  expect(await share(page, [[photo("original-a.png"), photo("original-b.png")]])).toEqual(["/share-target"]);
  const before = await parked(page);
  const [destination] = await share(page, [[photo("new.png")]]);
  expect(await parked(page)).toEqual(before);
  expect(destination).toBe("/share-target?share-error=pending");
  await page.goto(destination);
  await expect(page.locator("[data-share-count]")).toHaveText("2 Fotos bereit");
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("erneut");
  expect(await page.locator("[data-share-thumbs] img").evaluateAll(images => images.map(image => image.getAttribute("alt"))))
    .toEqual(["original-a.png", "original-b.png"]);
  await expect.poll(() => page.locator("[data-share-thumbs] img").evaluateAll(images =>
    images.length === 2 && images.every(image => (image as HTMLImageElement).complete && (image as HTMLImageElement).naturalWidth > 0),
  )).toBe(true);
  expect(imagePolicyErrors).toEqual([]);
  await test.info().attach("pending-share-warning", { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
});

test("ten photos are parked completely with a unique share identity", async ({ page }) => {
  const files = Array.from({ length: 10 }, (_, i) => photo(`photo-${i}.png`));
  expect(await share(page, [files])).toEqual(["/share-target"]);
  const record = await parked(page);
  expect(record?.id).toMatch(/^[0-9a-f-]{36}$/i);
  expect(record?.files.map(file => file.name)).toEqual(files.map(file => file.name));
  expect(record?.files.every(file => file.bytes.length > 0)).toBe(true);
  await page.goto("/share-target");
  await expect(page.locator("[data-share-count]")).toHaveText("10 Fotos bereit");
  await expect(page.locator("[data-share-thumbs] img")).toHaveCount(10);
  await expect.poll(() => page.locator("[data-share-thumbs] img").evaluateAll(images =>
    images.length === 10 && images.every(image => (image as HTMLImageElement).complete && (image as HTMLImageElement).naturalWidth > 0),
  )).toBe(true);
  await page.getByRole("button", { name: "Verwerfen", exact: true }).click();
  await expect(page).toHaveURL(/\/buchungen$/);
  expect(await parked(page)).toBeNull();
  expect(await share(page, [[photo("after-discard.png")]])).toEqual(["/share-target"]);
  expect((await parked(page))?.id).not.toBe(record?.id);
});

for (const invalid of [
  { name: "eleven photos", reason: "too-many", files: Array.from({ length: 11 }, (_, i) => photo(`photo-${i}.png`)) },
  { name: "an empty share", reason: "empty", files: [] },
  { name: "a mixed unsupported image batch", reason: "unsupported", files: [photo("keep.png"), { name: "unsupported.webp", type: "image/webp" }] },
  { name: "a mixed non-image batch", reason: "unsupported", files: [photo("keep.png"), { name: "note.txt", type: "text/plain", text: "not a photo" }] },
]) {
  test(`${invalid.name} is rejected atomically with a visible explanation`, async ({ page }) => {
    const [destination] = await share(page, [invalid.files]);
    expect(await parked(page)).toBeNull();
    expect(destination).toBe(`/share-target?share-error=${invalid.reason}`);
    await page.goto(destination);
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("alert")).not.toBeEmpty();
    await expect(page.locator("[data-share-form]")).toBeHidden();
    if (invalid.reason === "too-many") {
      await test.info().attach("oversized-share-warning", { body: await page.screenshot({ fullPage: true }), contentType: "image/png" });
    }
  });
}

test("simultaneous shares preserve one complete batch and reject the other", async ({ page }) => {
  const first = [photo("first-a.png"), photo("first-b.png")];
  const second = [photo("second-a.png"), photo("second-b.png")];
  const destinations = await share(page, [first, second]);
  expect(destinations.sort()).toEqual(["/share-target", "/share-target?share-error=pending"]);
  const names = (await parked(page))?.files.map(file => file.name);
  expect([first.map(file => file.name), second.map(file => file.name)]).toContainEqual(names);
});

test("an unreadable preview keeps the original file available for attachment", async ({ page }) => {
  expect(await share(page, [[{ name: "unreadable.png", type: "image/png", text: "original undecodable image bytes" }]]))
    .toEqual(["/share-target"]);
  const before = await parked(page);
  await page.goto("/share-target");
  await expect(page.locator("[data-share-count]")).toHaveText("1 Foto bereit");
  await expect(page.locator("[data-share-thumbs]")).toContainText("unreadable.png – Vorschau nicht verfügbar");
  await expect(page.locator("[data-share-form]")).toBeVisible();
  const formBytes = await page.locator("[data-share-files]").evaluate(async input => {
    const file = (input as HTMLInputElement).files![0];
    return Array.from(new Uint8Array(await file.arrayBuffer()));
  });
  expect(formBytes).toEqual(before?.files[0].bytes);
  expect(await parked(page)).toEqual(before);
});
