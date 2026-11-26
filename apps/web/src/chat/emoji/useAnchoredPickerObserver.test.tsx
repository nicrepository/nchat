/**
 * The picker's ResizeObserver against a caller whose band can move (CQR R6 of
 * issue #1238): a touch sheet's band is chosen from what the picker shows, so
 * the catalog arriving can move it. Applied inside the observer's callback,
 * the new band's cap resized the observed picker there — a loop WebKit
 * reported as a page error. That placement waits for the next frame; one in
 * an unchanged band stays immediate.
 */
import { render } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { flushResizeObservers } from "../../setupTests";
import { pickerPlacedEvent, useAnchoredPicker, type VisibleBounds } from "./useAnchoredPicker";

function Harness(props: {
  bounds: () => VisibleBounds;
  onPlaced?: (picker: HTMLElement) => boolean | void;
  onDismiss?: (restoreFocus: boolean) => void;
  placeClear?: () => void;
  withClear?: boolean;
  onLost?: () => void;
  placeRef?: { current: () => void };
}) {
  const anchorRef = useRef<HTMLButtonElement>(null);
  const clearRef = useRef<HTMLDivElement>(null);
  const pickerRef = useAnchoredPicker({
    open: true,
    anchorRef,
    onLost: props.onLost,
    placeRef: props.placeRef,
    onDismiss: props.onDismiss ?? (() => {}),
    bounds: props.bounds,
    onPlaced: props.onPlaced,
    clearRef: props.withClear ? clearRef : undefined,
    placeClear: props.placeClear,
  });
  return (
    <>
      <div ref={clearRef} data-clear="">
        <button ref={anchorRef} type="button" data-anchor="" />
      </div>
      <div ref={pickerRef} />
    </>
  );
}

const box = (top: number, left: number, width: number, height: number) =>
  ({ top, left, width, height, bottom: top + height, right: left + width }) as DOMRect;

let frames: FrameRequestCallback[] = [];
// Where the box the picker clears is drawn: the caller's to move.
let clearBox: DOMRect;

beforeEach(() => {
  frames = [];
  clearBox = box(400, 0, 400, 60);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.push(callback);
    return frames.length;
  });
  vi.stubGlobal("cancelAnimationFrame", vi.fn());
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this.hasAttribute("data-clear")) return clearBox;
    return this.hasAttribute("data-anchor") ? box(400, 200, 40, 40) : box(0, 0, 100, 100);
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("re-places at once in an unchanged band, and a frame later in a moved one", () => {
  let band: VisibleBounds = { top: 0, bottom: 800, left: 0, right: 400 };
  const onPlaced = vi.fn();
  render(<Harness bounds={() => band} onPlaced={onPlaced} />);
  expect(onPlaced).toHaveBeenCalledTimes(1);

  flushResizeObservers();
  expect(onPlaced).toHaveBeenCalledTimes(2);
  expect(frames).toHaveLength(0);

  band = { ...band, top: 100 };
  flushResizeObservers();
  expect(onPlaced).toHaveBeenCalledTimes(2);
  expect(frames).toHaveLength(1);
  frames[0](0);
  expect(onPlaced).toHaveBeenCalledTimes(3);
});

// CQR R9 of issue #1238: what hangs off a cell inside the picker follows it.
it("tells the page, bubbling from the picker, each time it has been placed", () => {
  const placed = vi.fn();
  document.addEventListener(pickerPlacedEvent, placed);
  try {
    render(<Harness bounds={() => ({ top: 0, bottom: 800, left: 0, right: 400 })} />);
    expect(placed).toHaveBeenCalledTimes(1);
    expect((placed.mock.calls[0][0] as Event).target).toBeInstanceOf(HTMLDivElement);
    flushResizeObservers();
    expect(placed).toHaveBeenCalledTimes(2);
  } finally {
    document.removeEventListener(pickerPlacedEvent, placed);
  }
});

it("places once more when the caller reports it moved what the picker clears, then stops", () => {
  const onPlaced = vi.fn().mockReturnValueOnce(true).mockReturnValue(false);
  render(
    <Harness bounds={() => ({ top: 0, bottom: 800, left: 0, right: 400 })} onPlaced={onPlaced} />,
  );
  expect(onPlaced).toHaveBeenCalledTimes(2);
});

