# DESIGN.md — NIC Chat / NIC-Labs

> Design system canônico para agentes de IA, geradores de UI e ferramentas como Google Stitch, Claude Code, Codex e Copilot.
> Este arquivo define a identidade visual, padrões de layout, componentes, tokens e regras de interface do **NIC Chat**, produto interno da **NIC-Labs**.

---

## 1. Produto

### Nome do produto

**NIC Chat**

### Marca institucional

**NIC-Labs**

### Categoria

Plataforma corporativa de comunicação interna.

### Escopo visual do MVP

O MVP deve comunicar claramente:

- chat interno em tempo real;
- canais públicos e privados;
- mensagens diretas;
- chamadas de voz simples;
- envio de arquivos;
- análise de segurança de arquivos;
- notificações básicas;
- perfil de usuário;
- configurações;
- painel administrativo básico.

### Funcionalidades fora do MVP

Não criar interfaces para:

- vídeo;
- compartilhamento de tela;
- gravação de chamadas;
- IA integrada;
- multi-workspace avançado;
- app mobile;
- billing;
- marketplace;
- DND corporativo avançado;
- URGENT bypass;
- white-label avançado;
- telas técnicas de Kubernetes, logs brutos ou infraestrutura.

---

## 2. Personalidade visual

A interface do NIC Chat deve parecer:

- corporativa;
- tecnológica;
- limpa;
- confiável;
- moderna;
- organizada;
- objetiva;
- segura;
- pronta para uso interno real.

A interface **não** deve parecer:

- genérica;
- colorida demais;
- infantil;
- informal demais;
- poluída;
- parecida com rede social casual;
- parecida com dashboard técnico pesado;
- neon;
- futurista exagerada.

---

## 3. Referência da marca NIC-Labs

A marca NIC-Labs possui:

- símbolo hexagonal;
- borda em grafite escuro;
- elementos internos em roxo e lilás;
- sensação de tecnologia e laboratório;
- contraste forte entre roxo, preto/grafite e branco;
- uso marcante de roxo como identidade.

### Uso da marca na UI

- Em tela de login: usar logo completa da NIC-Labs.
- Na sidebar: preferir o símbolo reduzido/ícone.
- No produto: usar o nome **NIC Chat**.
- Em contexto institucional: usar **NIC-Labs**.
- Não repetir a logo em excesso.
- Não usar a logo como marca d’água grande no fundo.
- Não distorcer a logo.
- Não aplicar efeitos, brilho, sombra pesada ou gradientes sobre a logo.

---

## 4. Paleta de cores

### Cores principais

| Token                    |     Valor | Uso                                                  |
| ------------------------ | --------: | ---------------------------------------------------- |
| `--color-primary`        | `#7B2FE3` | Botões principais, estados ativos, links importantes |
| `--color-primary-strong` | `#933EF2` | Hover, foco, destaques controlados                   |
| `--color-primary-soft`   | `#A865F7` | Badges suaves, highlights, detalhes                  |
| `--color-primary-dark`   | `#3B1B6D` | Fundos escuros com identidade                        |
| `--color-graphite`       | `#2F2938` | Sidebar, textos fortes, estrutura                    |
| `--color-ink`            | `#0E0B13` | Texto principal em fundos claros                     |
| `--color-bg`             | `#F6F4FA` | Fundo geral claro                                    |
| `--color-surface`        | `#FFFFFF` | Cards, painéis e áreas de conteúdo                   |
| `--color-border`         | `#E8E3F0` | Bordas e divisores                                   |
| `--color-muted`          | `#8C8499` | Metadados e textos secundários                       |

### Cores semânticas

| Token             |     Valor | Uso                              |
| ----------------- | --------: | -------------------------------- |
| `--color-success` | `#22C55E` | Online, aprovado, sucesso        |
| `--color-warning` | `#F59E0B` | Em análise, atenção              |
| `--color-danger`  | `#EF4444` | Bloqueado, erro, ação destrutiva |
| `--color-info`    | `#3B82F6` | Informação auxiliar              |

