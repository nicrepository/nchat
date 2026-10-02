# #1043 — Ownership: migrations e ativação Blue/Green

## Contrato e pré-requisitos

Grupos e canais privados usam `member/admin/owner` em `ownership_role`. Durante
coexistência com releases anteriores à #953, `role` conserva `member` nos grupos
e `member/moderator` nos canais. `moderator` de canal corresponde a `admin`;
papéis de workspace não são migrados. A coluna transitória é necessária para
compatibilidade com leitores antigos e só poderá ser removida em contract futuro.

Aplicar as migrations pelo runner oficial, com credenciais do migrator e backup
verificado. A migration 64 exige as migrations 60–63; não altera seus checksums,
não ativa ownership e não executa o backfill automaticamente. Não usar o owner
da aplicação para conceder privilégios novos a scripts.

O operador deve identificar o release por SHA completo e pelo manifesto de imagens
imutáveis já utilizado pelo deploy. Antes da ativação, registrar evidência de que
chat, auth, admin e workers ativos **e reservados para rollback** compreendem os
papéis normalizados e o schema expandido. Readiness sozinho não prova isso.
Retirar todo binário incompatível, inclusive instâncias sem tráfego, e substituir
o rollback target por um artefato compatível ensaiado. Usar o inventário e a
reserva de rollback do [runbook de produção](production-blue-green-deployment.md).

## Expand, backfill e preflight

Executar da raiz do repositório, na conexão autorizada do ambiente. Configurar
`PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` e autenticação via `.pgpass`/secret;
não colocar senha ou DSN em logs ou evidência.

```sh
pnpm migrations:status
pnpm migrations:up
psql -X --no-password -f scripts/db/ownership/backfill.sql
psql -X --no-password -f scripts/db/ownership/preflight.sql
```

O backfill mantém owners elegíveis já existentes. Para conversas órfãs, exclui
candidatos guests e prefere criador elegível. Grupos usam o membro mais antigo
como fallback; canais privados preferem admin legado e depois o mais antigo.
Desempate: `joined_at ASC, user_id ASC`. Conta precisa estar ativa/não excluída,
workspace ativo e membership ativa no workspace correto; grupos exigem também
membership ativa na conversa. Canais não possuem coluna de status na membership.
Guests já designados owner continuam owners válidos.

Conversas sem participantes elegíveis não recebem proprietário artificial.
Conversas com participantes ativos mas sem candidato automático, incluindo um
grupo composto somente por guests sem owner, aparecem no preflight e bloqueiam
a ativação. Resolver a inconsistência por procedimento autorizado existente;
não inventar usuário, rebaixar dados ou ignorar o preflight.

`owners_assigned` conta promoções, conservando o contrato da função existente.
`audited_role_changes` conta também inicialização da representação transitória.
A repetição sem mudanças de entrada retorna zero e não acrescenta auditoria.
`chat.ownership_audit` registra alterações com motivo `backfill`; o runner registra
filename, checksum, versão e clean/dirty em `public.schema_migrations`. Arquivar a
saída operacional e o código de saída, incluindo falhas. Uma falha transacional
não deixa auditorias de sucesso; sequences podem ter lacunas após rollback.

Preflight retorna identificadores internos e quantidade de órfãos; resultado
exigido: **zero**. Qualquer inconsistência retorna código de saída diferente de
zero. A consulta independente é diagnóstica: a ativação repete backfill/preflight
sob locks, antes de ampliar constraints ou copiar papéis.

## Ativação explícita

Agendar janela para locks. Backfill permite leituras e bloqueia writers durante a
transação; ativação também bloqueia leituras das tabelas de membership de
conversa porque altera constraints. Leituras de contas/workspaces continuam
permitidas. Ambos incluem workspace, recursos, memberships e contas. Timeout de
lock: 5 segundos; timeout operacional: 5 minutos. Falha/timeout aborta a transação;
reagendar a janela e repetir a operação inteira. Não executar em deploy automático.