/*
 * CQR R7 of issue #1238: at 360×350 a search with results after one with none
 * shrank what the sheet needs, so its band moved up above the composer — while
 * the menu the picker clears still sat where the old band had put it, wholly
 * below the new one. Judged against that stale box, the picker took its anchor
 * for one scrolled out of sight and closed, focus and all. The caller that owns
 * the box now places it first; the verdict is on where it belongs.
 */
describe("a caller that places the box the picker clears", () => {
  const tall: VisibleBounds = { top: 0, bottom: 800, left: 0, right: 400 };
  const short: VisibleBounds = { top: 0, bottom: 300, left: 0, right: 400 };

  it("places it before judging it, so a band that moved does not close the picker", () => {
    let band = tall;
    const onDismiss = vi.fn();
    const placeClear = vi.fn(() => {
      clearBox = band === short ? box(220, 0, 400, 60) : box(700, 0, 400, 60);
    });
    clearBox = box(700, 0, 400, 60);
    render(<Harness bounds={() => band} onDismiss={onDismiss} placeClear={placeClear} withClear />);

    band = short;
    flushResizeObservers();
    // Deferred, as any placement in a band that moved: no timer, the next frame.
    expect(frames).toHaveLength(1);
    frames[0](0);
    expect(onDismiss).not.toHaveBeenCalled();
    expect(clearBox.bottom).toBeLessThanOrEqual(short.bottom);
  });

  it("still closes the picker when the box it clears is out of sight once placed", () => {
    const onDismiss = vi.fn();
    const placeClear = vi.fn(() => {
      clearBox = box(900, 0, 400, 60);
    });
    render(<Harness bounds={() => tall} onDismiss={onDismiss} placeClear={placeClear} withClear />);
    expect(placeClear).toHaveBeenCalled();
    expect(onDismiss).toHaveBeenCalledWith(false);
  });

  it("closes, without placing, against a stale box when no caller places it", () => {
    let band = tall;
    const onDismiss = vi.fn();
    clearBox = box(700, 0, 400, 60);
    render(<Harness bounds={() => band} onDismiss={onDismiss} withClear />);
    expect(onDismiss).not.toHaveBeenCalled();
    band = short;
    flushResizeObservers();
    frames[0](0);
    expect(onDismiss).toHaveBeenCalledWith(false);
  });

  // CQR R10 F-1: a caller with its own verdict on a lost anchor gets it there;
  // a press outside still closes through onDismiss.
  it("hands an anchor out of its band to onLost, leaving onDismiss for a press outside", () => {
    const onDismiss = vi.fn();
    const onLost = vi.fn();
    clearBox = box(900, 0, 400, 60);
    render(<Harness bounds={() => tall} onDismiss={onDismiss} onLost={onLost} withClear />);
    expect(onLost).toHaveBeenCalled();
    expect(onDismiss).not.toHaveBeenCalled();
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    expect(onDismiss).toHaveBeenCalledWith(false);
  });

  // CQR R10 F-1, WebKit after a rotation: the first reading, lost while the
  // caller confirms it, capped nothing. The observer's next placement — the
  // anchor back — is the first cap in that band, and waits for the frame.
  it("does not take a band judged lost for one placed in: the next cap waits for the frame", () => {
    const onPlaced = vi.fn();
    clearBox = box(900, 0, 400, 60);
    render(<Harness bounds={() => tall} onLost={() => {}} onPlaced={onPlaced} withClear />);
    expect(onPlaced).not.toHaveBeenCalled();
    clearBox = box(400, 0, 400, 60);
    flushResizeObservers();
    expect(onPlaced).not.toHaveBeenCalled();
    expect(frames).toHaveLength(1);
    frames[0](0);
    expect(onPlaced).toHaveBeenCalledTimes(1);
  });

  it("gives its placement to placeRef, which places the picker again on demand", () => {
    const placeRef = { current: () => {} };
    const onPlaced = vi.fn();
    render(<Harness bounds={() => tall} placeRef={placeRef} onPlaced={onPlaced} withClear />);
    const before = onPlaced.mock.calls.length;
    placeRef.current();
    expect(onPlaced).toHaveBeenCalledTimes(before + 1);
  });

  it("drops a placement still waiting for its frame when the picker goes", () => {
    let band = tall;
    const placeClear = vi.fn();
    const { unmount } = render(<Harness bounds={() => band} placeClear={placeClear} withClear />);
    band = short;
    flushResizeObservers();
    expect(frames).toHaveLength(1);
    unmount();
    expect(cancelAnimationFrame).toHaveBeenCalledWith(1);
  });
});
