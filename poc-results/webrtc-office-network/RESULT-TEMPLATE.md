# WebRTC office network validation — resultado (template)

> Copie este template para preencher um novo relatório manual, ou use como
> referência para revisar o `*-summary.md` sanitizado gerado por
> `scripts/qa/validate-webrtc-office-network.sh` (saída bruta é gitignored).
>
> Não preencha este arquivo com dados reais — ele é versionado. Resultados
> reais (com timestamp) ficam apenas em `poc-results/webrtc-office-network/`,
> fora do Git.

## Metadados

- Data e horário: `AAAA-MM-DDThh:mm` (fuso horário local do escritório)
- Ambiente: `escritório Nic-Labs — identificação não sensível (ex.: "sala X")`
- Sistema operacional do cliente: `ex.: Windows 11`
- Browser(s) usado(s) (quando aplicável): `ex.: Chrome 12x, Firefox 12x`
- Versão LiveKit: `ex.: v1.13.3`
- Versão coturn: `ex.: 4.14.0-r0`
- Referência da issue: `#187 (TASK-158)`

## Topologia resumida

`ex.: 1x host rodando LiveKit+coturn (profile media) na rede do escritório; 2x clientes em dispositivos separados na mesma rede/VLAN.`

## Cenários executados

| Cenário | Descrição | Executado | Resultado |
| --- | --- | --- | --- |
| A (`A_reachability`) | Conectividade básica (LiveKit reachability) | sim/não | OK / FALHA |
| B (`B_stun_binding`) | STUN binding | sim/não | OK / FALHA |
| C-UDP (`C_turn_udp`) | TURN relay forçado via UDP | sim/não | OK / FALHA |
| C-TCP (`C_turn_tcp`) | TURN relay forçado via TCP | sim/não | OK / FALHA |
| C-TLS/443 (`C_turn_tls`) | TURN relay forçado via TLS | sim/não | OK / NÃO CONFIGURADO / FALHA |
| D | UDP bloqueado (fallback TCP/TLS) | sim/não | OK / PARCIAL / FALHA |
| E (`E_room_connectivity`) | Conectividade de sala e presença de participantes (infra apenas — não prova mídia/relay/segundo dispositivo) | sim/não | OK / FALHA |
| F (`F_stability`) | Estabilidade (duração mínima) | sim/não | OK / PARCIAL (N drops) |
| G | Falha controlada (endpoint inválido) | sim/não | OK (erro previsível) / FALHA |

## Transporte selecionado

- Transporte efetivamente usado na conexão (host / srflx / relay-UDP / relay-TCP / relay-TLS): `preencher`
- Porta/protocolo efetivamente usados: `preencher`

## Evidências manuais obrigatórias (chrome://webrtc-internals ou equivalente)

> Estas evidências são **obrigatórias** para o resultado `APPROVED`. O
> cenário E automatizado só prova conectividade de sala/presença — não
> substitui nenhum item abaixo (ver runbook, seção "O que o Cenário E prova
> e o que não prova").

- Segundo dispositivo/browser físico real usado (não apenas dois processos
  `livekit-cli` no mesmo host): sim/não — `qual dispositivo/browser`
- Publicação de mídia confirmada (participante A → B): sim/não
- Recepção de mídia confirmada (participante B recebendo A): sim/não
- Tipo de candidato ICE selecionado por participante (host/srflx/relay):
  `preencher`
- Candidato `relay` confirmado quando o teste exigia TURN: sim/não
- Ferramenta usada para inspeção (`chrome://webrtc-internals`,
  `about:webrtc` ou equivalente): `preencher`
- `WEBRTC_QA_MANUAL_EVIDENCE_CONFIRMED=1` foi usado na execução do script
  correspondente a este relatório: sim/não

## Candidatos ICE observados (inspeção manual via browser)

| Participante | host | srflx | relay |
| --- | --- | --- | --- |
| A | sim/não | sim/não | sim/não |
| B | sim/não | sim/não | sim/não |

## Publicação e recepção de mídia

- Publicação confirmada: sim/não
- Recepção confirmada: sim/não

## Duração do teste

- Janela de estabilidade: `N minutos`
- Quedas/reconnects observados: `N`

## Resultado final

`APPROVED` / `PARTIAL` / `FAILED` / `PENDING` — justificativa:

`preencher conforme critérios e exit codes do runbook docs/runbooks/task-158-webrtc-office-network-validation.md`

## Limitações

`preencher`

## Próximas ações

`preencher`
