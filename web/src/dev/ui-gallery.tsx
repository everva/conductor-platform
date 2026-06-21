// DEV-ONLY UI gallery (not in the prod single-page build). Served at /ui.html by
// the Vite dev server. Renders every V1 primitive in its variants/states on the
// real token canvas so the design system can be screenshot-reviewed each wave.
// Exports nothing (side-effect mount) — keeps react-refresh happy.
import { StrictMode, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/geist/index.css";
import "@fontsource-variable/geist-mono/index.css";
import "../theme/tokens.css";
import "../index.css";
import { Check as CheckIcon, MoreHorizontal, Plus as PlusIcon } from "lucide-react";
import {
  Badge,
  Button,
  Card,
  Chip,
  IconButton,
  Panel,
  Skeleton,
  StatusDot,
} from "../ui/index.ts";

// Sized lucide aliases so the showcase mirrors the real surfaces (V4: lucide
// everywhere, no inline glyph SVGs).
const Plus = () => <PlusIcon size={14} strokeWidth={2.2} />;
const Check = () => <CheckIcon size={14} strokeWidth={2.4} />;
const Dots = () => <MoreHorizontal size={15} />;

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section style={{ display: "flex", flexDirection: "column", gap: "0.75rem" }}>
      <h2
        style={{
          margin: 0,
          fontSize: "0.7rem",
          textTransform: "uppercase",
          letterSpacing: "0.09em",
          color: "var(--text-3)",
          fontWeight: 650,
        }}
      >
        {title}
      </h2>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "0.75rem", alignItems: "center" }}>
        {children}
      </div>
    </section>
  );
}

function Gallery() {
  return (
    <div
      style={{
        maxWidth: "920px",
        margin: "0 auto",
        padding: "2.5rem 2rem",
        display: "flex",
        flexDirection: "column",
        gap: "2.25rem",
      }}
    >
      <header style={{ display: "flex", flexDirection: "column", gap: "0.35rem" }}>
        <h1 style={{ margin: 0, fontSize: "1.625rem", letterSpacing: "-0.012em" }}>
          Conductor UI primitives
        </h1>
        <p style={{ margin: 0, color: "var(--text-3)", fontSize: "0.875rem" }}>
          V1 — token-driven, accessible building blocks (Geist · semantic color · 8px rhythm).
        </p>
      </header>

      <Section title="Button — variants">
        <Button variant="primary">Primary</Button>
        <Button variant="secondary">Secondary</Button>
        <Button variant="ghost">Ghost</Button>
        <Button variant="success" leftIcon={<Check />}>Approve</Button>
        <Button variant="danger">Abort</Button>
      </Section>

      <Section title="Button — sizes · icon · loading · disabled">
        <Button variant="primary" size="sm" leftIcon={<Plus />}>New work</Button>
        <Button variant="primary" size="md" leftIcon={<Plus />}>New work</Button>
        <Button variant="secondary" loading>Working</Button>
        <Button variant="primary" disabled>Disabled</Button>
      </Section>

      <Section title="IconButton">
        <IconButton label="More" ><Dots /></IconButton>
        <IconButton label="Add" bordered><Plus /></IconButton>
        <IconButton label="Confirm" size="sm" bordered><Check /></IconButton>
      </Section>

      <Section title="Badge — status tones">
        <Badge tone="neutral">T2</Badge>
        <Badge tone="brand">distilling</Badge>
        <Badge tone="success">approved</Badge>
        <Badge tone="warn">awaiting</Badge>
        <Badge tone="danger">blocked</Badge>
        <Badge tone="info">running</Badge>
      </Section>

      <Section title="Chip — neutral metadata">
        <Chip>web</Chip>
        <Chip>backend</Chip>
        <Chip mono>host-linux</Chip>
        <Chip mono>W-run</Chip>
      </Section>

      <Section title="StatusDot">
        <StatusDot tone="success" pulse label="live" />
        <StatusDot tone="info" pulse />
        <StatusDot tone="warn" />
        <StatusDot tone="danger" />
        <StatusDot tone="neutral" />
      </Section>

      <Section title="Card — accents · interactive · selected">
        <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: "0.75rem", width: "100%" }}>
          <Card interactive accent="info">
            <div style={{ fontWeight: 600 }}>W-run</div>
            <div style={{ color: "var(--text-3)", fontSize: "0.8rem" }}>running</div>
          </Card>
          <Card interactive accent="warn">
            <div style={{ fontWeight: 600 }}>I-await</div>
            <div style={{ color: "var(--text-3)", fontSize: "0.8rem" }}>awaiting</div>
          </Card>
          <Card interactive accent="danger" selected>
            <div style={{ fontWeight: 600 }}>I-block</div>
            <div style={{ color: "var(--text-3)", fontSize: "0.8rem" }}>blocked · selected</div>
          </Card>
          <Card interactive accent="success">
            <div style={{ fontWeight: 600 }}>W-done</div>
            <div style={{ color: "var(--text-3)", fontSize: "0.8rem" }}>done</div>
          </Card>
        </div>
      </Section>

      <Section title="Panel">
        <Panel
          title="Hosts"
          actions={<Button variant="ghost" size="sm">Refresh</Button>}
          style={{ width: "100%" }}
        >
          <div style={{ padding: "0.85rem 1rem", color: "var(--text-2)", fontSize: "0.85rem" }}>
            <StatusDot tone="success" /> &nbsp;host-linux — linux, web, backend
          </div>
        </Panel>
      </Section>

      <Section title="Skeleton — loading states">
        <div style={{ display: "flex", gap: "0.75rem", alignItems: "center", width: "100%" }}>
          <Skeleton circle width={36} />
          <div style={{ display: "flex", flexDirection: "column", gap: "0.5rem", flex: 1 }}>
            <Skeleton width="40%" />
            <Skeleton width="70%" />
          </div>
        </div>
      </Section>
    </div>
  );
}

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <Gallery />
    </StrictMode>,
  );
}
