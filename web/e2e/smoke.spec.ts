// Smoke e2e: load the cockpit and assert the token gate renders (no token in
// sessionStorage → the sign-in form is shown). This proves the built app boots in
// a real browser. Requires `npx playwright install chromium` + the previewed app
// (see playwright.config.ts webServer). It is NOT part of the 3B-0 must-pass gate
// (that is tsc+eslint+vitest); it joins CI in 3C.
import { expect, test } from "@playwright/test";

test("renders the token gate when signed out", async ({ page }) => {
  await page.goto("/");

  // The sign-in form and its token input must be present.
  await expect(page.getByRole("heading", { name: /conductor cockpit/i })).toBeVisible();
  await expect(page.getByLabel(/api token/i)).toBeVisible();
  await expect(page.getByRole("button", { name: /sign in/i })).toBeVisible();
});
