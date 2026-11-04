# Agendamento contextual — #1055

O header e o menu de DM, grupo e canal abrem o mesmo formulário. `AppShell`
compõe `MeetingSchedulingProvider`; ele guarda apenas drafts e resultados de
operações em memória, por sessão/workspace/conversa. Trocar de rota fecha o
formulário e preserva o draft. Fechar/“Continuar depois” preserva os dados;
“Descartar rascunho” exige confirmação. Concluir um resultado confirmado remove
o draft. Logout, troca de sessão ou identidade limpam os drafts; rotação do
access token preserva a intenção em andamento. Enquanto o `useChatSidebar`
confirma a identidade do token renovado, o formulário fica oculto e operações
aguardam confirmação. O sender existente recusa replay automático de writes
após 401 (mesmo padrão de read cursors): o retry mantém chave/ID e só pode sair
sob a identidade confirmada. Não há política de autorização paralela.
Nada é gravado em Web Storage.

## Responsabilidades e contratos

- `meetingTime.ts`: validação Unicode e temporal, presets e payload fechado.
  `@js-temporal/polyfill` 0.5.1 foi adicionado porque não havia biblioteca temporal;
  utiliza a base IANA do navegador, sem implementar regras de DST manualmente.
- `meetingSession.ts`: snapshot editável, dirty state, chave por intenção,
  exclusão de submits concorrentes e orquestração create → invite-all.
- `useMeetingCandidates.ts`: prefixo com debounce de 300 ms, cursor reiniciado
  na busca, AbortController e proteção contra respostas obsoletas. A página
  concluída é preservada localmente pela identidade de sessão/conversa/organizer,
  busca, cursor e retry. Pausar durante uma escrita não invalida essa página;
  uma consulta interrompida pode recomeçar ao retomar.
- `MeetingFields`, `MeetingParticipants` e `MeetingSelection`: apresentação e
  inspeção da seleção. Candidatos e selecionados renderizam até 50 por página;
  a seleção completa permanece no draft, independentemente da página/filtro.
- `MeetingSchedulingDialog`: diálogo modal nativo em portal, validação inline,
  foco, fechamento e feedback. Erros temporais já apresentados são revalidados
  após mudanças relacionadas, preservando erros independentes. Duração personalizada
  associa erro e foco ao input numérico. Em telas abaixo de 768 px usa a viewport inteira,
  com scroll e ações sticky. A viewport existente já usa
  `interactive-widget=resizes-content`. Atalhos de navegação da aplicação não
  atravessam um modal nativo aberto.
- `meetingApi.ts`: usa exclusivamente `authenticatedFetch`, com o mesmo bearer,
  refresh e envelope compartilhado. Não implementa cliente ou cache HTTP novo.

[Create](../api/chat-meetings.md) envia apenas detalhes, conversa e attendees
únicos, excluindo o organizer. A chave UUID permanece estável nos retries do
mesmo payload normalizado; alteração material após recusa definitiva gera outra
chave. Falha de transporte, parsing ou 5xx mantém a intenção e bloqueia edição
até confirmar seu resultado. O body é validado contra o limite de 64 KiB.
Respostas 201/200 distinguem criação e replay; não há GET posterior.

[Candidatos e bulk](../api/chat-meeting-participants.md) mantêm os contratos
existentes. Somente a primeira consulta sem filtro de uma DM individual
pré-seleciona o outro membro elegível. Grupo usa `kind=dm`, sem ser confundido
com canal. Canal/grupo começam com seleção parcial. “Convidar todos os membros
elegíveis” é explícito e não mostra uma contagem que a API não fornece.

O bulk usa `{}` sobre o ID persistido. Falha preserva a reunião e mostra convite
pendente; retry executa apenas `invite-all` sobre o mesmo ID. Cada tentativa
resolve elegibilidade atual, podendo incorporar novos membros. Não há rollback
ou segundo create. Acima de 500 elegíveis, o usuário é orientado a seleção
parcial; pode concluir a reunião já criada com a seleção original, sem afirmar
que o convite coletivo terminou. Replay de reunião terminal apresenta o estado
retornado, sem tentar convidar participantes.

