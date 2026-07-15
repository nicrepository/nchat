# TASK-158 — Validação de WebRTC na rede real do escritório (Nic-Labs)

## Status

PoC de infraestrutura e conectividade. Reaproveita o stack dev LiveKit + coturn
já existente (`docs/runbooks/task-livekit-coturn-dev.md`, issue #326). Não
implementa a funcionalidade de chamadas do NChat, não cria UI de chamadas e
não introduz signaling próprio.

## Objetivo

Executar e documentar uma validação técnica reproduzível da conectividade
WebRTC (STUN/TURN/ICE/mídia) na rede real do escritório da Nic-Labs, conforme
issue [#187 — TASK-158](https://github.com/nicrepository/nchat/issues/187).

## Fora de escopo

- Implementar chamadas de áudio/vídeo no frontend do NChat.
- Criar interface de chamadas.
- Signaling próprio (usa-se o signaling do LiveKit).
- Alterar arquitetura de produto, adicionar Istio/ArgoCD ou infraestrutura de
  produção.
- Substituir LiveKit/coturn.
- Alterar firewall corporativo automaticamente (apenas documentado como passo
  manual do operador).

## Pré-requisitos

- Docker + Docker Compose v2 na máquina de teste.
- Stack LiveKit + coturn (profile `media`) já preparado — ver
  `docs/runbooks/task-livekit-coturn-dev.md`.
- Acesso físico à rede do escritório da Nic-Labs (Wi-Fi ou cabo, conforme o
  cenário a validar).
- Dois dispositivos/máquinas na mesma rede para os cenários E/F (dois
  participantes).

## Como usar

### 1. Suba o stack (mesma máquina ou outra na rede)

```bash
make dev-media-up
```

Por padrão, LiveKit/coturn ficam publicados apenas em `127.0.0.1` (ver
`scripts/ci/media-config-check.sh`), o que é suficiente para o cenário A (o
próprio host acessando os serviços). Para os cenários que exigem um **segundo
dispositivo físico** na rede (E, F), veja a seção "Testando com um segundo
dispositivo" abaixo.

### 2. Rode a validação automatizável

```bash
WEBRTC_QA_TARGET_HOST=127.0.0.1 \
WEBRTC_QA_STABILITY_SECONDS=60 \
bash scripts/qa/validate-webrtc-office-network.sh
# ou: pnpm qa:webrtc-office-network
```

Para a validação de campo real (rede do escritório), aumente a janela de
estabilidade para 10-15 minutos e aponte para o IP do host que está rodando o
stack:

```bash
WEBRTC_QA_TARGET_HOST=<IP LAN do host, ex.: 10.x.x.x> \
WEBRTC_QA_STABILITY_SECONDS=900 \
bash scripts/qa/validate-webrtc-office-network.sh
```

O script grava um resumo sanitizado em
`poc-results/webrtc-office-network/<timestamp>-summary.md` (gitignored — não
é versionado). Use `poc-results/webrtc-office-network/RESULT-TEMPLATE.md`
como referência para consolidar o relatório final versionável.

### Testando com um segundo dispositivo (rede real)

O compose dev publica as portas de LiveKit/coturn apenas em `127.0.0.1` por
padrão (postura de segurança padrão, não alterada por esta tarefa). Para um
teste real com dois dispositivos físicos na rede do escritório, siga **esta
ordem exata** — definir o IP e re-renderizar a config **antes** de subir os
containers evita que o LiveKit anuncie `node_ip: 127.0.0.1` para o segundo
dispositivo (que então falharia ao conectar):

1. **Identifique o IP LAN da máquina host** (não `127.0.0.1`, não `0.0.0.0`):
   ```bash
   # Linux/macOS
   ip -4 addr show | grep inet
   # Windows (PowerShell)
   ipconfig
   ```
2. **Copie o override de exemplo** (nunca edite `compose.dev.yml`
   diretamente) e substitua `OFFICE_LAN_BIND_IP` pelo IP LAN identificado:
   ```bash
   cp infra/compose/compose.media.lan-office-test.override.example.yml \
      infra/compose/compose.media.lan-office-test.override.yml
   # edite a cópia com o IP LAN real
   ```
3. **Defina `LIVEKIT_NODE_IP` em `infra/compose/.env.dev`** para o mesmo IP
   LAN — **antes** de subir os containers. Confirme com o responsável de
   rede/segurança que apenas a sub-rede do escritório alcança esse IP
   (perfil de firewall do SO), não a internet pública.
4. **Re-renderize e suba a stack reutilizando o fluxo existente**
   (`scripts/dev/_media_env.sh` / `dev-media-up.sh`), passando o override
   opcional via `MEDIA_COMPOSE_EXTRA_FILE` — isso garante que o
   `livekit.yaml` seja re-renderizado a partir do template com o
   `LIVEKIT_NODE_IP` atualizado, em vez de subir a config antiga/padrão:
   ```bash
   MEDIA_COMPOSE_EXTRA_FILE=infra/compose/compose.media.lan-office-test.override.yml \
     bash scripts/dev/dev-media-up.sh
   ```
5. **Valide a configuração renderizada** antes de testar — confirme que o
   `node_ip` correto foi aplicado (o script de validação também faz este
   preflight automaticamente quando `WEBRTC_QA_TARGET_HOST` não é loopback):
   ```bash
   bash scripts/dev/dev-media-validate.sh
   grep node_ip infra/compose/livekit/livekit.runtime.yaml
   ```
   Para um `WEBRTC_QA_TARGET_HOST` que seja IP literal, o preflight exige
   igualdade entre o target e o `node_ip` renderizado. Se o target for DNS,
   o preflight compara o valor renderizado com `LIVEKIT_NODE_IP` de
   `infra/compose/.env.dev`; não faz resolução DNS nem compara o hostname
   textualmente com um IP.
6. **Execute o teste do segundo dispositivo/browser real** na rede do
   escritório, apontando para o IP LAN do host.
7. Ao final do teste, derrube o stack e apague sua cópia local do override.
   Não deixe a porta exposta na LAN sem necessidade.

Esse arquivo de override é apenas um **exemplo versionado** — a cópia real do
operador (com o IP real) é gitignored e nunca deve ser commitada. Não
duplique a lógica de renderização: `MEDIA_COMPOSE_EXTRA_FILE` é aceito pelo
helper existente (`scripts/dev/_media_env.sh`) de forma aditiva/opt-in —
o comportamento padrão (sem a variável) não muda.

## Cenários mínimos de teste

| Cenário | Descrição                                                                              | Automatizável                                                                                | Como validar                                                                                                                                   |
| ------- | -------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| A       | Conectividade básica ao endpoint LiveKit                                               | Sim                                                                                          | `validate-webrtc-office-network.sh` (curl com timeout)                                                                                         |
| B       | ICE com UDP disponível — STUN binding                                                  | Sim                                                                                          | `validate-webrtc-office-network.sh` (`turnutils_stunclient`)                                                                                   |
| C       | TURN obrigatório (relay forçado) — UDP, TCP, TLS/443                                   | Sim (UDP/TCP); TLS reportado como NÃO CONFIGURADO no template dev atual                      | `validate-webrtc-office-network.sh` (`turnutils_uclient -y [-t] [-S]`)                                                                         |
| D       | UDP bloqueado → fallback TCP/TLS                                                       | Parcial — bloqueio de UDP é manual (firewall do operador); detecção do fallback é automática | Bloquear UDP manualmente (regra de firewall local, temporária) e reexecutar o script                                                           |
| E       | **Conectividade de sala e presença de participantes** (infra apenas — ver nota abaixo) | Sim (via `livekit-cli`, mídia sintética `--publish-demo`)                                    | `validate-webrtc-office-network.sh`                                                                                                            |
| F       | Estabilidade por período mínimo (10-15 min sugerido)                                   | Sim (polling, sem sleep único)                                                               | `WEBRTC_QA_STABILITY_SECONDS=900`                                                                                                              |
| G       | Falha controlada (endpoint indisponível)                                               | Sim                                                                                          | Rodar com `WEBRTC_QA_TARGET_HOST` inválido/inacessível — script falha rápido, com mensagem clara, exit code != 0                               |
| —       | Tipos de candidato ICE por sessão (host/srflx/relay)                                   | **Manual** — requer browser real                                                             | `chrome://webrtc-internals` ou `about:webrtc` (Firefox), inspecionar `candidate-pair` selecionado e `local-candidate`/`remote-candidate` types |

### Importante: o que o Cenário E prova (e o que não prova)

O Cenário E automatizado (`E_room_connectivity`) usa dois processos
`livekit-cli` na mesma máquina/rede Docker para confirmar que uma sala pode
ser criada e que dois participantes aparecem conectados nela. **Isso prova
apenas conectividade de infraestrutura e presença na sala.** Isso **não**
prova, e não deve ser apresentado como prova de:

- publicação e recepção efetiva de mídia (publish/subscribe funcional);
- qual tipo de candidato ICE foi selecionado (host/srflx/relay);
- que o candidato `relay` foi de fato usado (TURN funcionalmente comprovado);
- um segundo dispositivo físico real (os dois processos rodam no mesmo host
  de teste).

As evidências abaixo são **obrigatoriamente manuais** e devem ser registradas
antes de considerar o resultado **APPROVED**:

- publicação e recepção de mídia confirmadas (dois browsers/dispositivos
  reais);
- tipo de candidato ICE selecionado (host/srflx/relay);
- candidato `relay` confirmado quando o cenário exige TURN;
- transporte efetivamente utilizado (UDP/TCP/TLS);
- teste com um segundo dispositivo ou browser real (não apenas dois
  processos `livekit-cli` no mesmo host);
- inspeção em `chrome://webrtc-internals` (ou `about:webrtc`/ferramenta
  equivalente).

Quando essas evidências manuais não existirem ou não forem confirmadas
explicitamente (ver `WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED` abaixo), o
resultado final nunca será **APPROVED** — será **PENDING**, conforme a
precedência `FAILED > PENDING > PARTIAL > APPROVED`.

### Confirmando evidência manual: `WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED`

O script só permite o resultado final `APPROVED` quando o operador confirma
explicitamente, via variável de ambiente, que as evidências manuais acima
foram coletadas e registradas (por exemplo, no `RESULT-TEMPLATE.md`
preenchido):

```bash
WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=1 \
WEBRTC_QA_TARGET_HOST=<IP LAN do host> \
WEBRTC_QA_STABILITY_SECONDS=900 \
bash scripts/qa/validate-webrtc-office-network.sh
```

O padrão é `0` (não confirmado). Definir `1` sem ter de fato coletado as
evidências manuais é responsabilidade do operador — o script não pode
verificar isso automaticamente; ele apenas impõe que o operador declare
explicitamente que o fez.

### Por que os tipos de candidato ICE são validados manualmente

`livekit-cli` não expõe os tipos de candidato ICE (host/srflx/relay) de forma
programática simples. Automatizar isso exigiria um browser real controlado
(ex.: Puppeteer) — uma dependência nova não justificada para esta PoC de
infraestrutura. A inspeção via `chrome://webrtc-internals` (Chrome/Edge) ou
`about:webrtc` (Firefox) é o procedimento padrão da indústria e é rápida:
conecte dois participantes reais em um browser, abra a página de diagnóstico
WebRTC, localize a seção "ICE Candidate pair" do `PeerConnection` ativo e
registre os tipos de candidato local/remoto e o par selecionado.

## Resultado final e exit codes

Ao final da execução, o script consolida todos os cenários em um resultado
único (`FINAL_RESULT`) e retorna um exit code correspondente — **nunca 0**
para um resultado que não seja `APPROVED`:

| Resultado  | Exit code | Significado                                                                                                                                                                                              |
| ---------- | --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `APPROVED` | `0`       | Todos os critérios automatizáveis obrigatórios passaram **e** a evidência manual obrigatória foi confirmada (`WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=1`) **e** o alvo não é loopback (teste de campo real). |
| `FAILED`   | `1`       | Um cenário obrigatório automatizado falhou (A, B, C-UDP ou E).                                                                                                                                           |
| `PARTIAL`  | `2`       | Conectividade parcial — ex.: fallback TCP/TLS ausente/falhou, ou a estabilidade detectou quedas, mas nenhum cenário obrigatório falhou.                                                                  |
| `PENDING`  | `3`       | Um resultado automatizado obrigatório está ausente, vazio ou foi pulado; e/ou a execução física/evidência manual não foi confirmada (alvo loopback e/ou `WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=0`).        |

Esses códigos são o contrato interno do script executado diretamente em
Bash: `0=APPROVED`, `1=FAILED`, `2=PARTIAL` e `3=PENDING`. Alguns
launchers do Windows, integrações WSL, comandos `pnpm` executados pelo host
ou outros wrappers podem normalizar um código Bash não zero para `1`. Nesses
ambientes, `0` continua significando aprovação e qualquer valor não zero
continua significando não aprovado; use o status textual no console e no
`*-summary.md` para distinguir `FAILED`, `PARTIAL` e `PENDING`.
O config-check executado em CI depende apenas de sucesso (`0`) versus falha
(não zero), e não da distinção entre `2` e `3`.

Precedência quando mais de uma condição se aplica: **FAILED > PENDING >
PARTIAL > APPROVED**. Isto é, uma falha obrigatória sempre vence; na ausência
de falha obrigatória, resultado obrigatório ausente/vazio e evidência
manual/campo pendente vencem sobre um resultado meramente parcial (não é
possível confirmar que a mídia/relay foram de fato exercitados sem a
evidência manual).

Os resultados automatizados obrigatórios para a consolidação são
`A_reachability`, `B_stun_binding`, `C_turn_udp` e
`E_room_connectivity`. A lista é declarada uma única vez em
`scripts/qa/lib/webrtc-office-network-result.sh`. Os resultados adicionais
`C_turn_tcp`, `C_turn_tls` e `F_stability` continuam sujeitos à
precedência acima e podem reduzir uma execução completa para `PARTIAL` ou
`PENDING`.

O resultado final e as razões são gravados no resumo sanitizado
(`poc-results/webrtc-office-network/<timestamp>-summary.md`) e impressos no
console ao final da execução.

## Interpretação de resultado

Conforme critério do cronograma:

- Se UDP funcionar mas o fallback TCP/TLS não funcionar → **PARTIAL** (risco
  de conectividade em redes restritivas).
- Se apenas a porta estiver acessível mas não houver candidato relay nem
  mídia efetivamente trafegando pelo TURN → **FAILED** (TURN não comprovado
  funcionalmente) — a menos que o script já tenha reportado `FAILED` por
  outro motivo obrigatório, caso em que este permanece o resultado.
- Se o teste não for executado fisicamente na rede real do escritório, ou se
  a evidência manual obrigatória (mídia, candidato ICE, relay, transporte,
  segundo dispositivo) não tiver sido confirmada → **PENDING** (implementação
  preparada, validação de campo não executada/comprovada).
- Caso todos os critérios de aceite abaixo sejam satisfeitos com evidência
  real, incluindo a manual, e o teste tenha sido executado em um alvo real
  (não loopback) → **APPROVED**.

## Critérios de aceite

- [ ] STUN/TURN alcançável na rede real do escritório.
- [ ] Candidato relay obtido em teste que força TURN.
- [ ] Sessão entre dois participantes estabelecida.
- [ ] Publicação e recepção de mídia confirmadas.
- [ ] Resultado do transporte UDP registrado.
- [ ] Resultado do fallback TCP/TLS registrado.
- [ ] Porta e protocolo efetivamente usados documentados.
- [ ] Teste de estabilidade executado (10-15 min sugeridos).
- [ ] Falhas e limitações registradas.
- [ ] Procedimento reproduzível por outra pessoa.
- [ ] Resultado sanitizado versionável (`RESULT-TEMPLATE.md` preenchido, sem
      dados sensíveis).
- [ ] Evidências brutas sensíveis mantidas fora do Git
      (`poc-results/webrtc-office-network/*-summary.md` é gitignored).
- [ ] Nenhum secret ou token incluído em scripts, logs ou documentação.

## Segurança

- `LIVEKIT_API_SECRET` e `COTURN_STATIC_AUTH_SECRET` nunca são impressos pelo
  script (passados via variáveis de ambiente ao container, nunca como
  argumento de CLI).
- Nenhum token de participante LiveKit é impresso.
- O script nunca desabilita verificação TLS.
- O script não altera firewall corporativo — bloqueio de UDP (cenário D) é um
  passo manual e reversível do operador.
- O override de porta para teste em LAN é opt-in, documentado, reversível, e
  a cópia real do operador nunca é versionada.
- coturn permanece com autenticação obrigatória (`use-auth-secret`); nenhuma
  alocação TURN anônima é aceita (herdado do template dev existente).

## Limitações conhecidas

- O template dev de coturn atual (`infra/compose/coturn/turnserver.conf.template`)
  usa `no-tls`/`no-dtls` — não há um listener TLS real disponível hoje. O
  cenário C (TLS/porta 443) será reportado como **NÃO CONFIGURADO** até que um
  certificado TLS seja provisionado para coturn (fora do escopo desta tarefa,
  ver `infra/compose/coturn/README.md`, seção "Not Configured").
- Tipos de candidato ICE por sessão exigem inspeção manual via browser (ver
  acima).
- O bloqueio de UDP (cenário D) depende de uma ação manual do operador no
  firewall local — não é simulado automaticamente por este script.
- Execução física na rede do escritório não foi realizada nesta sessão de
  autoria (sem acesso à rede da Nic-Labs) — resultado registrado como
  **PENDING** até execução de campo.

## Próximas ações

1. Executar fisicamente na rede do escritório da Nic-Labs, com dois
   dispositivos reais, seguindo este runbook.
2. Preencher `poc-results/webrtc-office-network/RESULT-TEMPLATE.md` com os
   resultados reais e anexar (fora do Git) as evidências brutas sanitizadas.
3. Caso o cenário C (TLS/443) seja necessário para redes restritivas do
   escritório, abrir uma tarefa específica para provisionar TLS real em
   coturn antes de repetir o teste.
4. Reportar o resultado final na issue [#187](https://github.com/nicrepository/nchat/issues/187).

## Definition of Done

- [x] Script de validação criado (`scripts/qa/validate-webrtc-office-network.sh`)
- [x] CI config-check criado, sem depender de rede real
      (`scripts/ci/webrtc-office-network-config-check.sh`)
- [x] Runbook criado (este arquivo)
- [x] Template de resultado sanitizado criado (sem dados sensíveis)
- [x] Override de LAN documentado como exemplo opt-in, não aplicado por padrão
- [x] Nenhum secret real versionado
- [ ] Validação executada fisicamente na rede do escritório — **pendente**
- [ ] `RESULT-TEMPLATE.md` preenchido com resultado real — **pendente**
