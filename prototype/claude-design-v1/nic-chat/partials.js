/* NIC Chat — shared partials
   Renders the standardized sidebar so every screen looks identical.
   Links are wired so the prototype is navigable as a real flow. */

const NIC_USER = {
  name: "Álvaro Neto",
  role: "Infraestrutura & Segurança",
  initials: "AN",
  email: "alvaro.neto@nic-labs.com",
};

const NIC_PEOPLE = [
  { name: "Juliane Lino",     role: "Infraestrutura & Suporte", initials: "JL", color: "rose",  status: "online"  },
  { name: "Caio Almeida",     role: "DevOps",                   initials: "CA", color: "blue",  status: "away"    },
  { name: "Bruno Lima",       role: "Suporte",                  initials: "BL", color: "amber", status: "offline" },
  { name: "Fernanda Nicácio", role: "Gestão",                   initials: "FN", color: "teal",  status: "online"  },
];

/* Where each sidebar entry goes — single source of truth for the flow. */
const ROUTES = {
  home: "canal.html",
  canais: "canal.html",
  mensagens: "dm.html",
  arquivos: "arquivos.html",
  configuracoes: "notificacoes.html",
  notificacoes: "notificacoes.html",
  seguranca: "seguranca.html",
  sessoes: "sessoes.html",
  perfil: "perfil.html",
  admin: "admin.html",
  "admin-overview": "admin.html",
  "admin-usuarios": "admin-usuarios.html",
  "admin-canais": "admin-canais.html",
  "admin-auditoria": "admin-auditoria.html",
  busca: "busca.html",
  // channels
  geral:          "shell.html",
  infraestrutura: "canal.html",
  "infraestrutura-call": "chamada.html",
  suporte:        "shell.html",
  projetos:       "shell.html",
  avisos:         "shell.html",
  // direct messages — all DM rows lead to the same DM screen
  "dm-juliane":   "dm.html",
  "dm-caio":      "dm.html",
  "dm-bruno":     "dm.html",
  "dm-fernanda":  "dm.html",
};

function ms(name, opts = "") {
  return `<span class="material-symbols-outlined icon" style="${opts}">${name}</span>`;
}

function avatarDot(person, onLight = false) {
  return `
    <span class="avatar avatar--sm avatar--${person.color} ${onLight ? "avatar--on-light" : ""}">
      ${person.initials}
      <span class="avatar__status avatar__status--${person.status}"></span>
    </span>
  `;
}

/* active: one of "canais", "mensagens", "arquivos", "configuracoes",
   or a channel key like "infraestrutura". */
function renderSidebar({ active = "canais", variant = "default" } = {}) {
  const channels = [
    { key: "geral",         name: "#geral"          },
    { key: "infraestrutura",name: "#infraestrutura" },
    { key: "suporte",       name: "#suporte"        },
    { key: "projetos",      name: "#projetos"       },
    { key: "avisos",        name: "#avisos"         },
  ];

  const isActive = (k) => active === k || (k === "infraestrutura" && active === "infraestrutura-call");
  const channelsExpanded = variant === "with-channels";
  const href = (k) => ROUTES[k] || "#";

  return `
    <aside class="sidebar">
      <a class="sidebar__brand" href="${ROUTES.home}" style="text-decoration:none; color:inherit;">
        <div class="sidebar__brand-mark">
          <img src="assets/nic-labs-icon.png" alt="NIC-Labs" />
        </div>
        <div>
          <h1 class="sidebar__brand-title">NIC Chat</h1>
          <div class="sidebar__brand-sub">Workspace NIC-Labs</div>
        </div>
      </a>

      <button class="sidebar__cta" data-action="new-channel">
        ${ms("add", "font-size:18px;")}
        Novo canal
      </button>

      <div class="sidebar__nav">
        ${channelsExpanded ? `
          <div class="sidebar__section-label">
            <span>Canais</span>
            <span style="cursor:pointer;">${ms("add", "font-size:16px;")}</span>
          </div>
          ${channels.map(c => `
            <a class="nav-item ${isActive(c.key) ? "nav-item--active" : ""}" href="${href(c.key)}">
              ${ms("tag")}
              <span>${c.name}</span>
              ${c.key === "infraestrutura" && active === "infraestrutura-call" ? '<span class="badge-dot"></span>' : ""}
            </a>
          `).join("")}
        ` : `
          <a class="nav-item ${isActive("canais") ? "nav-item--active" : ""}" href="${ROUTES.canais}">
            ${ms("tag")}<span>Canais</span>
          </a>
          <a class="nav-item ${isActive("mensagens") ? "nav-item--active" : ""}" href="${ROUTES.mensagens}">
            ${ms("forum")}<span>Mensagens diretas</span>
          </a>
          <a class="nav-item ${isActive("arquivos") ? "nav-item--active" : ""}" href="${ROUTES.arquivos}">
            ${ms("folder")}<span>Arquivos</span>
          </a>
        `}

        ${channelsExpanded ? `
          <div class="sidebar__section-label" style="margin-top:18px;">
            <span>Mensagens diretas</span>
            <span style="cursor:pointer;" data-action="new-dm" title="Nova mensagem direta">${ms("add", "font-size:16px;")}</span>
          </div>
          ${NIC_PEOPLE.slice(0, 4).map(p => {
            const key = "dm-" + p.name.split(" ")[0].toLowerCase();
            const dmActive = active === key;
            return `
            <a class="sidebar__dm-item ${dmActive ? "nav-item--active" : ""}"
               style="${dmActive ? "background: var(--color-sidebar-active);" : ""}"
               href="${href(key) || ROUTES.mensagens}">
              ${avatarDot(p)}
              <span class="truncate">${p.name}</span>
            </a>
          `;}).join("")}
        ` : ""}
      </div>

      <div class="sidebar__footer">
        ${variant === "with-channels" ? `
          <a class="sidebar__user" href="${ROUTES.perfil}" style="text-decoration:none; color:inherit;">
            <span class="avatar avatar--lg">${NIC_USER.initials}<span class="avatar__status avatar__status--online"></span></span>
            <div style="min-width:0;">
              <div class="sidebar__user-name truncate">${NIC_USER.name}</div>
              <div class="sidebar__user-role truncate">${NIC_USER.role}</div>
            </div>
            <button class="sidebar__user-action" title="Configurações" onclick="event.preventDefault(); event.stopPropagation(); window.location='${ROUTES.configuracoes}'">${ms("settings", "font-size:18px;")}</button>
          </a>
        ` : `
          <a class="nav-item ${isActive("configuracoes") ? "nav-item--active" : ""}" style="margin:2px 0;" href="${ROUTES.configuracoes}">
            ${ms("settings")}<span>Configurações</span>
          </a>
          <a class="sidebar__user" href="${ROUTES.perfil}" style="margin-top:6px; text-decoration:none; color:inherit;">
            <span class="avatar avatar--lg">${NIC_USER.initials}<span class="avatar__status avatar__status--online"></span></span>
            <div style="min-width:0;">
              <div class="sidebar__user-name truncate">${NIC_USER.name}</div>
              <div class="sidebar__user-role truncate">${NIC_USER.role}</div>
            </div>
          </a>
        `}
      </div>
    </aside>
  `;
}

