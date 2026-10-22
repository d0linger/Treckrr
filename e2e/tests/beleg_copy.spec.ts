import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

test("copied booking labels separate adjacent note, meter and field details", async ({ page }) => {
	page.on("pageerror", error => { throw error; });
	await page.addInitScript(() => {
		Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
			writeText: async (text: string) => { (window as any).__copiedBeleg = text; },
		} });
	});
	await page.route("**/*", route => {
		if (new URL(route.request().url()).pathname === "/app.js") return route.fulfill({ contentType: "application/javascript; charset=utf-8", body: readFileSync(path.resolve(__dirname, "../../internal/web/static/js/app.js"), "utf8") });
		return route.fulfill({ contentType: "text/html; charset=utf-8", body: `<html lang="de"><head><meta charset="utf-8"></head><body>
			<article id="beleg"><div class="beleg__items"><div class="beleg__lrow"><span class="beleg__d">22.10.2026</span><span class="beleg__t">Mähen<small>Notiz</small><small>Zähler 100–102 h</small><small>Feld Nord</small></span><span class="beleg__h">2</span><span class="beleg__b">92,00 €</span></div></div></article>
			<button data-beleg-copy>Text kopieren</button><script src="/app.js"></script></body></html>` });
	});
	await page.goto("http://beleg-copy.test/fixture");
	await page.getByRole("button", { name: "Text kopieren" }).click();
	await expect.poll(() => page.evaluate(() => (window as any).__copiedBeleg)).toContain("Mähen · Notiz · Zähler 100–102 h · Feld Nord");
	const text = await page.evaluate(() => (window as any).__copiedBeleg);
	expect(text).toContain("2 h · 92,00 €");
});
