/**
 * The five message reads and the one write, addressed to one conversation.
 *
 * Channels and DMs are separate endpoints with identical shapes, so every caller
 * used to repeat the same `kind === "channel" ? … : …` before every request.
 * Resolving that once, here, is what lets the hooks above talk about "this
 * conversation" instead of about two of them — and it keeps the choice of
 * endpoint in one place, where it can be read against the API surface.
 *
 * This is routing only. No request is issued here, nothing is cached, and every
 * call forwards the caller's AbortSignal unchanged.
 */

import {
  fetchChannelMessage,
  fetchChannelMessageSecuritySnapshots,
  fetchChannelMessages,
  fetchDMMessage,
  fetchDMMessageSecuritySnapshots,
  fetchDMMessages,
  postChannelMessage,
  postDMMessage,
  resolveChannelMessageReferences,
  resolveDMMessageReferences,
  type PostMessageOptions,
} from "../chatApi";
import type { Message, MessagePage, MessageSecuritySnapshot } from "../chatTypes";
import type { ConversationTarget } from "./types";

export interface MessagesGateway {
  fetchPage(beforeCursor: string | undefined, signal: AbortSignal): Promise<MessagePage>;
  fetchMessage(messageId: string, signal: AbortSignal): Promise<Message>;
  fetchSecuritySnapshots(
    messageIDs: string[],
    signal: AbortSignal,
  ): Promise<MessageSecuritySnapshot[]>;
  resolveReferences(
    messageIDs: string[],
    signal: AbortSignal,
  ): Promise<Record<string, NonNullable<Message["reference"]>>>;
  post(body: string, options: PostMessageOptions): Promise<Message>;
}

const channelGateway = (channelId: string): MessagesGateway => ({
  fetchPage: (cursor, signal) => fetchChannelMessages(channelId, cursor, signal),
  fetchMessage: (messageId, signal) => fetchChannelMessage(channelId, messageId, signal),
  fetchSecuritySnapshots: (messageIDs, signal) =>
    fetchChannelMessageSecuritySnapshots(channelId, messageIDs, signal),
  resolveReferences: (messageIDs, signal) =>
    resolveChannelMessageReferences(channelId, messageIDs, signal),
  post: (body, options) => postChannelMessage(channelId, body, options),
});

const dmGateway = (conversationId: string): MessagesGateway => ({
  fetchPage: (cursor, signal) => fetchDMMessages(conversationId, cursor, signal),
  fetchMessage: (messageId, signal) => fetchDMMessage(conversationId, messageId, signal),
  fetchSecuritySnapshots: (messageIDs, signal) =>
    fetchDMMessageSecuritySnapshots(conversationId, messageIDs, signal),
  resolveReferences: (messageIDs, signal) =>
    resolveDMMessageReferences(conversationId, messageIDs, signal),
  post: (body, options) => postDMMessage(conversationId, body, options),
});

export function messagesGateway(target: ConversationTarget): MessagesGateway {
  return target.kind === "channel" ? channelGateway(target.targetId) : dmGateway(target.targetId);
}

/**
 * How many message ids one authorized batch request may carry.
 *
 * Matches the server's cap, so a page larger than this is split rather than
 * refused wholesale.
 */
export const messageBatchSize = 100;

/** Splits ids into requests no larger than the server accepts. */
export function batchMessageIds(ids: string[]): string[][] {
  return Array.from({ length: Math.ceil(ids.length / messageBatchSize) }, (_, index) =>
    ids.slice(index * messageBatchSize, (index + 1) * messageBatchSize),
  );
}
