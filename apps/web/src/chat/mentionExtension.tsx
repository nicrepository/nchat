import Mention from "@tiptap/extension-mention";
import { ReactRenderer } from "@tiptap/react";
import { fetchMentionCandidates } from "./chatApi";
import type { MentionCandidate } from "./chatTypes";
import { ALL_MENTION_ID } from "./richTextMarkers";
import MentionList from "./MentionList";
import type { MentionListRef } from "./MentionList";

// viewportMargin keeps the popup a small gap away from the viewport edge
// so it never touches the browser chrome edge-to-edge.
const viewportMargin = 8;

// Debounce delay for the mention-candidate fetch, so a normal typing burst
// (e.g. "joao") issues one request instead of one per keystroke — reduces
// load and keeps well under the 30/min rate limit on the mentions endpoint.
const mentionFetchDebounceMs = 150;

// Synthetic — never fetched from the server, unlike every other candidate.
// Prepended client-side so @all is discoverable the same way a person is:
// typing "@" and narrowing by name.
const ALL_CANDIDATE: MentionCandidate = { mentionType: "all", id: ALL_MENTION_ID, label: "all" };

function withAllCandidate(query: string, candidates: MentionCandidate[]): MentionCandidate[] {
  const matches = ALL_CANDIDATE.label.startsWith(query.toLowerCase());
  return matches ? [ALL_CANDIDATE, ...candidates] : candidates;
}

function positionPopup(element: HTMLElement, clientRect?: (() => DOMRect | null) | null) {
  const rect = clientRect?.();
  if (!rect) return;

  // The composer sits at the bottom of the message area, so the caret is
  // frequently close to the viewport's bottom edge. Opening the popup
  // downward unconditionally (the old behavior) clipped it against the
  // viewport, making it look cut off / overlapping neighboring elements.
  // Flip it above the caret when there isn't enough room below.
  const { offsetWidth: popupWidth, offsetHeight: popupHeight } = element;
  const spaceBelow = window.innerHeight - rect.bottom;
  const top =
    popupHeight > 0 && spaceBelow < popupHeight + viewportMargin
      ? rect.top - popupHeight - 6
      : rect.bottom + 6;
  const left = Math.min(rect.left, window.innerWidth - popupWidth - viewportMargin);

  element.style.top = `${Math.max(viewportMargin, top)}px`;
  element.style.left = `${Math.max(viewportMargin, left)}px`;
}

export function createMentionExtension() {
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let pendingResolvers: Array<(candidates: MentionCandidate[]) => void> = [];
  let activeAbort: AbortController | undefined;

  function flushPending(candidates: MentionCandidate[]) {
    const resolvers = pendingResolvers;
    pendingResolvers = [];
    resolvers.forEach((resolve) => resolve(candidates));
  }

  function cancelPendingFetch() {
    if (debounceTimer !== undefined) {
      clearTimeout(debounceTimer);
      debounceTimer = undefined;
    }
    // Defense in depth: clear the timer before it fires, and also abort the
    // HTTP request if it's already in flight (e.g. channel switched mid-fetch).
    activeAbort?.abort();
    activeAbort = undefined;
    // Every caller must eventually settle — TipTap's suggestion plugin awaits
    // items() as part of its own async state-update cycle, so leaving a
    // resolver unresolved (e.g. when the popup exits mid-debounce) would
    // hang that update indefinitely.
    flushPending([]);
  }

  // Debounces fetchMentionCandidates: each keystroke resets the timer, and
  // every caller within the debounce window shares the single fetch's
  // result, so a normal typing burst (e.g. "joao") issues one request
  // instead of one per keystroke.
  function debouncedFetchMentionCandidates(
    channelId: string,
    query: string,
  ): Promise<MentionCandidate[]> {
    if (debounceTimer !== undefined) clearTimeout(debounceTimer);
    return new Promise((resolve) => {
      pendingResolvers.push(resolve);
      debounceTimer = setTimeout(() => {
        debounceTimer = undefined;
        activeAbort?.abort();
        const controller = new AbortController();
        activeAbort = controller;
        fetchMentionCandidates(channelId, query, controller.signal)
          .then((candidates) => flushPending(withAllCandidate(query, candidates)))
          .catch((err: unknown) => {
            if (err instanceof Error && err.name === "AbortError") return;
            flushPending([]);
          });
      }, mentionFetchDebounceMs);
    });
  }

  return Mention.extend({
    addAttributes() {
      return {
        ...this.parent?.(),
        mentionType: {
          default: "user",
          parseHTML: (element) => element.getAttribute("data-mention-type"),
          renderHTML: (attributes) => ({ "data-mention-type": attributes.mentionType }),
        },
      };
    },
  }).configure({
    HTMLAttributes: { class: "chat-mention", "data-type": "mention" },
    renderText: ({ node }) => `@${node.attrs.label ?? node.attrs.id}`,
    renderHTML: ({ node, options }) => [
      "span",
      {
        ...options.HTMLAttributes,
        "data-type": "mention",
        "data-id": node.attrs.id,
        "data-label": node.attrs.label,
        "data-mention-type": node.attrs.mentionType,
      },
      `@${node.attrs.label ?? node.attrs.id}`,
    ],
    suggestion: {
      char: "@",
      items: ({ query, editor }) => {
        const channelId = editor.storage.mentionChannelContext?.channelId as string | undefined;
        return channelId ? debouncedFetchMentionCandidates(channelId, query.slice(0, 64)) : [];
      },
      render: () => {
        let component: ReactRenderer<MentionListRef> | null = null;

        return {
          onStart: (props) => {
            component = new ReactRenderer(MentionList, { props, editor: props.editor });
            const element = component.element as HTMLElement;
            element.classList.add("mention-popup");
            document.body.appendChild(element);
            positionPopup(element, props.clientRect);
          },
          onUpdate: (props) => {
            component?.updateProps(props);
            if (component) positionPopup(component.element as HTMLElement, props.clientRect);
          },
          onKeyDown: ({ event }) => {
            if (event.key === "Escape") {
              component?.element.remove();
              return true;
            }
            return component?.ref?.onKeyDown(event) ?? false;
          },
          onExit: () => {
            cancelPendingFetch();
            component?.element.remove();
            component?.destroy();
            component = null;
          },
        };
      },
    },
  });
}
