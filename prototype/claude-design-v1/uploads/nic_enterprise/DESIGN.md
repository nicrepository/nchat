---
name: NIC Enterprise
colors:
  surface: "#fef7ff"
  surface-dim: "#dfd7e5"
  surface-bright: "#fef7ff"
  surface-container-lowest: "#ffffff"
  surface-container-low: "#f9f1ff"
  surface-container: "#f3ebf9"
  surface-container-high: "#ede5f3"
  surface-container-highest: "#e8e0ee"
  on-surface: "#1d1a24"
  on-surface-variant: "#4a4455"
  inverse-surface: "#332f39"
  inverse-on-surface: "#f6eefc"
  outline: "#7b7486"
  outline-variant: "#ccc3d7"
  surface-tint: "#7331df"
  primary: "#5300b7"
  on-primary: "#ffffff"
  primary-container: "#6d28d9"
  on-primary-container: "#dac5ff"
  inverse-primary: "#d3bbff"
  secondary: "#6b38d4"
  on-secondary: "#ffffff"
  secondary-container: "#8455ef"
  on-secondary-container: "#fffbff"
  tertiary: "#6b3000"
  on-tertiary: "#ffffff"
  tertiary-container: "#8f4200"
  on-tertiary-container: "#ffc19e"
  error: "#ba1a1a"
  on-error: "#ffffff"
  error-container: "#ffdad6"
  on-error-container: "#93000a"
  primary-fixed: "#ebddff"
  primary-fixed-dim: "#d3bbff"
  on-primary-fixed: "#250059"
  on-primary-fixed-variant: "#5b00c5"
  secondary-fixed: "#e9ddff"
  secondary-fixed-dim: "#d0bcff"
  on-secondary-fixed: "#23005c"
  on-secondary-fixed-variant: "#5516be"
  tertiary-fixed: "#ffdbc8"
  tertiary-fixed-dim: "#ffb68b"
  on-tertiary-fixed: "#321300"
  on-tertiary-fixed-variant: "#743400"
  background: "#fef7ff"
  on-background: "#1d1a24"
  surface-variant: "#e8e0ee"
typography:
  display-lg:
    fontFamily: Inter
    fontSize: 48px
    fontWeight: "700"
    lineHeight: 56px
    letterSpacing: -0.02em
  headline-lg:
    fontFamily: Inter
    fontSize: 32px
    fontWeight: "600"
    lineHeight: 40px
    letterSpacing: -0.01em
  headline-lg-mobile:
    fontFamily: Inter
    fontSize: 24px
    fontWeight: "600"
    lineHeight: 32px
  title-lg:
    fontFamily: Inter
    fontSize: 20px
    fontWeight: "600"
    lineHeight: 28px
  title-md:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: "600"
    lineHeight: 24px
  body-lg:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: "400"
    lineHeight: 24px
  body-md:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: "400"
    lineHeight: 20px
  label-md:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: "500"
    lineHeight: 16px
    letterSpacing: 0.05em
  code-md:
    fontFamily: jetbrainsMono
    fontSize: 14px
    fontWeight: "400"
    lineHeight: 20px
rounded:
  sm: 0.25rem
  DEFAULT: 0.5rem
  md: 0.75rem
  lg: 1rem
  xl: 1.5rem
  full: 9999px
spacing:
  base: 8px
  xs: 4px
  sm: 12px
  md: 16px
  lg: 24px
  xl: 32px
  sidebar-width: 280px
  max-content-width: 1200px
---

## Brand & Style

The design system is engineered for high-performance enterprise communication. It balances technical precision with a modern, sophisticated aesthetic to facilitate deep work and clear information hierarchy. The brand personality is authoritative yet innovative, positioning itself as a mission-critical tool rather than a casual utility.

The design style follows a **Corporate / Modern** approach with elements of **Minimalism**. It prioritizes clarity through generous whitespace, a structured color palette, and high-contrast navigation. By utilizing a dark, high-contrast sidebar against a light, airy workspace, the system creates a clear mental model of "navigation vs. execution." The interface evokes a sense of reliability and architectural order, ensuring that even complex data-heavy interactions remain intuitive.

