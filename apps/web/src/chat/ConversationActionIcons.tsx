/**
 * The icons the row action menu draws (issue #527).
 *
 * Inline SVG in the same shape as ChatSidebar's own icon set — 24×24 viewBox,
 * `currentColor`, `stroke-width: 2`, `aria-hidden` — rather than a new icon
 * dependency. Each is decorative: the menu item's text is its accessible name,
 * so nothing here is announced twice.
 */

import type { ConversationActionIcon } from "./conversationActions";

interface IconProps {
  className?: string;
}

function Svg({ className, children }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

const icons: Record<ConversationActionIcon, (props: IconProps) => React.ReactElement> = {
  pin: (props) => (
    <Svg {...props}>
      <path d="M12 17v5M7 3h10l-2 5v4l3 3H6l3-3V8L7 3Z" />
    </Svg>
  ),
  check: (props) => (
    <Svg {...props}>
      <polyline points="20 6 9 17 4 12" />
    </Svg>
  ),
  bell: (props) => (
    <Svg {...props}>
      <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
      <path d="M13.73 21a2 2 0 0 1-3.46 0" />
    </Svg>
  ),
  "bell-off": (props) => (
    <Svg {...props}>
      <path d="M13.73 21a2 2 0 0 1-3.46 0" />
      <path d="M18.63 13A17.89 17.89 0 0 1 18 8" />
      <path d="M6.26 6.26A5.86 5.86 0 0 0 6 8c0 7-3 9-3 9h14" />
      <path d="M18 8a6 6 0 0 0-9.33-5" />
      <line x1="1" y1="1" x2="23" y2="23" />
    </Svg>
  ),
  pencil: (props) => (
    <Svg {...props}>
      <path d="M17 3a2.83 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z" />
    </Svg>
  ),
  info: (props) => (
    <Svg {...props}>
      <circle cx="12" cy="12" r="10" />
      <line x1="12" y1="16" x2="12" y2="12" />
      <line x1="12" y1="8" x2="12.01" y2="8" />
    </Svg>
  ),
  logout: (props) => (
    <Svg {...props}>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <polyline points="16 17 21 12 16 7" />
      <line x1="21" y1="12" x2="9" y2="12" />
    </Svg>
  ),
};

/**
 * Renders one action's icon by name.
 *
 * A lookup rather than a switch so adding an action is a data change in
 * conversationActions.ts plus one entry here, and never a new branch in the menu
 * component itself.
 */
export function ConversationActionGlyph({
  icon,
  className,
}: {
  icon: ConversationActionIcon;
  className?: string;
}) {
  const Glyph = icons[icon];
  return <Glyph className={className} />;
}
