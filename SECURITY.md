# Security Policy

## Política de release

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

- **Admin Master** — escopo de plataforma. Nao existe papel global no banco: o
  unico mecanismo e a identidade de bootstrap `X-NChat-Admin-Token`, restrita as
  tres rotas `/admin/*` do auth-service. Nao le canal nem DM.
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
Esse bypass e o unico escopo de plataforma que existe hoje e e o que o RF-74
documenta como **Admin Master**: nao ha tabela, coluna nem papel global de
administrador de plataforma, e o RF-74 nao criou um -- um papel que nenhum
endpoint sabe atribuir seria o mesmo codigo morto ja recusado acima para o
moderador. O token continua porque e o unico caminho que funciona antes de
existir um primeiro administrador; substitui-lo exige decidir como esse primeiro
administrador nasce, o que esta fora do RF-74. A limitacao esta registrada em
`docs/security/rbac-matrix.md`.

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
