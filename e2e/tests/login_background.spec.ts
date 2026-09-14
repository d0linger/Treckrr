import { test, expect, type Page } from "@playwright/test";
import { createHash } from "node:crypto";

/** Pins the worksheet, theme and clock so movement cannot be confused with a reload or random layout. */
async function openWorksheet(page: Page, sheet = "graph", theme = "light", stalled = false) {
  await page.clock.install({ time: new Date("2026-09-14T00:00:02Z") });
  await page.clock.pauseAt(new Date("2026-09-14T00:00:02Z"));
  await page.addInitScript(({ sheet, theme, stalled }) => {
    localStorage.setItem("treckrr-loginbg", sheet);
    localStorage.setItem("treckrr-loginbg-seed", "12345");
    localStorage.setItem("treckrr-theme", theme);
    localStorage.removeItem("treckrr-sw-ctrl");
    sessionStorage.setItem("treckrr-loginbg-s", "1");
    // The login backdrop owns the page's recurring timers; track their cleanup, not just pixels.
    const intervals = new Set<number>(), frames = new Set<number>();
    const interval = window.setInterval.bind(window), clear = window.clearInterval.bind(window);
    window.setInterval = (callback, delay, ...args) => {
      const id = interval(callback, delay, ...args);
      intervals.add(id);
      return id;
    };
    window.clearInterval = (id) => { intervals.delete(id!); clear(id); };
    const request = window.requestAnimationFrame.bind(window), cancel = window.cancelAnimationFrame.bind(window);
    window.requestAnimationFrame = (callback) => {
      const id = request((time) => { frames.delete(id); callback(time); });
      frames.add(id);
      return id;
    };
    window.cancelAnimationFrame = (id) => { frames.delete(id); cancel(id); };
    (window as any).loginAnimationResources = () => ({ frames: frames.size, intervals: intervals.size });
    if (stalled) {
      const requestFrame = window.requestAnimationFrame.bind(window);
      let pending: FrameRequestCallback;
      window.requestAnimationFrame = (callback) => { pending = callback; return 0; };
      (window as any).resumeLoginFrames = () => {
        window.requestAnimationFrame = requestFrame;
        requestFrame(pending);
      };
    }
    const gradient = CanvasRenderingContext2D.prototype.createLinearGradient;
    (window as any).loginSweeps = [];
    CanvasRenderingContext2D.prototype.createLinearGradient = function (...args) {
      if (this.canvas.id === "login-bg") (window as any).loginSweeps.push(args[0]);
      return gradient.apply(this, args);
    };
  }, { sheet, theme, stalled });
  await page.goto("/login");
  await page.clock.runFor(100);
  await expect(page.locator("#login-bg")).toBeVisible();
}

/** Hashes the rendered canvas rather than its DOM, which stays unchanged throughout an animation. */
async function bitmap(page: Page) {
  const pixels = await page.locator("#login-bg").evaluate((canvas: HTMLCanvasElement) => canvas.toDataURL());
  return createHash("sha256").update(pixels).digest("hex");
}

for (const sheet of ["graph", "hatch"]) {
  for (const theme of ["light", "dark"]) {
    for (const mobile of [false, true]) {
      /** Verifies visible sweep movement, bounded frame rate and an unobstructed login form in each presentation. */
      test(`login sweep moves: ${sheet} ${theme} ${mobile ? "mobile" : "desktop"}`, async ({ page }) => {
        await page.setViewportSize(mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 });
        await page.emulateMedia({ reducedMotion: "no-preference" });
        await openWorksheet(page, sheet, theme);
        const before = await bitmap(page);
        await page.evaluate(() => { (window as any).loginSweeps = []; });
        await page.clock.runFor(1000);
        expect(await bitmap(page)).not.toBe(before);
        const sweeps: number[] = await page.evaluate(() => (window as any).loginSweeps);
        expect(sweeps.length).toBeGreaterThan(10);
        expect(sweeps.length).toBeLessThanOrEqual(30);
        expect(sweeps.at(-1)).toBeGreaterThan(sweeps[0]);
        await expect(page.getByRole("button", { name: "Anmelden", exact: true })).toBeVisible();
        await page.locator('input[name="username"]').fill("animation-test");
        await expect(page.locator('input[name="username"]')).toHaveValue("animation-test");
        expect(await page.locator("#login-bg").evaluate((canvas) => getComputedStyle(canvas).pointerEvents)).toBe("none");
      });
    }
  }
}

