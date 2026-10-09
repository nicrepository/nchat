import type { ConversationRef, SearchCategory, SearchTab } from "./searchTypes";

export const TAB_LABELS: Record<SearchTab, string> = {
  all: "Tudo",
  messages: "Mensagens",
  users: "Pessoas",
  channels: "Canais",
  groups: "Grupos",
  files: "Arquivos",
  links: "Links",
};

export const SEARCH_PANEL_ID = "global-search-panel";

export function tabId(tab: SearchTab): string {
  return `global-search-tab-${tab}`;
}

export function plural(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`;
}

export function resultCount(count: number): string {
  return plural(count, "resultado", "resultados");
}

export function participantCount(count: number): string {
  return plural(count, "participante", "participantes");
}

/** Where a message or file was posted, the way the sidebar names it. */
export function conversationLabel(conversation: ConversationRef): string {
  if (conversation.kind === "channel") return `#${conversation.name}`;
  if (conversation.type === "group") return conversation.name || "Grupo sem nome";
  return conversation.name ? `Conversa com ${conversation.name}` : "Conversa direta";
}

export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" });
}

export const CATEGORY_EMPTY: Record<SearchCategory, string> = {
  messages: "Nenhuma mensagem encontrada",
  users: "Nenhuma pessoa encontrada",
  channels: "Nenhum canal encontrado",
  groups: "Nenhum grupo encontrado",
  files: "Nenhum arquivo encontrado",
  links: "Nenhum link encontrado",
};