### Regras de uso de cor

- Roxo é cor de identidade e ação, não deve dominar toda a interface.
- Sidebar pode ser grafite/roxo muito escuro.
- Área principal deve ser clara.
- Usar verde/amarelo/vermelho apenas para status.
- Não usar gradientes fortes.
- Gradientes, quando necessários, devem ser sutis e restritos a login ou elementos hero.
- Não usar roxo em todos os cards, tabelas e banners ao mesmo tempo.

---

## 5. Tipografia

### Fonte preferencial

Usar uma fonte sem serifa moderna:

1. Inter;
2. Google Sans;
3. Roboto;
4. system-ui fallback.

### Escala tipográfica

| Token         | Tamanho |    Peso | Uso                                    |
| ------------- | ------: | ------: | -------------------------------------- |
| `--text-xs`   |    12px | 400/500 | Metadados, horários, labels pequenos   |
| `--text-sm`   |    14px | 400/500 | Texto secundário, mensagens auxiliares |
| `--text-base` |    16px |     400 | Mensagens e conteúdo principal         |
| `--text-lg`   |    18px |     600 | Headers de painéis                     |
| `--text-xl`   |    22px | 600/700 | Títulos de tela                        |
| `--text-2xl`  |    28px |     700 | Login e títulos especiais              |

### Regras de tipografia

- Mensagens devem ter excelente legibilidade.
- Metadados devem ser discretos.
- Evitar textos muito pequenos abaixo de 12px.
- Evitar pesos muito pesados em excesso.
- Títulos devem guiar a tela sem competir com o conteúdo.

---

## 6. Layout global

### Viewport principal

Desktop 1440px.

### Estrutura padrão

A aplicação usa três áreas:

1. sidebar esquerda;
2. área central;
3. painel direito opcional.

### Dimensões recomendadas

| Região                | Tamanho |
| --------------------- | ------: |
| Sidebar esquerda      |   280px |
| Header central        |    64px |
| Input de mensagem     |    72px |
| Painel direito        |   320px |
| Border radius padrão  |    12px |
| Border radius pequeno |     8px |
| Border radius grande  |    16px |

### Espaçamento

| Token       | Valor |
| ----------- | ----: |
| `--space-1` |   4px |
| `--space-2` |   8px |
| `--space-3` |  12px |
| `--space-4` |  16px |
| `--space-5` |  20px |
| `--space-6` |  24px |
| `--space-8` |  32px |

### Regras de layout

- Usar muito espaçamento.
- Evitar telas densas demais.
- Painel direito deve ser útil, mas discreto.
- A conversa é o foco principal.
- Sidebar deve facilitar navegação sem competir visualmente.
- Preferir separação por blocos e espaçamento, não por muitas linhas.
- Não usar cards demais na tela principal do chat.

---

## 7. Sidebar

### Aparência

- Fundo grafite escuro ou roxo quase preto.
- Texto branco ou cinza claro.
- Item ativo com fundo roxo discreto.
- Indicador vertical roxo no item ativo.
- Avatares pequenos e status visível.

### Conteúdo padrão

- logo/símbolo NIC-Labs;
- texto “NIC Chat”;
- workspace “NIC-Labs”;
- busca compacta;
- canais;
- mensagens diretas;
- botão “Novo canal”;
- usuário logado no rodapé.

### Canais padrão

- `#geral`
- `#infraestrutura`
- `#suporte`
- `#projetos`
- `#avisos`

### DMs padrão

- Ana Paula — online;
- Bruno Lima — ausente;
- Carla Souza — offline;
- Diego Martins — online.

---

## 8. Área de mensagens

### Estrutura de mensagem

Cada mensagem deve conter:

- avatar;
- nome;
- horário;
- texto;
- reações, quando houver;
- ação de responder;
- menu de ações discreto.

### Regras