## Semântica temporal

O formulário guarda data/hora civil e timezone IANA. Duração é tempo decorrido
positivo em minutos inteiros, default 30; o término é sempre derivado. O payload
contém instantes RFC 3339 em UTC e o identificador IANA. Mudanças de dia e de
offset são calculadas por Temporal. Horários inexistentes são recusados; os
ambíguos exigem escolha entre primeira/segunda ocorrência, mostrando os offsets.
Não há restrição a horários passados nem teto de duração de produto. Os controles
operam entre os anos 0001–9999, permitindo serialização RFC 3339 de quatro dígitos;
o término que ultrapassa 9999, no civil ou em UTC, recebe erro. A timezone
respeita casing IANA e o limite de 64 bytes aceito pelo backend. O servidor continua autoritativo,
inclusive quando sua tzdata difere da base Intl do navegador.

## Threat model e limites

| Fronteira / risco                            | Controle implementado                                                                                                                                     |
| -------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Sessão → conversa; BOLA/BFLA/cross-workspace | Contexto da coleção canônica; escopo do draft por identidade. A API reautoriza cada request; visibilidade não concede capability.                         |
| Busca → seleção; TOCTOU                      | Elegibilidade consultada no servidor, seleção por ID, exclusão do organizer. Recusas posteriores preservam o draft e não identificam a pessoa inelegível. |
| Draft → payload; mass assignment/escalada    | Allowlist tipada, somente `attendee`, sem organizer/workspace/status/room key no body.                                                                    |
| Dados → DOM; XSS                             | React text nodes e controles nativos, sem HTML dinâmico. Unicode validado por code points e controles conforme domínio.                                   |
| Retry/concorrência                           | Trava síncrona por intenção, chave estável, payload congelado após resposta incerta, ID persistido para a segunda transação.                              |
| Busca/canal grande; exaustão                 | Debounce, cancelamento, proteção geracional, página 50, nenhum N+1 ou leitura de todas as páginas.                                                        |
| Erro → apresentação; exposição               | Mensagens locais por status, sem mensagens privadas do servidor; nenhum log de payload, chave, token ou participantes.                                    |
| Overlay → chat/chamada/WS                    | Provider e diálogo não escrevem no chat, timeline ou call state; portal não altera rota nem identidade dos owners.                                        |

A infraestrutura HTTP existente continua diagnosticando rotas/status. Não foi
criado pipeline de analytics. A confirmação de descarte é inline e Escape
fecha primeiro essa confirmação; o draft não é perdido. Durante writes, fechar
e editar ficam bloqueados; os resultados dos candidatos permanecem montados.
O foco retorna ao opener após liberar o modal. A função existente do chat,
`restoreOpenerFocus`, é compartilhada com o provider e verifica conexão,
visibilidade, disabled/inert e aceitação efetiva do foco com `preventScroll`.
Se o opener não puder receber foco, o shell tenta a navegação global e a busca.
Navegar não restaura foco na conversa anterior. A remoção da conversa das coleções
canônicas fecha o diálogo, preserva o draft e usa o fallback global.

## Evidências de validação

- Vitest: transformação temporal (DST gap/overlap, offsets, mudança de dia,
  limites, Unicode), payload, HTTP autenticado, renovação de token e troca de conta, dirty state, descarte/reabertura,
  seleção entre páginas, cancelamento/respostas atrasadas, erros e retries,
  idempotência e falha parcial. Não são testes com backend real.
- `CallSessionProvider.test.tsx`: harness com mídia mockada verifica identidade
  do floating, contagem de connect/stop/claim e aquisição do owner do socket
  ao agendar em outra conversa. Não prova uma sessão LiveKit/SFU real.
