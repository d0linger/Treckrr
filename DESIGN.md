---
name: Treckrr
description: Field-ready Werkblatt UI for agricultural neighbour-work billing.
colors:
  iron-green: "#115638"
  action-orange: "#e05910"
  danger-crimson: "#a4262e"
  paper: "#edf1ec"
  sheet: "#f9fbf9"
  quiet-sheet: "#eff2ef"
  ink: "#19211d"
  muted-ink: "#57645c"
  hairline: "#ccd4ce"
  night-ground: "#0c1511"
  night-sheet: "#19221d"
  night-ink: "#dbe5de"
  mint-selection: "#85d7a9"
  night-action-orange: "#ff8142"
  focus-blue: "#1f6feb"
typography:
  display:
    fontFamily: "JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: "1.9rem"
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: "-0.01em"
  title:
    fontFamily: "JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: "1.25rem"
    fontWeight: 700
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  body:
    fontFamily: "Hanken Grotesk, system-ui, -apple-system, Segoe UI, Roboto, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.45
  label:
    fontFamily: "JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: "0.04em"
rounded:
  plate: "6px"
  sheet: "10px"
components:
  button-primary:
    backgroundColor: "{colors.action-orange}"
    textColor: "{colors.ink}"
    typography: "{typography.label}"
    rounded: "{rounded.plate}"
    padding: "0.6rem 1.05rem"
    height: "44px"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.iron-green}"
    typography: "{typography.label}"
    rounded: "{rounded.plate}"
    padding: "0.6rem 1.05rem"
    height: "44px"
  card:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sheet}"
    padding: "0.9rem 1rem"
  input:
    backgroundColor: "{colors.quiet-sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.plate}"
    padding: "0.65rem 0.75rem"
    height: "44px"
---

# Design System: Treckrr

## Overview

**Creative North Star: "The Working Werkblatt"**

Treckrr should feel like a well-kept machine ledger brought into the field: technical, calm, compact, and trustworthy. Blueprint paper, stamped mono labels, restrained green structure, and precise tabular figures create character without competing with operational work.

The interface is dense enough for billing and audit tasks but never cramped. Decoration stays quiet and static; state, hierarchy, and readable numbers carry the experience.

**Key Characteristics:**

- Blueprint-paper ground with crisp bordered sheets.
- Mono machine-ledger headings and figures over a humanist body face.
- Deep green structure, mint selection, orange action, and crimson danger.
- Mobile-first controls sized for field use, with compact desktop density.

## Colors

The palette reads as technical paper and agricultural machinery: green provides structure, while saturated colors are reserved for meaning.

### Primary

- **Iron Green:** navigation, links, data marks, and stable brand structure.
- **Mint Selection:** active navigation and positive selection states on dark green surfaces.

### Secondary

- **Action Orange:** the single high-energy signal for primary actions.
- **Danger Crimson:** destructive intent; use the separate high-contrast text token for labels and icons.

### Neutral

- **Paper and Sheet:** green-tinted page ground with opaque work surfaces and quiet nested fields.
- **Ink and Muted Ink:** primary content and supporting metadata; muted text must remain AA-readable.
- **Night Ground, Night Sheet, and Night Ink:** dark-theme equivalents, never simple inversion.

### Named Rules

**The One Signal Rule.** Orange is for the page's primary action, not navigation, decoration, or ordinary selection.

**The Semantic Foreground Rule.** Fill tokens may define borders and solid controls; text and icons use their contrast-safe foreground token.

## Typography

**Display Font:** JetBrains Mono (with system monospace fallbacks)

**Body Font:** Hanken Grotesk (with system UI fallbacks)

**Label/Mono Font:** JetBrains Mono

**Character:** Mono type supplies the stamped machine-ledger voice for headings, labels, KPIs, and money. Hanken Grotesk keeps instructions, forms, and longer content approachable.

### Hierarchy