- Não usar bolhas exageradas estilo WhatsApp.
- Layout deve parecer Slack/Google Chat enterprise, mas com identidade própria.
- Mensagens devem ser fáceis de escanear.
- Sistema deve ter mensagens visuais discretas.
- Reações devem ser pequenas e claras.
- Horários e metadados devem ser discretos.

### Exemplo de mensagens

- “Bom dia, pessoal. O monitoramento do servidor de aplicações ficou estável durante a madrugada.”
- “Vou validar os alertas do Prometheus e revisar os logs do Traefik antes do meio-dia.”
- “Também vou testar o fluxo de upload e o scan de arquivos no ambiente de staging.”
- “Arquivo `relatorio-backup.pdf` enviado e aguardando análise de segurança.”

---

## 9. Chamadas de voz

### Regra principal

O MVP tem **voz**, não vídeo.

### Componentes de voz

- botão “Iniciar chamada de voz”;
- banner “Chamada de voz ativa”;
- botão “Entrar na chamada”;
- barra compacta de chamada;
- avatares dos participantes;
- indicador de quem está falando;
- botão “Mutar”;
- botão “Sair”;
- timer.

### Aparência

- integrada ao chat;
- compacta;
- elegante;
- sem parecer Google Meet/Zoom;
- sem grid de vídeo;
- sem câmera;
- sem tela cheia.

### Texto padrão

- “Chamada de voz ativa no canal”
- “3 participantes agora”
- “Chamada de voz — #infraestrutura”
- “Chamada com Juliane Lino”

---

## 10. Arquivos e segurança

### Estados de arquivo

| Estado     | Cor      | Descrição                                   |
| ---------- | -------- | ------------------------------------------- |
| Em análise | Amarelo  | Arquivo enviado, aguardando verificação     |
| Aprovado   | Verde    | Arquivo liberado para download              |
| Bloqueado  | Vermelho | Arquivo bloqueado por política de segurança |

### Regras

- Arquivo recém-enviado deve aparecer como “Em análise”.
- Download deve parecer desabilitado enquanto estiver em análise.
- Arquivo bloqueado deve ter mensagem clara, mas não alarmista.
- Segurança deve ser percebida sem deixar a interface pesada.

### Textos padrão

- “Arquivo enviado e aguardando análise de segurança.”
- “Este arquivo está em análise de segurança. O download será liberado após aprovação.”
- “Arquivo bloqueado por política de segurança.”

---

## 11. Notificações

### MVP

O MVP possui:

- web push;
- badge no navegador;
- som de notificação;
- e-mail digest.

### Fora do MVP visual

Não desenhar ainda:

- regras avançadas de expediente;
- DND corporativo;
- URGENT;
- cotas por cargo;
- bypass.

### Texto informativo

“No MVP, as notificações incluem web push, badge no navegador e e-mail digest. Regras avançadas de expediente entram em fase futura.”

---

## 12. Administração

### Objetivo do admin

O painel administrativo deve ser compreensível para TI e gestão.

### Deve conter

- usuários ativos;
- mensagens/dia;
- canais ativos;
- armazenamento usado;
- tabela de usuários;
- moderação;
- auditoria;
- exportação de logs.

### Não deve conter

- billing;
- detalhes de Kubernetes;
- logs brutos excessivos;
- tuning de infraestrutura;
- gráficos complexos demais;
- marketplace.

### Métricas padrão

- Usuários ativos hoje: 38
- Mensagens hoje: 1.248
- Canais ativos: 12
- Armazenamento usado: 18.6 GB

---

## 13. Componentes

### Botão primário

- Fundo roxo principal.
- Texto branco.
- Radius 10px.
- Hover com roxo vibrante.
- Usar para ações principais.

### Botão secundário

- Fundo branco ou transparente.
- Borda cinza clara.
- Texto grafite.
- Usar para ações complementares.

### Botão destrutivo

- Vermelho com uso moderado.
- Pode ser outline ou fundo vermelho suave.
- Usar para sair da chamada, revogar sessão, remover mensagem.

### Input

- Fundo branco.
- Borda cinza clara.
- Radius 10px.
- Foco com borda roxa.
- Placeholder discreto.