- `e2e/messaging/meeting-scheduling.spec.ts`: navegador com HTTP e WebSocket
  instrumentados; DM/grupo/canal, menu/header, seleção/paginação, 650 candidatos,
  falha parcial, transporte incerto, 429, dupla submissão, foco/teclado/mobile,
  identidade do composer/timeline, scroll e lifecycle do socket. Não prova
  WebSocket de produção nem teclado virtual físico ou leitores de tela reais.

O prompt mestre v4 e o documento de requisitos v5 não estavam no checkout.
A implementação segue o prompt da execução e os contratos/documentos versionados.

## Validação local em 2026-10-30

- Vitest completo: **7755 testes / 280 arquivos passando**, também em
  `make test-web`. A cobertura completa foi executada com
  `pnpm --filter @nchat/web exec vitest run --coverage --maxWorkers=4`, mantendo
  os thresholds oficiais. Uma execução de `make web-coverage` encontrou timeout no
  teste preexistente de paginação de 50 páginas sob carga concorrente; o caso
  isolado e a execução completa final passaram, sem alterar teste ou timeout.
- Cobertura geral: linhas **97,69%**, funções **96,36%**, branches **92,20%**,
  statements **95,96%**. Delta `src/meetings`: **134 testes**, linhas **99,69%**,
  funções **100%**, branches **97,51%**, statements **99,72%**.
- Playwright da feature: **18 passando**. Suíte geral: **519 passando, seis
  skips preexistentes e uma falha** em
  `quote-history-navigation.spec.ts:193` (assertion de foco na linha 211).
  A mesma falha foi reproduzida numa exportação de `HEAD` (`c531fe82`) sem a
  implementação, servida separadamente em localhost:5174. Não foi alterada
  por pertencer ao histórico de mensagens, fora desta issue.
- `make ci-static-web`, `make build-web`, formatação dos 36 arquivos alterados/
  novos e `git diff --check`: aprovados. ESLint registra três avisos em viewport
  não alterado. Build registra aviso de chunks maiores que 500 kB.
- `make format-check` global é bloqueado somente por três JSONs `.swarm`
  preexistentes/não rastreados, preservados sem modificação.
- SonarJS local **4.2.2**, regras recomendadas: **zero achados** nos 12 arquivos
  novos de código. Máximos: ciclomática **10**, cognitiva **8**. SonarQube não
  estava disponível; ratings, Quality Gate remoto e duplicação CPD não foram
  medidos.
- `pnpm audit --prod`: zero Critical/High; um Moderate preexistente em
  `@tiptap/core` 2.27.2 (`GHSA-cp6q-959q-f8rh`), versão já presente em `HEAD`.
  Nenhum advisory foi apontado para as dependências adicionadas.

## Correções locais pós-CQ em 2026-11-03

`HEAD`, `origin/develop` e merge-base eram `c531fe82`. As rotas registradas,
DTOs, domínio, normalização de participantes, testes HTTP e configuração do
gateway foram confrontados com o frontend. O gateway normal encaminha
`/api/chat/*` sem strip; somente probes têm routers de reescrita separados.

| Item                  | Frontend antes/depois                                                                                              | Backend efetivo e documentação                                                 | Resultado                         |
| --------------------- | ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------ | --------------------------------- |
| Create                | `POST /api/chat/meetings`                                                                                          | Mesma rota, 201/200, Bearer/JSON/Idempotency-Key, envelope compartilhado       | Compatível; mantido               |
| Candidatos            | `GET /api/chat/meeting-candidates/{kind}/{id}`                                                                     | `items[{user_id,display_name}]`, `next_cursor` opcional; prefixo, limit/cursor | Compatível; mantido               |
| Invite all            | `POST /api/chat/meetings/{id}/participants/invite-all`, `{}`                                                       | Mesma rota; `{data:{added}}`                                                   | Compatível; mantido               |
| Texto                 | Título 120; descrição 5000 code points                                                                             | Trim Unicode e controles conforme domínio                                      | Compatível; mantido               |
| Duração/participantes | Positiva sem teto de 720; sem cota de 100 no create                                                                | Intervalo positivo; body de 64 KiB; bulk até 500 elegíveis                     | Compatível; mantido               |
| Timezone/ano          | Antes aceitava casing que Intl normaliza e verificava apenas ano civil final; depois valida casing e fronteira UTC | IANA case sensitive, até 64 bytes; timestamps JSON RFC3339                     | Bordas corrigidas, com regressões |

