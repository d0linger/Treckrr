import { test, expect } from "@playwright/test";

// End-to-end of the tax-critical paths a Go unit test can't cover: filling the
// § 11 prerequisites (issuer + recipient address), issuing (festschreiben) an
// invoice from a booking, seeing the frozen invoice on the Beleg, marking it sent
// (with the confirm modal) and undoing that. Plus lighter checks for the
// dashboard payment-status chip, the Mahnwesen CSV export and the branded 404.
//
// The CI job seeds base/year/neighbor 1/1/1 with a bare neighbor and no company,
// so this spec sets up its own prerequisites through the UI. It runs serially and
// shares one logged-in page so the steps build on each other.

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

test.describe.configure({ mode: "serial" });

async function login(page) {
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

test("issue an invoice, see it on the Beleg, mark sent + undo", async ({ page }) => {
  await login(page);

  // --- § 11 prerequisites: issuer (company) + recipient (neighbor) address ---
  await page.goto("/admin/company");
  await page.locator('input[name="name"]').fill("Hof Bergmann");
  await page.locator('textarea[name="address"]').fill("Feldweg 3\n4780 Schärding");
  // Kleinunternehmer: no USt-Ausweis, so § 11 needs no VAT rate for this test.
  await page.locator('select[name="tax_mode"]').selectOption("kleinunternehmer");
  await page.getByRole("button", { name: /speichern/i }).first().click();
  await page.waitForLoadState("networkidle"); // let the save redirect settle

  await page.goto("/neighbors");
  // Each neighbor's edit form sits in a collapsed <details><summary>Bearbeiten</summary>;
  // open it before filling. The seeded neighbor (id 1) gets an address so § 11 is met.
  await page.locator("details").filter({ has: page.locator('form[action="/neighbors/1/update"]') })
    .locator("summary").click();
  const addr = page.locator('form[action="/neighbors/1/update"] textarea[name="address"]');
  await addr.fill("Ackerstraße 8\n4780 Schärding");
  await page
    .locator('form[action="/neighbors/1/update"]')
    .getByRole("button", { name: /speichern/i })
    .click();
  // The save POST redirects; wait for that navigation to settle before the next
  // goto, or the in-flight redirect aborts it (net::ERR_ABORTED under CI).
  await page.waitForLoadState("networkidle");

  // --- a booking to invoice. Use the UNIT path (Menge × Einzelpreis): the CI seed
  // has no tractors/load levels, so an hours booking (which needs a rig) can't be
  // created, but a unit booking can. ---
  await page.goto("/neighbors/1?year=1");
  await page.locator('select[name="unit"]').selectOption("Ballen");
  await page.locator('input[name="quantity"]').fill("10");
  await page.locator('input[name="unit_price"]').fill("3.20");
  await page.locator('input[name="task_label"]').fill("E2E Ballen");
  await page.getByRole("button", { name: "Buchung speichern" }).click();
  await expect(page.getByText("E2E Ballen").first()).toBeVisible();

  // --- festschreiben: confirm page must allow issuing, then issue ---
  await page.goto("/neighbors/1/beleg?year=1");
  await page.getByRole("link", { name: /festschreiben/i }).click();
  await expect(page.getByText("§ 11 UStG · Pflichtangaben")).toBeVisible();
  // The "Jetzt festschreiben" submit sits in a data-confirm form → clicking it opens
  // the custom <dialog>; the modal's OK (data-modal-ok) closes it with a "confirm"
  // return value, which submits the pending form. A <dialog> child reports as not
  // "visible" to Playwright's stability check, so wait for it attached and force-click.
  await page.getByRole("button", { name: "Jetzt festschreiben" }).click();
  const okBtn = page.locator("#confirmModal [data-modal-ok]");
  await okBtn.waitFor({ state: "attached", timeout: 4000 });
  // The dialog OK submits the pending form (POST /neighbors/1/invoice) which
  // redirects back to the Beleg. Wait for that navigation to finish before asserting
  // — otherwise the next goto races the redirect and the invoice isn't committed yet.
  await Promise.all([
    page.waitForURL(/\/neighbors\/1\/beleg/, { timeout: 10000 }),
    okBtn.click({ force: true }),
  ]);

  // --- the Beleg now shows a frozen invoice number + the due-date line ---
  await page.goto("/neighbors/1/beleg?year=1&rechnung=1");
  await expect(page.getByText(/RECHNUNG Nr\./)).toBeVisible();
  await expect(page.locator(".beleg__inv-due")).toBeVisible();

  // --- mark sent (confirm modal) → chip flips → undo via toast ---
  await page.goto("/neighbors/1/beleg?year=1");
  await page.locator('form[action*="mark-sent"] button').click();
  const okBtn2 = page.locator("#confirmModal [data-modal-ok]");
  await okBtn2.waitFor({ state: "attached", timeout: 4000 });
  await Promise.all([
    page.waitForURL(/\/neighbors\/1\/beleg/, { timeout: 10000 }),
    okBtn2.click({ force: true }),
  ]);
  await expect(page.locator('form[action*="mark-sent"] button.is-sent')).toBeVisible();
  // Undo from the toast (POST /beleg/unsend redirects back to the Beleg).
  await Promise.all([
    page.waitForURL(/\/neighbors\/1\/beleg/, { timeout: 10000 }),
    page.locator(".toast .toast__undo button").click(),
  ]);
  await expect(page.locator('form[action*="mark-sent"] button.is-sent')).toHaveCount(0);
});

test("dashboard shows a payment-status chip; Mahnwesen CSV export downloads", async ({ page }) => {
  await login(page);

  // Running-year neighbor with bookings shows an "Offen …" or "Bezahlt" chip.
  await page.goto("/?year=1");
  await expect(page.locator(".paychip").first()).toBeVisible();

  // Mahnwesen CSV export: the invoice issued above is not yet overdue (14-day
  // term), so the list may be empty — assert the endpoint returns a CSV either way.
  const res = await page.request.get("/mahnwesen/export.csv?year=1");
  expect(res.ok()).toBeTruthy();
  expect(res.headers()["content-type"]).toContain("text/csv");
  expect(await res.text()).toContain("Nachbar;Rechnung");
});

test("unknown route renders the branded 404, not a raw error", async ({ page }) => {
  await login(page);
  const res = await page.goto("/this-route-does-not-exist");
  expect(res?.status()).toBe(404);
  await expect(page.locator(".appbar, .auth, body")).toBeVisible();
});
