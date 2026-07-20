# nchat-dev deployment review

## Entendimento

O ambiente Kubernetes de desenvolvimento usa o namespace `nchat-dev`, Traefik no
host operacional `NCHAT_DEV_HOST`, uma réplica por workload, imagens próprias imutáveis no
GHCR e dados locais `Retain` no nó `srv-apps-01`. Operações cluster-scoped e
segredos permanecem fora do deploy automático.

## Design review

- Há três raízes Kustomize sem sobreposição: `data/` gerencia StatefulSets e o
  bootstrap de papéis; `migrations/`, somente o Job; e o overlay raiz, somente a
  aplicação. O workflow aplica todas com `kubectl apply -k`.
- A topologia operacional não fica no Git. O arquivo ignorado `topology.env`, ou as
  variables do GitHub Environment `NCHAT_DEV_NODE_IP`, `NCHAT_DEV_NODE_CIDR` e
  `NCHAT_DEV_HOST`, alimenta uma cópia temporária do overlay. O arquivo
  `topology.env.example` mantém os placeholders e é a fonte canônica das portas.
- PostgreSQL, Valkey e SeaweedFS usam StatefulSets e PVs estáticos. As tags humanas
  e os digests de manifest list das três imagens são centralizados no Kustomization
  de dados; LiveKit e coturn também usam tag mais digest verificado.
- LiveKit e coturn usam `hostNetwork` somente para WebRTC. A API LiveKit permanece
  em TCP 7880; RTC usa 7881/TCP e 7882/UDP; coturn usa 3480/TCP+UDP e
  49300-49340/UDP. A porta 3478 continua reservada ao UniFi.
- Build e deploy são workflows separados. O inventário canônico
  `scripts/deploy/nchat-dev/images.txt` gera a matriz de build, valida o
  `Dockerfile.service`, define os digests exigidos e os rollouts. O build ocorre em
  GitHub-hosted com cache isolado por serviço; o deploy dedicado consome somente
  digests SHA-256 do build aprovado, materializa topologia em cópia temporária e não
  altera YAML versionado. A publicação manual por `workflow_dispatch` foi removida.
- O Job de migrations usa somente `MIGRATIONS_DATABASE_URL`, prazo ativo de 300s e
  nenhuma repetição automática. Os Deployments recebem `DATABASE_URL` runtime
  apenas quando o contrato real usa PostgreSQL.
- O manifesto do controller Sealed Secrets v0.36.6 é versionado e conferido por
  SHA-256 antes da instalação manual. Todos os Secrets de ambiente permanecem
  strict-scope e fora do Git em formato aberto.

## Matriz real de comunicação

| Origem                          | Destino                | Porta/protocolo           | Evidência/estado                               |
| ------------------------------- | ---------------------- | ------------------------- | ---------------------------------------------- |
| Traefik                         | web e sete serviços Go | porta nomeada `http`/TCP  | rotas públicas do Ingress                      |
| Traefik                         | LiveKit                | TCP 7880                  | signaling em `/livekit`                        |
| auth-service                    | PostgreSQL             | TCP 5432                  | `DATABASE_URL`/pgx                             |
| chat-service                    | PostgreSQL             | TCP 5432                  | `DATABASE_URL`/pgx                             |
| chat-service                    | Valkey                 | TCP 6379                  | cache, rate limit e broadcast por `VALKEY_URL` |
| notification-service            | PostgreSQL             | TCP 5432                  | outbox por `DATABASE_URL`                      |
| media-service                   | IP canônico do LiveKit | TCP 7880                  | `LIVEKIT_URL` e readiness TCP da API           |
| migrations e postgres-bootstrap | PostgreSQL             | TCP 5432                  | schema, grants e papéis                        |
| browser                         | LiveKit/coturn         | WSS/HTTPS e portas WebRTC | cliente fora do PodNetwork                     |

Não há chamada HTTP/gRPC entre microsserviços no código atual. `file-service`,
`admin-service` e `search-service` são executáveis, mas hoje expõem apenas contratos
HTTP básicos: não recebem DNS nem egress adicional e continuam isolados pelo
default-deny. SeaweedFS está provisionado para evolução futura, mas o código não usa
filer nem S3. Por isso o gateway S3 e a porta 8333 ficam desabilitados, e nenhuma
aplicação acessa filer, S3 ou master 9333. OIDC/HTTPS externo e SMTP permanecem
desabilitados em `nchat-dev`; ativá-los exige políticas específicas para destinos
aprovados. DNS é permitido somente a workloads que resolvem Services/hosts atuais.

