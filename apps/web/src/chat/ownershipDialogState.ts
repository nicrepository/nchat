import { ApiRequestError } from "../lib/api";
import type { OwnershipDetails } from "./ownershipApi";

export type OwnershipSubmitState =
  | { phase: "selecting" }
  | { phase: "submitting" }
  | { phase: "success" }
  | { phase: "conflict"; error: string; previous: OwnershipDetails }
  | { phase: "recoverable_error"; error: string; uncertain: boolean };

export function ownershipFailure(error: unknown, previous: OwnershipDetails): OwnershipSubmitState {
  if (error instanceof ApiRequestError) {
    if (error.status === 409)
      return {
        phase: "conflict",
        previous,
        error:
          "A propriedade mudou ou o participante não está mais disponível. Confira os detalhes atualizados e tente novamente.",
      };
    if (error.status === 403)
      return {
        phase: "recoverable_error",
        uncertain: false,
        error: "Você não tem permissão para esta ação.",
      };
    if (error.status === 404)
      return {
        phase: "recoverable_error",
        uncertain: false,
        error: "Esta conversa ou participante não está mais disponível.",
      };
  }
  return {
    phase: "recoverable_error",
    uncertain: true,
    error:
      "Não foi possível confirmar o resultado. Tente novamente para verificar a mesma operação.",
  };
}

export const ownershipCopy = {
  transfer: "Transferir propriedade",
  search: "Buscar participante",
  target: "Novo proprietário",
  actorRole: "Seu papel depois",
  chooseAnother: "Escolher outro proprietário",
  automatic: "Usar sucessor automático",
  lastOwner: "Você é o último proprietário.",
  empty: "Você é o último participante. A conversa ficará sem participantes ativos.",
  blocked: "Não há sucessor automático elegível. Escolha um proprietário antes de sair.",
  noCandidates: "Nenhum participante disponível para esta busca.",
  cancel: "Cancelar",
  leave: "Sair da conversa",
  leaveTransfer: "Sair e transferir",
  submitting: "Confirmando…",
  refreshing: "Atualizando participantes…",
  refreshError: "Não foi possível atualizar os participantes. Atualize os detalhes para continuar.",
  refresh: "Atualizar detalhes",
  done: "Alteração concluída.",
};
