import { useRef } from "react";

import { ApiRequestError } from "../lib/api";
import { randomId } from "../lib/randomId";
import { createChannel, createChannelCategory } from "./chatApi";
import { type ChannelDraft, channelDraftFingerprint, NEW_CATEGORY_OPTION } from "./channelForm";
import { useSingleSubmission } from "./useSingleSubmission";

/**
 * Turns a failed creation into something the user can act on.
 *
 * 401 and 403 keep their own wording: being signed out and being refused by the
 * workspace are different problems with different fixes. With invitees, a 403
 * can also mean one of them is no longer available — the server deliberately
 * does not say which, so neither does this. Server detail is never echoed.
 */
const statusCopy: Record<number, string> = {
  0: "Sem conexão. Verifique sua rede e tente novamente.",
  400: "Revise o nome, o identificador e as pessoas do canal.",
  401: "Sua sessão expirou. Entre novamente para criar canais.",
  403: "Você não tem permissão para criar canais neste workspace.",
  409: "Já existe um canal com esse identificador.",
  429: "Muitas solicitações em sequência. Aguarde um momento e tente novamente.",
};

function createErrorMessage(error: unknown, withInvitees: boolean): string {
  if (!(error instanceof ApiRequestError))
    return "Não foi possível criar o canal. Tente novamente.";
  if (error.status === 403 && withInvitees) {
    return "Você não tem permissão para criar este canal, ou alguma pessoa selecionada não está disponível.";
  }
  if (error.code === "idempotency_key_reused") {
    // Not a name clash: this attempt's key already belongs to another draft.
    return "O canal mudou desde a última tentativa. Revise os dados e tente novamente.";
  }
  return statusCopy[error.status] ?? "Não foi possível criar o canal. Tente novamente.";
}

/**
 * The channel form's single write (RF-01, issue #1025).
 *
 * Owns what makes a retry safe and nothing about the fields:
 * - one Idempotency-Key per creation intent. A retry of the same draft — after a
 *   timeout, a dropped connection, a 5xx — reuses it, so the server answers with
 *   the channel it may already have created; any material edit is a new intent
 *   and gets a new key. Keys are never rotated per attempt.
 * - a category created by an earlier attempt of the same intent is reused
 *   rather than created again, so the retry repeats no side effect.
 * - useSingleSubmission's guard against a second concurrent submit.
 */
export function useChannelCreation(
  onCreated: (channelId: string) => void,
  onPendingChange: (pending: boolean) => void,
) {
  const submission = useSingleSubmission(onPendingChange);
  const intentRef = useRef<{ fingerprint: string; key: string }>(null);
  const categoryRef = useRef<{ name: string; id: string }>(null);

  function intentKey(draft: ChannelDraft): string {
    const fingerprint = channelDraftFingerprint(draft);
    if (intentRef.current?.fingerprint !== fingerprint) {
      intentRef.current = { fingerprint, key: randomId() };
    }
    return intentRef.current.key;
  }

  /** The new category's ID, created at most once per category name. */
  async function newCategoryId(draft: ChannelDraft, signal: AbortSignal) {
    const name = draft.newCategoryName.trim();
    if (categoryRef.current?.name !== name) {
      const created = await createChannelCategory(name, signal);
      categoryRef.current = { name, id: created.id || "" };
    }
    return categoryRef.current.id || undefined;
  }

  /** Starts the creation unless one is already in flight. */
  function submit(draft: ChannelDraft) {
    const idempotencyKey = intentKey(draft);
    const initialMemberIds = draft.type === "private" ? [...draft.memberIds] : undefined;
    submission.run(
      async (signal) => {
        // Awaited only when a category must be created first, so an ordinary
        // submit reaches the network in the same tick as the click.
        const categoryId =
          draft.categoryId === NEW_CATEGORY_OPTION
            ? await newCategoryId(draft, signal)
            : draft.categoryId || undefined;
        return createChannel(
          {
            slug: draft.slug,
            displayName: draft.displayName,
            type: draft.type,
            categoryId,
            initialMemberIds,
            idempotencyKey,
          },
          signal,
        );
      },
      (channel) => onCreated(channel.id),
      (error) => createErrorMessage(error, Boolean(initialMemberIds?.length)),
    );
  }

  return {
    pending: submission.pending,
    error: submission.error,
    setError: submission.setError,
    submit,
  };
}
