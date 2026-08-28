/**
 * @test-only  Chat sidebar fixture data.
 *
 * This file is for TESTS ONLY. Production code must never import from here.
 *
 * The sidebar now consumes GET /api/chat/sidebar via authenticatedFetch.
 * See chatApi.ts for the real implementation.
 */

import type { Channel, DMConversation } from "./chatTypes";

export const FIXTURE_CHANNELS: Channel[] = [
  { id: "geral", name: "geral", type: "public", canWrite: true },
  { id: "infraestrutura", name: "infraestrutura", type: "public", canWrite: true },
  { id: "suporte", name: "suporte", type: "public", canWrite: true },
  { id: "projetos", name: "projetos", type: "private", canWrite: true },
  { id: "avisos", name: "avisos", type: "public", canWrite: true },
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
