# Executar o NChat no Windows com PowerShell

Este guia descreve como iniciar o ambiente local do NChat diretamente no
Windows 11 usando PowerShell, Docker Desktop, Go e pnpm, sem depender de WSL ou
dos scripts Bash do projeto.

## Resultado esperado

Ao final da inicialização, o ambiente disponibiliza:

- frontend React/Vite;
- gateway Traefik;
- PostgreSQL, Valkey e SeaweedFS;
- `auth-service`;
- `chat-service`;
- `file-service`;
- `notification-service`;
- `admin-service`;
- `search-service`;
- `media-service`.

Endereço da aplicação:

```text
http://nchat.local:8080
```

Credenciais locais criadas para desenvolvimento:

```text
Usuário: admin@nchat.local
Senha:   NChatDev#2026
```

A conta está ativa, vinculada ao workspace `NChat` e possui papel `owner`.

## Pré-requisitos

Antes de iniciar, confirme que estão instalados:

- Docker Desktop em execução;
- Go 1.25.x;
- Node.js 24.x;
- pnpm 11.x via Corepack;
- PowerShell 5.1 ou mais recente.

Confirme as ferramentas:

```powershell
docker version
docker compose version
go version
node --version
pnpm.cmd --version
```

O arquivo abaixo também deve existir:

```text
infra/compose/.env.dev
```

Se ele não existir, crie-o a partir do exemplo:

```powershell
Copy-Item infra/compose/.env.dev.example infra/compose/.env.dev
```

## Configuração de `nchat.local`

O gateway seleciona as rotas pelo hostname `nchat.local`. O arquivo abaixo deve
conter a entrada correspondente:

```text
C:\Windows\System32\drivers\etc\hosts
```

Entrada necessária:

```text
127.0.0.1 nchat.local
```

O editor precisa ser aberto como Administrador para alterar esse arquivo.

Valide no PowerShell:

```powershell
Resolve-DnsName nchat.local
```

O resultado deve apontar para `127.0.0.1`.

## Inicialização com um único comando

Na raiz do repositório, execute:

```powershell
cd "C:\Users\Matheus Evangelista\Documents\Projetos_internos\Nchat\nchat"

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\dev\start-windows.ps1
```

O launcher executa as seguintes ações:

1. verifica a presença de Docker, Go e pnpm;
2. lê as configurações locais de `infra/compose/.env.dev`;
3. sobe PostgreSQL, Valkey, SeaweedFS e os demais contêineres básicos;
4. sobe o gateway Traefik;
5. inicia os serviços Go em segundo plano;
6. inicia o `search-service` em Docker;
7. inicia o frontend Vite;
8. aguarda os health checks das portas 8081 a 8087;
9. valida o frontend por meio do gateway;
10. abre o NChat no navegador padrão.

O script pode ser executado novamente com segurança. Se uma porta já estiver
em uso, o processo correspondente não será duplicado.

## Por que usar `ExecutionPolicy Bypass`

A política local do Windows pode impedir a execução de arquivos `.ps1`,
inclusive o wrapper `pnpm.ps1`. O comando usa `ExecutionPolicy Bypass` somente
no processo atual e não altera permanentemente a política do computador.

O launcher utiliza `pnpm.cmd`, evitando o wrapper bloqueado.

## Portas utilizadas

| Componente           | Porta |
| -------------------- | ----: |
| Frontend Vite        |  5173 |
| Gateway HTTP         |  8080 |
| Auth service         |  8081 |
| Chat service         |  8082 |
| File service         |  8083 |
| Notification service |  8084 |
| Admin service        |  8085 |
| Search service       |  8086 |
| Media service        |  8087 |

Verifique as portas:

```powershell
Get-NetTCPConnection -State Listen |
  Where-Object LocalPort -in 5173,8080,8081,8082,8083,8084,8085,8086,8087 |
  Sort-Object LocalPort
```

## Health checks

Gateway e frontend:

```powershell
Invoke-WebRequest http://nchat.local:8080 -UseBasicParsing
```

Serviços pelo gateway:

```powershell
$services = "auth", "chat", "files", "notifications", "admin", "search", "media"

foreach ($service in $services) {
  $response = Invoke-WebRequest `
    "http://nchat.local:8080/api/$service/healthz" `
    -UseBasicParsing

  "$service`: $($response.StatusCode)"
}
```

Todos devem responder com status `200`.

## Logs

Os processos iniciados pelo launcher escrevem seus logs em:

```text
%LOCALAPPDATA%\NChat\logs
```

Abra a pasta:

```powershell
explorer.exe "$env:LOCALAPPDATA\NChat\logs"
```

Exemplo para acompanhar o auth-service:

```powershell
Get-Content "$env:LOCALAPPDATA\NChat\logs\auth-service.out.log" -Wait
```

Erros do frontend:

```powershell
Get-Content "$env:LOCALAPPDATA\NChat\logs\web.err.log" -Wait
```

Logs do search-service:

```powershell
docker logs -f nchat-search-local
```

## Migrações e dados iniciais

As migrações criam os schemas `auth`, `chat` e `files`, além dos dados iniciais:

- workspace `NChat`;
- canal `#Geral`;
- políticas padrão de autenticação.

