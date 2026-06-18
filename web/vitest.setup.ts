// Vitest global setup: register @testing-library/jest-dom matchers (toBeInTheDocument,
// etc.) and clean up the DOM between tests so component tests stay isolated.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
});
