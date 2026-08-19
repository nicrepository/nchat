import { useMemo } from "react";

import { splitHighlightSegments } from "./searchHighlight";

interface HighlightedTextProps {
  text: string;
  query: string;
  className?: string;
}

/**
 * Renders `text` with every case-insensitive occurrence of `query` wrapped in
 * a <mark>. Every segment is a plain string child, so React escapes it like
 * any other text node — this never touches dangerouslySetInnerHTML and cannot
 * be used to inject markup, however the query or text are shaped.
 */
export default function HighlightedText({ text, query, className }: HighlightedTextProps) {
  const segments = useMemo(() => splitHighlightSegments(text, query), [text, query]);

  return (
    <>
      {segments.map((segment, index) =>
        segment.matched ? (
          <mark key={index} className={className ?? "search-hl"}>
            {segment.value}
          </mark>
        ) : (
          segment.value
        ),
      )}
    </>
  );
}
