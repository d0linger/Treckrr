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

// These scenarios intentionally share issued documents. Retrying a failed
// serial group against its already-mutated database can reissue or duplicate
// work and mask the original failure; rerun it only on a fresh E2E stack.
test.describe.configure({ mode: "serial", retries: 0 });

/** Signs into the seeded admin account before changing the isolated invoice fixture through the UI. */
async function login(page) {
  await page.goto("/login");
  await page.locator('input[name="username"]').fill(USER);
  await page.locator('input[name="password"]').fill(PASS);
  await page.getByRole("button", { name: "Anmelden", exact: true }).click();
  await expect(page.locator(".appbar")).toBeVisible();
}

/**
 * Builds the issuer and recipient prerequisites, then checks invoice and reminder document visibility.
 * Also verifies that marking an invoice sent can be undone through the confirmation and toast flow.
 */
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
    .locator(":scope > summary").click();
  const billingDetails = page
    .locator('form[action="/neighbors/1/update"] details')
    .filter({ has: page.locator('textarea[name="address"]') });
  if (await billingDetails.getAttribute("open") === null) await billingDetails.locator(":scope > summary").click();
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
  await page.goto("/neighbors/1?year=1&view=booking");
  await page.getByText("Schnellerfassung (mehrere Zeilen)", { exact: true }).click();
  await expect(page.getByRole("button", { name: "Zeilen speichern", exact: true })).toBeEnabled();
  await page.locator('[data-booking-kind]').selectOption("quantity");
  await page.locator('[data-unit]').selectOption("Ballen");
  await page.locator('input[name="quantity"]').fill("10");
  await page.locator('input[name="unit_price"]').fill("3.20");
  await page.locator('input[name="task_label"]').fill("E2E Ballen");
  await page.getByRole("button", { name: "Buchung speichern" }).click();
  await expect(page.getByText("E2E Ballen").first()).toBeVisible();
  // The save POST redirects back to the neighbor page; the visibility assertion can
  // pass while that redirect's tail is still in flight, so let it settle before the
  // next goto — otherwise the in-flight navigation aborts it (net::ERR_ABORTED in CI).
  await page.waitForLoadState("networkidle");

  // --- festschreiben: confirm page must allow issuing, then issue ---
  await page.goto("/neighbors/1/beleg?year=1");
  const calculationPaths = page.locator(".beleg__calc");
  await expect(calculationPaths.first()).toBeHidden();
  const calculationToggle = page.getByRole("button", { name: "Rechenwege", exact: true });
  await calculationToggle.click();
  await expect(calculationToggle).toHaveAttribute("aria-pressed", "true");
  await expect(calculationPaths.first()).toBeVisible();
  await page.getByRole("link", { name: /festschreiben/i }).click();
  // The link NAVIGATES to the confirm page. Wait for that page to finish loading so
  // the deferred app.js has attached the data-confirm submit handler BEFORE we click
  // "Jetzt festschreiben" — otherwise the form posts directly (issuing the invoice
  // with no modal), and the modal-OK click below has no open dialog to act on.
  await page.waitForLoadState("load");
  await expect(page.getByText("§ 11 UStG · Pflichtangaben")).toBeVisible();
  // The "Jetzt festschreiben" submit sits in a data-confirm form → clicking it opens
  // the custom <dialog>; the modal's OK (data-modal-ok) closes it with a "confirm"
  // return value, which submits the pending form.
  await page.getByRole("button", { name: "Jetzt festschreiben" }).click();
  const okBtn = page.locator("#confirmModal [data-modal-ok]");
  // Wait for the dialog to actually OPEN (button visible), not merely be in the DOM:
  // #confirmModal lives in the layout on every page, so "attached" is always true and
  // would race ahead of showModal().
  await okBtn.waitFor({ state: "visible", timeout: 4000 });
  // The dialog OK submits the pending form (POST /neighbors/1/invoice) which
  // redirects back to the Beleg. Wait for that navigation to finish before asserting
  // — otherwise the next goto races the redirect and the invoice isn't committed yet.
  await Promise.all([
    page.waitForURL(/\/neighbors\/1\/beleg/, { timeout: 10000 }),
    okBtn.click({ force: true }),
  ]);

  // --- selected document contains only its frozen invoice lines ---
  await page.goto("/neighbors/1/beleg?year=1&rechnung=1");
  await expect(page.locator("#beleg").getByRole("heading", { name: /^Rechnung / })).toBeVisible();
  await expect(page.locator(".beleg--document > .tablewrap table")).toContainText("E2E Ballen");
  await expect(page.locator(".beleg__inv-due")).toContainText("Fällig am");

  // An issued invoice freezes only its own sources, not future work.
  await page.goto("/neighbors/1?year=1&view=booking");
  await expect(page.getByRole("button", { name: "Buchung speichern", exact: true })).toBeEnabled();
  await page.getByText("Schnellerfassung (mehrere Zeilen)", { exact: true }).click();
  await expect(page.getByRole("button", { name: "Zeilen speichern", exact: true })).toBeEnabled();

  // A reminder always uses the formal document presentation, including parties
  // and the open amount; it has no invoice-mode toggle to reveal hidden details.
  await page.goto("/neighbors/1/mahnung?year=1&stufe=1");
  await expect(page.locator(".beleg__invoice")).toBeVisible();
  await expect(page.locator(".beleg__invoice")).toContainText("Hof Bergmann");
  await expect(page.locator(".beleg__invoice")).toContainText("E2E Nachbar");
  await expect(page.getByText("Offener Betrag", { exact: true })).toBeVisible();
  await expect(page.locator(".beleg__hv.beleg__sum-invoice")).toBeVisible();

  // A reminder's allocated payments use a wide table too. Only that region may
  // scroll on a narrow phone; the parties, open amount and PDF action must fit.
  const reminderAmount = await page.locator(".beleg__hv").innerText();
  const settlement = page.locator(".settlement-lines");
  await expect(settlement).toBeVisible();
  const settlementText = await settlement.innerText();
  await page.setViewportSize({ width: 320, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1);
  await expect(page.locator(".beleg__hv")).toHaveText(reminderAmount);
  await expect(page.getByRole("link", { name: "Als PDF", exact: true })).toBeVisible();
  const scrollRight = page.locator(".beleg--reminder").getByRole("button", { name: /: nach rechts scrollen$/ });
  await expect(scrollRight).toBeVisible();
  await scrollRight.click();
  await expect.poll(() => settlement.evaluate(node => node.scrollLeft)).toBeGreaterThan(0);
  expect(await settlement.innerText()).toBe(settlementText);

  // A populated journal adds an export action beside its long heading. The
  // action must wrap into the viewport without hiding any financial columns.
  await page.setViewportSize({ width: 320, height: 844 });
  await page.goto("/rechnungsjournal?year=1");
  await expect(page.getByRole("link", { name: "Export CSV", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.setViewportSize({ width: 1280, height: 720 });

  // --- mark sent (confirm modal) → chip flips → undo via toast ---
  await page.goto("/neighbors/1/beleg?year=1&rechnung=1");
  await page.locator('form[action*="mark-sent"] button').click();
  const okBtn2 = page.locator("#confirmModal [data-modal-ok]");
  await okBtn2.waitFor({ state: "visible", timeout: 4000 });
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

test("new work creates a separate invoice; selected and FIFO payments preserve both documents", async ({ page }) => {
  test.setTimeout(90_000);
  await login(page);
  await page.goto("/neighbors/1/beleg?year=1&rechnung=1");
  const firstLink = await page.getByRole("link", { name: "Rechnung als PDF", exact: true }).getAttribute("href");
  const firstID = new URL(firstLink!, page.url()).searchParams.get("invoice_id")!;
  const firstURL = `/neighbors/1/beleg?year=1&invoice_id=${firstID}`;
  const firstLines = await page.locator(".beleg--document > .tablewrap table").innerText();
  const firstOpen = await page.locator(".beleg__payinfo strong").innerText();
  const firstAmount = Number(firstOpen.replace("Offen: ", "").replace(/\./g, "").replace(",", ".").replace(/[^0-9.-]/g, ""));
  expect(firstAmount).toBeGreaterThan(7);

  const book = async (label: string, price: string) => {
    await page.goto("/neighbors/1?year=1&view=booking");
    await expect(page.getByRole("button", { name: "Buchung speichern", exact: true })).toBeEnabled();
    await page.locator("[data-booking-kind]").selectOption("quantity");
    await page.locator("[data-unit]").selectOption("Ballen");
    await page.locator('input[name="quantity"]').fill("10");
    await page.locator('input[name="unit_price"]').fill(price);
    await page.locator('input[name="task_label"]').fill(label);
    await page.getByRole("button", { name: "Buchung speichern", exact: true }).click();
    await expect(page.getByText(label).first()).toBeVisible();
    await page.waitForLoadState("load");
  };
  await book("E2E Folgearbeit B", "4.00");
  await page.goto("/neighbors/1/beleg?year=1");
  await page.getByRole("link", { name: /festschreiben/i }).click();
  await page.waitForLoadState("load");
  await expect(page.locator("main")).toContainText("E2E Folgearbeit B");
  await expect(page.locator("main")).not.toContainText("E2E Ballen");
  await page.getByRole("button", { name: "Jetzt festschreiben", exact: true }).click();
  const confirm = page.locator("#confirmModal [data-modal-ok]");
  await expect(confirm).toBeVisible();
  await Promise.all([page.waitForURL(/\/neighbors\/1\/beleg\?.*invoice_id=/), confirm.click()]);
  const secondID = new URL(page.url()).searchParams.get("invoice_id")!;
  expect(secondID).not.toBe(firstID);
  const secondURL = `/neighbors/1/beleg?year=1&invoice_id=${secondID}`;
  await expect(page.locator(".beleg--document > .tablewrap table")).toContainText("E2E Folgearbeit B");
  await expect(page.locator(".beleg--document > .tablewrap table")).not.toContainText("E2E Ballen");
  const secondLines = await page.locator(".beleg--document > .tablewrap table").innerText();
  await expect(page.locator(".beleg__payinfo strong")).toHaveText("Offen: 40,00 €");

  // Bounded responsive checks using only this synthetic frozen document.
  // Successful CI runs must not generate additional screenshot artifacts.
  for (const viewport of [{ name: "mobile", width: 320, height: 844 }, { name: "desktop", width: 1280, height: 720 }]) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    for (const theme of ["light", "dark"] as const) {
      // Exercise the real top-bar control; an initial "auto" can require two clicks.
      for (let attempt = 0; attempt < 2 && await page.locator("html").getAttribute("data-theme") !== theme; attempt++) {
        await page.getByRole("button", { name: "Hell/Dunkel umschalten", exact: true }).click();
      }
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), `${viewport.name}/${theme}: page overflow`).toBeLessThanOrEqual(1);
      const overflow = await page.locator("#beleg").evaluate((sheet) =>
        [sheet, ...sheet.querySelectorAll<HTMLElement>("*")]
          .filter((node) => node instanceof HTMLElement && node.clientWidth > 0 &&
            node.scrollWidth > node.clientWidth + 1 && !node.closest(".tablewrap"))
          .map((node) => `${node.tagName}.${node.className}`));
      expect(overflow, `${viewport.name}/${theme}: only the invoice table may scroll horizontally`).toEqual([]);
      const tableRegion = page.getByRole("region", { name: "Festgeschriebene Rechnungspositionen", exact: true });
      await tableRegion.scrollIntoViewIfNeeded();
      await tableRegion.focus();
      await expect(tableRegion).toBeFocused();
      for (const action of [
        page.getByRole("link", { name: "Rechnung als PDF", exact: true }),
        page.getByRole("link", { name: "Zahlung verbuchen", exact: true }),
        page.locator('form[action*="mark-sent"] button'),
      ]) {
        // Center the action explicitly: Chromium may regard a button clipped by
        // a fraction of a pixel as already in view and otherwise leave it there.
        await action.evaluate((element) => element.scrollIntoView({ block: "center", inline: "nearest", behavior: "instant" }));
        await expect(action).toBeVisible();
        await expect(action).toBeInViewport({ ratio: 1 });
        await action.focus();
        await expect(action).toBeFocused();
        await action.click({ trial: true }); // reachability only: no send/payment mutation
      }
      await page.evaluate(() => window.scrollTo(0, 0));
    }
  }
  await page.setViewportSize({ width: 1280, height: 720 });

  await book("E2E Noch nicht abgerechnet C", "1.00");
  await page.goto(firstURL);
  await expect(page.locator(".beleg--document > .tablewrap table")).toHaveText(firstLines, { useInnerText: true });
  await expect(page.locator(".beleg__payinfo strong")).toHaveText(firstOpen);
  await page.goto(secondURL);
  await expect(page.locator(".beleg--document > .tablewrap table")).toHaveText(secondLines, { useInnerText: true });
  await expect(page.locator(".beleg--document > .tablewrap table")).not.toContainText("E2E Noch nicht abgerechnet C");
  await page.goto("/neighbors/1/beleg?year=1");
  for (const label of ["E2E Ballen", "E2E Folgearbeit B", "E2E Noch nicht abgerechnet C"]) {
    await expect(page.locator("#beleg")).toContainText(label);
  }

  const pay = async (invoiceID: string, amount: string) => {
    if (invoiceID) {
      await page.goto(invoiceID === secondID ? secondURL : firstURL);
      await page.getByRole("link", { name: "Zahlung verbuchen", exact: true }).click();
    } else {
      await page.goto("/neighbors/1?year=1&view=payments");
    }
    const form = page.locator('form[action="/neighbors/1/payments"]');
    await expect(form.locator('select[name="invoice_id"]')).toHaveValue(invoiceID);
    if (invoiceID === secondID) await expect(form.locator('input[name="amount"]')).toHaveValue("40.00");
    await form.locator('input[name="amount"]').fill(amount);
    await form.getByRole("button", { name: "Zahlung erfassen", exact: true }).click();
    await page.waitForLoadState("load");
  };
  await pay(secondID, "5.00");
  await page.goto(secondURL);
  await expect(page.locator(".beleg__payinfo strong")).toHaveText("Offen: 35,00 €");
  await page.goto(firstURL);
  await expect(page.locator(".beleg__payinfo strong")).toHaveText(firstOpen);
  await pay("", "7.00");
  await page.goto(firstURL);
  await expect(page.locator(".beleg__payinfo strong")).toHaveText(`Offen: ${(firstAmount - 7).toLocaleString("de-AT", { minimumFractionDigits: 2, maximumFractionDigits: 2 })} €`);
  await page.goto(secondURL);
  await expect(page.locator(".beleg__payinfo strong")).toHaveText("Offen: 35,00 €");

  // The existing one-click action settles the entire year, including pending
  // work, independently of which invoice is selected in the payment form.
  await page.goto("/neighbors/1?year=1&view=payments");
  await page.locator('select[name="invoice_id"]').selectOption(secondID);
  await page.getByRole("button", { name: "Gesamten Rest bezahlen", exact: true }).click();
  await page.waitForLoadState("load");
  await page.goto("/neighbors/1?year=1&view=payments");
  await expect(page.locator("header.summary-card .summary-card__label").first()).toContainText("Ausgeglichen");
  await expect(page.locator("header.summary-card .summary-card__value")).toHaveText("0,00 €");
  for (const [url, lines] of [[firstURL, firstLines], [secondURL, secondLines]]) {
    await page.goto(url);
    await expect(page.locator(".beleg--document > .tablewrap table")).toHaveText(lines, { useInnerText: true });
    await expect(page.locator(".beleg__payinfo strong")).toHaveText("Offen: 0,00 €");
  }
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
