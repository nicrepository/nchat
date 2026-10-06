/**
 * PresenceDot — the one presence indicator (RF-58).
 *
 * Every surface that shows whether someone is around draws this, so the colour
 * mapping, the size ratios, the ring that separates it from a photo and the
 * hover text have exactly one definition. Before it there were three
 * hand-rolled dots that had already drifted apart on the away colour.
 *
 * It is decorative markup on purpose. The dot is `aria-hidden` and the word it
 * stands for is supplied by whoever places it — as part of a row's accessible
 * name, or as visible text next to the avatar. That is what stops a screen
 * reader from hearing "Online" twice for one person, and what makes the state
 * readable without colour: there is no surface where this dot is the only thing
 * saying it.
 *
 * `unknown` renders nothing. A grey dot means "this person is offline", which
 * is a claim; before the server has answered there is nothing to claim.
 *
 * Colour is never the only difference between states (issue #798): Do Not
 * Disturb carries a bar that Busy does not, Volto já a hollow centre that
 * Ausente does not, and Offline is an outline rather than a fill.
 *
 * The dot is absolutely positioned inside the avatar it decorates, so it never
 * changes the avatar's box: appearing, disappearing or changing colour moves no
 * layout.
 */

import "./PresenceDot.css";
import { presenceLabel, type PresenceState } from "./presence";

export interface PresenceDotProps {
  state: PresenceState;
  /**
   * Matches the avatar it sits on. Sizes are relative steps, not pixels, so a
   * caller never has to know the dot's geometry.
   */
  size?: "sm" | "md" | "lg";
  /**
   * The colour the dot's ring blends into. Defaults to the panel surface; the
   * sidebar passes its own darker background.
   */
  ringColor?: string;
  /**
   * The hover text, when the caller has more to say than the state's word —
   * "Ocupado · Em chamada", "Offline · visto hoje às 15:42". Never the only
   * place that information is: it is a mouse affordance.
   */
  title?: string;
  /**
   * Beside a word in a line of text rather than pinned to an avatar: the same
   * shapes and colours, in the flow. Every presence indicator in the product is
   * this component, so no surface keeps its own idea of what a state looks like.
   */
  inline?: boolean;
}

export default function PresenceDot({
  state,
  size = "sm",
  ringColor,
  title,
  inline = false,
}: PresenceDotProps) {
  if (state === "unknown") return null;
  return (
    <span
      className={`presence-dot presence-dot--${state} presence-dot--${size}${inline ? " presence-dot--inline" : ""}`}
      data-testid="presence-dot"
      data-presence={state}
      // The native tooltip: hover text with no popup to position, nothing to
      // leave open on unmount, nothing to clip against a panel's overflow, and
      // it follows the platform's own timing. The state is already announced
      // through the accessible name, so this adds a mouse affordance rather
      // than being the only way to read it.
      title={title ?? presenceLabel(state)}
      style={ringColor ? { borderColor: ringColor } : undefined}
      aria-hidden="true"
    />
  );
}
