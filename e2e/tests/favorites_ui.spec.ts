import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

const favorites = readFileSync(
  path.resolve(__dirname, "../../internal/web/static/js/favorites.js"),
  "utf8",
);

/** Verifies that device-local favorites stay user-scoped and only reorder presentation. */
test("neighbor and rig favorites are remembered without changing submitted values", async ({ page }) => {
	await page.route("**/static/js/favorites-test.js*", (route) => route.fulfill({
		contentType: "application/javascript",
		body: favorites,
	}));
  await page.goto("/login");
  await page.setContent(`<!doctype html><html><head><meta name="user-id" content="7"></head><body>
    <div data-favorite-list="neighbor">
      <div data-favorite-id="1"><button type="button" data-favorite-toggle></button><a>Anna</a></div>
      <div data-favorite-id="2"><button type="button" data-favorite-toggle></button><a>Berta</a></div>
    </div>
    <select name="gespann_id" data-favorite-select="gespann">
      <option value="">— wählen —</option><option value="10">Pflug</option><option value="20">Schwader</option>
    </select>
    <button type="button" data-favorite-select-toggle="gespann"><span data-favorite-label>Merken</span></button>
  </body></html>`);
  await page.addScriptTag({ url: "/static/js/favorites-test.js" });

  await page.locator('[data-favorite-id="2"] [data-favorite-toggle]').click();
  await expect(page.locator("[data-favorite-id]").first()).toHaveAttribute("data-favorite-id", "2");

  const rig = page.locator('[name="gespann_id"]');
  await rig.selectOption("20");
  await page.locator('[data-favorite-select-toggle="gespann"]').click();
  await expect(rig).toHaveValue("20");
  await expect(rig.locator("option").nth(1)).toHaveAttribute("value", "20");

  const stored = await page.evaluate(() => ({
    user7: localStorage.getItem("treckrr:favorites:v1:7:neighbor"),
    user8: localStorage.getItem("treckrr:favorites:v1:8:neighbor"),
  }));
  expect(stored.user7).toBe('["2"]');
  expect(stored.user8).toBeNull();
});
