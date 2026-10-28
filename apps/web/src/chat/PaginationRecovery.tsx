/** Presentation only: cursors and results remain in the owning list. */
export default function PaginationRecovery({ remaining, hasMore, busy, onRecover, onCancel }: {
  remaining: number;
  hasMore: boolean;
  busy: boolean;
  onRecover: () => void;
  onCancel: () => void;
}) {
  return <div data-reading-recovery="true" aria-live="polite">
    <p>{remaining > 0 && hasMore
      ? `A posição anterior ainda não foi restaurada. Faltam até ${remaining} páginas para tentar retomar a leitura. Cada ação recupera até cinco páginas.`
      : remaining > 0
        ? "A lista terminou antes de recuperar o trecho anterior. A posição anterior não pôde ser restaurada."
        : "Conteúdo recarregado. A posição de leitura é aproximada e pode mudar se os resultados mudaram."}</p>
    {remaining > 0 && hasMore && <>
      <button type="button" disabled={busy} onClick={onRecover}>Recuperar contexto de leitura</button>
      <button type="button" onClick={onCancel}>Continuar sem restaurar posição</button>
      <p>Carregar mais continua a navegação normal e cancela a restauração pendente.</p>
    </>}
  </div>;
}
