import { test, expect } from "@playwright/test";

// Session scripts for the flows this app is actually used for (Ausbaukarte 96).
// booking.spec and invoice.spec cover capture and invoicing; this file walks the
// paths added later — money in, the bookings list with its filters and bulk
// actions, and closing the year — because those are the ones where a regression
// costs an operator real work rather than a re-click.
//
// The CI job seeds base/year/neighbor 1/1/1, master data and one booking.

const USER = process.env.E2E_ADMIN_USER || "admin";
const PASS = process.env.E2E_ADMIN_PASS || "e2e-admin-password-123";

async function login(page) {
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

// Actions guarded by data-confirm open the custom <dialog>; its OK submits the
// pending form. #confirmModal lives in the layout on every page, so waiting for
// "attached" would race showModal() — wait for the button to be VISIBLE.
async function confirmModal(page, waitForURL?: RegExp) {
  const okBtn = page.locator("#confirmModal [data-modal-ok]");
  await okBtn.waitFor({ state: "visible", timeout: 4000 });
  if (waitForURL) {
    await Promise.all([
      page.waitForURL(waitForURL, { timeout: 10000 }),
      okBtn.click({ force: true }),
    ]);
  } else {
    await Promise.all([
      page.waitForNavigation({ waitUntil: "load", timeout: 10000 }),
      okBtn.click({ force: true }),
    ]);
  }
}

test("record a payment and see it in the cross-year history", async ({ page }) => {
  await login(page);
  await page.goto("/neighbors/1?year=1");

  const details = page.locator("details").filter({
    has: page.locator("summary", { hasText: "Zahlung erfassen" }),
  }).first();
  await details.locator("summary").click();
  await details.locator('input[name="amount"]').fill("25");
  await details.locator('select[name="method"]').selectOption("bar");
  await details.locator('input[name="note"]').fill("E2E Teilzahlung");
  await details.getByRole("button", { name: "Zahlung erfassen" }).click();

  // The row shows method and note where the payment was booked …
  await expect(page.locator("body")).toContainText("E2E Teilzahlung");
  // … and the cross-year history lists it too (Ausbaukarte 79).
  await page.goto("/neighbors/1/overview");
  await expect(page.locator("body")).toContainText("25,00");
  await expect(page.locator("body")).toContainText("E2E Teilzahlung");
});

test("bookings list filters, and bulk storno leaves a reason", async ({ page }) => {
  await login(page);
  await page.goto("/buchungen?year=1");
  await expect(page.locator("body")).toContainText("Buchung(en)");

  // A task filter that matches nothing must say so, not show everything.
  await page.goto("/buchungen?year=1&task=gibtesnicht");
  await expect(page.locator("body")).toContainText("0 Buchung(en)");

  // The specs share one database and invoice.spec may have frozen an invoice,
  // which locks this neighbor's bookings against any bulk action. Undo that
  // first — exactly what an operator does before correcting a booking — so the
  // test states something about bulk storno rather than about test ordering.
  await page.goto("/neighbors/1/beleg?year=1");
  const storno = page.getByRole("button", { name: "Rechnung stornieren" });
  if (await storno.count()) {
    await storno.click();
    await confirmModal(page);
  }

  // Select every booking and cancel it with a reason.
  await page.goto("/buchungen?year=1");
  const boxes = page.locator('input[name="entry_id"]');
  const count = await boxes.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i++) await boxes.nth(i).check();
  await page.locator('input[name="reason"]').fill("E2E Sammelstorno");
  await page.getByRole("button", { name: "Stornieren", exact: true }).click();
  await confirmModal(page, /\/buchungen/);

  // The count in the flash must be the selection, not zero — a bulk action that
  // silently does nothing is the failure mode worth pinning.
  await expect(page.locator("body")).toContainText(`${count} Buchung(en) storniert`);
  await page.goto("/buchungen?year=1&voided=only");
  await expect(page.locator("body")).toContainText(`${count} Buchung(en)`);
});

test("year closing review lists what is open, and closing locks documents", async ({ page }) => {
  await login(page);
  await page.goto("/years/1/abschluss");
  await expect(page.locator("body")).toContainText("Jahresabschluss");
  await expect(page.locator("body")).toContainText("Buchungen ohne festgeschriebene Rechnung");

  await page.getByRole("button", { name: /Jahr 2025 abschließen/ }).click();
  await confirmModal(page, /\/years/);
  await expect(page.locator("body")).toContainText("Abgeschlossen");

  // A closed year refuses a new document and says why …
  await page.goto("/neighbors/1/beleg?year=1");
  await expect(page.locator("body")).not.toContainText("Rechnung ausstellen & festschreiben");

  // … and reopening needs a reason, which the form asks for.
  await page.goto("/years");
  await expect(page.locator("body")).toContainText("Wieder öffnen");
});
