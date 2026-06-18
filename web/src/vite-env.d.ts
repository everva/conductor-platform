/// <reference types="vite/client" />

// Build-time env vars the cockpit reads. VITE_API_BASE overrides the gateway
// origin (default same-origin ""); see api/client.ts defaultBaseUrl().
interface ImportMetaEnv {
  readonly VITE_API_BASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
