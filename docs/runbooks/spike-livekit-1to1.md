# Spike LiveKit 1:1

## Objetivo e status

Prova de conceito descartável para validar uma chamada de áudio e vídeo entre dois
navegadores locais usando React, o emissor de tokens temporário do media-service,
LiveKit e coturn.

Esta Spike não é o desenho definitivo de chamadas do NChat. O frontend só registra a
rota em builds de desenvolvimento. O backend exige simultaneamente
`APP_ENV=development`, `MEDIA_SPIKE_ENABLED=true` e
`MEDIA_SPIKE_LOCAL_ONLY=true`, além de URL LiveKit e origens locais aprovadas.
Staging, produção, testes e ambientes desconhecidos são bloqueados.

## Pré-requisitos

- Node.js 24, pnpm 11, Go 1.25 e Bash.
- Docker Desktop com Compose v2.
- Câmera e microfone disponíveis para os dois perfis de navegador.
- Portas locais descritas em
  [task-livekit-coturn-dev.md](task-livekit-coturn-dev.md) livres.

## Variáveis

Use somente os placeholders locais já compartilhados por
infra/compose/.env.dev.example. Não use credenciais de staging ou produção.

| Variável                      | Valor local                                    |
| ----------------------------- | ---------------------------------------------- |
| APP_ENV                       | development                                    |
| MEDIA_SPIKE_ENABLED           | true                                           |
| MEDIA_SPIKE_LOCAL_ONLY        | true                                           |
| LIVEKIT_URL                   | ws://127.0.0.1:7880                            |
| LIVEKIT_API_KEY               | mesmo placeholder de infra/compose/.env.dev    |
| LIVEKIT_API_SECRET            | mesmo placeholder de infra/compose/.env.dev    |
| MEDIA_SPIKE_ROOM              | spike-1to1                                     |
| MEDIA_SPIKE_ALLOWED_ORIGINS   | http://localhost:5173,https://nchat.local:8443 |
| MEDIA_SPIKE_TOKEN_TTL_SECONDS | 300                                            |

O segredo LiveKit permanece apenas nos ambientes locais ignorados do LiveKit Server e
do media-service. Ele não deve ser colocado em variável VITE\_\*, URL, log ou captura
de tela. Não reutilize esta configuração em staging, produção ou qualquer ambiente
compartilhado.

## Execução local

### 1. Subir LiveKit e coturn

No diretório raiz:

    Copy-Item infra\compose\.env.dev.example infra\compose\.env.dev -ErrorAction SilentlyContinue
    make dev-media-up
    make dev-media-status

O profile media existente publica as portas somente em 127.0.0.1. Para diagnóstico:

    make dev-media-logs
    make dev-media-validate

### 2. Subir o media-service

Em um segundo terminal PowerShell:

    Set-Location services\media-service
    $env:APP_ENV = "development"
    $env:MEDIA_SPIKE_ENABLED = "true"
    $env:MEDIA_SPIKE_LOCAL_ONLY = "true"
    $env:LIVEKIT_URL = "ws://127.0.0.1:7880"
    $env:LIVEKIT_API_KEY = "nchat_dev_key_change_me"
    $env:LIVEKIT_API_SECRET = "nchat_dev_livekit_secret_change_me_32c"
    $env:MEDIA_SPIKE_ROOM = "spike-1to1"
    $env:MEDIA_SPIKE_ALLOWED_ORIGINS = "http://localhost:5173,https://nchat.local:8443"
    $env:MEDIA_SPIKE_TOKEN_TTL_SECONDS = "300"
    go run ./cmd/media-service

O serviço deve ser usado somente em http://127.0.0.1:8087. O endpoint interno é
POST /spike/token; o proxy Vite o expõe ao browser como
POST /api/media/spike/token.

O marcador `MEDIA_SPIKE_LOCAL_ONLY` não é um controle de produção. Ele é um bloqueio
explícito para impedir promoção acidental desta PoC. O media-service também rejeita URL
LiveKit não loopback e origens fora de `localhost`, endereços IP loopback ou
`nchat.local`.

### 3. Subir o frontend

Em um terceiro terminal, na raiz:

    corepack pnpm dev:web

Abra http://localhost:5173/spike/livekit-1to1.

### 4. Validar com dois navegadores

1. Abra a URL em dois navegadores ou em dois perfis isolados.
2. Mantenha spike-1to1 nos dois.
3. Use identidades diferentes, por exemplo browser-a e browser-b.
4. Clique em **Entrar na chamada** em cada perfil e conceda câmera/microfone.
5. Confirme vídeo local e remoto nos dois lados.
6. Use fones ou mute um lado para evitar realimentação acústica.
7. Teste **Mutar microfone**, **Ativar microfone**, **Desligar câmera** e
   **Ligar câmera**.
8. Clique em **Sair da chamada**, entre novamente e confirme que não há track ou
   participante fantasma.
9. Derrube o LiveKit durante uma chamada e confirme o estado de reconexão/erro.

## Critérios de sucesso

- Dois participantes distintos entram na sala configurada.
- Cada navegador publica câmera e microfone e recebe vídeo/áudio remoto.
- Controles refletem mute/unmute e câmera ligada/desligada.
- Saída e desmontagem encerram tracks, elementos e conexão.
- Negação de câmera ou microfone mostra mensagem específica.
- Falha de token, LiveKit indisponível e queda de conexão mostram erro compreensível.
- Em rede local estável, a conversa não apresenta travamentos ou latência perceptível
  incompatível com uma chamada 1:1 básica.

