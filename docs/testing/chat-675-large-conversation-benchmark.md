# #675 — conversa grande: invariantes e benchmark

Dois documentos em um, e a separação é o ponto principal.

**Invariantes** são o que o CI cobra. São relações, não números: valem para
qualquer viewport, qualquer overscan e qualquer máquina, e um deles quebrando é
uma regressão.

**Números observados** são a fotografia de uma execução. Servem para dar ordem
de grandeza a quem revisa. Variam com o tamanho da janela, com o overscan, com o
quanto o agendador conseguiu adiantar — e por isso nenhum deles está codificado
como asserção.

O cenário é o da issue: 500 mensagens, 104 anexos mistos (imagem pronta, imagem
com preview pendente, GIF, PDF, vídeo — dois por mensagem, em blocos de quatro
mensagens a cada quarenta), alturas de linha bem diferentes, paginação por cursor
de 50 em 50 e latência deliberada nas rotas de preview e de histórico.

## Invariantes cobradas

Ficam em `apps/web/e2e/messaging/large-conversation-performance.spec.ts`:

| Invariante                                                      | Forma                                             |
| --------------------------------------------------------------- | ------------------------------------------------- |
| O DOM não cresce com o histórico                                | linhas montadas ≪ mensagens servidas              |
| A janela montada fica em ordem de grandeza de dezenas           | `maxMountedVirtualRows < 2 × PAGE_SIZE`           |
| Nem todos os anexos são buscados                                | `previewRequestsTotal < anexos no histórico`      |
| Nenhum original pesado antes de interação                       | `heavyOriginalRequestsAtOpen === 0`               |
| Nenhum original pesado depois, exceto o que o teste manda tocar | resto de `heavyOriginals()` vazio                 |
| Nenhum `<video>` antes de alguém apertar Play                   | `videoElementsBeforePlay === 0`                   |
| A concorrência de previews respeita o teto                      | `maxConcurrentPreviewRequests <= 5`               |
| …e a fixture gera demanda suficiente para encostar nele         | `maxConcurrentPreviewRequests === 5`              |
| Um prepend preserva a **mesma** mensagem na âncora              | `anchorIdentityPreserved === true`                |
| …no **mesmo** lugar                                             | `anchorOffsetDriftPx !== null` e `<= 4`           |
| Uma remedição real depois do prepend não muda nem uma nem outra | idem, após crescer uma linha fora do campo visual |
| "Ir para o final" chega ao fim real                             | botão some **e** distância < 4px                  |
| O foco não some quando a linha que o continha é desmontada      | volta para a timeline, nunca `<body>`             |

A concorrência é a única igualdade exata, e é deliberada: a fixture foi montada
para saturar o limite (seis previews elegíveis por bloco contra um teto de
cinco), então "exatamente cinco" prova que o teto foi exercitado e não apenas
respeitado por falta de demanda.

A âncora é exigida por **identidade e pixel ao mesmo tempo**. Cada uma sozinha
passa com a outra quebrada: a mesma coordenada com outra mensagem é o leitor
deslocado uma linha, e a mesma mensagem em outra coordenada é o leitor deslocado
em pixels.

### Como rodar

```
cd apps/web
pnpm exec playwright test e2e/messaging/large-conversation-performance.spec.ts --project=chromium
```

## O benchmark, e como obter o "antes"

O instrumento é um spec separado que **não afirma nada** — só mede e anexa
`benchmark.json` ao relatório:

```
cd apps/web
pnpm exec playwright test e2e/messaging/large-conversation-baseline.spec.ts --project=chromium
```

Ele roda em duas árvores porque não importa nada de `src/`: a fixture inteira
vive em `apps/web/e2e/helpers/largeConversationFixture.ts`, que também não
importa. São esses dois arquivos — e só eles, ambos código de teste — que
atravessam a fronteira para o snapshot.

### Procedimento completo do "antes"

```bash
# 1. Exporta develop para fora do repositório. Read-only: nada de checkout,
#    branch, worktree ou stash na árvore de trabalho.
SNAP=$(mktemp -d)
git archive develop | tar -x -C "$SNAP"

# 2. Instala as dependências do snapshot.
cd "$SNAP" && pnpm install --frozen-lockfile

# 3. Copia o instrumento — dois arquivos de teste, nenhuma linha de produção da
#    feature. develop não tem virtualização, agendador nem gate de proximidade,
#    e é justamente por isso que ele é o "antes".
cp <repo>/apps/web/e2e/helpers/largeConversationFixture.ts "$SNAP/apps/web/e2e/helpers/"
cp <repo>/apps/web/e2e/messaging/large-conversation-baseline.spec.ts "$SNAP/apps/web/e2e/messaging/"

# 4. Porta própria para o dev-server do snapshot. Sem isto o Playwright
#    reaproveita o servidor da árvore atual e o "antes" mede o "depois".
cd "$SNAP/apps/web"
sed -i 's/5173/5199/g; s/reuseExistingServer: !process.env.CI/reuseExistingServer: false/' playwright.config.ts

# 5. Mede.
pnpm exec playwright test e2e/messaging/large-conversation-baseline.spec.ts --project=chromium
```