As migrações não criam usuários automaticamente. A conta local descrita neste
guia foi criada separadamente pelo endpoint administrativo de bootstrap.

Para conferir o estado atual:

```powershell
docker exec nchat-dev-postgres-1 psql -U nchat -d nchat `
  -c "SELECT COUNT(*) AS applied FROM public.schema_migrations;" `
  -c "SELECT email, status FROM auth.users;"
```

O ambiente preparado possui 32 migrações aplicadas e a conta
`admin@nchat.local` ativa.

## Particularidades do Windows

### Search service

O Windows App Control bloqueou o executável temporário produzido por `go run`
para o `search-service`. Por isso o launcher executa esse serviço no contêiner
`nchat-search-local`. Ele continua acessível normalmente pela porta 8086 e pelo
Traefik.

### Frontend e nomes de arquivos

O Windows usa filesystem case-insensitive. O frontend possuía dois módulos cujos
nomes diferiam apenas entre maiúsculas e minúsculas, causando resolução do
módulo errado pelo Vite. O helper foi renomeado para `videoAttachment.ts`,
eliminando o conflito.

### Scripts Bash

Comandos como `make dev-env-up` e `pnpm migrations:up` chamam scripts Bash.
O launcher PowerShell usa diretamente Docker Compose, `go.exe` e `pnpm.cmd`, sem
alterar a política global do Windows e sem exigir WSL.

## Integrações opcionais

O ambiente local inicia os serviços, mas mantém estas integrações desabilitadas
por não haver credenciais locais:

- SMTP e envio real de e-mails;
- OIDC/Keycloak;
- LiveKit.

O `file-service` é iniciado com uploads desabilitados no launcher básico. Isso
permite que o serviço e seus health checks funcionem sem armazenar uma chave de
criptografia de desenvolvimento no repositório.

## Como parar o ambiente

Pare os processos locais associados às portas da aplicação:

```powershell
$ports = 5173,8081,8082,8083,8084,8085,8087

Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
  Where-Object LocalPort -in $ports |
  Select-Object -ExpandProperty OwningProcess -Unique |
  ForEach-Object { Stop-Process -Id $_ -ErrorAction SilentlyContinue }
```

Remova o contêiner local de busca:

```powershell
docker rm -f nchat-search-local 2>$null
```

Pare o gateway e a infraestrutura sem apagar os volumes:

```powershell
docker compose `
  --env-file infra/compose/.env.dev `
  -f infra/compose/compose.dev.yml `
  --profile gateway `
  down
```

Esse comando preserva os volumes. Portanto, usuário, workspace, canal e demais
dados locais permanecem disponíveis na próxima inicialização.

## Solução de problemas

### `http://localhost:8080` retorna 404

Use o hostname esperado pelo Traefik:

```text
http://nchat.local:8080
```

### A página retorna 502

O gateway está ativo, mas o frontend ou um serviço de destino ainda não abriu a
porta. Consulte os logs e execute novamente o launcher.

### `pnpm.ps1` foi bloqueado

Use `pnpm.cmd`. O launcher já faz isso automaticamente.

### O primeiro início demora

Go, pnpm e o contêiner do search-service podem baixar e compilar dependências na
primeira execução. O launcher aguarda os health checks antes de abrir a página.

### Login inválido

Confirme que está usando exatamente:

```text
admin@nchat.local
NChatDev#2026
```

Depois teste o auth-service:

```powershell
$body = @{
  email = "admin@nchat.local"
  password = "NChatDev#2026"
  device_name = "PowerShell test"
} | ConvertTo-Json

Invoke-RestMethod `
  -Method Post `
  -Uri "http://nchat.local:8080/api/auth/login" `
  -ContentType "application/json" `
  -Body $body
```

Uma resposta válida contém `access_token` e os dados do usuário.

## Arquivos relacionados

- Launcher: `scripts/dev/start-windows.ps1`
- Teste do launcher: `scripts/dev/start-windows.test.ps1`
- Configuração local: `infra/compose/.env.dev`
- Docker Compose: `infra/compose/compose.dev.yml`
- Gateway: `infra/traefik/local/`
- Logs locais: `%LOCALAPPDATA%\NChat\logs`
