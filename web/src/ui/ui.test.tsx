// Unit tests for the V1 UI primitives: variant/size class mapping, the loading +
// disabled behavior, accessible names on icon-only + status elements, and the
// Card/Panel composition. Pure render assertions (jsdom) — the look itself is
// verified live in the gallery screenshot.
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Badge } from "./Badge.tsx";
import { Button } from "./Button.tsx";
import { Card } from "./Card.tsx";
import { Chip } from "./Chip.tsx";
import { IconButton } from "./IconButton.tsx";
import { Panel } from "./Panel.tsx";
import { Skeleton } from "./Skeleton.tsx";
import { StatusDot } from "./StatusDot.tsx";

describe("Button", () => {
  it("defaults to a type=button secondary/md and renders children", () => {
    render(<Button>Go</Button>);
    const btn = screen.getByRole("button", { name: "Go" });
    expect(btn).toHaveAttribute("type", "button");
    expect(btn).toHaveClass("ui-btn", "ui-btn-md", "ui-btn-secondary");
  });

  it("maps variant + size to classes", () => {
    render(
      <Button variant="primary" size="sm">
        New
      </Button>,
    );
    expect(screen.getByRole("button")).toHaveClass("ui-btn-primary", "ui-btn-sm");
  });

  it("loading disables the button, marks aria-busy, and keeps the label", () => {
    render(<Button loading>Saving</Button>);
    const btn = screen.getByRole("button", { name: "Saving" });
    expect(btn).toBeDisabled();
    expect(btn).toHaveAttribute("aria-busy", "true");
    expect(btn.querySelector(".ui-btn-spinner")).not.toBeNull();
  });

  it("fires onClick when enabled, not when disabled", () => {
    const onClick = vi.fn();
    const { rerender } = render(<Button onClick={onClick}>Tap</Button>);
    fireEvent.click(screen.getByRole("button"));
    expect(onClick).toHaveBeenCalledTimes(1);
    rerender(
      <Button onClick={onClick} disabled>
        Tap
      </Button>,
    );
    fireEvent.click(screen.getByRole("button"));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("renders the leftIcon but swaps it for the spinner while loading", () => {
    const { rerender } = render(
      <Button leftIcon={<span data-testid="ic" />}>X</Button>,
    );
    expect(screen.queryByTestId("ic")).not.toBeNull();
    rerender(
      <Button leftIcon={<span data-testid="ic" />} loading>
        X
      </Button>,
    );
    expect(screen.queryByTestId("ic")).toBeNull();
  });
});

describe("IconButton", () => {
  it("requires-and-exposes an accessible name via label", () => {
    render(
      <IconButton label="More options">
        <span />
      </IconButton>,
    );
    const btn = screen.getByRole("button", { name: "More options" });
    expect(btn).toHaveAttribute("title", "More options");
    expect(btn).toHaveClass("ui-iconbtn", "ui-iconbtn-md");
  });

  it("bordered adds the surface class", () => {
    render(
      <IconButton label="Add" bordered>
        <span />
      </IconButton>,
    );
    expect(screen.getByRole("button")).toHaveClass("ui-iconbtn-bordered");
  });
});

describe("Badge + Chip", () => {
  it("Badge maps tone, default neutral", () => {
    const { rerender, container } = render(<Badge>n</Badge>);
    expect(container.firstChild).toHaveClass("ui-badge", "ui-badge-neutral");
    rerender(<Badge tone="success">ok</Badge>);
    expect(container.firstChild).toHaveClass("ui-badge-success");
  });

  it("Chip mono adds the mono class", () => {
    const { rerender, container } = render(<Chip>web</Chip>);
    expect(container.firstChild).toHaveClass("ui-chip");
    expect(container.firstChild).not.toHaveClass("ui-chip-mono");
    rerender(<Chip mono>W-1</Chip>);
    expect(container.firstChild).toHaveClass("ui-chip-mono");
  });
});

describe("Card", () => {
  it("composes interactive/selected/accent/padded classes", () => {
    const { container } = render(
      <Card interactive selected accent="info">
        body
      </Card>,
    );
    expect(container.firstChild).toHaveClass(
      "ui-card",
      "ui-card-pad",
      "ui-card-interactive",
      "ui-card-selected",
      "ui-card-accent",
      "ui-card-accent-info",
    );
  });

  it('accent="none" and padded={false} omit those classes', () => {
    const { container } = render(
      <Card accent="none" padded={false}>
        body
      </Card>,
    );
    const el = container.firstChild as HTMLElement;
    expect(el).not.toHaveClass("ui-card-accent");
    expect(el).not.toHaveClass("ui-card-pad");
  });

  it("fires onClick", () => {
    const onClick = vi.fn();
    render(
      <Card onClick={onClick}>
        <span>hit</span>
      </Card>,
    );
    fireEvent.click(screen.getByText("hit"));
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});

describe("Panel", () => {
  it("renders a header only when a title or actions is given", () => {
    const { container, rerender } = render(<Panel>just body</Panel>);
    expect(container.querySelector(".ui-panel-head")).toBeNull();
    expect(screen.getByText("just body")).toBeTruthy();
    rerender(
      <Panel title="Hosts" actions={<button type="button">Refresh</button>}>
        body
      </Panel>,
    );
    expect(container.querySelector(".ui-panel-head")).not.toBeNull();
    expect(screen.getByRole("heading", { name: "Hosts" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
  });
});

describe("Skeleton", () => {
  it("is a busy status region; circle vs line variant", () => {
    const { rerender, container } = render(<Skeleton width="50%" />);
    const el = () => container.firstChild as HTMLElement;
    expect(el()).toHaveAttribute("role", "status");
    expect(el()).toHaveAttribute("aria-busy", "true");
    expect(el()).toHaveClass("ui-skeleton-line");
    rerender(<Skeleton circle width={32} />);
    expect(el()).toHaveClass("ui-skeleton-circle");
  });
});

describe("StatusDot", () => {
  it("is decorative (aria-hidden) by default and maps tone + pulse", () => {
    const { container } = render(<StatusDot tone="success" pulse />);
    const el = container.firstChild as HTMLElement;
    expect(el).toHaveClass("ui-dot", "ui-dot-success", "ui-dot-pulse");
    expect(el).toHaveAttribute("aria-hidden", "true");
  });

  it("exposes a label to assistive tech as an img role", () => {
    render(<StatusDot tone="info" label="live" />);
    expect(screen.getByRole("img", { name: "live" })).toBeTruthy();
  });
});