function mountSidebar(selector, opts) {
  const host = document.querySelector(selector);
  if (host) host.outerHTML = renderSidebar(opts);
}

/* Render a consistent settings tab strip. active: "perfil" | "notificacoes" | "seguranca" | "sessoes" */
function renderSettingsTabs(active) {
  const tabs = [
    { key: "perfil",        label: "Perfil",        href: ROUTES.perfil },
    { key: "notificacoes",  label: "Notificações",  href: ROUTES.notificacoes },
    { key: "seguranca",     label: "Segurança",     href: ROUTES.seguranca },
    { key: "sessoes",       label: "Sessões",       href: ROUTES.sessoes },
  ];
  return tabs.map(t => `
    <a href="${t.href}" class="tabnav__item ${t.key === active ? "tabnav__item--active" : ""}">${t.label}</a>
  `).join("");
}

/* Render the admin tab strip. active: "overview" | "usuarios" | "canais" | "auditoria" */
function renderAdminTabs(active) {
  const tabs = [
    { key: "overview",   label: "Visão geral", href: ROUTES.admin },
    { key: "usuarios",   label: "Usuários",    href: ROUTES["admin-usuarios"] },
    { key: "canais",     label: "Canais",      href: ROUTES["admin-canais"] },
    { key: "auditoria",  label: "Auditoria",   href: ROUTES["admin-auditoria"] },
  ];
  return tabs.map(t => `
    <a href="${t.href}" class="tabnav__item ${t.key === active ? "tabnav__item--active" : ""}">${t.label}</a>
  `).join("");
}

/* Wire any [data-toggle-details] button to open/close .details-panel */
function wireDetailsToggle() {
  document.querySelectorAll("[data-toggle-details]").forEach(btn => {
    btn.addEventListener("click", () => {
      const app = document.querySelector(".app");
      if (!app) return;
      const wasClosed = app.classList.contains("details-closed");
      app.classList.toggle("details-closed");
      btn.classList.toggle("icon-btn--active", wasClosed);
    });
    const app = document.querySelector(".app");
    if (app && !app.classList.contains("details-closed")) {
      btn.classList.add("icon-btn--active");
    }
  });
  // any [data-close-details] inside a .details-panel closes it
  document.querySelectorAll("[data-close-details]").forEach(btn => {
    btn.addEventListener("click", () => {
      const app = document.querySelector(".app");
      if (app) app.classList.add("details-closed");
      document.querySelectorAll("[data-toggle-details]").forEach(b => b.classList.remove("icon-btn--active"));
    });
  });
}

window.NIC = { renderSidebar, mountSidebar, renderSettingsTabs, renderAdminTabs, wireDetailsToggle, NIC_USER, NIC_PEOPLE, ROUTES, ms, avatarDot };
