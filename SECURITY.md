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

## Autorizacao para pin/unpin de mensagens

Fixar e desafixar mensagem (RF-05) nao exige papel de Moderador ou
Admin. Qualquer membro do workspace com acesso de leitura ao canal ou
a DM (incluindo Guest) pode fixar e desafixar. Mesma verificacao de
acesso usada para leitura de mensagens (RF-04), sem RBAC adicional.
Decisao de produto registrada em 2026-07-08 (issue #105), substituindo
a suposicao original de acesso restrito a Moderador/Admin.

## Autorizacao para categorias de canais

Criar, renomear, reordenar e excluir categoria de canal (RF-17) exige
papel ativo de `owner` ou `admin` no workspace -- o mesmo gate ja usado
para update/archive de canal e para workspace settings, exposto como
`domain.CanManageChannelCategories`. Usuario comum e Guest recebem 403.
Ler a listagem agrupada e aberto a qualquer membro ativo e nao amplia
acesso a canal: os canais de cada grupo vem da mesma politica SQL de
leitura usada pelo sidebar.

O RF-17 foi especificado como "Admin e Moderador". Nao existe papel de
moderador em nivel de workspace neste schema:
`chat.workspace_members.role` aceita apenas
`owner/admin/member/guest`, e `moderator` existe somente em
`chat.channel_members`, como papel por canal. Tratar "modera algum
canal" como "pode reestruturar a sidebar do workspace" seria escalacao
de privilegio, e criar um papel de workspace que nenhum endpoint
atribui seria codigo morto alargando uma constraint de seguranca fora
do escopo da tarefa. `CanManageChannelCategories` e a costura nomeada
para incluir um papel real de moderacao de workspace quando o RF-74
criar um. Mesma forma de divergencia registrada acima para RF-05.

## Autorizacao para adicionar membros

Adicionar participantes a um **canal** (issue #398) exige papel ativo de `owner`
ou `admin` no workspace, exposto como `domain.CanManageChannelMembers`, que
delega a `CanManageWorkspace`. E o mesmo gate ja usado para update/archive de
canal, para categorias e para a operacao inversa -- remover membro de canal, que
`MemberService.RemoveMemberFromChannel` ja restringia a owner/admin. Adicionar e
remover a mesma linha sao a mesma autoridade;
`docs/runbooks/task-chat-channel-join-leave.md` ja chamava a adicao de
"manager-add flow".

Deliberadamente **nao** e "qualquer membro do canal": isso permitiria a quem
apenas le um canal privado ampliar a audiencia dele, que e precisamente a
propriedade que um canal privado tem. O papel `moderator` de
`chat.channel_members` tambem nao e consultado -- nenhum caminho de codigo o
atribui, entao decidir por ele seria codigo morto ocupando o lugar de uma
verificacao real. Mesma forma de divergencia registrada para RF-05 e RF-17.
`CanManageChannelMembers` e a costura nomeada para alargar quando o RF-74 criar
um papel real de moderacao.

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
