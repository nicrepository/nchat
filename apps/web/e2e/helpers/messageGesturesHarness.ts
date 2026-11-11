import { createElement, StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { useMessageTouchGestures } from "../../src/chat/useMessageTouchGestures";

const harness = window as unknown as {
  events: string[];
  mountRow: (id: string, parent: HTMLElement) => void;
  ready: boolean;
};

harness.events = [];

function Row({ id }: { id: string }) {
  const handlers = useMessageTouchGestures({
    messageId: id,
    disabled: false,
    swipeBlocked: false,
    onLongPress: (messageId) => harness.events.push("long:" + messageId),
    onSwipeReply: (messageId) => harness.events.push("reply:" + messageId),
  });
  return createElement("div", { className: "row", "data-row": id, ...handlers }, "mensagem " + id);
}

harness.mountRow = (id, parent) => {
  const host = document.createElement("div");
  parent.append(host);
  createRoot(host).render(createElement(StrictMode, null, createElement(Row, { id })));
};
harness.ready = true;
