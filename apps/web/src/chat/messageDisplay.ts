import type { Message } from "./chatTypes";

export function formatTime(iso: string): string {
  try {
    return new Date(iso).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
  } catch {
    return "";
  }
}

export function senderLabel(msg: Message): string {
  return msg.senderDisplayName || msg.senderEmail || msg.senderId.slice(0, 8);
}
