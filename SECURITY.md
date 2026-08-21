# Security Policy

## Politica de release

Nenhum release pode sair com vulnerabilidade Critical ou High conhecida sem mitigacao documentada.

## Checks obrigatorios

Backend Go:

```bash
go test ./...
go vet ./...
govulncheck ./...
```

Frontend:

```bash
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

Seguranca:

```bash
trivy fs .
trivy config .
gitleaks detect --source .
```

## Security CI

O pipeline de seguranca executa:

- `govulncheck` por modulo Go para vulnerabilidades conhecidas com analise de codigo.
- Secret scanning com gitleaks no GitHub Actions e fallback local para Trivy quando gitleaks nao estiver instalado.
- Trivy filesystem scan para dependencias e artefatos versionados.
- Trivy config scan para IaC e manifests Kubernetes.

Os scans de Trivy devem falhar para severidades HIGH e CRITICAL quando configurados no CI.

Falsos positivos devem ser tratados explicitamente:

1. Abrir issue com evidencia do achado.
2. Justificar por que o achado e falso positivo ou nao exploravel.
3. Definir prazo de correcao ou mitigacao.
4. Registrar qualquer ignore com escopo minimo e motivo.
5. Nunca ignorar silenciosamente.

## Modelo de confianca do Repository governance

O workflow `Repository governance` usa o evento `pull_request`. Pull Requests
sao codigo nao confiavel e o workflow possui somente:

```yaml
permissions:
  contents: read
```

Nenhum secret e disponibilizado ou necessario ao check.

Os scripts executados apos o checkout fazem parte do conteudo da PR. Portanto,
uma PR que altere o proprio mecanismo de governance pode alterar o
comportamento daquela execucao. Essa limitacao de self-modification e conhecida
e deliberadamente aceita no modelo atual, compensada por revisao humana
obrigatoria antes do merge. Alteracoes em mecanismos de governance devem
receber atencao explicita durante o code review.

Nao use `pull_request_target` com checkout ou execucao de codigo nao confiavel
apenas para tentar resolver essa limitacao. Mudancas futuras que aumentem
permissoes, adicionem secrets ou alterem este modelo de confianca exigem nova
Security Review.

## TLS dev/staging

- Endpoints publicos do MVP devem exigir TLS 1.3.
- O ambiente local usa Traefik HTTPS em `https://nchat.local:8443` com `VersionTLS13` como minimo quando suportado.
- Certificados locais devem ser gerados com `make dev-tls-generate` e permanecer fora do Git.
- O fallback `openssl` e apenas self-signed local e nao representa confianca publica.
- O overlay `k3s-staging` usa Ingress TLS placeholder com Secret `nchat-staging-tls`.
- Cert-manager e TLS publico real nao estao configurados nesta etapa.

## Regras para segredos

E proibido commitar:

- arquivos `.env`;
- tokens;
- senhas;
- chaves privadas;
- certificados privados;
- dumps reais;
- logs sensiveis.

Use arquivos de exemplo sem valores reais quando necessario.

Sealed Secrets e obrigatorio para versionar secrets do MVP:

- Escopo `strict` e o padrao.
- `namespace-wide` exige justificativa no PR.
- `cluster-wide` e proibido sem excecao aprovada.
- Todo secret deve ter owner em `docs/security/secrets-owners.md`.
- Rotacao manual deve seguir `docs/runbooks/sealed-secrets-rotation.md`.
- CI bloqueia manifests unsealed e marcadores obvios de secrets plaintext em locais proibidos.

## Health endpoints

- `/healthz` e `/readyz` nao devem retornar secrets, DSNs, tokens, stack traces, hostnames internos, variaveis de ambiente sensiveis ou detalhes de topologia sensivel.
- Readiness pode indicar status geral (`ready`, `degraded`, `unready`) e nomes de checks operacionais aprovados, mas nao deve revelar credenciais nem infraestrutura interna.

## Regras para WebSocket

- Validar token no handshake.
- Validar origem.
- Validar sessao.
- Validar permissoes por canal.
- Aplicar rate limit.
- Definir tamanho maximo de mensagem.
- Aplicar timeout de inatividade.

## RBAC (RF-74)

A matriz canonica de permissoes por papel, escopo e acao esta em
`docs/security/rbac-matrix.md`. As secoes abaixo registram as decisoes
concretas; a matriz e a referencia completa.

