/**
 * The reader's own emoji history and skin tone, as conversation state
 * (issue #496).
 *
 * One owner for one preference: the quick row above a message, the reaction
 * picker and the composer's picker read the same object, and any confirmed use
 * updates it once.
 * Reading and writing storage lives in emojiUsage.ts; this is only the React
 * lifetime around it.
 */

import { useCallback, useState } from "react";

import { readEmojiUsage, recordEmojiUse, storeEmojiTone, type EmojiUsage } from "./emojiUsage";

export interface EmojiUsageState {
  usage: EmojiUsage;
  /**
   * Records an emoji the reader actually used — a reaction the server
   * confirmed as theirs, or one they inserted in the composer. Both feed the
   * same "Recentes", because to a reader it is one history.
   */
  remember: (emoji: string) => void;
  changeTone: (tone: number) => void;
}

export function useEmojiUsage(userId: string): EmojiUsageState {
  const [usage, setUsage] = useState<EmojiUsage>(() => readEmojiUsage(userId));
  const [owner, setOwner] = useState(userId);

  // The preference belongs to whoever is reading. It is read once at mount and
  // re-read here if the reader ever changes without this hook being remounted —
  // adjusted during render rather than in an effect, so the first paint after
  // the change already shows the right history.
  if (owner !== userId) {
    setOwner(userId);
    setUsage(readEmojiUsage(userId));
  }

  const remember = useCallback(
    (emoji: string) => setUsage((current) => recordEmojiUse(userId, emoji, current)),
    [userId],
  );
  const changeTone = useCallback(
    (tone: number) => setUsage((current) => storeEmojiTone(userId, tone, current)),
    [userId],
  );

  return { usage, remember, changeTone };
}
