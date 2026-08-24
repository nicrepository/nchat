interface ConfigSearchProps {
  term: string;
  onTerm: (term: string) => void;
  /** How many settings the settled term matched. */
  matches: number;
  /** Total declared settings, so an untouched box says how much there is. */
  total: number;
}

/**
 * The configuration search (issue #582).
 *
 * It searches metadata and nothing else — the label, the description, the key,
 * the section, the owning service and the variable name. **No value is
 * indexed**, and that is a security property rather than a simplification: a
 * search that matched values would confirm a guess. Credentials never reach
 * this console as values at all, but a masked or derived form would leak the
 * same way, so the index has none.
 *
 * The input is a real search field with a real label and a live region for the
 * count, so a keyboard or screen-reader operator learns how many settings are
 * left without having to tab through the list to find out.
 */
export default function ConfigSearch({ term, onTerm, matches, total }: ConfigSearchProps) {
  return (
    <div className="admin-config-search">
      <label className="admin-field" htmlFor="config-search">
        <span>Buscar configuração</span>
        <input
          id="config-search"
          type="search"
          value={term}
          placeholder="Keycloak, SMTP, LiveKit, ClamAV, storage…"
          aria-describedby="config-search-help config-search-count"
          onChange={(event) => onTerm(event.target.value)}
        />
      </label>
      <p id="config-search-help" className="admin-field__help">
        Procura por nome, descrição, chave, seção, serviço responsável e variável de ambiente. Não
        procura em valores, e nenhum valor de credencial é indexado.
      </p>
      <p
        id="config-search-count"
        className="admin-table__muted"
        role="status"
        data-testid="config-search-count"
      >
        {term.trim() === ""
          ? `${total} configurações declaradas.`
          : `${matches} de ${total} configurações correspondem.`}
      </p>
    </div>
  );
}
