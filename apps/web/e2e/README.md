# Testes de gestos e ResizeObserver (#1152)

Execute os comandos a partir da raiz do repositório, após instalar as dependências
com o lockfile. A suíte Playwright padrão continua sendo `pnpm test:e2e:web`.

## Testes unitários da alternativa B

```sh
pnpm --filter @nchat/web exec vitest run --config e2e/vitest.config.ts
```

São 61 testes do classificador, coleta, teardown e instrumentação da página. A
configuração usa Node por padrão; `resizeObserverGate.page.vitest.ts` seleciona
jsdom pela anotação do próprio arquivo. O Vitest da aplicação exclui `e2e/**`,
portanto este comando precisa ser executado separadamente.

## Playwright dedicado

`resize-observer-gate.spec.ts` é excluído explicitamente da coleta padrão. A
configuração dedicada seleciona somente esse spec, nos projetos Chromium e
WebKit, com um worker e zero retries. Ela inicia seu próprio Vite e não reutiliza
um servidor existente. Nenhum backend real ou launcher externo é necessário.

Defina `E2E_BASE_URL` como `http://127.0.0.1:<porta-livre>`, substituindo
`<porta-livre>` por uma porta disponível. A configuração não fixa uma porta.
Em PowerShell, use `$env:E2E_BASE_URL = "http://127.0.0.1:<porta-livre>"`; em um
shell POSIX, use `export E2E_BASE_URL="http://127.0.0.1:<porta-livre>"`.

```sh
pnpm --filter @nchat/web exec playwright test --config playwright.resize-observer.config.ts --list
pnpm --filter @nchat/web exec playwright test --config playwright.resize-observer.config.ts --project webkit
pnpm --filter @nchat/web exec playwright test --config playwright.resize-observer.config.ts --project chromium
```

Os browsers correspondentes precisam estar instalados. Para executar um recorte,
acrescente `--grep RO-A`, `--grep RO-D`, `--grep RO-S` ou `--grep RO-N`.
Use `--output <diretorio-externo-novo>` para preservar artefatos fora da árvore Git.

O gate registra os erros e aceita somente a notificação transitória exata dentro
das condições aprovadas, com convergência, geometria, reação única e cleanup.
Os controles negativos devem passar por detectar a regressão provocada; nenhum
filtro global de erros é instalado. O classificador e suas tolerâncias não foram
alterados para este isolamento.

Aprovação na configuração dedicada não comprova execução no CI padrão. Testes
com `tap()` e rolagem programática não substituem Safari iOS e Chrome Android
físicos, scroll touch nativo, teclado virtual e safe areas.