## Colors

The palette is anchored by a deep **Vibrant Purple** (Primary) that signals action and importance.

- **Primary & Accent:** Used for primary actions, active indicators, and brand touchpoints. The lilás accent provides a softer transition for hover states or secondary active elements.
- **Surface Strategy:** The main workspace utilizes pure white for maximum legibility, while a very light gray (Surface-Variant) provides subtle grounding for containers and backgrounds.
- **High Contrast Navigation:** The **Inverse-Surface** is reserved for the global sidebar. This creates a powerful visual anchor, clearly separating system-level navigation from content.
- **Typography:** All primary text uses a Graphite dark gray to reduce eye strain while maintaining a high AA/AAA contrast ratio against white surfaces.

## Typography

This design system uses **Inter** as its primary typeface across all levels. Inter is chosen for its exceptional legibility at small sizes and its neutral, systematic feel.

- **Headlines:** Utilize tighter letter-spacing and heavier weights to establish a clear hierarchy.
- **Body Text:** Standardized at 14px and 16px to ensure comfort during long reading sessions.
- **Labels:** Uppercase or medium-weight labels are used for metadata and overlines to distinguish them from interactive text.
- **Data/Code:** For chat-based snippets or technical data, **JetBrains Mono** (secondary) is introduced to provide a clear distinction from conversational text.

## Layout & Spacing

The layout is based on an **8px grid system**, ensuring mathematical consistency across all components.

- **Sidebar:** A fixed-width dark sidebar on the left provides immediate access to workspaces and channels.
- **Main Content:** A fluid grid that centers content when the viewport exceeds 1200px.
- **Margins & Gutters:** Desktop views utilize a 24px (lg) margin for the main container, while mobile views compress this to 16px (md).
- **Rhythm:** Vertical spacing between chat bubbles and interface elements should follow the 8px increments to maintain a disciplined, professional structure.

## Elevation & Depth

To maintain a "Modern Enterprise" feel, this design system avoids heavy drop shadows. Instead, it utilizes:

- **Tonal Tiers:** Differentiation between the sidebar (Inverse), the background (Surface-Variant), and the content cards (Surface) creates depth without the need for shadows.
- **Minimalist Shadows:** A single elevation level (Elevation 1) is used sparingly. It consists of a very soft, diffused shadow (Blur: 4px, Y: 2px, Opacity: 4%) only for floating elements like dropdown menus, tooltips, or active cards.
- **Ghost Borders:** 1px borders in a light gray (#e2e8f0) are used to define boundaries on light surfaces, ensuring a crisp, architectural look.

## Shapes

The design system employs a **Rounded** shape language to soften the corporate structure and make the interface more approachable.

- **Standard Elements:** Buttons, input fields, and chat bubbles use a 0.5rem (8px) radius.
- **Large Containers:** Modals and large card components use 1rem (16px) to emphasize they are distinct layers.
- **Pill Elements:** Status indicators (Chips) and search bars may use fully rounded (pill) shapes to distinguish them as high-utility, interactive elements.

## Components

- **Buttons:**
  - _Primary:_ Solid #6d28d9 with white text. 8px radius.
  - _Secondary:_ Ghost style with 1px border of #6d28d9 or subtle gray backgrounds.
- **Input Fields:** Use Surface-Variant (#f8fafc) background with a 1px border. Focus state triggers a 2px Primary border and a subtle glow.
- **Chat Bubbles:**
  - _User:_ Primary color with white text, aligned right.
  - _System/Other:_ Surface-Variant with On-Surface text, aligned left.
- **Sidebar Nav:** High contrast text on #1e1b4b. Active states use a "Vertical Bar" indicator in Accent (#8b5cf6) on the left edge.
- **Cards:** White background, 1px subtle border, 8px radius. No shadow unless hovered.
- **Chips:** Small, 12px font-size, 4px vertical padding, pill-shaped, used for categorization and status.
- **Lists:** Clean rows with 1px bottom dividers. Hover states should use a subtle gray background shift to indicate interactivity.