Os cinco papeis do RF-74 e onde cada um vive:

- **Admin Master** — escopo de plataforma. Desde a issue #578 existe um modelo
  persistido: `auth.admin_principals` diz quem e administrador de plataforma e
  `auth.admin_principal_roles` → `auth.admin_role_capabilities` diz o que cada um
  pode. A identidade de bootstrap `X-NChat-Admin-Token` continua existindo, agora
  estritamente como caminho CLI das tres rotas `/admin/*` do auth-service. Nem o
  papel nem o token concedem leitura de canal ou de DM. Ver "Console
  administrativo" abaixo.
- **Admin de Workspace** — `chat.workspace_members.role IN ('owner','admin')`,
  exposto como `domain.CanManageWorkspace`. `owner` **nao** e Admin Master: e a
  autoridade maxima dentro de um workspace e nada fora dele.
- **Moderador** — `chat.workspace_members.role = 'moderator'`, criado pela
  migration 000022 e exposto como `domain.CanModerateWorkspace`. Nao administra
  o workspace, nao altera settings e recebe `403` na API administrativa de
  usuarios.
- **Usuario** — `member`.
- **Guest** — `guest`. Membership de workspace **nao concede canal algum**.

O `moderator` de `chat.channel_members` continua sendo um papel **por canal** e
nunca e lido como autoridade de workspace. Nenhuma decisao de autorizacao
consulta essa coluna.

Nenhuma claim de papel existe no JWT: o papel e lido de
`chat.workspace_members` a cada request. Nao existe rota que escreva
`chat.workspace_members.role` — a atribuicao de papel e feita no banco, como
ja era para `owner` e `admin`.

## Escopo de canais do Guest (RF-74)

Um Guest acessa **somente os canais em que foi explicitamente incluido**
(`chat.channel_members`). Integrar o workspace nao lhe da nenhum canal publico,
nem o `#geral`.

A regra tem uma unica definicao: a funcao SQL
`chat.channel_visible_to_user(channel_id, user_id)` (migration 000022),
compartilhada por chat-service, file-service e media-service. Ela e a autoridade
— o backend decide, o frontend nao e controle de seguranca — e cobre pelo mesmo
predicado a listagem/sidebar, o acesso direto por ID e por slug, a leitura e a
publicacao de mensagens, o encaminhamento, reacoes, favoritos, pins, download de
anexos, o token de midia e o autorizador de subscricao do WebSocket. Um Guest
nao recebe eventos de canal ao qual nao pertence porque a subscricao passa pela
mesma consulta.

A verificacao de papel e uma **allowlist** (`owner/admin/moderator/member`), nao
uma negacao de `guest`: um papel nao reconhecido e recusado em vez de ser
tratado como membro comum, entao alargar o CHECK de papeis nunca alarga acesso
como efeito colateral. `domain.CanReadChannel` e a mesma regra em Go e deve
concordar com a funcao SQL exatamente.

Consequencias deliberadas para o Guest:

- **nao** entra sozinho em canal publico (`SelfJoinChannel` recusa) — sem isso,
  o isolamento seria contornavel em uma requisicao;
- **nao** cria canal (`domain.CanCreateChannel`), re-verificado no proprio
  `INSERT`;
- **nao** e adicionado automaticamente a `#geral`; chega la sendo adicionado,
  como a qualquer outro canal. O backfill `SyncGeneralMemberships` deixou de
  criar essas linhas e nunca remove as existentes.

Canal privado continua exigindo membership de canal para **todos** os papeis:
nem owner, nem admin, nem moderador leem um canal privado do qual nao
participam.

## Autorizacao para pin/unpin de mensagens