```sh
psql -X --no-password \
  -v legacy_retired=true \
  -v rollback_target_sha=SHA_COMPLETO_DO_ARTEFATO_COMPATIVEL \
  -v retirement_evidence=REFERENCIA_DO_REGISTRO_OPERACIONAL \
  -f scripts/db/ownership/activate.sql
```

Substituir os placeholders antes de executar. SHA: 40 caracteres hexadecimais
minúsculos. Evidência: referência não sensível de 1–256 caracteres, usando letras,
dígitos ou `_./:#-`; não passar texto livre, tokens, URLs com query ou PII.
O booleano é uma declaração operacional, não descoberta automática de Kubernetes.
O SHA sozinho não certifica compatibilidade: o inventário, manifesto e ensaio são
pré-requisitos. A função SQL de preparação também verifica os parâmetros, inclusive quando
invocada diretamente com a conta autorizada do migrator; ela conserva locks até
commit, mas não altera constraints nem ativa o rollout por conta própria.

Na primeira ativação bem-sucedida, a transação expande `role`, copia os valores
transitórios e registra target/evidência/timestamp com `enabled=true`. Repetição com
o mesmo target não modifica dados nem timestamp; outro target é recusado. Registro
ausente, estado incoerente ou órfãos recusam a operação, sem alterações parciais.

Se ownership já estava ativado pela ferramenta anterior à #1043, não presumir
seu rollback target: a nova ativação recusa a falta de evidência. Após verificar o
inventário e ensaiar o artefato, o DBA pode reparar somente os dois metadados novos
numa transação com os mesmos locks e preflight, preservando `enabled` e o timestamp
original. Usar os mesmos formatos validados; registrar essa intervenção no ticket.
A migration 64 não preenche evidência retroativamente.

## Abort, rollback e contract

Antes da ativação, abortar significa manter `enabled=false`: o schema expandido e
os valores legados permitem continuar com o release anterior à #953. O down da
migration 64 restaura a função anterior, remove apenas seus metadados/tooling e
preserva memberships/auditoria; o runner continua verificando os checksums.
Down exige rollout presente/pré-ativação e valores legados em `role`: desabilitar
manualmente a flag ou apagar evidência não reverte os dados normalizados.

Depois da ativação, **o rollback target é o SHA registrado em
`chat.ownership_rollout.rollback_target_sha`**, correspondente ao artefato
compatível ensaiado. Fazer rollback da aplicação pelo fluxo de slots existente,
conservando migrations, constraints e ambas as representações. Não voltar para
binário anterior à #953 nem executar down do schema ativado: migrations 60 e 64
recusam esse downgrade. Ativação não garante reversão semântica para o legado.

O schema gate de rollback lê migrations do Git e não detecta sozinho a ativação
manual; anexar a evidência de ativação e consultar o target registrado antes de
mover tráfego. A migration 64 contém somente expand; o DDL de ativação permanece no script
operacional explícito. Não é necessária atestação de exceção para a migration.
O gate de schema não substitui a validação operacional do estado ativado.

Remover `moderator` da constraint ou eliminar a coluna transitória exige outro
contract, depois do retirement de todos os readers/writers e rollback targets que
ainda dependam deles. Nenhum cleanup destrutivo é executado pela #1043.

## Verificação dirigida

Em PostgreSQL descartável, nunca no banco de aplicação, configurar
`OWNERSHIP_TEST_DATABASE_URL` para a base exclusiva `ownership_953_test`, com
`psql` instalado, e executar:

```sh
go test ./services/chat-service/internal/storage \
  -run '^TestOwnership(MigrationUpDown|BackfillTable|OperationalActivation)PostgreSQL$' \
  -count=1 -v
```

Os três testes cobrem migração com dados anteriores, rollback suportado,
backfill determinístico/idempotente, mapeamento legado, isolamento de workspace e
os scripts reais de backfill/preflight/ativação. Compatibilidade é comprovada no
contrato SQL e nas projeções existentes; o ensaio do artefato exato instalado é
obrigatório na operação, não é simulado como teste de dois slots de produção.