/** Simulates the rAF stall seen in occluded Windows/RDP sessions without marking the document hidden. */
test("login sweep recovers when animation frames stall", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await openWorksheet(page, "graph", "dark", true);
  const before = await bitmap(page);
  await page.clock.runFor(1800);
  expect(await bitmap(page)).not.toBe(before);
  await page.evaluate(() => {
    (window as any).loginSweeps = [];
    (window as any).resumeLoginFrames();
  });
  await page.clock.runFor(1000);
  const count = await page.evaluate(() => (window as any).loginSweeps.length);
  expect(count).toBeGreaterThan(10);
  expect(count).toBeLessThanOrEqual(30);
});

/** Exercises the visibility lifecycle without relying on headless browser window-occlusion heuristics. */
test("login sweep pauses while hidden and resumes once", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await openWorksheet(page);
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const paused = await bitmap(page);
  await page.clock.runFor(2000);
  expect(await bitmap(page)).toBe(paused);
  await page.evaluate(() => {
    delete (document as any).hidden;
    document.dispatchEvent(new Event("visibilitychange"));
    document.dispatchEvent(new Event("visibilitychange"));
    (window as any).loginSweeps = [];
  });
  await page.clock.runFor(1000);
  expect(await bitmap(page)).not.toBe(paused);
  expect(await page.evaluate(() => (window as any).loginSweeps.length)).toBeLessThanOrEqual(30);
});

/** Keeps the moving composition alive after changes that replace the backing bitmap and palette. */
test("login sweep survives resizing, theme changes and page restoration", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await openWorksheet(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.clock.runFor(100);
  const resized = await bitmap(page);
  await page.clock.runFor(1000);
  expect(await bitmap(page)).not.toBe(resized);
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  const paused = await bitmap(page);
  await page.clock.runFor(1500);
  expect(await bitmap(page)).toBe(paused);
  await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  await page.clock.runFor(1000);
  expect(await bitmap(page)).not.toBe(paused);
});

/** Pins the established Windows/RDP compatibility behavior without changing other UI motion preferences. */
test("login sweep is not frozen by the Windows reduced-motion signal", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await openWorksheet(page);
  const before = await bitmap(page);
  await page.clock.runFor(1000);
  expect(await bitmap(page)).not.toBe(before);
  expect(await page.evaluate(() => matchMedia("(prefers-reduced-motion: reduce)").matches)).toBe(true);
});

