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
