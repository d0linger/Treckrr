import { test, expect, Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

const appScript = path.resolve(__dirname, "../../internal/web/static/js/app.js");
// Mirrors the helper markup in booking_form.html around the real hours field.
const fixture = `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="user-id" content="7"></head><body>
  <form method="post" action="/entries">
    <label>Stunden <input type="number" name="hours" min="0.0001" step="0.0001" data-hours></label>
    <details class="disclosure form-details time-helper" data-time-helper>
      <summary>Zeit erfassen</summary>
      <label>Von <input type="time" data-time-from></label>
      <label>Bis <input type="time" data-time-to></label>
      <label>Pause (Min.) <input type="number" min="0" max="1440" step="1" data-time-break></label>
      <button type="button" data-stopwatch-toggle aria-pressed="false">Stoppuhr starten</button>
      <span data-stopwatch-display aria-hidden="true">0:00:00</span>
      <button type="button" data-stopwatch-reset hidden>Verwerfen</button>
      <p data-time-status role="status"></p>
    </details>
    <button type="submit">Buchung speichern</button>
  </form>
  <script src="/static/js/app.js"></script>
  </body></html>`;

// Deny-by-default fixture: production app.js against static markup, no server.
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
  await page.goto("http://time-helper.test/fixture");
}

test("Von – Bis minus Pause fills the hours field and nothing else is submitted", async ({ page }) => {
  await openFixture(page);
  await page.getByText("Zeit erfassen").click();
  await page.getByLabel("Von").fill("07:30");
  await page.getByLabel("Bis").fill("10:15");
  await page.getByLabel("Pause (Min.)").fill("15");
  await page.getByLabel("Pause (Min.)").blur();
  await expect(page.getByLabel("Stunden")).toHaveValue("2.50");
  await expect(page.locator("[data-time-status]")).toHaveText("2 h 30 min → 2,50 h übernommen.");

  // A shift past midnight counts forward instead of going negative.
  await page.getByLabel("Von").fill("22:00");
  await page.getByLabel("Bis").fill("01:20");
  await page.getByLabel("Pause (Min.)").fill("");
  await page.getByLabel("Bis").blur();
  await expect(page.getByLabel("Stunden")).toHaveValue("3.33");

  const posted: URLSearchParams[] = [];
  await page.route("**/entries", async route => {
    posted.push(new URLSearchParams(route.request().postData() || ""));
    return route.fulfill({ contentType: "text/html", body: "ok" });
  });
  await page.getByRole("button", { name: "Buchung speichern" }).click();
  await expect.poll(() => posted.length).toBe(1);
  expect([...posted[0].keys()]).toEqual(["hours"]);
});

test("the stopwatch survives a reload and fills whole minutes on stop", async ({ page }) => {
  await page.clock.install({ time: new Date("2026-10-16T08:00:00") });
  await openFixture(page);
  await page.getByText("Zeit erfassen").click();
  await page.getByRole("button", { name: "Stoppuhr starten" }).click();
  await expect(page.getByRole("button", { name: "Verwerfen" })).toBeVisible();

  await page.clock.fastForward("01:30:00");
  await page.reload();
  // Still running after the reload: the helper opens itself and keeps counting.
  const stop = page.getByRole("button", { name: "Stoppuhr anhalten und übernehmen" });
  await expect(stop).toBeVisible();
  await expect(page.locator("[data-stopwatch-display]")).toHaveText(/^1:30:/);
  await stop.click();
  await expect(page.getByLabel("Stunden")).toHaveValue("1.50");
  await expect(page.getByLabel("Von")).toHaveValue("08:00");
  await expect(page.getByLabel("Bis")).toHaveValue("09:30");
  await expect(page.getByRole("button", { name: "Stoppuhr starten" })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("treckrr-stopwatch-7"))).toBeNull();
});

test("stopping or clearing the stopwatch in another tab clears the initiating tab", async ({ page, context }) => {
	await openFixture(page);
	await page.getByText("Zeit erfassen").click();
	await page.locator("[data-stopwatch-toggle]").click();
	const other = await context.newPage();
	await openFixture(other);
	await expect(other.locator("[data-stopwatch-toggle]")).toHaveAttribute("aria-pressed", "true");
	await other.locator("[data-stopwatch-toggle]").click();
	await expect(page.locator("[data-stopwatch-toggle]")).toHaveAttribute("aria-pressed", "false");
	await expect(page.locator("[data-hours]")).toHaveValue("");
	await page.locator("[data-stopwatch-toggle]").click();
	await expect(other.locator("[data-stopwatch-toggle]")).toHaveAttribute("aria-pressed", "true");
	await other.evaluate(() => localStorage.clear());
	await expect(page.locator("[data-stopwatch-toggle]")).toHaveAttribute("aria-pressed", "false");
});