for (const stalled of [false, true]) {
  /** Guards against timed shutdowns while retaining keyboard pause/resume and full timer cleanup. */
  test(`login sweep keeps running until paused: ${stalled ? "stalled frames" : "normal frames"}`, async ({ page }) => {
    await openWorksheet(page, "graph", "dark", stalled);
    await page.clock.runFor(6100);
    for (const elapsed of [0, 60000]) {
      await page.clock.fastForward(elapsed);
      const moving = await bitmap(page);
      await page.evaluate(() => { (window as any).loginSweeps = []; });
      // Cover the short off-canvas gap while the band wraps around the sheet.
      await page.clock.runFor(3100);
      expect(await bitmap(page)).not.toBe(moving);
      const count = await page.evaluate(() => (window as any).loginSweeps.length);
      expect(count).toBeGreaterThan(50);
      expect(count).toBeLessThanOrEqual(80);
    }
    expect(await page.evaluate(() => (window as any).loginAnimationResources().intervals)).toBe(stalled ? 2 : 1);
    const control = page.getByRole("button", { name: "Hintergrund pausieren", exact: true });
    await control.focus();
    await control.press("Space");
    await expect(page.getByRole("button", { name: "Hintergrund fortsetzen", exact: true })).toBeFocused();
    expect(await page.evaluate(() => (window as any).loginAnimationResources())).toEqual({ frames: 0, intervals: 0 });
    const stopped = await bitmap(page);
    await page.evaluate(() => { (window as any).loginSweeps = []; });
    await page.clock.runFor(6000);
    expect(await bitmap(page)).toBe(stopped);
    expect(await page.evaluate(() => (window as any).loginSweeps)).toEqual([]);
    await page.locator('input[name="username"]').fill("still-usable");
    await expect(page.locator('input[name="username"]')).toHaveValue("still-usable");
    await page.getByRole("button", { name: "Hintergrund fortsetzen", exact: true }).press("Enter");
    await page.clock.runFor(2200);
    expect(await bitmap(page)).not.toBe(stopped);
    await expect(page.getByRole("button", { name: "Hintergrund pausieren", exact: true })).toBeVisible();
    expect(await page.evaluate(() => (window as any).loginAnimationResources().intervals)).toBe(stalled ? 2 : 1);
  });
}

/** Ensures long absences release resources without expiring the animation when the page returns. */
test("login sweep resumes after long hidden periods and page restoration", async ({ page }) => {
  await openWorksheet(page);
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(await page.evaluate(() => (window as any).loginAnimationResources())).toEqual({ frames: 0, intervals: 0 });
  await page.clock.fastForward(60000);
  const hidden = await bitmap(page);
  await page.evaluate(() => {
    delete (document as any).hidden;
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.runFor(1000);
  expect(await bitmap(page)).not.toBe(hidden);
  await page.evaluate(() => window.dispatchEvent(new Event("pagehide")));
  expect(await page.evaluate(() => (window as any).loginAnimationResources())).toEqual({ frames: 0, intervals: 0 });
  await page.clock.fastForward(60000);
  await page.evaluate(() => window.dispatchEvent(new Event("pageshow")));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.clock.runFor(1000);
  expect(await page.evaluate(() => (window as any).loginAnimationResources())).toEqual({ frames: 1, intervals: 1 });
  const moving = await bitmap(page);
  await page.clock.runFor(1000);
  expect(await bitmap(page)).not.toBe(moving);
});

/** Allows static layout/theme updates without overriding the user's explicit pause choice. */
test("paused login sweep stays stopped after resize, theme and page restoration", async ({ page }) => {
  await openWorksheet(page);
  await page.getByRole("button", { name: "Hintergrund pausieren", exact: true }).click();
  const stopped = await bitmap(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-theme", "dark");
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("pageshow"));
  });
  await page.clock.runFor(100);
  const redrawn = await bitmap(page);
  expect(redrawn).not.toBe(stopped);
  expect(await page.evaluate(() => (window as any).loginAnimationResources())).toEqual({ frames: 0, intervals: 0 });
  await page.clock.runFor(2000);
  expect(await bitmap(page)).toBe(redrawn);
  await expect(page.getByRole("button", { name: "Hintergrund fortsetzen", exact: true })).toBeVisible();
});

/** Does not expose a nonfunctional motion control when JavaScript is unavailable. */
test("login motion control stays hidden without JavaScript", async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL, javaScriptEnabled: false });
  try {
    const page = await context.newPage();
    await page.goto("/login");
    await expect(page.locator("#login-bg-toggle")).toBeHidden();
    await expect(page.locator('input[name="username"]')).toBeVisible();
    await expect(page.getByRole("button", { name: "Anmelden", exact: true })).toBeVisible();
  } finally {
    await context.close();
  }
});
