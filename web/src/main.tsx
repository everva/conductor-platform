// Cockpit entrypoint: mount the React app under #root. The actual auth gate +
// shell live in App; this file only bootstraps the tree.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App.tsx";
// Premium type (self-hosted via @fontsource — CSP-safe, no external requests),
// then the design tokens (defines the :root custom properties), then the global
// stylesheet that consumes them. Order matters: @font-face → tokens → base.
import "@fontsource-variable/geist/index.css";
import "@fontsource-variable/geist-mono/index.css";
import "./theme/tokens.css";
import "./index.css";

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("cockpit: #root element not found");
}

createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
