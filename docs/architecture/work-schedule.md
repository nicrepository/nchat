# Horario de trabalho (work schedule)

Dominio temporal de disponibilidade do NChat (issue #743, parent #678). Policy
Engine, Web Push, som, toast, digest, feriados, ferias, overrides e a UI de
Perfil > Notificacoes (#729) **estao fora** desta camada e nao existem ainda.

Contrato em Go: `libs/go/platform/workschedule`.

## O que este dominio responde

Dado um schedule e um **instante absoluto**, uma unica pergunta:

| Estado               | Significado                                               |
| -------------------- | --------------------------------------------------------- |
| `within_work_hours`  | o instante caiu dentro de um intervalo do dia local       |
| `outside_work_hours` | ha schedule e o instante caiu fora de todos os intervalos |
| `not_configured`     | nao ha schedule; nada e afirmado sobre a pessoa           |

Nao decide entrega. "Estar fora do expediente" e um fato; suprimir ou nao uma
notificacao por causa disso e decisao do Policy Engine, que ainda vai combinar
esta resposta com mute, prioridade, feriados e overrides. Misturar as duas
coisas tornaria a regra temporal impossivel de reutilizar e de testar sem um
canal de entrega — o mesmo acoplamento que `notificationevent` ja recusa.

## Ownership e source of truth

O pacote fica em `libs/go/platform` pelo motivo dos vizinhos (`notificationevent`,
`uploadpolicy`, `antispampolicy`): a regra tem mais de um consumidor previsto e
nenhum dono natural entre os servicos. O consumidor real sera o Policy Engine em
notification-service.

**Nenhuma tabela nova foi criada nesta issue**, e isso e deliberado:

- `auth.users.timezone` ja existe e ja e validada contra a base IANA real
  (`validateTimezone`, auth-service). O timezone da pessoa **nao** e duplicado
  aqui; `New` recebe o nome IANA de quem ja o possui.
- Nenhuma estrutura existente guarda intervalos por dia da semana —
  `auth.auth_policy_settings` so aceita escalares numericos/booleanos por
  constraint, e as politicas de workspace vivem como colunas em
  `chat.workspaces`. Uma tabela de jornada seria mesmo nova.
- Nao existe hoje **nenhum escritor**: a UI administrativa esta fora de escopo
  (#729 e o Admin Console de jornada nao existem). Criar o schema antes do
  escritor deixaria uma tabela permanentemente vazia e um store sem chamador.

Quando o escritor existir, o formato normalizado esperado e uma linha de
schedule por escopo (timezone + `source`) e uma linha por intervalo
(`weekday`, `start_minute`, `end_minute`), com as mesmas constraints que `New`
aplica. `Source` ja distingue politica organizacional de preferencia pessoal, e
`Source.ReadOnly` falha fechado: um valor que este build nao conhece e tratado
como politica de outra pessoa, nunca como algo que o usuario pode editar.

## Semantica dos intervalos

- Meio-abertos: `[start, end)`. No `start` a pessoa ja esta dentro; no `end` ja
  esta fora. Um instante pertence a exatamente um intervalo.
- Minutos desde a meia-noite local, `0 <= start < end <= 1440`.
- Almoco **nao** e uma flag: e a ausencia de intervalo entre dois blocos.
- Um intervalo nunca cruza a meia-noite. Turno 22:00-06:00 e dois intervalos em
  dois dias.
- Intervalos que se sobrepoem, iguais ou **encostados** (`07:00-12:00` seguido de
  `12:00-16:00`) sao recusados. Encostados nao sao ambiguos quanto a conter, mas
  seriam quanto a mudar: o limite das 12:00 seria reportado como proxima
  transicao sem que o estado mude ali. Um bloco e um intervalo so.
- A ordem de entrada nao importa: `New` ordena antes de validar, e ordenar nao
  salva nenhuma entrada invalida.
- Um dia com mais de 720 intervalos e recusado **antes** de ser ordenado. E
  aritmetica, nao politica: intervalos nao encostam e nao sao vazios, entao cada
  um custa ao menos um minuto de trabalho e um de intervalo, e 1440 minutos
  comportam 720. Uma lista maior e provadamente invalida sem ser lida — a
  validacao nao pode ser um lugar onde se gasta CPU do servidor.

## Timezone

Timezone IANA e a unica autoridade temporal.

- A avaliacao recebe um instante **absoluto**; nenhum relogio e lido dentro do
  dominio. Isso e o que torna a funcao determinista, replayavel e independente
  do relogio do browser.
- O timezone do processo nunca e consultado, e `"Local"` e recusado justamente
  por nomear a zona do servidor.
- Offset UTC nao e aceito como substituto do nome IANA.
- A validacao usa `time.LoadLocation` contra a base real; regex nao prova que uma
  zona existe. O pacote embute `time/tzdata` porque a imagem de runtime e
  distroless e nao traz `/usr/share/zoneinfo` — mesmo motivo do auth-service.

### DST: leituras que a zona pula ou repete

Uma leitura civil nao e um instante. Quando a zona atrasa o relogio, `01:15`
nomeia **dois** instantes; quando adianta, pode nao nomear **nenhum**.

Por isso o schedule nao e comparado como horario civil: para cada data local ele
e **resolvido** em janelas absolutas — uma resolucao por offset que aquela data
viveu, recortada ao trecho em que o offset esteve realmente em vigor. `Evaluate`
e `NextTransition` leem essas mesmas janelas na mesma passagem, entao os dois nao
podem descrever linhas do tempo diferentes.

O contrato que sai disso:

- **Aritmetica de relogio de parede.** Num dia de spring-forward um bloco
  `00:00-06:00` continua terminando as seis da manha e dura cinco horas reais;
  num dia de fall-back dura sete.
- **Hora repetida (fall-back): o bloco acontece duas vezes.** Um bloco
  `01:15-01:45` e trabalhado nas duas passagens, porque o relogio da pessoa o
  mostra duas vezes. Sao duas janelas absolutas distintas, e a transicao
  reportada no fim da primeira aponta para o inicio da segunda — nunca para a
  semana seguinte.
- **Recorte.** Se so o fim cai na hora repetida (`00:30-01:30`), a segunda janela
  comeca no proprio instante da mudanca: `00:30` aconteceu uma vez, `01:00-01:30`
  aconteceu de novo.
- **Hora inexistente (spring-forward): o bloco simplesmente nao existe.** Um
  bloco inteiramente dentro do buraco (`02:10-02:40` num dia em que 02:00 vira
  03:00) nao produz janela nenhuma e **nao gera transicao**, porque nenhum
  relogio leu aqueles minutos. Na semana seguinte o mesmo bloco e trabalhado
  normalmente.
- **Recorte no buraco.** Um bloco que so comeca dentro dele (`02:30-05:00`)
  comeca quando o relogio alcanca o bloco, ou seja, no proprio instante da
  mudanca, e termina no seu fim civil.
- **Blocos que se encostam no tempo absoluto nao sao transicao.** As duas
  passagens de `01:00-02:00` numa noite de fall-back sao trabalho continuo, assim
  como um bloco que termina a meia-noite seguido de outro que comeca nela. O
  estado nao muda ali, entao nada e anunciado ali.

Nada disso e uma regra sobre horario de verao: tudo cai fora da mesma resolucao.
Nao ha `if DST`, nenhum offset somado a mao e nenhuma zona tratada como caso
especial.

## Proxima transicao

`Evaluation.NextTransition` e o instante absoluto em que o estado muda, e e
exatamente isso: o primeiro instante posterior ao avaliado em que uma nova
chamada a `Evaluate` responderia diferente. Nenhuma mudanca e pulada e nenhuma e
inventada no meio. Na pratica e o fim da sequencia de trabalho atual quando
dentro, e o inicio da proxima janela quando fora.

A busca e limitada a quinze datas civis a partir da avaliada: o resto do dia
atual, um ciclo semanal completo e um segundo ciclo. O segundo nao e folga — uma
zona pode **remover uma data civil** do calendario (Pacific/Apia removeu
2011-12-30 quando Samoa mudou de lado da linha internacional da data), e a
ocorrencia que cair nessa data simplesmente nao acontece, empurrando a proxima
para uma semana depois. Um terceiro ciclo exigiria duas datas removidas com
exatamente sete dias de diferenca, coisa que nunca ocorreu na base IANA.

A resolucao de cada data tambem caminha sobre as transicoes da zona com um teto
pequeno — a sondagem cobre tres dias e nenhuma zona da base IANA muda de offset
mais de duas vezes nesse intervalo. Nenhum dos dois loops e ilimitado, nem com
dado de timezone malformado.

Zero (`IsZero`) significa "nao calculavel": sem schedule, ou schedule sem nenhum
intervalo em dia nenhum. Um schedule vazio e aceito e **nao** e o mesmo que
`not_configured` — "nunca trabalha" e "nao sabemos" sao fatos distintos.

## Extensao

```text
work_schedule + holidays + out_of_office/vacation + temporary_override
    = notification availability
```

Nada disso e implementado aqui e nenhuma interface especulativa foi declarada
para isso. O que o desenho garante e apenas que compor nao custa nada deste lado:
quem decide disponibilidade parte do estado do work schedule e subtrai dias sem
que o schedule precise saber.
