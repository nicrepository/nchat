# Admin Console — Configuration & Secrets Management (issue #580)

**Branch:** `feature/admin-580-config-secrets-management`
**Depende de:** issue #578 (fundacao) e #579 (gestao operacional)
**Implementa:** registry tipado de configuracao, estado efetivo, validacao
server-side, diff, versionamento, concorrencia otimista, rollback e auditoria.

---

## O que foi entregue

| Slice | Escopo                                                                                      |
| ----- | ------------------------------------------------------------------------------------------- |
| A     | Registry server-side com classe, fonte de verdade, tipo, faixa, capability e nota de perigo |
| B     | Estado efetivo: valor do banco, valor observado do ambiente, status de credencial           |
| C     | Preview com validacao server-side e diff tipado                                             |
| D     | Aplicacao sob compare-and-swap, versionamento e auditoria                                   |
| E     | Rollback forward-only para o que e tecnicamente seguro reverter                             |
| F     | Tela `/configuration` no `apps/admin-web`                                                   |

O inventario versionado esta em
[`docs/security/config-inventory.md`](../security/config-inventory.md); o
contrato em [`docs/api/admin-endpoints.md`](../api/admin-endpoints.md); a
politica em [`docs/security/rbac-matrix.md`](../security/rbac-matrix.md).

---

## Decisoes que valem registrar

### So existe uma configuracao de plataforma editavel, e nao e uma limitacao da tela

O levantamento do repositorio encontrou exatamente um documento de configuracao
que e (a) de plataforma, (b) armazenado no banco e (c) lido pelo servico que o
aplica na propria requisicao que o aplica: `auth.auth_policy_settings`. Todo o
resto e variavel de ambiente lida no boot, vinda do ConfigMap `nchat-config`
(Git) ou de um Sealed Secret.

Isso decide quase tudo o que segue. Persistir **e** aplicar para a classe A, o
que dispensa maquina de estados de rollout; e nao ha classe C editavel porque
nao existe mecanismo aprovado de rollout acionavel pela Admin API — nem Flux,
nem Argo, nem service account com permissao de patch. A issue previa esse caso e
manda classificar como somente leitura em vez de improvisar, e foi o que se fez.

### Nao existe classe B, e a ausencia e verificada

Nao ha backend de secret que a Admin API possa escrever. A constante
`ConfigClassRuntimeSecret` existe, nenhuma definicao a usa, e
`ValidateConfigCatalog` falha se alguma passar a usar. O efeito pratico e que
adicionar a primeira credencial editavel obriga a desenhar o caminho de escrita
antes, em vez de aparecer atras de uma flag conveniente.

Consequencia visivel na tela: uma credencial mostra **Configurado** ou **Nao
configurado**, aponta o runbook de rotacao, e nao tem botao algum. Nao ha
"mostrar", nao ha "substituir".

### O historico nao consegue guardar credencial

`auth.admin_config_version_changes.value_from` e `value_to` sao JSONB com
`CHECK (jsonb_typeof(...) IN ('number','boolean','null'))`. Uma string nao entra
ali. Isso e mais forte do que "o codigo nao grava": um erro futuro tambem nao
grava. O teste de integracao
`TestPostgreSQLConfigStore_HistoryRefusesANonScalarValue` prova contra o banco
real.

### Concorrencia: um statement, sem janela

```sql
UPDATE auth.auth_policy_settings
SET ... , revision = revision + 1
WHERE id = 1 AND revision = $expected
```

A verificacao e a escrita sao o mesmo statement e o mesmo snapshot. Nao ha
read-then-write para perder corrida dentro. `TestPostgreSQLConfigStore_ConcurrentSavesProduceOneWriteAndOneConflict`
dispara os dois saves de verdade e exige exatamente uma escrita e um conflito.

A `revision` mora na propria linha, e nao numa tabela ao lado, porque um
contador separado pode ser incrementado por quem nao mudou valor nenhum, ou
esquecido por quem mudou.

### Revision e precondition respondem perguntas diferentes

O mesmo `WHERE` carrega duas assercoes, e nenhuma substitui a outra:

- **`revision = $expected`** protege contra uma edicao feita desde que o
  formulario foi carregado. Dois administradores salvando ao mesmo tempo
  produzem uma escrita e um conflito.
- **as preconditions** protegem contra uma _versao_ que deixou de estar em
  vigor. Reverter `10 -> 20` depois que alguem moveu o valor para `30` apagaria
  a mudanca dessa pessoa, e a revision nao percebe: o console carregou **depois**
  daquela escrita, entao a revision dele esta em dia.

