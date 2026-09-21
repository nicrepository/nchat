/**
 * ExpandableDetailsSection — the structural half of every collection section in
 * the conversation details panel (issue #892).
 *
 * The panel has four collections that behave identically and describe entirely
 * different things: members, participants, pinned messages, recent files. What
 * they share is a shape — a heading, a compact preview of the first few rows, a
 * control that reveals the rest into a bounded scrolling region, and the three
 * ways a remote collection can fail to be a list yet. What they do *not* share
 * is a single domain concept, so this component knows none of them.
 *
 * It receives rows already rendered by their own domain and never looks inside
 * them. It has no API client, no store, no hook of its own beyond `useState`
 * and `useId`, and no notion of who may see what: the panel decides what to
 * pass, the server decides what the panel may have, and neither decision is
 * reachable from here.
 *
 * Expansion state is per instance and lives in this component. Two sections
 * rendered side by side are two independent states by construction — there is
 * no map, no context and no store for one to read the other through — and a
 * caller that needs expansion to end with the conversation gives the element a
 * `key`, which is the remount React already offers rather than a reset protocol
 * invented here.
 *
 * Security: every slot is a ReactNode supplied by the caller and rendered as a
 * child. Nothing here is parsed, concatenated into markup or turned into a URL,
 * so there is no sink to inject into, and expanding reveals only rows the
 * caller already held — it never asks for more on its own.
 */

import { useId, useState, type ReactNode } from "react";

import "./ExpandableDetailsSection.css";

/**
 * How many rows the compact state shows. Stated once, here, so a consumer never
 * repeats the number to slice its own collection: the section slices, and a
 * consumer that needs a different cap passes `collapsedLimit`.
 */
export const defaultCollapsedLimit = 5;

/**
 * What the caller has, as one discriminated value rather than a set of flags.
 *
 * Three independent booleans would make "loading and errored", "empty while
 * still loading" and "ready with an error message" all representable, and the
 * panel would have to keep proving they never happen. Here they are not
 * spellable.
 *
 * In the ready case the three figures are deliberately separate, and exactly one
 * of them is about *presentation* while the other two are about *capability*:
 *  - `items` is what the caller actually holds and can render right now;
 *  - `count` is the collection's true total when the caller knows it, which for
 *    a server-capped preview is larger than `items.length`. It is shown beside
 *    the heading and it is nothing else. Knowing that forty people are online
 *    is not the same as being able to list them, so a total larger than `items`
 *    never turns into an expand control — see `canExpand` below;
 *  - `hasMore` is the caller stating that rows it does not hold can still be
 *    fetched, for a collection whose total may not be known at all. It is a
 *    claim about a capability, and it only counts as one when the caller has
 *    also supplied the `onExpand` that performs the load.
 */
export type ExpandableSectionContent =
  | { status: "loading"; message: ReactNode }
  | { status: "error"; message: ReactNode }
  | {
      status: "ready";
      /** One node per row, keyed by the caller; rendered as children of the list. */
      items: readonly ReactNode[];
      /** Shown instead of the list when there is genuinely nothing. */
      empty: ReactNode;
      /**
       * The collection's total, when known. Presentation only: it is drawn
       * beside the heading and never decides whether the section can expand.
       */
      count?: number;
      /**
       * True when the caller can still load rows it does not hold yet. Only
       * acted on together with `onExpand`, which is what does the loading.
       */
      hasMore?: boolean;
    };

/**
 * A one-line section message: the loading line, the failure line, an empty
 * state. `role` is what separates them — a loading line announces itself, a
 * failure interrupts, and static prose does neither.
 */
export function SectionMessage({
  children,
  role,
}: {
  children: ReactNode;
  role?: "status" | "alert";
}) {
  return (
    <p className="chat-details__note" role={role}>
      {children}
    </p>
  );
}

/**
 * Whether more rows can actually be fetched.
 *
 * `hasMore` alone is a claim, not an ability: a caller that says more exists
 * but supplies no loader has described a section that can never produce those
 * rows, and a control offering them would promise what nothing can deliver. The
 * loader being present is what makes the claim actionable, so the two are read
 * together and never apart.
 */
function canLoadMore(content: ExpandableSectionContent, onExpand?: () => void): boolean {
  return content.status === "ready" && content.hasMore === true && onExpand !== undefined;
}

/**
 * Whether the control has anything to reveal.
 *
 * Two, and only two, things qualify: rows the caller already holds and the
 * compact cap is hiding, and rows the caller can actually go and fetch. A
 * larger `count` is neither. It says how many exist somewhere, which is exactly
 * the question "can this section show them?" does not answer — a channel can
 * report forty people online and hand over the thirty its preview is capped at,
 * and no amount of expanding will produce the other ten. Treating that total as
 * a capability is how "Ver todos" became a button that expanded to the same
 * list it started from, which is the affordance issue #892 exists to delete.
 *
 * An empty collection is never expandable either: there would be nothing to
 * reveal, and `aria-controls` would point at a list that is not in the DOM.
 */
function hasHiddenContent(
  content: ExpandableSectionContent,
  collapsedLimit: number,
  loadable: boolean,
): boolean {
  if (content.status !== "ready" || content.items.length === 0) return false;
  return content.items.length > collapsedLimit || loadable;
}

