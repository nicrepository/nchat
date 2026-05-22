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

## Regras para uploads

- Definir limite de tamanho.
- Validar tipo.
- Armazenar fora do webroot.
- Usar envelope encryption.
- Executar scan assincrono com ClamAV.
- Bloquear download ate aprovacao do scan.

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
