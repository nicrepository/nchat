import { useCallback, useState } from "react";

import {
  listChannels,
  listConversations,
  listUsers,
  updateChannelStatus,
  type AdminChannel,
  type ChannelFilters,
} from "../api/managementApi";
import AdminUserSearchSelect, { type UserOption } from "../components/AdminUserSearchSelect";
import ConfirmDialog from "../components/ConfirmDialog";
import Pagination from "../components/Pagination";
import QueryStates from "../components/QueryStates";
import { formatDateTime } from "../lib/units";
import { classify, useAdminQuery } from "../lib/useAdminQuery";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { useAdminSession } from "../session/useAdminSession";
import ChannelDetailDialog from "./ChannelDetailDialog";

const PAGE_SIZE = 25;

/** A picker shows a handful of people, not a page of the directory. */
const PEOPLE_PAGE_SIZE = 8;

const TYPE_OPTIONS = [
  { value: "", label: "Públicos e privados" },
  { value: "public", label: "Somente públicos" },
  { value: "private", label: "Somente privados" },
];

const STATUS_OPTIONS = [
  { value: "", label: "Ativos e arquivados" },
  { value: "active", label: "Somente ativos" },
  { value: "archived", label: "Somente arquivados" },
];

const SIZE_OPTIONS = [
  { value: "", label: "Qualquer tamanho" },
  { value: "2", label: "2 membros ou mais" },
  { value: "10", label: "10 membros ou mais" },
  { value: "50", label: "50 membros ou mais" },
];

const ACTIVITY_OPTIONS = [
  { value: "", label: "Qualquer atividade" },
  { value: "7d", label: "Ativos nos últimos 7 dias" },
  { value: "30d", label: "Ativos nos últimos 30 dias" },
  { value: "90d", label: "Ativos nos últimos 90 dias" },
];

const CONVERSATION_TYPE_OPTIONS = [
  { value: "", label: "Diretas e em grupo" },
  { value: "direct", label: "Somente diretas" },
  { value: "group", label: "Somente em grupo" },
];

/**
 * Channels, groups and private conversations.
 *
 * Two tables with deliberately different shapes. Channels are a workspace's
 * public structure and are managed here: listed, inspected and archived.
 * Private conversations are not — the second table carries counts and
 * timestamps and nothing else, because a platform administrator does not become
 * a participant by being an administrator, and there is no endpoint in this
 * service that would let them read one.
 */
