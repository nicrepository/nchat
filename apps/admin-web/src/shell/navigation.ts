/**
 * The console's navigation map.
 *
 * Three fields, and the difference between them is the honesty of the UI:
 *
 *  - `capability` is what the shell asks the session before rendering the entry
 *    at all. It is a display rule; the endpoint behind the section enforces the
 *    same capability server-side, and that is the one that decides.
 *  - `path` exists only for sections that are actually implemented.
 *  - a section without a `path` renders as unavailable and refuses to be
 *    clicked. It is never drawn as a working control, in any environment: a
 *    button that looks live and does nothing is how an operator ends up
 *    believing a policy was applied.
 */
export interface AdminNavItem {
  id: string;
  label: string;
  capability: string;
  path?: string;
}

export const ADMIN_NAV: AdminNavItem[] = [
  { id: "overview", label: "Visão geral", capability: "admin.config.read", path: "/" },
  { id: "users", label: "Usuários", capability: "admin.users.read", path: "/users" },
  {
    id: "channels",
    label: "Canais e grupos",
    capability: "admin.channels.read",
    path: "/channels",
  },
  {
    id: "security",
    label: "Segurança e políticas",
    capability: "admin.security.read",
    path: "/security",
  },
  {
    id: "files",
    label: "Arquivos e armazenamento",
    capability: "admin.infrastructure.read",
    path: "/files",
  },
  {
    id: "configuration",
    label: "Configurações",
    capability: "admin.config.read",
    path: "/configuration",
  },
  { id: "authentication", label: "Autenticação", capability: "admin.integrations.read" },
  { id: "email", label: "E-mail", capability: "admin.integrations.read" },
  { id: "calls", label: "Chamadas", capability: "admin.integrations.read" },
  { id: "links", label: "Links e conteúdo", capability: "admin.security.read" },
  { id: "integrations", label: "Integrações", capability: "admin.integrations.read" },
  { id: "system", label: "Sistema", capability: "admin.infrastructure.read" },
  { id: "health", label: "Health Center", capability: "admin.infrastructure.read" },
  { id: "audit", label: "Auditoria", capability: "admin.audit.read", path: "/audit" },
];

/**
 * The entries a given administrator should see.
 *
 * Hiding an entry is navigation, not authorization: an administrator who edits
 * this list in a debugger gets a longer sidebar and the same 403s.
 */
export function visibleNavItems(can: (capability: string) => boolean): AdminNavItem[] {
  return ADMIN_NAV.filter((item) => can(item.capability));
}