O `benchmark.json` de cada lado é o que a tabela abaixo compara.

Uma nota sobre o nome: **`servedMessagesCount` são os ids de mensagens distintos
devolvidos com sucesso pelas respostas da fixture durante o cenário.** Não é o
tamanho do estado do React, nem do store, nem o número de mensagens
renderizadas — nada disso é observável de fora sem instrumentar a aplicação, e o
benchmark não instrumenta. O nome é modesto de propósito.

## Números observados

Uma execução, mesma máquina, mesma sessão, mesmo instrumento, viewport padrão do
projeto (`Desktop Chrome`). Ordem de grandeza, não contrato: os totais de
requisições variam alguns pedidos entre execuções, conforme o quanto a rolagem
adianta e o que o agendador chega a começar antes de a linha sair da região
útil. O que **não** varia são as invariantes da seção anterior — é lá que estão
os limites que o CI cobra.

| Métrica                        | Antes (`develop`) | Depois (#675) |
| ------------------------------ | ----------------: | ------------: |
| `servedMessagesCount`          |               400 |           400 |
| `mountedMessageRowsAtOpen`     |                50 |            50 |
| `maxMountedMessageRows`        |               400 |            50 |
| `previewRequestsAtOpen`        |                16 |             4 |
| `previewRequestsTotal`         |                80 |            41 |
| `originalRequestsAtOpen`       |                16 |             1 |
| `originalRequestsTotal`        |                80 |             9 |
| `heavyOriginalRequestsAtOpen`  |                 8 |             0 |
| `heavyOriginalRequestsTotal`   |                40 |             0 |
| `videoElementsBeforePlay`      |                 2 |             0 |
| `maxConcurrentPreviewRequests` |                16 |             5 |
| `reachedRealBottom`            |               não |           sim |

### A âncora, que é medida à parte

O deslocamento da âncora **só é um número quando a mesma mensagem continua sendo
a âncora**. Subtrair o deslocamento de duas mensagens diferentes produz um valor,
e esse valor pode até ser pequeno, mas ele não diz nada sobre preservação de
âncora — dizer que "a deriva foi de 2px" quando a mensagem no topo mudou é
descrever um acaso de coordenada como se fosse a invariante.

|                           | Antes (`develop`)                 | Depois (#675) |
| ------------------------- | --------------------------------- | ------------- |
| `anchorMessageBefore`     | `…-m-154`                         | `…-m-154`     |
| `anchorMessageAfter`      | `…-m-205`                         | `…-m-154`     |
| `anchorIdentityPreserved` | **não**                           | **sim**       |
| `anchorOffsetDriftPx`     | **N/A** — a mensagem-âncora mudou | **0 px**      |

Em `develop` a âncora depois do prepend é uma mensagem **cinquenta e uma
posições adiante** da que o leitor tinha. É esse o comportamento antigo que a
#675 corrige, e é por isso que `anchorOffsetDriftPx` vem `null` ali: não há o que
comparar. Um `null` no baseline não é falha do instrumento — é o resultado.

### Leitura

- **O que a fixture serviu não mudou.** As mesmas 400 mensagens são entregues
  nas duas árvores; a virtualização não esconde histórico, só deixa de montá-lo.
- **O DOM parou de crescer com o histórico.** 400 linhas montadas contra 50 — e
  as 50 são a página de abertura, antes de a virtualização sequer valer.
- **Nada pesado é baixado antes de interação.** `heavyOriginalRequests` (vídeo,
  documento, imagem estática) cai de 40 para 0. E nenhum elemento `<video>`
  existe antes de alguém apertar Play — em `develop` já existiam dois na
  abertura.
- **A concorrência passou a ter teto.** 16 previews simultâneos viravam 5.
- **A posição de leitura deixou de escapar.** `develop` troca a mensagem-âncora
  no prepend; a #675 mantém a mesma mensagem no mesmo pixel.
- **O fim voltou a ser alcançável.** Com 400 mensagens montadas, `develop` não
  chega ao fim real dentro do orçamento do instrumento.

## Pendência conhecida

O diretório `apps/web/e2e` não é coberto por `tsc` (o `tsconfig.app.json` inclui
apenas `src`) nem pelo Vitest (que exclui `e2e/**` de propósito, para não rodar
specs do Playwright). Na prática, os arquivos de teste E2E não têm typecheck no
CI — foi assim que campos escritos em `Metrics` sobreviveram sem constar do tipo.
Adicionar um projeto de typecheck para `e2e` é uma melhoria de infraestrutura
independente desta issue, e está registrada aqui em vez de resolvida junto.

## O que este benchmark não mede

Tempo de frame e memória. Ambos dependem da máquina e do estado do navegador de
um jeito que um número único num relatório de PR esconderia mais do que
mostraria; as métricas acima são todas contagens e pixels, reprodutíveis.
