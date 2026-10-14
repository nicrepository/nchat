import type { ComponentType } from "react";

import ChannelResultRow from "./ChannelResultRow";
import FileResultRow from "./FileResultRow";
import GroupResultRow from "./GroupResultRow";
import LinkResultRow from "./LinkResultRow";
import MessageResultRow from "./MessageResultRow";
import UserResultRow from "./UserResultRow";
import type { SearchCategory, SearchResultByCategory } from "./searchTypes";

const ROWS: {
  [C in SearchCategory]: ComponentType<{ result: SearchResultByCategory[C]; query: string }>;
} = {
  messages: MessageResultRow,
  users: UserResultRow,
  channels: ChannelResultRow,
  groups: GroupResultRow,
  files: FileResultRow,
  links: LinkResultRow,
};

/** One result card, the same in the overview and in its own tab. */
export default function SearchResultRow<C extends SearchCategory>({
  category,
  result,
  query,
}: {
  category: C;
  result: SearchResultByCategory[C];
  query: string;
}) {
  const Row = ROWS[category] as ComponentType<{
    result: SearchResultByCategory[C];
    query: string;
  }>;
  return <Row result={result} query={query} />;
}
