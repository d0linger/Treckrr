import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

const css = readFileSync(path.resolve(__dirname, "../../internal/web/static/css/app.css"), "utf8");
// The warning belongs to the authenticated sticky header stack; anonymous
// pages have the same warning on its own. It must work without JavaScript.
for (const signedIn of [false, true]) {
	test(`demo warning stays visible while scrolling (${signedIn ? "account" : "public"})`, async ({ page }) => {
		const banner = `<div class="demo-banner" role="note" data-demo-banner><strong>Übungsinstanz</strong><span>Alle Daten sind Beispieldaten. E-Mail-Versand und externe Backups sind abgeschaltet.</span></div>`;
		await page.setContent(`<html lang="de"><head><meta name="viewport" content="width=device-width"><style>${css}</style></head><body>
			${signedIn ? `<div class="topstack">${banner}<header class="appbar"><h1 class="appbar__title">Testkonto</h1></header></div>` : banner}
			<main style="min-height:3000px">Synthetischer Inhalt</main></body></html>`);
		for (const width of [1280, 390, 320]) {
			await page.setViewportSize({ width, height: 844 });
			await page.evaluate(() => window.scrollTo(0, 1800));
			const warning = page.locator("[data-demo-banner]");
			await expect(warning).toBeInViewport({ ratio: 1 });
			expect((await warning.boundingBox())!.y).toBeGreaterThanOrEqual(0);
			if (signedIn) expect((await page.locator(".appbar").boundingBox())!.y).toBeGreaterThanOrEqual((await warning.boundingBox())!.height);
		}
	});
}
