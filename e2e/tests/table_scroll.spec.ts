import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

/** Reads a built static asset so the fixture runs the shipped script and styles. */
const asset = (file: string) => readFileSync(path.resolve(__dirname, "../../internal/web/static", file));
const fixture = `<!doctype html><html lang="de"><head><meta charset="utf-8">
  <meta name="viewport" content="width=device-width"><link rel="stylesheet" href="/static/css/app.css">
  <style>body{padding:16px}main{max-width:100%}.tablewrap{width:100%}.wide{min-width:800px}td{padding:16px}</style></head>
  <body><main><h1>Tabellenprüfung</h1><p id="context">Beträge bleiben unverändert.</p>
  <article id="beleg" class="beleg beleg--document">
    <div id="wide" class="tablewrap" aria-label="Rechnungspositionen" aria-describedby="context" role="region" tabindex="0">
      <table class="wide"><tbody><tr><td>Leistung</td><td>322,20 €</td><td>Bezahlt</td><td>100,00 €</td><td>Offen</td><td>222,20 €</td></tr></tbody></table>
    </div>
  </article>
  <button type="button" data-beleg-image>Bild</button>
  <div id="narrow" class="tablewrap"><table><tbody><tr><td>Passt</td><td>1,00 €</td></tr></tbody></table></div>
  <details><summary>Weitere Tabelle</summary><div id="disclosed" class="tablewrap"><table class="wide"><tbody><tr><td>Weiterer Betrag</td><td>27,20 €</td></tr></tbody></table></div></details>
  </main><script src="/static/js/app.js"></script></body></html>`;

/** Opens the offline table fixture on a phone viewport with reduced motion. */
async function openFixture(page: Page) {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.route("**/*", route => {
    const url = new URL(route.request().url());
    if (url.pathname === "/fixture") return route.fulfill({ contentType: "text/html", body: fixture });
    if (url.pathname === "/static/js/app.js") return route.fulfill({ contentType: "application/javascript", body: asset("js/app.js") });
    if (url.pathname === "/static/css/app.css") return route.fulfill({ contentType: "text/css", body: asset("css/app.css") });
    if (/^\/static\/fonts\/[a-z0-9-]+\.woff2$/.test(url.pathname)) {
      return route.fulfill({ contentType: "font/woff2", body: asset(url.pathname.slice(8)) });
    }
    return route.fulfill({ status: 404 });
  });
  await page.goto("http://table-polish.test/fixture");
}

test("overflow-only controls scroll real tables and preserve their content", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await openFixture(page);
  const table = page.locator("#wide");
  const before = await table.innerText();
  const left = page.getByRole("button", { name: "Rechnungspositionen: nach links scrollen" });
  const right = page.getByRole("button", { name: "Rechnungspositionen: nach rechts scrollen" });
  await expect(right).toHaveCount(1);
  await expect(right).toBeVisible();
  await expect(left).toBeDisabled();
  await expect(table).toHaveAttribute("aria-describedby", "context wide-hint");
  expect((await right.boundingBox())?.height).toBeGreaterThanOrEqual(44);
  await right.click();
  await expect.poll(() => table.evaluate(node => node.scrollLeft)).toBeGreaterThan(100);
  await expect(left).toBeEnabled();
  await right.click();
  await expect(right).toBeDisabled();
  await expect(left).toBeEnabled();
  expect(await table.innerText()).toBe(before);
  await table.focus();
  await page.keyboard.press("Home");
  // Native keyboard scrolling remains available on the named region.
  await table.evaluate(node => { node.scrollLeft = 0; });
  await page.keyboard.press("ArrowRight");
  await expect.poll(() => table.evaluate(node => node.scrollLeft)).toBeGreaterThan(0);
  await expect(page.locator("#narrow")).toHaveAttribute("role", "region");
  await expect(page.locator("#narrow")).not.toHaveAttribute("tabindex");
  expect(errors).toEqual([]);
});

test("overflow hints react to disclosure, resizing and print without duplicate descriptions", async ({ page }) => {
  await openFixture(page);
  await page.getByText("Weitere Tabelle", { exact: true }).click();
  await expect(page.locator(".table-scroll-tools:visible")).toHaveCount(2);
  await expect(page.locator("#disclosed")).toHaveAttribute("aria-describedby", "disclosed-hint");
  await expect(page.locator("#disclosed")).toHaveAttribute("tabindex", "0");
  const labels = await page.getByRole("button", { name: /: nach rechts scrollen/ }).evaluateAll(nodes => nodes.map(node => node.getAttribute("aria-label")));
  expect(new Set(labels).size).toBe(labels.length);
  await page.setViewportSize({ width: 1440, height: 900 });
  // The invoice card has a max-width; explicitly widen it for the no-overflow state.
  await page.locator("#beleg").evaluate(node => { node.style.maxWidth = "none"; });
  await expect(page.locator(".table-scroll-tools:visible")).toHaveCount(0);
  await expect(page.locator("#wide")).toHaveAttribute("aria-describedby", "context");
  await expect(page.locator("#disclosed")).not.toHaveAttribute("aria-describedby");
  await expect(page.locator("#disclosed")).not.toHaveAttribute("tabindex");
  await expect(page.locator("#wide")).toHaveAttribute("tabindex", "0"); // authored focusability survives
  await page.setViewportSize({ width: 375, height: 812 });
  await expect(page.locator(".table-scroll-tools:visible")).toHaveCount(2);
  await expect(page.locator("#wide")).toHaveAttribute("aria-describedby", "context wide-hint");
  await page.emulateMedia({ media: "print" });
  await expect(page.locator(".table-scroll-tools:visible")).toHaveCount(0);
  await expect(page.getByText("322,20 €", { exact: true })).toBeVisible();
});

test("image export excludes scroll controls and restores the live view", async ({ page }) => {
  await page.addInitScript(() => {
    const serialize = XMLSerializer.prototype.serializeToString;
    XMLSerializer.prototype.serializeToString = function (node) {
      const result = serialize.call(this, node);
      (window as Window & { exportedDocument?: string }).exportedDocument = result;
      return result;
    };
  });
  await openFixture(page);
  await expect(page.locator("#beleg .table-scroll-tools")).toBeVisible();
  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "Bild", exact: true }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe("beleg.png");
  expect(await download.failure()).toBeNull();
  const exported = await page.evaluate(() => (window as Window & { exportedDocument?: string }).exportedDocument);
  expect(exported).toContain("322,20 €");
  expect(exported).toContain("222,20 €");
  expect(exported).not.toContain("table-scroll-tools");
  expect(exported).not.toContain("Weitere Spalten");
  await expect(page.locator("#beleg .table-scroll-tools")).toBeVisible();
  await expect(page.locator("#wide")).toHaveText(/322,20 €.*100,00 €.*222,20 €/);
});
