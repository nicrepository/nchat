export interface Point {
  x: number;
  y: number;
}

export interface Size {
  width: number;
  height: number;
}

export type FloatingCorner = "top-left" | "top-right" | "bottom-left" | "bottom-right";

export function clampPosition(point: Point, size: Size, viewport: Size, margin: number): Point {
  const maxX = Math.max(margin, viewport.width - size.width - margin);
  const maxY = Math.max(margin, viewport.height - size.height - margin);
  return {
    x: Math.min(Math.max(point.x, margin), maxX),
    y: Math.min(Math.max(point.y, margin), maxY),
  };
}

export function positionForCorner(
  corner: FloatingCorner,
  size: Size,
  viewport: Size,
  margin: number,
): Point {
  const right = Math.max(margin, viewport.width - size.width - margin);
  const bottom = Math.max(margin, viewport.height - size.height - margin);
  return {
    x: corner.endsWith("right") ? right : margin,
    y: corner.startsWith("bottom") ? bottom : margin,
  };
}

export function snapPosition(
  point: Point,
  size: Size,
  viewport: Size,
  threshold: number,
  margin: number,
): { position: Point; corner: FloatingCorner | null } {
  const position = clampPosition(point, size, viewport, margin);
  const corners: FloatingCorner[] = ["top-left", "top-right", "bottom-left", "bottom-right"];
  const corner = corners.find((candidate) => {
    const target = positionForCorner(candidate, size, viewport, margin);
    return (
      Math.abs(position.x - target.x) <= threshold && Math.abs(position.y - target.y) <= threshold
    );
  });
  return corner
    ? { position: positionForCorner(corner, size, viewport, margin), corner }
    : { position, corner: null };
}