Registre o resultado manual com data, navegadores/versões, dispositivos usados, rede,
resultado de cada critério e qualquer sintoma observado. Não registre tokens, nomes
reais, credenciais, áudio ou vídeo.

## Limitações e riscos conhecidos

- A identidade é informada pelo cliente e não está vinculada ao usuário autenticado.
- A proteção é dev-only, opt-in e por allowlist de origem; Origin não substitui
  autenticação contra clientes não-browser.
- A rota temporária não possui autenticação definitiva e não pode ser publicada em
  ambiente compartilhado.
- A sala é fixa no servidor, mas a Spike não implementa membership definitivo nem
  controla estado de chamada.
- O fluxo valida dois participantes, mas não cria política definitiva contra um terceiro
  participante.
- Tokens podem ser reutilizados até o TTL de 300 segundos; não há revogação na Spike.
- Em falhas de WebSocket, o Chromium/DevTools pode mostrar o JWT temporário na URL de
  conexão usada pelo SDK. Não compartilhe capturas de console/rede antes do TTL expirar.
- ws:// e coturn sem TLS/DTLS são limitações deliberadas da pilha local existente.
- HTTP e WebSocket sem TLS são permitidos somente na execução local desta Spike.
- A validação manual não mede MOS, jitter ou perda de pacotes com rigor laboratorial.
- Não há gravação, compartilhamento de tela, moderação, persistência ou E2E WebRTC
  automatizado.

## Encerramento

    make dev-media-down

Remova as variáveis `MEDIA_SPIKE_*` e `LIVEKIT_*` do terminal após o teste, ou feche
o terminal. Não commite infra/compose/.env.dev. Remova toda a configuração temporária
ao encerrar a PoC; ela não deve permanecer disponível para promoção.

## Como remover a Spike

1. Remover a rota condicional de apps/web/src/App.tsx e o proxy de
   apps/web/vite.config.ts.
2. Remover apps/web/src/mediaSpike/ e a dependência livekit-client.
3. Remover a rota, handler, emissor, configuração e testes de Spike do
   services/media-service.
4. Remover as variáveis da Spike em services/media-service/.env.example.
5. Remover este runbook.

A infraestrutura LiveKit/coturn pré-existente não pertence a esta Spike e não deve ser
removida junto com ela.

## Decisão recomendada

**Prosseguir com ressalvas.** A integração técnica é adequada para validação local, mas
a implementação definitiva deve derivar identidade e autorização do usuário real,
aplicar membership 1:1 server-side, definir revogação/renovação de tokens, TLS, limites
de sala e observabilidade sem alta cardinalidade. A recomendação só deve avançar após a
execução manual bidirecional descrita acima.

## Resultado executado em 2026-07-16

- Ambiente: Docker Engine 29.2.1, Docker Compose 5.1.0, LiveKit Server 1.13.3,
  coturn 4.14.0-r0 e Chromium headless do Playwright.
- `media:config-check` validou o Compose com Docker disponível.
- O smoke test existente validou HTTP do LiveKit, STUN, rejeição de TURN anônimo,
  TURN autenticado com credencial temporária, criação de sala e participante com mídia
  demo.
- Dois contextos Chromium isolados entraram em `spike-1to1` com identidades distintas.
  Cada lado apresentou um vídeo local, um vídeo remoto e um áudio remoto, com tracks em
  estado `live` e áudio em reprodução.
- Mute/unmute, câmera off/on, saída, remoção das mídias remotas, reentrada e nova
  assinatura de tracks passaram.
- A reinicialização do LiveKit levou a interface para `Conectando` e depois de volta a
  `Conectado` sem intervenção do usuário.
- A validação encontrou e corrigiu elementos remotos obsoletos após
  `ParticipantDisconnected` e um `pattern` HTML incompatível com RegExp `v`.
- Câmera, microfone e qualidade perceptiva humanas não foram avaliados. O Chromium usou
  dispositivos sintéticos; nesse modo, bloqueio de permissões retorna `NotSupportedError`
  em vez de uma negação real do usuário.

**Decisão após a execução: prosseguir com ressalvas.** A viabilidade técnica local de
sinalização, publicação, assinatura e controle 1:1 foi confirmada. Antes da implementação
definitiva ainda são necessários testes com dispositivos físicos, autenticação/membership
server-side, TLS e avaliação de rede real.

## Revalidação do Code Quality Review em 2026-07-17

- Dois contextos Chromium isolados, com dispositivos sintéticos, entraram na sala com
  identidades distintas após as correções de lifecycle e cleanup.
- Cada contexto apresentou um vídeo local, um vídeo remoto e um áudio remoto. As tracks
  estavam em estado live; os elementos de áudio remoto estavam com paused=false e
  muted=false.
- Mute/unmute, câmera off/on, saída, remoção de mídia no peer e reentrada passaram.
- A saída enquanto a requisição de token estava atrasada terminou em Desconectado, sem
  elementos de mídia.
- Após restart do LiveKit, a interface passou por Conectando, retornou a Conectado e
  recuperou vídeo e áudio remotos.
- O encerramento dos dois participantes deixou zero elementos e tracks associados na
  página.
- A validação com câmera, microfone e percepção humana continua pendente; esta execução
  não substitui o teste manual com dispositivos físicos e fones.

**Decisão mantida: prosseguir com ressalvas.**