Um rollback e tudo ou nada. Se um campo da versao ja mudou, o rollback inteiro e
recusado em vez de restaurar os campos que ainda batem. A comparacao usa
`IS NOT DISTINCT FROM` e nao `=`, porque uma configuracao anulavel pode
legitimamente estar sem valor e `coluna = NULL` nunca e verdadeiro — o que
transformaria "continua vazio" em conflito permanente.

### Preview de rollback e o mesmo calculo do rollback confirmado

Reverter uma versao tem rota propria
(`POST /config/versions/{id}/rollback/preview`) e nao passa pelo preview
generico. O console informa **so** a identidade da operacao — qual versao, e a
revision que ele leu por ultimo. Os valores a restaurar, as preconditions e o
veredito saem da versao registrada, no servidor, pela mesma derivacao
(`rollbackRequest`) que o rollback confirmado usa.

Isso existe porque o contrario ja tinha um bug embutido: o console montava a
mutation a partir do historico que ele mesmo renderiza. Com `v1: 10 -> 20` e
`v2: 20 -> 30`, reverter `v1` virava a edicao generica `30 -> 10` — um diff que
parecia aplicavel e que o apply so recusava depois do clique. Historico no
frontend e apresentacao; nao pode virar autoridade para construir mutation
administrativa.

O diff do plano descreve a transicao da **propria versao**
(`version.To -> version.From`), nao um diff contra o valor atual. Enquanto a
versao esta em vigor os dois coincidem; quando nao esta, descrever contra o
estado atual mostraria uma operacao diferente da que foi pedida.

`superseded` e `stale` continuam separados: `stale` e "o documento mudou desde
que **voce** leu"; `superseded` e "a **versao alvo** nao esta mais em vigor". Um
rollback superado normalmente tem `stale: false`, porque o console carregou
_depois_ da mudanca que o superou — e por isso que optimistic locking sozinho
nao pega esse caso.

O preview e informativo. O apply revalida tudo atomicamente de qualquer forma:
entre as duas requisicoes outra pessoa pode mudar a configuracao, e um preview
nunca autoriza uma escrita.

### Commit confirmado nao vira falha

A escrita devolve a linha que ela mesma gravou (`RETURNING`), entao nao existe
releitura depois do commit. Isso e o que impede o pior caso: commit bem-sucedido
seguido de leitura falha era reportado como `Applied=false`, com a versao
perdida e auditoria de falha — e um cliente que recebe erro reenvia a mutation.
Depois do commit, a resposta, a versao registrada e o evento de auditoria
descrevem esse commit, e nada que aconteca depois pode desmenti-lo.

### Perigo e propriedade do valor, nao do campo

`Dangerous(novoValor) bool` por definicao. Endurecer uma politica nunca e
perigoso; enfraquece-la e, tenha o valor chegado por edicao ou por rollback.
Isso e o que faz "reverter um endurecimento" exigir `admin.superuser` sem
nenhuma regra extra escrita para o rollback.

### Expiracao de senha e a prova de que a configuracao nao e decorativa

`auth.password.expiration_days` e a unica configuracao editavel que nao tinha
consumidor em runtime: era gravada, versionada e auditada, e nao mudava
comportamento nenhum. Agora o `auth-service` a le no proprio login, junto do
resto da politica, e compara com
`auth.user_password_credentials.password_changed_at` — coluna que ja existia com
exatamente essa semantica, e que todos os caminhos que definem senha (criacao
manual, ativacao por convite, reset) ja reiniciavam.

A recusa acontece **depois** de a senha ser verificada e **antes** de qualquer
concessao: nenhum dispositivo e vinculado, nenhuma sessao e criada, nenhum token
e emitido. E registrada com motivo proprio (`password_expired`), que a consulta
de lockout nao conta: apresentar a senha correta nao e ataque de forca bruta, e
bloquear a conta por isso puniria justamente quem e dono dela.

O erro tem codigo proprio (`password_expired`, 401). E o unico 401 do login que
diz mais que "invalid credentials", e pode: so e alcancavel por quem ja provou a
senha.

### Rollback e forward-only

Reverter cria uma **nova** versao que nomeia a que ela desfaz
(`reverts_revision`). O historico nunca encolhe, e um ciclo aplicar → reverter →
aplicar deixa tres registros em vez de apagar um. A elegibilidade e recalculada
contra o registry de hoje: se a faixa aceita apertou, restaurar o valor antigo e
recusado com `409`, para que o historico nao vire um caminho em volta da
validacao.