export default function ChannelsPage() {
  const { can } = useAdminSession();
  const canManage = can("admin.channels.manage");
  // The people search reads the platform user directory, which is guarded by
  // its own capability. Without it the filter is not offered — and the API
  // would refuse the search regardless.
  const canSearchPeople = can("admin.users.read");

  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  // A chosen person, not typed text. Typing searches for candidates; only a
  // selection becomes a filter value, so a partially typed name is never sent
  // as an identifier the API would (correctly) refuse with 400.
  const [administeredBy, setAdministeredBy] = useState<UserOption | null>(null);
  const [filters, setFilters] = useState<ChannelFilters>({});
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const cursor = cursors[cursors.length - 1];

  const [pending, setPending] = useState(false);
  const [feedback, setFeedback] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [confirming, setConfirming] = useState<AdminChannel | null>(null);
  const [openChannelID, setOpenChannelID] = useState<string | null>(null);

  const loadChannels = useCallback(
    (signal: AbortSignal) =>
      listChannels(
        { ...filters, q: debouncedSearch, administeredBy: administeredBy?.id },
        cursor,
        PAGE_SIZE,
        signal,
      ),
    [filters, debouncedSearch, administeredBy, cursor],
  );

  // The channel directory is platform-wide — no request names a workspace — so
  // the person administering a channel may come from anywhere, and the source
  // is the platform user directory. It carries its own capability, which is why
  // the filter is only offered to somebody who holds it.
  const searchPeople = useCallback(
    async (term: string, signal: AbortSignal): Promise<UserOption[]> => {
      const page = await listUsers({ q: term }, null, PEOPLE_PAGE_SIZE, signal);
      return page.items.map((person) => ({
        id: person.id,
        displayName: person.fullName || person.displayName,
        secondary: person.email,
        avatarUrl: person.avatarUrl || undefined,
      }));
    },
    [],
  );
  const channels = useAdminQuery(loadChannels);
  const page = channels.data;

  const applyFilter = (patch: ChannelFilters) => {
    setFilters((current) => ({ ...current, ...patch }));
    setCursors([null]);
  };

  const archive = () => {
    if (confirming === null || pending) return;
    const channel = confirming;
    const to = channel.status === "active" ? "archived" : "active";
    setPending(true);
    setFeedback(null);
    updateChannelStatus(channel.id, to)
      .then(() => {
        setFeedback({
          tone: "ok",
          text:
            to === "archived"
              ? `#${channel.slug} foi arquivado. O histórico permanece.`
              : `#${channel.slug} voltou a ficar ativo.`,
        });
        channels.reload();
      })
      .catch((error: unknown) => setFeedback({ tone: "error", text: classify(error).message }))
      .finally(() => {
        setPending(false);
        setConfirming(null);
      });
  };

  return (
    <section aria-labelledby="admin-channels-title">
      <h1 id="admin-channels-title">Canais e grupos</h1>
      <p className="admin-lead">
        Estrutura de conversação da plataforma. Arquivar preserva o histórico e é reversível; não
        existe exclusão administrativa de canal.
      </p>

      <form
        className="admin-filters"
        role="search"
        aria-label="Filtrar canais"
        onSubmit={(event) => event.preventDefault()}
      >
        <label className="admin-field">
          <span>Buscar por nome ou identificador</span>
          <input
            type="search"
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setCursors([null]);
            }}
            placeholder="engenharia, #geral…"
          />
        </label>
        <Select
          label="Visibilidade"
          value={filters.type ?? ""}
          options={TYPE_OPTIONS}
          onChange={(type) => applyFilter({ type })}
        />
        <Select
          label="Situação"
          value={filters.status ?? ""}
          options={STATUS_OPTIONS}
          onChange={(status) => applyFilter({ status })}
        />
        <Select
          label="Tamanho"
          value={filters.minMembers ?? ""}
          options={SIZE_OPTIONS}
          onChange={(minMembers) => applyFilter({ minMembers })}
        />
        <Select
          label="Atividade recente"
          value={filters.activeWithin ?? ""}
          options={ACTIVITY_OPTIONS}
          onChange={(activeWithin) => applyFilter({ activeWithin })}
        />
        {canSearchPeople ? (
          <AdminUserSearchSelect
            label="Administrado por"
            placeholder="Busque por nome ou e-mail"
            search={searchPeople}
            selected={administeredBy}
            onSelect={(person) => {
              setAdministeredBy(person);
              // A cursor from the previous filter names a position in a
              // different result set.
              setCursors([null]);
            }}
            emptyLabel="Nenhuma pessoa encontrada."
            help="Criador ou moderador do canal. Papéis de workspace não contam aqui."
          />
        ) : (
          <p className="admin-notice">
            Filtrar por quem administra exige a permissão <code>admin.users.read</code>.
          </p>
        )}
      </form>

      {feedback !== null && (
        <p
          className={feedback.tone === "ok" ? "admin-notice" : "admin-alert"}
          role={feedback.tone === "ok" ? "status" : "alert"}
          data-testid="admin-feedback"
        >
          {feedback.text}
        </p>
      )}

      <QueryStates
        status={channels.status}
        message={channels.message}
        empty="Nenhum canal corresponde aos filtros aplicados."
        isEmpty={page !== null && page.items.length === 0}
        onRetry={channels.reload}
        skeletonRows={5}
      />

      {channels.status === "ready" && page !== null && page.items.length > 0 && (
        <>
          <div className="admin-table-scroll">
            <table className="admin-table">
              <caption className="admin-visually-hidden">Canais da plataforma</caption>
              <thead>
                <tr>
                  <th scope="col">Canal</th>
                  <th scope="col">Workspace</th>
                  <th scope="col">Visibilidade</th>
                  <th scope="col">Situação</th>
                  <th scope="col">Membros</th>
                  <th scope="col">Criado por</th>
                  <th scope="col">Última atividade</th>
                  <th scope="col">Ações</th>
                </tr>
              </thead>
              <tbody>
                {page.items.map((channel) => (
                  <tr key={channel.id}>
                    <th scope="row" className="admin-table__person">
                      <span className="admin-table__name">{channel.displayName}</span>
                      <span className="admin-table__muted">#{channel.slug}</span>
                    </th>
                    <td>{channel.workspaceName}</td>
                    <td>
                      <span className={`admin-status admin-status--${channel.type}`}>
                        {channel.type === "private" ? "Privado" : "Público"}
                      </span>
                    </td>
                    <td>
                      <span className={`admin-status admin-status--${channel.status}`}>
                        {channel.status === "archived" ? "Arquivado" : "Ativo"}
                      </span>
                    </td>
                    <td>
                      {channel.memberCount}
                      {channel.moderatorCount > 0 && (
                        <span className="admin-table__muted"> · {channel.moderatorCount} mod.</span>
                      )}
                    </td>
                    <td>{channel.createdByName || channel.createdByEmail || "—"}</td>
                    <td>{formatDateTime(channel.lastActivityAt)}</td>
                    <td>
                      <div className="admin-actions">
                        <button
                          type="button"
                          className="admin-button admin-button--ghost"
                          onClick={() => setOpenChannelID(channel.id)}
                        >
                          Detalhes
                        </button>
                        {canManage && !channel.isGeneral && (
                          <button
                            type="button"
                            className="admin-button admin-button--ghost"
                            disabled={pending}
                            onClick={() => setConfirming(channel)}
                          >
                            {channel.status === "active" ? "Arquivar" : "Desarquivar"}
                          </button>
                        )}
                        {canManage && channel.isGeneral && (
                          <span className="admin-table__muted" title="O canal #geral é imutável">
                            canal geral
                          </span>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <Pagination
            count={page.items.length}
            hasMore={page.hasMore}
            canGoBack={cursors.length > 1}
            busy={pending}
            onNext={() => setCursors((stack) => [...stack, page.nextCursor])}
            onPrevious={() => setCursors((stack) => stack.slice(0, -1))}
          />
        </>
      )}

      <ConversationsSection />

      {confirming !== null && (
        <ConfirmDialog
          title={
            confirming.status === "active" ? "Arquivar este canal?" : "Desarquivar este canal?"
          }
          description={
            confirming.status === "active"
              ? `#${confirming.slug} deixará de aparecer para os membros como canal ativo.`
              : `#${confirming.slug} volta a ficar ativo para os membros.`
          }
          impact={
            confirming.status === "active"
              ? "O histórico e as pessoas do canal são preservados. A ação é reversível a qualquer momento."
              : "Nenhum dado é alterado além da situação do canal."
          }
          confirmLabel={confirming.status === "active" ? "Arquivar" : "Desarquivar"}
          pending={pending}
          onConfirm={archive}
          onCancel={() => setConfirming(null)}
        />
      )}

      {openChannelID !== null && (
        <ChannelDetailDialog channelID={openChannelID} onClose={() => setOpenChannelID(null)} />
      )}
    </section>
  );
}

/**
 * Private conversation metadata.
 *
 * Its own listing state, because it answers a different question and is paged
 * separately. Everything it renders is a count, a state or a timestamp.
 */
function ConversationsSection() {
  const [type, setType] = useState("");
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const cursor = cursors[cursors.length - 1];

  const load = useCallback(
    (signal: AbortSignal) => listConversations({ type }, cursor, PAGE_SIZE, signal),
    [type, cursor],
  );
  const query = useAdminQuery(load);
  const page = query.data;

  return (
    <section aria-labelledby="admin-conversations-title" className="admin-subsection">
      <h2 id="admin-conversations-title">Conversas privadas</h2>
      <p className="admin-lead">
        Somente metadados operacionais. O console não expõe mensagens, títulos, participantes nem
        anexos de conversas privadas, e não existe endpoint administrativo que os leia.
      </p>

      <form className="admin-filters" onSubmit={(event) => event.preventDefault()}>
        <Select
          label="Tipo de conversa"
          value={type}
          options={CONVERSATION_TYPE_OPTIONS}
          onChange={(value) => {
            setType(value);
            setCursors([null]);
          }}
        />
      </form>

      <QueryStates
        status={query.status}
        message={query.message}
        empty="Nenhuma conversa privada registrada."
        isEmpty={page !== null && page.items.length === 0}
        onRetry={query.reload}
      />

      {query.status === "ready" && page !== null && page.items.length > 0 && (
        <>
          <div className="admin-table-scroll">
            <table className="admin-table">
              <caption className="admin-visually-hidden">Metadados de conversas privadas</caption>
              <thead>
                <tr>
                  <th scope="col">Identificador</th>
                  <th scope="col">Workspace</th>
                  <th scope="col">Tipo</th>
                  <th scope="col">Situação</th>
                  <th scope="col">Participantes</th>
                  <th scope="col">Mensagens</th>
                  <th scope="col">Última atividade</th>
                </tr>
              </thead>
              <tbody>
                {page.items.map((conversation) => (
                  <tr key={conversation.id}>
                    <th scope="row">
                      <code className="admin-table__muted">{conversation.id}</code>
                    </th>
                    <td>{conversation.workspaceName}</td>
                    <td>{conversation.type === "direct" ? "Direta" : "Grupo"}</td>
                    <td>{conversation.status === "archived" ? "Arquivada" : "Ativa"}</td>
                    <td>{conversation.participantCount}</td>
                    <td>{conversation.messageCount}</td>
                    <td>{formatDateTime(conversation.lastActivityAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pagination
            count={page.items.length}
            hasMore={page.hasMore}
            canGoBack={cursors.length > 1}
            busy={false}
            onNext={() => setCursors((stack) => [...stack, page.nextCursor])}
            onPrevious={() => setCursors((stack) => stack.slice(0, -1))}
          />
        </>
      )}
    </section>
  );
}

interface SelectProps {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}

function Select({ label, value, options, onChange }: SelectProps) {
  return (
    <label className="admin-field">
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </label>
  );
}