Fixar e desafixar mensagem (RF-05) nao exige papel de Moderador ou
Admin. Qualquer membro do workspace com acesso de leitura ao canal ou
a DM (incluindo Guest) pode fixar e desafixar. Mesma verificacao de
acesso usada para leitura de mensagens (RF-04), sem RBAC adicional.
Decisao de produto registrada em 2026-07-08 (issue #105), substituindo
a suposicao original de acesso restrito a Moderador/Admin.

O RF-74 nao alterou essa regra e a reforcou: pin e unpin reutilizam a
autorizacao de leitura do dominio em vez de terem uma excecao propria para
Guest. `PGXPinStore.AddPin` e `RemovePin` chamam
`chat.channel_visible_to_user` pelo mesmo fragmento que os favoritos usam,
entao um Guest incluido no canal fixa e desafixa, e um Guest fora dele recebe o
mesmo `404` nao-enumerante que recebe ao tentar ler.

## Autorizacao para categorias de canais

Criar, renomear, reordenar e excluir categoria de canal (RF-17) exige
papel ativo de `owner`, `admin` ou `moderator` no workspace, exposto como
`domain.CanManageChannelCategories`, que delega a
`domain.CanModerateWorkspace`. Usuario comum e Guest recebem 403.
Ler a listagem agrupada e aberto a qualquer membro ativo e nao amplia
acesso a canal: os canais de cada grupo vem da mesma politica SQL de
leitura usada pelo sidebar, entao um Guest ve os grupos com apenas os
canais que integra.

O RF-17 foi especificado como "Admin e Moderador" e teve de se contentar com
owner/admin: `chat.workspace_members.role` aceitava apenas
`owner/admin/member/guest`, e `moderator` existia somente em
`chat.channel_members`, como papel por canal. Tratar "modera algum canal" como
"pode reestruturar a sidebar do workspace" seria escalacao de privilegio, e
criar um papel de workspace que nenhum endpoint atribui seria codigo morto
alargando uma constraint de seguranca. O RF-74 criou o papel de verdade
(migration 000022), entao `CanManageChannelCategories` passa a dizer o que o
RF-17 pediu. O papel por canal continua sem ser consultado.

Junto com isso, as quatro mutacoes de categoria passaram a consultar
`CanManageChannelCategories` diretamente. Antes chamavam
`requireWorkspaceManager`, que aplica `CanManageWorkspace` -- o predicado
nomeado era decoracao, e alarga-lo para o RF-74 nao teria tido efeito nenhum
nessas rotas. Uma rota deve executar o predicado que ela nomeia.

Update e archive de canal **nao** acompanharam: continuam exigindo
`CanManageWorkspace`. Moderar a estrutura e a composicao de canais e diferente
de mudar o que um canal e ou de arquiva-lo.

## Autorizacao para adicionar membros

Adicionar participantes a um **canal** (issue #398) exige papel ativo de
`owner`, `admin` ou `moderator` no workspace, exposto como
`domain.CanManageChannelMembers`, que delega a `CanModerateWorkspace`. E o mesmo
gate usado para categorias e para a operacao inversa -- remover membro de canal.
Adicionar e remover a mesma linha sao a mesma autoridade;
`docs/runbooks/task-chat-channel-join-leave.md` ja chamava a adicao de
"manager-add flow". `MemberService.RemoveMemberFromChannel` deixou de repetir
uma lista de papeis propria e passou a consultar o mesmo predicado, para que as
duas nao possam divergir.

O RF-74 e o que alargou esse predicado de owner/admin para incluir o moderador:
ele foi escrito como costura nomeada exatamente para isso, e a costura foi
gasta. O alargamento chega ao store tambem --
`PGXMemberStore.AddChannelMembers` re-deriva a mesma lista de papeis dentro da
sua transacao, sob `FOR SHARE`, entao uma revogacao de papel em voo e
serializada contra a escrita em vez de correr com ela.

Deliberadamente **nao** e "qualquer membro do canal": isso permitiria a quem
apenas le um canal privado ampliar a audiencia dele, que e precisamente a
propriedade que um canal privado tem. O papel `moderator` de
`chat.channel_members` tambem nao e consultado -- ele e por canal, e moderar um
canal nao confere autoridade de workspace.

Adicionar participantes a um **grupo** (`chat.dm_conversations` com
`type = 'group'`) exige apenas participacao ativa na conversa. Nao ha gestor a
exigir: `chat.dm_members.role` e fechado por CHECK ao unico valor `'member'`,
entao um grupo nao tem owner, admin nem moderador. Usar `CanManageWorkspace` aqui
seria **mais permissivo, nao menos**: um admin de workspace nao e participante,
nao enxerga a conversa pela politica SQL de DM, e lhe dar autoridade sobre uma
conversa privada entre pares que ele nao pode ler seria escalacao. Usar
`created_by` congelaria o grupo quando o criador sai e essa coluna nunca e usada
para autorizacao neste servico. Qualquer participante ja pode criar um grupo novo
com qualquer pessoa do workspace, entao a regra nao concede poder novo.

DM 1:1 nao aceita a operacao: adicionar uma terceira pessoa converteria a
conversa em grupo, o que esta fora do escopo da issue.

Em ambas as rotas, um chamador sem permissao e um usuario inelegivel respondem o
mesmo `403`, sem dizer qual usuario nem se ele esta suspenso, deletado, em outro
workspace ou inexistente -- distinguir isso transformaria a rota em um oraculo de
contas.

O RF-74 nao ampliou o acesso a DMs. Participacao (`chat.dm_members`) continua a
unica pergunta feita em qualquer caminho de DM; `chat.workspace_members.role`
nao e consultada em nenhum deles, entao nem o Moderador criado pelo RF-74 nem o
Admin Master ganham leitura, detalhes ou administracao de conversa privada.

## Autorizacao administrativa de usuarios

`/auth/admin/users` e `/auth/admin/invites` sao browser-side e exigem
`BearerAuth` + `RequireActiveSession` + `RequireWorkspaceAdmin`. O workspace vem
de `PGXUserStore.GetAdminWorkspaceID`, que exige `role IN ('owner','admin')`
ativo em workspace ativo e nao le nenhum valor enviado pelo cliente. O RF-74
deliberadamente **nao** incluiu o `moderator` ai: moderar canais e categorias e
diferente de listar, convidar e suspender pessoas.

As rotas `/admin/users`, `/admin/invites` e `/admin/users/{id}/status`
permanecem atras de `AdminBootstrapGuard` (`X-NChat-Admin-Token`), CLI-only.
Elas nao foram substituidas pela issue #578 e continuam sendo o caminho de
provisionamento anterior a existencia de um administrador de plataforma. O que
mudou e que esse token deixou de ser o _unico_ escopo de plataforma: a issue #578
persistiu o modelo de administrador global descrito abaixo, e o primeiro
administrador passa a nascer de uma concessao no banco em vez de um segredo
estatico compartilhado.

## Console administrativo (issue #578)

O Admin Console e uma aplicacao separada (`apps/admin-web`), com bundle proprio,
Content-Security-Policy propria e host proprio, servida pela Admin API do
`admin-service` em `/api/admin`. O contrato esta em `docs/api/admin-endpoints.md`
e a operacao em `docs/runbooks/task-admin-console-foundation.md`.

### O que autoriza

Nao existe `isAdmin` em lugar algum. A unidade de autorizacao e a _capability_, e
cada endpoint privilegiado declara a que exige. As capabilities sao lidas do
banco a cada requisicao — nunca de uma claim de token — entao remover um papel
passa a valer na requisicao seguinte, e nao no proximo login.

Deny by default em tres pontos independentes:

- uma capability que a plataforma nao define e recusada mesmo para
  `admin.superuser`, entao um erro de digitacao num guard falha fechado;
- o conjunto vazio (principal sem papel, ou principal nao carregado) nao concede
  nada;
- o `CHECK` de `auth.admin_role_capabilities` impede que uma capability
  desconhecida sequer seja gravada.

Estar autenticado no NChat **nao** e autoridade administrativa: a sessao ativa
prova identidade, e `auth.admin_principals` decide o resto. As capabilities
enviadas ao frontend sao dado de apresentacao — elas escolhem o que a barra
lateral desenha, nunca o que um endpoint permite.

### A credencial do console

Cookie opaco gerado pelo servidor, nunca um token no Web Storage:

```
__Host-nchat_admin_session=<opaco>; Path=/; HttpOnly; Secure; SameSite=Strict
```

`HttpOnly` impede que um XSS no console leia a credencial; `SameSite=Strict`
retira o cookie de toda requisicao cross-site; o prefixo `__Host-` faz o
navegador recusar o cookie se ele nao for `Secure`, `Path=/` e sem `Domain` — o
que impede o host do chat, um subdominio irmao, de plantar ou ler a sessao
administrativa. Session fixation e impossivel por construcao: o valor e gerado
por `crypto/rand` no servidor e nenhum valor enviado pelo cliente e adotado como
identificador de sessao.

O token de acesso do chat e aceito uma unica vez, no handshake
`POST /api/admin/session`, para provar quem esta pedindo. Ele nao autoriza nada,
nao e persistido pelo console e nao vira a credencial do console.

`X-NChat-Admin-Token` nao participa de nenhuma rota da Admin API. Ele nao pode
ser enviado ao frontend, aparecer em bundle, em `localStorage`, em
`sessionStorage`, em cookie de navegador, em resposta ou em log.

### Sessao administrativa

Politica propria e mais restritiva que a do chat: idle de 15 minutos, vida
absoluta de 8 horas, ambas gravadas na propria linha contra a qual a requisicao e
autorizada. Cada handshake cria uma linha nova, entao a credencial depois da
autenticacao nunca e a mesma de antes dela. Revogar a sessao administrativa, o
principal ou o login de origem encerra o acesso; a revalidacao por requisicao
verifica os tres.

### Isolamento de origem do console (issue #578)

`/api/admin/*` e publicado **somente** no host administrativo. O host do chat
nao possui rota alguma para o `admin-service`, em nenhum ambiente versionado nem
no gateway local.

Isso e o que sobrevive a um XSS no chat: um atacante que roube o access token do
`sessionStorage` e tente `POST /api/admin/session` a partir da origem do chat
nao alcanca backend administrativo nenhum — a requisicao cai no catch-all da SPA
e nenhuma sessao administrativa e criada. Esconder o link no frontend nao
produziria essa propriedade; a recusa esta na tabela de roteamento.

Se o mesmo atacante enderecar o host administrativo, ele encontra a segunda
barreira: o preflight CORS da origem do chat e recusado, o cookie de sessao e
`__Host-` (host-only, sem `Domain`) e `SameSite=Strict`, e os metodos mutaveis
exigem origem reconhecida mais o token de dupla submissao.

`scripts/ci/admin-route-contract-check.sh` renderiza os overlays e o gateway
local e falha se qualquer host que nao seja o do console passar a apontar para o
`admin-service`.

### MFA administrativo

A autoridade de autenticacao continua sendo o Keycloak: o NChat nao armazena
TOTP, nao implementa segundo fator e nao mantem senha administrativa paralela.

O que a foundation acrescenta e a outra metade do controle. `OIDC_ADMIN_ACR_VALUES`
lista os valores de `acr` que o ambiente aceita como prova de que o fluxo
administrativo rodou. Vazio significa que nenhum requisito foi declarado.
Preenchido, o requisito e real e fail-closed: o login administrativo envia
`acr_values` ao provedor e o callback recusa um token que nao volte com um dos
valores configurados, respondendo `403 oidc_insufficient_assurance` sem criar
sessao.

Tres decisoes deliberadas:

- **`acr` ausente e recusa.** O claim e opcional em OIDC; "o provedor nao disse
  nada" nunca e "usou segundo fator".
- **Comparacao exata**, sem prefixo ou substring: um nivel de assurance e um
  identificador, nao uma escala que este servico possa ordenar.
- **Somente `app=admin`.** Uma politica administrativa nao muda como todas as
  pessoas do produto entram no chat.

O valor nunca e inventado por este repositorio: e o que o realm emite, e a
configuracao operacional esta em
`docs/runbooks/task-admin-console-foundation.md`.

### Single sign-on nos dois hosts

O console reutiliza o fluxo Keycloak/OIDC do chat inteiro — mesmo client, mesmo
state, nonce e PKCE. A unica diferenca e a URI de callback, porque as duas
aplicacoes vivem em origens diferentes.

A escolha e feita por um rotulo de um conjunto fechado (`chat` | `admin`) no
parametro `app` de `/auth/oidc/keycloak/login`. O rotulo seleciona uma URI que
esta na configuracao do servidor; ele nunca vira destino, e um valor fora do
conjunto recebe `400` sem iniciar login algum. Nao existe `returnTo`,
`redirect_uri` nem qualquer outro parametro de destino aceito do cliente — a
superficie de open redirect e, por construcao, vazia.

O contexto escolhido e gravado em `auth.oidc_auth_requests.app_context`, na mesma
linha dos hashes de state e nonce que o callback ja consome atomicamente. O
callback o relê dali, nao da requisicao que volta: entre a saida e o retorno o
navegador passa pelo provedor de identidade, e o retorno e exatamente o que um
atacante controla.

O redirecionamento final para o frontend continua sendo o caminho **relativo**
`/oidc-callback`, entao o navegador permanece na origem em que o callback rodou.

Um login OIDC bem-sucedido nao concede capability administrativa alguma: ele
produz uma sessao NChat comum, e `POST /api/admin/session` ainda consulta
`auth.admin_principals`.

### CSRF, CORS e cabecalhos

Metodos mutaveis exigem, alem do cookie `SameSite=Strict`, uma origem
reconhecida (`Origin`, ou `Referer` quando o navegador omite `Origin` — a
ausencia dos dois e recusa) e um token de dupla submissao derivado da sessao em
`X-NChat-Admin-CSRF`. O CORS e uma allowlist explicita; `*` e descartado no
carregamento da configuracao, porque com credenciais ele nunca e valido.

A CSP do console e mais estreita que a do chat: `connect-src 'self'`,
`frame-ancestors 'none'`, sem `unsafe-inline` em `script-src`, sem `unsafe-eval`,
sem `blob:`, sem `media-src`. `scripts/ci/web-security-headers-check.sh` valida
as duas politicas e falha se qualquer outra camada do repositorio passar a emitir
uma terceira.

### Auditoria

`auth.admin_audit_events` registra login administrativo, logout e negacao de
autorizacao, com ator, acao, recurso, resultado, timestamp e o `X-Request-ID` da
chamada. `metadata` e um objeto JSON montado campo a campo pelo produtor. Nunca
sao gravados: access token, refresh token, bootstrap token, senha, client secret,
conteudo de mensagem, header `Authorization` e header `Cookie`. Uma falha de
escrita e registrada no log do servico e nao derruba a requisicao — a trilha e
evidencia, nao pre-condicao.

### Observabilidade

A Admin API usa a instrumentacao HTTP compartilhada, cujas labels sao metodo,
rota e status. Nenhum e-mail, user ID, session ID ou identificador de recurso
entra em label de metrica, e nenhum token entra em trace.

## Regras para uploads

- Definir limite de tamanho.
  O limite maximo por arquivo e configuravel pelo admin do workspace
  (`chat.workspaces.max_upload_bytes`, default 262144000 = 250 MiB, faixa de
  1 a 512 MiB, somente multiplos exatos de 1 MiB) via
  `PATCH /api/chat/workspaces/{id}/upload-limit`, que exige papel ativo de
  `owner` ou `admin` — verificado no handler e novamente de forma atomica no
  proprio `UPDATE`. Valor invalido e recusado, nunca arredondado nem ajustado
  silenciosamente.
  O **file-service e a unica fronteira de tamanho**: ele autentica, autoriza o
  destino, rele a politica na mesma consulta e conta os bytes que efetivamente
  le, entao nem o gateway nem o frontend sao controle de seguranca e contornar
  qualquer um dos dois nao amplia o limite. `Content-Length` nunca decide. Um
  upload recusado nao fica persistido, nao permanece no SeaweedFS e nunca
  alcanca a fila do ClamAV.
  O gateway aplica um **teto tecnico estatico** de 536879104 bytes (512 MiB +
  8 KiB de overhead multipart) por meio de um upload guard nginx que limita o
  corpo **enquanto o transmite**, somente nas duas rotas de upload. O middleware
  `buffering` do Traefik e proibido: ele le o corpo inteiro antes da
  autenticacao, o que permitiria a um cliente sem credencial esgotar o disco do
  gateway. `make gateway-config-check` falha se ele voltar, se o teto divergir
  das constantes Go, se o streaming for desligado ou se um upload deixar de
  passar pelo guard.
  Uploads simultaneos sao limitados **em todo o cluster** por vagas globais e por
  usuario (`FILE_UPLOAD_MAX_CONCURRENT`, `FILE_UPLOAD_MAX_CONCURRENT_PER_USER`),
  implementadas com session advisory locks do PostgreSQL e adquiridas **depois**
  da autenticacao e da autorizacao e **antes** do primeiro byte do corpo. Excesso
  do usuario responde 429, cluster cheio ou admission indisponivel respondem 503,
  sempre com `Retry-After` e sem revelar capacidade interna. A indisponibilidade
  falha fechada: nunca vira capacidade ilimitada.
  Ver `docs/api/chat-upload-limit.md` e `docs/api/file-attachments.md`.
- Validar tipo.
- Armazenar fora do webroot.
- Usar envelope encryption.
- Executar scan assincrono com ClamAV.
- Bloquear download ate aprovacao do scan.

O bloqueio de download ate a aprovacao do scan e controlado por
`FILE_MALWARE_SCAN_REQUIRED` no file-service, cujo default e `true`. O valor
`false` existe apenas para ambiente de desenvolvimento sem scanner e e
recusado na inicializacao quando `APP_ENV` nao e um valor de desenvolvimento
(`development`, `dev`, `local`, `test`, `nchat-dev`). A verificacao falha
fechada: um `APP_ENV` desconhecido e tratado como ambiente implantado. Ver
`docs/api/file-attachments.md`.

O scan em si e assincrono (RF-22) e falha fechada em todas as direcoes:

- indisponibilidade, timeout ou resposta invalida do daemon **nunca** produzem
  aprovacao. Nada e gravado e o anexo continua em `pending_scan`, nao baixavel,
  ate que um scan real conclua;
- `FILE_MALWARE_SCANNER_ADDRESS` vazio nao afrouxa o gate: sem scanner o worker
  nao inicia e nenhum anexo e aprovado;
- o veredito e decidido exclusivamente server-side. Nao existe rota, campo de
  request ou evento vindo do cliente que alcance a transicao para `clean`;
- o gate vale para toda entrega de bytes derivados do arquivo -- download,
  HTTP Range, preview e streaming inline -- e nao apenas para o download.

## Regras para fetch de conteudo externo

Vale para toda requisicao de saida cujo destino e escolhido, direta ou
indiretamente, por um usuario. Hoje o unico caso e o preview de links por Open
Graph (RF-10) no file-service; a regra e permanente e vale para qualquer feature
futura com a mesma forma.

- O destino e julgado pelo **endereco IP que a conexao vai usar**, nunca pelo
  hostname. Resolver, verificar **todas** as respostas e conectar ao endereco ja
  aceito: nao pode existir janela entre a checagem e o uso (DNS rebinding). Um
  nome com varios enderecos e recusado se qualquer um deles nao for publico.
- Destinos nao publicos sao recusados em IPv4 e IPv6, incluindo as formas em que
  um endereco IPv4 viaja dentro de um IPv6. Metadata services de nuvem sao
  cobertos por essa regra, e com eles qualquer alias que resolva para eles.
  Bloquear por hostname literal nao e controle.
- Somente `http` e `https`, somente a porta default do esquema, nunca
  credenciais na URL.
- Cada redirect passa por toda a validacao de novo, e o numero de saltos e
  limitado.
- Proxies do ambiente sao ignorados: um proxy resolveria e conectaria no lugar
  do servico.
- TLS e verificado contra o hostname original. `InsecureSkipVerify` e proibido.
- Allowlist explicita de Content-Type, decidida antes de ler o corpo; corpo lido
  atraves de limite aplicado sobre bytes descomprimidos; timeouts curtos e
  finitos por fase.
- A rota e autenticada e tem rate limit por usuario. Um endpoint anonimo seria um
  fetcher aberto usando o endereco e a banda do deployment.
- Nenhum conteudo remoto e devolvido ao cliente: apenas metadados normalizados,
  como dados e nunca como markup.
- A recusa nunca revela o destino, o endereco ou a mensagem do servidor remoto,
  em resposta, log ou label de metrica.

Ver `docs/api/link-preview.md`.

## Processo para vulnerabilidade Critical/High

1. Criar issue restrita.
2. Criar branch `security/<id>-<descricao>`.
3. Corrigir.
4. Adicionar teste de regressao.
5. Rodar scanners.
6. Fazer merge via PR.
7. Criar release/hotfix se necessario.

## Observability

- Observability must not collect secrets, credentials or personally identifiable information.
- `Authorization`, `Cookie`, `Set-Cookie` headers, tokens and request bodies must never be recorded in metrics labels, span attributes or logs emitted by the observability middleware.
- Metrics labels must avoid high cardinality and must not include raw URL paths with user IDs or other variable segments.
- Grafana credentials in `infra/compose/.env.dev.example` are dev-only placeholders. Do not use them in staging or production. Real credentials must be stored in Sealed Secrets or the organisation secret manager.
- The `/metrics` endpoint must be protected in staging and production (firewall, network policy, or authentication). In local dev it is only accessible on `127.0.0.1`.
- Prometheus and Jaeger UI must not be exposed publicly. In local dev they bind to `127.0.0.1`. Kubernetes-level network policies will be added when deploying observability to k3s.