### Falha nao vira versao

Uma validacao que falhou ou uma capability que faltou nunca chegaram a linha de
configuracao, entao registra-las como versao descreveria um estado em que a
plataforma nunca esteve. Elas viram evento de auditoria com `result = denied`,
que e onde uma recusa pertence. Por isso o enum de estado de versao nao tem
`pending`, `applying` nem `failed`: sao transicoes que este desenho nao alcanca,
e modelar transicao impossivel e como se acaba com codigo que ninguem sabe
exercitar.

---

## Operacao

### Alterar a politica de autenticacao

1. Console → **Configuracoes**.
2. Editar os campos. A validacao local e so conforto; o servidor revalida.
3. **Revisar alteracoes** — o diff vem do servidor, calculado contra o estado
   atual, nao contra o que o formulario acha que e o estado.
4. Conferir servico afetado, impacto e avisos.
5. Para alteracao perigosa: informar o motivo (obrigatorio) e ter
   `admin.superuser`.
6. **Aplicar**. A resposta traz a nova revisao.

O efeito e imediato: o proximo login, troca de senha, convite ou registro de
dispositivo ja usa o valor novo. Nao ha restart, nao ha rollout e nao ha cache.

### Quando aparece conflito (`409`)

Outra pessoa salvou entre o carregamento do formulario e o clique. Nada foi
escrito e nada foi mesclado. Recarregue a tela, confira o que mudou e revise o
diff de novo contra o estado atual.

### Reverter uma alteracao

Historico → **Reverter** na versao desejada. O fluxo e o mesmo: preview do
servidor, confirmacao, e uma nova versao apontando para a revertida. Se o botao
nao aparece, a versao nao e reversivel — chave removida do registry, ou valor
antigo que hoje seria recusado.

### Rotacionar uma credencial

Nao e aqui. Siga
[`sealed-secrets-rotation.md`](sealed-secrets-rotation.md): manifesto local,
`scripts/secrets/sealed-secrets-seal.sh`, commit do SealedSecret, aplicacao e
restart dos workloads. O console serve para confirmar depois que a credencial
ficou **Configurada** — sem mostrar valor algum.

### Alterar uma configuracao classe C ou D

Commit no ConfigMap ou no manifesto correspondente e rollout. O console mostra o
valor efetivo que o pod do `admin-service` observa; ate o rollout acontecer, ele
mostra o valor antigo, que e exatamente o valor que os servicos estao usando.

---

## Limitacoes conhecidas

Registradas explicitamente porque a issue pede cenarios que a plataforma ainda
nao consegue executar com seguranca:

| Cenario da issue                      | Situacao                                                                                                                         |
| ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| Substituir/rotacionar secret pela API | **Nao implementado.** Nao existe backend de secret gravavel pela Admin API. Definicoes sensiveis sao somente leitura.            |
| Rollout controlado pela Admin API     | **Nao implementado.** Nao ha mecanismo de deploy aprovado acionavel pela API. Classe C fica somente leitura, como a issue manda. |
| Change request / PR automatico GitOps | **Fora de escopo.** Nao ha automacao aprovada, e inventar uma aqui seria um segundo caminho de deploy sem revisao.               |
| Health check pos-aplicacao dedicado   | **Nao existe sonda.** Para a classe A a verificacao e o proximo login; nenhuma sonda foi inventada para preencher a secao.       |
| Validador que testa endpoint externo  | **Nao existe.** Nenhuma definicao editavel e URL, entao nao ha "testar endpoint" nem superficie de SSRF nesta camada.            |
| Estados `applying`/`rollback_pending` | **Inalcancaveis.** Persistir e aplicar; falha nao gera versao. Modelados apenas os estados que este desenho alcanca.             |

---

## Validacao

Testes focados:

```bash
cd services/admin-service
go test ./internal/domain/ ./internal/service/ ./internal/storage/ ./internal/http/
```

Suite PostgreSQL (opt-in; sem a variavel, e pulada):

```bash
cd services/admin-service
ADMIN_TEST_DATABASE_URL='postgresql://nchat:...@localhost:5432/nchat_test?sslmode=disable' \
  go test ./internal/storage/ -run PostgreSQL -count=1
```

Console:

```bash
cd apps/admin-web
npx vitest run
npx playwright test admin-configuration
```

Contrato de rota (prova que `/api/admin/config*` chega ao pod sem o prefixo, em
todos os overlays):

```bash
bash scripts/ci/admin-route-contract-check.sh
```

Migrations:

```bash
make migrations-check
```