Não foram encontradas rotas `/api/v1/meetings`, flags `auto_seeded`/`is_self`
ou limites de 200/4000/720/100 nos contratos de reuniões inspecionados. Nenhum
backend foi alterado para acomodar as correções.

CQ-01 tem regressões para foco/ARIA da duração personalizada e erros dependentes
sem apagar erros independentes. CQ-02 preserva página/lista e seleção em falhas
400/429 sem GET adicional, mantendo cancelamento, debounce, reset de contexto e
retry explícito. CQ-03 altera somente `.gitignore` para `.swarm/`, cobrindo todos
os níveis sem modificar dados locais. CQ-04 reutiliza o foco do shell e fecha
contexto removido; inclui verificação real de fallback após resize desktop →
mobile. CQ-05 cobre no componente o retry de busca e a paginação/seleção única.

### Bundle

Dois builds isolados com a mesma configuração e dependências instaladas mediram
os arquivos de JS iniciais efetivamente emitidos (kB decimais):

| Fonte                                    | Inicial minificado |      Gzip |
| ---------------------------------------- | -----------------: | --------: |
| `HEAD`                                   |          662,69 kB | 201,05 kB |
| Feature antes das correções desta rodada |          840,60 kB | 253,92 kB |
| Delta da feature                         |         +177,91 kB | +52,88 kB |

O grafo Rollup confirma Temporal e JSBI no entry inicial; somam 159113 bytes
renderizados antes do processamento final, medida que não representa custo
marginal gzip. O import é `AppShell → provider → meetingSession → meetingTime →
Temporal`. Lazy loading somente do diálogo não remove essa dependência inicial.
Adiar também a criação do draft exigiria carregamento assíncrono com tratamento
de falha/identidade/foco, uma mudança de fluxo além dos findings. A otimização
fica para uma mudança específica; não foi alterado o code splitting para
silenciar o warning, nem substituídas as regras temporais por cálculo manual.

### Revalidação das correções

Em 2026-11-03, os gates `make ci-static-web`, `make test-web`,
`make web-coverage`, `make build-web` e `make format-check` passaram.
A suíte web teve 7781 testes aprovados; a cobertura global foi 95,95% statements,
92,23% branches, 96,35% functions e 97,67% lines. A cobertura de reuniões foi
99,75% / 98,43% / 100% / 99,72%, respectivamente; 160 testes passaram no recorte
reuniões e helper compartilhado de foco. A análise local dos 13 arquivos de
produção desse recorte encontrou complexidade ciclomática máxima 10, cognitiva
máxima 8 e nenhum finding nas regras SonarJS aplicadas. SonarQube remoto não
estava disponível.

Os 19 cenários Playwright de agendamento passaram na versão final. A suíte E2E
geral teve 523 aprovações e seis skips já existentes, sem falhas; após o ajuste
final de apresentação da busca pausada, os 19 cenários específicos foram
reexecutados. O fallback mobile considera o estado fechado do drawer antes de
sua animação terminar, evitando restaurar foco para um controle que ficará
oculto. Nenhum dado `.swarm` foi alterado ou removido; os sete artefatos locais
inspecionados mantiveram seus hashes SHA-256. Não houve stage, commit ou PR.
