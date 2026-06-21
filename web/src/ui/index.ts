// Conductor UI primitives (V1) — the single import surface for token-driven,
// accessible building blocks. Importing the barrel also loads the primitive
// styles (ui.css), so consumers get both the component and its look.
//
// Surfaces (V2+) compose from here instead of hand-rolling buttons/cards/badges.
import "./ui.css";

// Note: cx() is intentionally NOT re-exported here — keeping this barrel
// component+type-only satisfies react-refresh/only-export-components. Import the
// helper directly from "../ui/cx.ts" where a surface needs it.
export { Button, type ButtonProps, type ButtonVariant, type ButtonSize } from "./Button.tsx";
export { IconButton, type IconButtonProps } from "./IconButton.tsx";
export { Badge, type BadgeProps, type BadgeTone } from "./Badge.tsx";
export { Chip, type ChipProps } from "./Chip.tsx";
export { Card, type CardProps, type CardAccent } from "./Card.tsx";
export { Panel, type PanelProps } from "./Panel.tsx";
export { Skeleton, type SkeletonProps } from "./Skeleton.tsx";
export { StatusDot, type StatusDotProps, type DotTone } from "./StatusDot.tsx";