### Badge

- Pequeno.
- Texto legível.
- Não depender apenas de cor.
- Estados: Online, Ausente, Offline, Em análise, Aprovado, Bloqueado.

### Card

- Fundo branco.
- Borda `#E8E3F0`.
- Radius 12px.
- Sombra muito discreta ou nenhuma.

### Modal

- Fundo branco.
- Overlay escurecido leve.
- Título claro.
- Texto curto.
- Ações alinhadas à direita.

### Toast

- Pequeno.
- Canto superior direito ou inferior direito.
- Mensagem curta.
- Sucesso, erro ou informação.

---

## 14. Estados

### Estados obrigatórios

- canal vazio;
- busca sem resultados;
- erro de conexão;
- upload em andamento;
- arquivo em análise;
- arquivo bloqueado;
- sem permissão;
- mensagem não enviada;
- sessão revogada;
- conexão restabelecida.

### Regras

- Estados devem ser claros.
- Evitar ilustrações grandes demais.
- Ícones devem ser lineares.
- Mensagens devem ser curtas.
- Sempre oferecer ação quando possível.

---

## 15. Microcopy

### Tom

- profissional;
- direto;
- amigável;
- sem gírias;
- sem jargão técnico desnecessário.

### Frases aprovadas

- “Mensagem enviada”
- “Arquivo aguardando análise”
- “Chamada de voz ativa”
- “Você foi mencionado em #infraestrutura”
- “Sessão revogada com sucesso”
- “Conexão restabelecida”
- “Não foi possível enviar a mensagem. Tente novamente.”
- “Este canal ainda não possui mensagens.”
- “Você não tem permissão para acessar este canal.”

### Evitar

- textos longos;
- linguagem informal demais;
- termos técnicos como WebSocket, PostgreSQL, Valkey, ClamAV na UI final;
- mensagens agressivas de erro.

---

## 16. Acessibilidade

### Regras

- Contraste adequado.
- Estados de foco visíveis.
- Ícones acompanhados de texto quando necessário.
- Não depender apenas de cor.
- Badges devem conter texto.
- Botões com área clicável confortável.
- Tabelas legíveis.
- Navegação por teclado deve parecer possível.

---

## 17. Do / Don’t

### Do

- Usar roxo como destaque.
- Usar sidebar escura.
- Manter área principal clara.
- Criar bastante respiro visual.
- Priorizar legibilidade.
- Mostrar segurança nos arquivos.
- Integrar voz ao chat de forma compacta.
- Usar português do Brasil.

### Don’t

- Não criar interface genérica.
- Não usar roxo em excesso.
- Não usar fundo escuro em toda a aplicação.
- Não criar telas de vídeo.
- Não criar dashboard técnico pesado.
- Não criar animações exageradas.
- Não criar visual neon.
- Não criar telas de funcionalidades fora do MVP.
- Não incluir IA, billing ou marketplace.

---

## 18. Critério visual de aceite

Uma tela está aprovada quando:

- parece produto corporativo real;
- usa a identidade NIC-Labs sem exagero;
- tem hierarquia clara;
- tem espaçamento suficiente;
- não parece template genérico;
- comunica a funcionalidade sem explicação adicional;
- pode ser apresentada para diretoria;
- não inclui funcionalidades fora do MVP;
- mantém consistência com este DESIGN.md.

---

## 19. Resumo para agentes de IA

Sempre que gerar uma tela para o NIC Chat:

1. Use identidade NIC-Labs.
2. Use roxo como destaque, não como fundo dominante.
3. Use sidebar escura e conteúdo claro.
4. Mantenha visual enterprise.
5. Escreva tudo em português do Brasil.
6. Foque no MVP: chat, voz, arquivos, notificações, perfil e admin.
7. Não gere vídeo, IA, billing, marketplace ou mobile.
8. Não use termos técnicos de infraestrutura na interface do usuário.
9. Priorize clareza, legibilidade e aparência apresentável.