- **Display:** bold mono for major monetary totals and the login brand.
- **Title:** bold mono for page and section titles.
- **Body:** regular humanist sans at the browser base size with a relaxed line height.
- **Label:** bold, tracked mono; uppercase only for short operational labels and actions.

### Named Rules

**The Ledger Voice Rule.** Use mono where operators scan identity, state, time, or money; use the body face where they must read a sentence.

## Layout

The application is mobile-first. Authenticated pages use a bottom tab bar on narrow screens and dock the same navigation as a 212px left rail at 1024px. The sticky app bar and optional year bar form one chassis. Primary content stays in a centered 720px work column; dense datasets may use responsive grids or controlled horizontal table scrolling.

Use a compact rhythm built from roughly 4px, 8px, 12px, and 16px increments. Shared controls are at least 44px high on narrow or coarse-pointer devices. At 480px, redundant app-bar actions may yield to their drawer equivalent so titles remain legible.

## Elevation & Depth

Depth is structural, not atmospheric. Cards use a hairline and a near-flat low shadow; stronger neutral shadows belong only to true overlays, the sticky chassis, and transient feedback. Primary buttons use a neutral contact shadow, never an orange halo.

**The Flat-by-Default Rule.** Prefer boundaries and tonal layers at rest; reserve visible elevation for overlays or active interaction.

## Shapes

Sheets use gently technical 10px corners; controls and small plates use 6–8px corners. Pills are reserved for compact states, years, and filters. Hairlines separate work areas. Narrow accent rails are acceptable only when they encode a stable role or status already present in the Werkblatt grammar.

## Components

### Buttons

- **Shape:** compact plate corners with a 44px interaction floor.
- **Primary:** orange signal fill with dark ink; normally one dominant action per region.
- **Hover / Focus:** restrained brightness or neutral elevation; focus uses the blue two-pixel ring and visible offset.
- **Ghost / Danger:** ghost actions remain outlined; danger is crimson outline until the final destructive confirmation.

### Chips

- **Style:** compact mono labels with a tint, border, and contrast-safe semantic foreground.
- **State:** text or glyph must name the state; color only reinforces it.

### Cards / Containers

- **Corner Style:** sheet corners.
- **Background:** opaque sheet over the blueprint ground.
- **Shadow Strategy:** nearly flat at rest.
- **Border:** one-pixel hairline.
- **Internal Padding:** compact, normally around 12–16px.

### Inputs / Fields

- **Style:** quiet nested sheet, hairline border, plate corners, and explicit labels.
- **Focus:** orange border emphasis plus the shared blue focus-visible ring where appropriate.
- **Error / Disabled:** contrast-safe crimson explanation; disabled state remains readable and clearly unavailable.

### Navigation

The chassis uses mono labels and explicit active state. Mobile uses four bottom destinations; desktop turns them into numbered rail entries. Active links carry `aria-current`, and drawer/palette overlays contain and restore keyboard focus.

### Werkblatt Backdrop

Application canvases choose from quiet technical-paper compositions. They paint once and repaint only for resize or theme changes. The login worksheet retains its travelling vertical light band and synchronized machine highlights, capped at 25 fps and paused while hidden. Its timer fallback recovers visible Windows/RDP sessions when animation frames stall. The login sweep preserves its established behavior independently of Windows' reduced-motion signal; other UI motion still honors reduced-motion preferences.

## Do's and Don'ts

### Do:

- **Do** keep orange rare and action-specific.
- **Do** use mono type for scan-heavy operational data and short labels.
- **Do** preserve 44px controls for mobile and coarse pointers.
- **Do** provide explicit empty, loading, failure, focus, and reduced-motion behavior.

### Don't:

- **Don't** introduce continuous decorative animation beyond the established login sweep, or colored glow shadows.
- **Don't** use danger fill colors directly for text or icons.
- **Don't** add generic dashboard decoration that does not encode data or state.
- **Don't** replace the bottom-tab/desktop-rail information architecture without product evidence.
