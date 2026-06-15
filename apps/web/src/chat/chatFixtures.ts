/**
 * Chat sidebar fixture data.
 *
 * PENDING BACKEND CONTRACT
 * ────────────────────────
 * These fixtures stand in for real API responses until the following
 * endpoints are implemented:
 *
 *   GET /api/chat/channels
 *     → Channel[]   (id, name, type, unreadCount?)
 *
 *   GET /api/chat/dms
 *     → DMConversation[]  (id, type, name, participants[], unreadCount?)
 *
 * Once those endpoints exist:
 *   1. Replace FIXTURE_CHANNELS / FIXTURE_DMS with real calls in chatApi.ts.
 *   2. Remove this file (or keep it for tests with a re-export).
 *
 * Auth: all endpoints require `Authorization: Bearer <access_token>` injected
 * by `authenticatedFetch`. Do NOT store tokens in localStorage/sessionStorage.
 */

import type { Channel, CurrentUser, DMConversation } from "./chatTypes";

export const FIXTURE_CHANNELS: Channel[] = [
  { id: "geral", name: "geral", type: "public" },
  { id: "infraestrutura", name: "infraestrutura", type: "public" },
  { id: "suporte", name: "suporte", type: "public" },
  { id: "projetos", name: "projetos", type: "private" },
  { id: "avisos", name: "avisos", type: "public" },
];

export const FIXTURE_DMS: DMConversation[] = [
  {
    id: "dm-juliane",
    type: "1:1",
    name: "Juliane Lino",
    participants: [
      {
        id: "juliane",
        displayName: "Juliane Lino",
        initials: "JL",
        color: "rose",
        status: "online",
      },
    ],
  },
  {
    id: "dm-caio",
    type: "1:1",
    name: "Caio Almeida",
    participants: [
      {
        id: "caio",
        displayName: "Caio Almeida",
        initials: "CA",
        color: "blue",
        status: "away",
      },
    ],
  },
  {
    id: "dm-bruno",
    type: "1:1",
    name: "Bruno Lima",
    participants: [
      {
        id: "bruno",
        displayName: "Bruno Lima",
        initials: "BL",
        color: "amber",
        status: "offline",
      },
    ],
  },
  {
    id: "dm-grupo-infra",
    type: "group",
    name: "Equipe Infra",
    participants: [
      {
        id: "juliane",
        displayName: "Juliane Lino",
        initials: "JL",
        color: "rose",
        status: "online",
      },
      { id: "caio", displayName: "Caio Almeida", initials: "CA", color: "blue", status: "away" },
      {
        id: "fernanda",
        displayName: "Fernanda Nicácio",
        initials: "FN",
        color: "teal",
        status: "online",
      },
    ],
  },
];

/**
 * PENDING PROFILE/AUTH CONTRACT
 * ──────────────────────────────
 * Placeholder for the authenticated user shown in the sidebar footer.
 * Replace with a call to the user profile endpoint once available:
 *   GET /api/auth/me → { displayName, initials?, role?, ... }
 */
export const FIXTURE_CURRENT_USER: CurrentUser = {
  displayName: "Álvaro Neto",
  initials: "AN",
  color: "purple",
  role: "Infraestrutura & Segurança",
};