## Políticas por serviço

| Workload             | Ingress                                     | Egress permitido                    |
| -------------------- | ------------------------------------------- | ----------------------------------- |
| web                  | Traefik → porta nomeada `http`              | nenhum                              |
| auth-service         | Traefik → `http`                            | DNS; PostgreSQL TCP 5432            |
| chat-service         | Traefik → `http`                            | DNS; PostgreSQL 5432; Valkey 6379   |
| file-service         | Traefik → `http`                            | nenhum                              |
| notification-service | Traefik → `http`                            | DNS; PostgreSQL TCP 5432            |
| admin-service        | Traefik → `http`                            | nenhum                              |
| search-service       | Traefik → `http`                            | nenhum                              |
| media-service        | Traefik → `http`                            | nó LiveKit `/32`, somente TCP 7880  |
| PostgreSQL           | auth, chat, notification, jobs → TCP 5432   | nenhum                              |
| Valkey               | chat-service → TCP 6379                     | nenhum acesso externo               |
| SeaweedFS            | nenhum cliente de aplicação                 | DNS interno; sem gateway S3         |
| LiveKit/coturn       | hostNetwork estritamente para fluxos WebRTC | DNS/configuração interna necessária |

## Threat model

| Ameaça                          | Controle                                                                |
| ------------------------------- | ----------------------------------------------------------------------- |
| secret ou credencial no Git/log | Sealed Secrets strict-scope, templates e diagnóstico sem Secret/env     |
| imagem trocada após aprovação   | tag de build por commit e deploy por digest validado byte a byte        |
| deploy concorrente/parcial      | concurrency sem cancelamento, migrations bloqueantes e rollout status   |
| runner comprometido             | runner dedicado, sem Docker socket e RBAC limitado ao `nchat-dev`       |
| movimento lateral               | default-deny e políticas específicas por origem, destino e porta        |
| privilégio excessivo no banco   | admin isolado; migrator owner; runtime CONNECT/USAGE/CRUD/sequences     |
| perda de dados                  | PV local, node affinity e reclaim policy `Retain`                       |
| relay aberto/SSRF no coturn     | autenticação, ranges privados/reservados negados e uma exceção canônica |
| supply chain CI                 | Actions por SHA; Kustomize/controller com versão e checksum fixos       |
| XSS/clickjacking no web         | CSP sem `unsafe-eval`, frame denial e headers defensivos do nginx       |

No coturn, `allowed-peer-ip` prevalece sobre um `denied-peer-ip` sobreposto. A única
exceção renderizada é `NCHAT_DEV_NODE_IP`, usado pelo LiveKit. Como coturn e LiveKit
compartilham `hostNetwork`, essa permissão identifica o host inteiro, não o processo
LiveKit; esse é um risco residual conhecido. As portas de mídia não podem ser
expostas à WAN. O hardening futuro é separar LiveKit e coturn em IPs ou nós dedicados
e então remover a exceção do host compartilhado.

## Imagens e atualização

O workflow lê o inventário canônico, constrói web, sete serviços Go e migrations com
tag do commit, publica os digests como artefatos e só então chama o deploy. O runner
dedicado valida o conjunto exato de nove arquivos, aplica o digest da migration,
executa o Job e aplica os oito digests runtime. Migrations não participa de rollout
de Deployment. Imagens externas são atualizadas preservando a tag legível e trocando
o digest somente após `docker buildx imagetools inspect` confirmar a tag e a presença
de `linux/amd64`; a CI rejeita referência externa sem SHA-256 integral.

## Decisões e dívidas

`scripts/db/migrate.sh` já existia em `develop`; o diff original desta branch apenas
adicionava resolução de URL. A engine psql foi preservada. Esta entrega acrescenta
somente fallback operacional, grants pós-migration e validação interna dos
identificadores. Uma eventual migração para ferramenta consolidada exige projeto
separado, compatibilidade de estado e versão/checksum fixos.

A chave TLS local não está no índice atual, e o gerador agora produz material
ignorado com permissões restritas. Remover um arquivo do HEAD não o remove do
histórico Git. Após merge, qualquer par anteriormente compartilhado deve ser rotado.
Limpeza de histórico é uma operação futura, coordenada e explicitamente autorizada;
nenhuma allowlist global deve ocultar novas chaves.