interface SectionBodyProps {
  content: ExpandableSectionContent;
  listId: string;
  listLabel: string;
  collapsedLimit: number;
  expanded: boolean;
}

/**
 * The part below the heading: one line, or the list.
 *
 * The list element is the same node collapsed and expanded — only its class and
 * its slice change — so expanding is not a remount and a row that was on screen
 * stays the same element.
 *
 * `tabIndex` appears only while expanded, which is the only state that scrolls.
 * A scrollable region with no focusable rows is unreachable by keyboard without
 * it (WCAG 2.1.1), and carrying it while collapsed would add a tab stop to a
 * region that has nothing to scroll.
 */
function SectionBody({ content, listId, listLabel, collapsedLimit, expanded }: SectionBodyProps) {
  if (content.status === "loading") {
    return <SectionMessage role="status">{content.message}</SectionMessage>;
  }
  if (content.status === "error") {
    return <SectionMessage role="alert">{content.message}</SectionMessage>;
  }
  if (content.items.length === 0) return <>{content.empty}</>;
  return (
    <ul
      id={listId}
      className={`chat-details__collection${expanded ? " chat-details__collection--expanded" : ""}`}
      aria-label={listLabel}
      tabIndex={expanded ? 0 : undefined}
    >
      {expanded ? content.items : content.items.slice(0, collapsedLimit)}
    </ul>
  );
}

export interface ExpandableDetailsSectionProps {
  /** The visible heading, e.g. "Participantes". */
  title: string;
  /** The list's accessible name, e.g. "Participantes do grupo". */
  listLabel: string;
  content: ExpandableSectionContent;
  /** Defaults to {@link defaultCollapsedLimit}. */
  collapsedLimit?: number;
  /**
   * The words on the control, when "Ver todos" would overstate what expanding
   * does (issue #895).
   *
   * A section whose `items` are a server-capped preview can reveal the rows it
   * holds and no more, so offering "all" of them is a promise it cannot keep.
   * The caller decides, because only the caller knows whether its collection is
   * complete — this component is handed rows and a count and has no way to tell
   * a whole collection from a page of one. It stays ignorant of what the rows
   * are: these are two strings, not a mode, and nothing here inspects them.
   *
   * Both default to the existing wording, so every section that says nothing
   * keeps reading "Ver todos" / "Mostrar menos".
   */
  expandLabel?: string;
  collapseLabel?: string;
  /**
   * Loads the rows `content.hasMore` promises. Supplying it is what turns that
   * promise into an offer the section is allowed to make; without it `hasMore`
   * is inert and no control appears.
   *
   * Called on a collapsed → expanded transition and only while there is
   * something remote left to load — never on a render, never on collapse, and
   * never for an expansion that merely uncaps rows already held, which needs no
   * fetch. Performing the request, deduplicating it and appending the result
   * are entirely the caller's; new rows arriving as `items` do not disturb the
   * expansion. A caller that leaves `hasMore` true after a page will be asked
   * again on the next expansion, which is the retry; one that sets it false
   * will not.
   */
  onExpand?: () => void;
  /** Rendered after the content: a retry, an action, a "loading more" line. */
  children?: ReactNode;
}

export default function ExpandableDetailsSection({
  title,
  listLabel,
  content,
  collapsedLimit = defaultCollapsedLimit,
  expandLabel = "Ver todos",
  collapseLabel = "Mostrar menos",
  onExpand,
  children,
}: ExpandableDetailsSectionProps) {
  // Generated per instance, so two sections on screen collide neither in their
  // heading association nor in what their toggles control.
  const headingId = useId();
  const toggleTextId = useId();
  const listId = useId();
  const [expanded, setExpanded] = useState(false);

  const loadable = canLoadMore(content, onExpand);
  const expandable = hasHiddenContent(content, collapsedLimit, loadable);

  function toggle() {
    const next = !expanded;
    setExpanded(next);
    // Outside the state updater on purpose: an updater may run twice, and a
    // caller that fetches here would fetch twice for one click.
    //
    // `loadable` and not just `next`: an expansion that only uncaps rows the
    // caller already holds has nothing to load, and asking for a page there
    // would be a request nobody is waiting for.
    if (next && loadable) onExpand?.();
  }

  return (
    <section className="chat-details__section" aria-labelledby={headingId}>
      <div className="chat-details__section-head">
        <h3 id={headingId} className="chat-details__label">
          {title}
          {content.status === "ready" && content.count !== undefined && ` (${content.count})`}
        </h3>
        {expandable && (
          <button
            type="button"
            className="chat-details__link-action"
            aria-expanded={expanded}
            aria-controls={listId}
            /*
              The label on its own says nothing about what of, and every
              section's control would share one name. Pointing at the visible
              label *and* the heading names it in context — "Ver todos Membros
              online (3)" — without the caller restating the section in a second
              prop, and without this component inventing Portuguese grammar for
              a noun it has never seen.
            */
            aria-labelledby={`${toggleTextId} ${headingId}`}
            onClick={toggle}
          >
            <span id={toggleTextId}>{expanded ? collapseLabel : expandLabel}</span>
          </button>
        )}
      </div>
      <SectionBody
        content={content}
        listId={listId}
        listLabel={listLabel}
        collapsedLimit={collapsedLimit}
        expanded={expanded}
      />
      {children}
    </section>
  );
}
