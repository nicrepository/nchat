# CDD — Context Driven Development

# NIC-Labs Chat — MVP Interno com Chat + Voz

**Versão:** 1.0
**Data-base:** 15/05/2026
**Período do MVP:** 15/05/2026 a 21/08/2026
**Produto:** NIC-Labs Chat
**Objetivo do documento:** fornecer contexto completo para desenvolvimento, prototipação, uso com IA, design, validação técnica e alinhamento de escopo.

---

## 1. Visão executiva

O **NIC-Labs Chat** é uma plataforma interna de comunicação corporativa para a NIC-Labs, com foco inicial em substituir ou reduzir o uso informal de ferramentas como WhatsApp, Telegram e chats fragmentados para comunicação de trabalho.

O MVP deve entregar uma solução funcional, estável e apresentável até agosto, permitindo que o time se comunique internamente por:

- canais públicos e privados;
- mensagens diretas 1:1;
- grupos ad-hoc;
- mensagens em tempo real;
- envio e visualização de arquivos;
- notificações básicas;
- painel administrativo mínimo;
- chamadas de voz básicas.

O produto deve nascer com arquitetura preparada para evoluir futuramente para uma solução comercial, on-premise, soberana, auditável e customizável.

Este documento deve ser usado como **fonte principal de contexto** para:

- Claude Code;
- OpenAI Codex;
- GitHub Copilot;
- Google Stitch;
- designers;
- desenvolvedores;
- stakeholders;
- revisões de arquitetura;
- revisão de escopo.

---

## 2. Problema que estamos resolvendo

A comunicação interna atual tende a ficar espalhada em ferramentas diferentes, com baixa rastreabilidade, pouca padronização, dificuldade de controle de acesso e dependência de soluções externas.

Problemas principais:

1. **Comunicação fragmentada**
   Informações ficam distribuídas entre WhatsApp, Telegram, e-mails, Google Chat ou outras ferramentas.

2. **Baixo controle corporativo**
   Dificuldade para auditar, moderar, organizar canais e preservar histórico.

3. **Dependência de fornecedores externos**
   Dados, disponibilidade, roadmap e limites dependem de terceiros.

4. **Ausência de comunicação estruturada por canais**
   Times, projetos e áreas precisam de espaços próprios e organizados.

5. **Necessidade de voz integrada**
   A comunicação por texto não cobre todos os cenários. Para suporte, infraestrutura e decisões rápidas, voz é necessária.

6. **Potencial de produto futuro**
   Caso o MVP funcione internamente, há possibilidade de evoluir para solução comercial soberana, on-premise e white-label.

---

## 3. Objetivo do MVP

O objetivo do MVP é entregar uma primeira versão funcional e confiável para uso interno até agosto.

### 3.1 O MVP deve permitir

- login e autenticação;
- criação e uso de canais;
- mensagens diretas;
- grupos de conversa;
- envio e recebimento de mensagens em tempo real;
- envio de arquivos;
- preview básico de arquivos;
- verificação assíncrona de arquivos com antivírus;
- busca básica;
- notificações web/e-mail;
- painel administrativo básico;
- chamadas de voz 1:1;
- salas de voz em canais.

### 3.2 O MVP não deve tentar resolver tudo

Ficam fora do MVP:

- vídeo;
- compartilhamento de tela;
- gravação de chamadas;
- criptografia ponta a ponta completa;
- DND/URGENT corporativo avançado;
- multi-workspace;
- app mobile nativo;
- app desktop Tauri;
- LGPD completa com hard delete automatizado;
- failover automático para cloud;
- integrações Google Calendar/Jira/GitHub;
- SDK público;
- white-label completo.

---

## 4. Princípios do projeto

### 4.1 Simplicidade operacional primeiro

O MVP deve ser simples o suficiente para ser entregue e mantido por um time pequeno.

Evitar no MVP:

- service mesh complexo;
- excesso de microsserviços sem necessidade;
- automações prematuras;
- E2E complexo;
- integrações externas não essenciais.

### 4.2 Confiabilidade acima de quantidade de features

Um chat com poucas features, mas confiável, é melhor que um chat cheio de recursos instáveis.

Prioridade:

1. mensagens chegam;
2. mensagens não duplicam indevidamente;
3. WebSocket reconecta;
4. notificações não se perdem;
5. arquivos são processados com segurança;
6. voz funciona de forma aceitável;
7. interface é simples e clara.

### 4.3 Código gerado por IA deve ser tratado como código de júnior

Ferramentas como Claude Code, Codex e Copilot serão usadas intensivamente, mas todo código gerado deve passar por revisão humana.

Regras:

- IA acelera, mas não substitui design técnico;
- todo código crítico precisa de teste;
- todo fluxo de auth, permissão, arquivo e WebSocket precisa de revisão manual;
- não aceitar código sem entender;
- não aceitar bibliotecas aleatórias sugeridas por IA sem validação de licença.

### 4.4 Produto apresentável desde cedo

O MVP será usado para validar internamente e possivelmente demonstrar para stakeholders.

Logo, a interface não pode parecer “protótipo sujo”.

Deve parecer:

- limpa;
- moderna;
- corporativa;
- organizada;
- confiável;
- com identidade visual da NIC-Labs.

---

## 5. Identidade visual e branding

### 5.1 Nome do produto

Nome de trabalho:

**NIC-Labs Chat**

Nomes alternativos futuros podem ser avaliados, mas para MVP e protótipo usaremos NIC-Labs Chat.

### 5.2 Logo

Foram fornecidas variações da logo NIC-Labs:

- `icononly.png`
- `icononly_transparent.png`
- `fulllogo.png`
- `fulllogo_transparent.png`

Uso recomendado:

| Contexto                | Arquivo recomendado        |
| ----------------------- | -------------------------- |
| Sidebar compacta        | `icononly_transparent.png` |
| Tela de login clara     | `fulllogo.png`             |
| Tela de login escura    | `fulllogo_transparent.png` |
| Splash/loading          | `icononly_transparent.png` |
| Cabeçalho institucional | `fulllogo_transparent.png` |

### 5.3 Paleta visual sugerida

A logo usa uma identidade forte baseada em roxo. A interface deve usar essa cor como destaque, não como fundo dominante em todos os elementos.

#### Cores principais

| Uso               | Cor sugerida |
| ----------------- | ------------ |
| Roxo principal    | `#6D28D9`    |
| Roxo vibrante     | `#8B5CF6`    |
| Roxo claro        | `#A855F7`    |
| Roxo escuro       | `#3B2F45`    |
| Fundo claro       | `#F8FAFC`    |
| Superfície clara  | `#FFFFFF`    |
| Texto principal   | `#111827`    |
| Texto secundário  | `#6B7280`    |
| Borda             | `#E5E7EB`    |
| Sucesso/online    | `#22C55E`    |
| Alerta            | `#F59E0B`    |
| Erro/bloqueado    | `#EF4444`    |
| Fundo escuro      | `#0B0B0F`    |
| Superfície escura | `#17141F`    |

### 5.4 Direção visual

A interface deve ter aparência próxima de:

- Google Chat pela simplicidade;
- Slack pela estrutura de canais;
- Linear pela limpeza visual;
- Discord apenas no conceito de sala de voz, não no visual gamer.

Evitar:

- excesso de roxo;
- gradientes exagerados;
- botões muito chamativos;
- excesso de cards;
- muitas bordas pesadas;
- aparência de dashboard genérico;
- aparência de app gamer.

### 5.5 Tipografia

Sugestão:

- Inter;
- Roboto;
- Geist;
- system font stack.

Preferência para interface:

```css
font-family:
  Inter,
  Roboto,
  system-ui,
  -apple-system,
  BlinkMacSystemFont,
  "Segoe UI",
  sans-serif;
```

### 5.6 Tom de comunicação

Microcopy em português do Brasil.

Tom:

- profissional;
- direto;
- amigável;
- sem piadas excessivas;
- sem linguagem muito informal.

Exemplos:

- “Arquivo enviado e aguardando análise de segurança.”
- “Chamada de voz ativa no canal.”
- “Você foi mencionado em #infraestrutura.”
- “Sua sessão foi encerrada neste dispositivo.”
- “Não foi possível reconectar. Tentando novamente...”

---

## 6. Usuários e perfis

### 6.1 Usuário comum

Pessoa que usa o chat diariamente para:

- ler canais;
- responder mensagens;
- enviar arquivos;
- entrar em chamadas de voz;
- receber notificações;
- conversar com colegas.

Precisa de:

- simplicidade;
- clareza;
- notificações confiáveis;
- busca rápida;
- interface sem ruído.

### 6.2 Moderador

Usuário com permissões adicionais para:

- moderar mensagens;
- remover mensagens inadequadas;
- gerenciar canais específicos;
- apoiar organização do workspace.

### 6.3 Admin de Workspace

Responsável por:

- usuários;
- permissões;
- convites;
- moderação;
- retenção;
- dashboard;
- logs.

### 6.4 Admin Master

Responsável por:

- configuração global;
- segurança;
- auditoria;
- políticas do sistema;
- administração geral.

---

## 7. Escopo funcional do MVP

### 7.1 Autenticação

O MVP deve conter:

- login com e-mail/senha;
- SSO/OIDC;
- cadastro manual pelo admin;
- convite por e-mail;
- recuperação de senha;
- expiração de sessão;
- proteção contra brute-force;
- log de tentativas falhas;
- listagem e revogação de dispositivos.

### 7.2 Canais

O MVP deve conter:

- canais públicos;
- canais privados;
- canal `#geral` obrigatório;
- categorias/pastas de canais;
- controle de acesso por canal;
- lista de membros;
- descrição do canal;
- mensagens fixadas.

### 7.3 Mensagens

O MVP deve conter:

- envio em tempo real;
- persistência no PostgreSQL;
- WebSocket para entrega;
- Valkey para Pub/Sub e fan-out;
- paginação cursor-based;
- rich text básico;
- menções;
- reações;
- quote-reply;
- favoritos;
- edição com histórico;
- deleção com placeholder;
- indicador de digitação;
- presença básica.

### 7.4 DMs

O MVP deve conter:

- conversa 1:1;
- grupos ad-hoc;
- header com nome, cargo e status;
- botão de voz 1:1;
- histórico paginado.

### 7.5 Arquivos

O MVP deve conter:

- upload de arquivos;
- limite configurável, padrão 50 MB;
- preview de imagens;
- preview básico de PDF;
- preview de vídeo com range request, se viável;
- criptografia AES-256 via envelope encryption no `file-service`;
- armazenamento em SeaweedFS;
- scan assíncrono com ClamAV;
- status do arquivo:
  - `em_analise`;
  - `aprovado`;
  - `bloqueado`.

### 7.6 Busca

O MVP deve conter:

- PostgreSQL FTS para canais públicos;
- busca por mensagens;
- busca por usuários;
- busca por canais;
- ranking por relevância e data;
- highlight no frontend.

Não incluir no MVP:

- busca local sobre conteúdo E2E;
- busca semântica;
- busca com IA.

### 7.7 Notificações

O MVP deve conter:

- arquitetura outbox no PostgreSQL;
- Valkey como scheduler/fila rápida;
- worker idempotente;
- retry com backoff;
- `notification_id` único;
- deduplicação no cliente;
- FCM web push;
- e-mail digest básico;
- badge/som via WebSocket.

### 7.8 Voz MVP

O MVP deve conter voz mínima usando LiveKit:

- chamada de voz 1:1;
- sala de voz por canal;
- entrar e sair sem encerrar a sala;
- mute/unmute;
- indicador de chamada ativa;
- indicador de participante falando;
- controle básico de permissão: só entra se pertence ao canal;
- token LiveKit gerado pelo backend.

Não incluir no MVP:

- vídeo;
- compartilhamento de tela;
- gravação;
- transcrição;
- efeitos;
- IA em chamadas;
- chamadas externas via SIP.

### 7.9 Admin

O MVP deve conter:

- painel administrativo responsivo;
- gestão de usuários;
- convite por e-mail;
- banimento/suspensão;
- RBAC;
- moderação de mensagens;
- exportação de logs CSV/JSON;
- dashboard básico:
  - usuários ativos;
  - mensagens/dia;
  - canais ativos;
  - uso de storage;
  - chamadas de voz ativas, se já implementado no MVP de voz.

### 7.10 Offline básico

O MVP deve conter:

- cache shell da aplicação;
- últimos canais acessados;
- mensagem amigável quando offline.

Não incluir no MVP:

- sincronização offline complexa;
- histórico local completo;
- resolução de conflitos offline.

---

## 8. Escopo fora do MVP

Fora do MVP, mas previsto para evolução:

- E2E com MLS RFC 9420;
- DND corporativo com horário de expediente;
- URGENT com auditoria;
- multi-workspace;
- app desktop Tauri;
- app mobile React Native;
- APNs real para iOS;
- FCM Android;
- integrações Google Calendar, Jira e GitHub;
- PWA offline completo;
- failover automático;
- LGPD completa;
- status page pública;
- SDKs;
- white-label;
- IA integrada.

---

## 9. Stack técnica aprovada para MVP

### 9.1 Backend

- Go;
- microsserviços ou monólito modular com separação clara por domínio;
- REST para APIs principais;
- WebSocket para realtime.

### 9.2 Banco de dados

- PostgreSQL 16;
- Patroni para HA, se necessário;
- migrations com Goose, Atlas ou ferramenta equivalente.

### 9.3 Cache, Pub/Sub e locks

- Valkey 8;
- comandos que devem ser validados:
  - Pub/Sub;
  - Streams;
  - SETNX;
  - TTL;
  - locks distribuídos;
  - sliding window.

### 9.4 Storage

- SeaweedFS;
- API S3-compatible;
- decisão provisória após PoC;
- decisão definitiva após validação com:
  - upload grande;
  - preview;
  - replicação;
  - backup/restore;
  - falha de nó;
  - recuperação.

### 9.5 Voz

- LiveKit;
- coturn;
- WebRTC;
- backend gera tokens de acesso às salas.

### 9.6 Frontend

- React;
- TypeScript;
- Vite;
- React Router;
- TanStack Query ou equivalente;
- TipTap para editor;
- WebSocket client;
- Service Worker para cache básico.

### 9.7 Observabilidade

- Prometheus;
- Grafana para uso interno;
- Jaeger;
- OpenTelemetry;
- Alertmanager.

### 9.8 Segurança e secrets

- TLS 1.3 em endpoints públicos;
- Sealed Secrets;
- CSP/Helmet/security headers;
- rate limiting;
- logs de auditoria.

---

## 10. Arquitetura lógica

### 10.1 Serviços principais

```text
auth-service
chat-service
file-service
notification-service
media-service
admin-service
search-service
```

### 10.2 Responsabilidades

#### auth-service

- login;
- refresh token;
- SSO;
- convite;
- recuperação de senha;
- sessão;
- dispositivos.

#### chat-service

- canais;
- DMs;
- mensagens;
- WebSocket;
- presença;
- menções;
- reações;
- quote-reply.

#### file-service

- upload;
- download;
- preview;
- envelope encryption;
- ClamAV;
- status do arquivo.

#### notification-service

- outbox;
- worker de notificação;
- FCM web;
- e-mail;
- badge/som WS;
- retries.

#### media-service

- integração com LiveKit;
- criação de sala;
- geração de token;
- permissões de entrada;
- eventos de chamada ativa.

#### admin-service

- RBAC;
- usuários;
- moderação;
- dashboard;
- logs.

#### search-service

- PostgreSQL FTS;
- busca por mensagem, usuário e canal.

---

## 11. Fluxos críticos

### 11.1 Fluxo de envio de mensagem

```text
Frontend -> chat-service -> PostgreSQL -> Valkey Pub/Sub -> WebSocket clients
```

Regras:

- persistir antes de emitir;
- mensagem deve ter ID único;
- cliente deve conseguir deduplicar;
- WebSocket deve reconectar;
- mensagem deve aparecer para o remetente rapidamente.

### 11.2 Fluxo de notificação

```text
chat-service -> notification_outbox(PostgreSQL) -> notification-worker -> FCM/e-mail/WS badge
```

Regras:

- PostgreSQL é fonte de verdade;
- Valkey acelera, mas não é fonte primária;
- worker deve ser idempotente;
- retry com backoff;
- `notification_id` único.

### 11.3 Fluxo de upload de arquivo

```text
Frontend -> file-service -> encrypt -> SeaweedFS -> ClamAV async -> status aprovado/bloqueado
```

Regras:

- arquivo inicia como `em_analise`;
- download definitivo só após scan aprovado;
- se bloqueado, mostrar aviso claro;
- logs de auditoria para upload e bloqueio.

### 11.4 Fluxo de chamada de voz 1:1

```text
Frontend -> media-service -> valida permissão -> gera token LiveKit -> cliente conecta no LiveKit
```

Regras:

- usuário precisa estar autenticado;
- usuário só pode entrar em sala autorizada;
- token deve ter expiração curta;
- eventos de entrada/saída devem atualizar UI.

### 11.5 Fluxo de sala de voz em canal

```text
Usuário clica "Entrar na voz" -> media-service valida membership -> LiveKit token -> sala ativa -> chat-service publica estado
```

Regras:

- sala não acaba quando um participante sai;
- canal mostra indicador de voz ativa;
- participantes aparecem com avatar;
- indicador visual para quem está falando.

---

## 12. Modelo de dados conceitual

### 12.1 User

Campos sugeridos:

- id;
- name;
- email;
- avatar_file_id;
- role;
- job_title;
- bio;
- timezone;
- status_text;
- status_emoji;
- created_at;
- updated_at;
- deleted_at.

### 12.2 DeviceSession

- id;
- user_id;
- device_name;
- user_agent;
- ip_address;
- last_seen_at;
- revoked_at;
- created_at.

### 12.3 Channel

- id;
- name;
- slug;
- description;
- type: public/private;
- category_id;
- created_by;
- created_at;
- archived_at.

### 12.4 ChannelMember

- channel_id;
- user_id;
- role;
- joined_at;
- muted_until.

### 12.5 DirectConversation

- id;
- type: one_to_one/group;
- created_at.

### 12.6 DirectConversationMember

- conversation_id;
- user_id;
- joined_at.

### 12.7 Message

- id;
- channel_id nullable;
- conversation_id nullable;
- sender_id;
- parent_message_id nullable;
- body;
- rich_body_json;
- edited_at;
- deleted_at;
- created_at.

### 12.8 MessageReaction

- message_id;
- user_id;
- emoji;
- created_at.

### 12.9 MessageFavorite

- message_id;
- user_id;
- created_at.

### 12.10 PinnedMessage

- message_id;
- channel_id;
- pinned_by;
- pinned_at.

### 12.11 FileObject

- id;
- owner_id;
- channel_id nullable;
- conversation_id nullable;
- original_name;
- mime_type;
- size_bytes;
- storage_key;
- encryption_key_id;
- scan_status;
- scan_result;
- created_at;
- approved_at;
- blocked_at.

### 12.12 NotificationOutbox

- id;
- user_id;
- event_type;
- payload_json;
- status;
- attempts;
- next_attempt_at;
- created_at;
- processed_at.

### 12.13 VoiceRoom

- id;
- type: direct/channel;
- channel_id nullable;
- conversation_id nullable;
- livekit_room_name;
- active;
- created_by;
- created_at;
- ended_at.

### 12.14 AuditLog

- id;
- actor_id;
- action;
- entity_type;
- entity_id;
- metadata_json;
- ip_address;
- created_at.

---

## 13. API context

### 13.1 Auth

```http
POST /api/auth/login
POST /api/auth/refresh
POST /api/auth/logout
POST /api/auth/forgot-password
POST /api/auth/reset-password
GET  /api/auth/sessions
DELETE /api/auth/sessions/{id}
```

### 13.2 Channels

```http
GET    /api/channels
POST   /api/channels
GET    /api/channels/{id}
PATCH  /api/channels/{id}
POST   /api/channels/{id}/join
POST   /api/channels/{id}/leave
GET    /api/channels/{id}/members
```

### 13.3 Messages

```http
GET    /api/channels/{id}/messages
POST   /api/channels/{id}/messages
PATCH  /api/messages/{id}
DELETE /api/messages/{id}
POST   /api/messages/{id}/reactions
DELETE /api/messages/{id}/reactions/{emoji}
POST   /api/messages/{id}/favorite
POST   /api/messages/{id}/pin
```

### 13.4 Direct messages

```http
GET  /api/dms
POST /api/dms
GET  /api/dms/{id}/messages
POST /api/dms/{id}/messages
```

### 13.5 Files

```http
POST /api/files
GET  /api/files/{id}
GET  /api/files/{id}/download
GET  /api/files/{id}/preview
GET  /api/files/recent
```

### 13.6 Search

```http
GET /api/search?q=&type=messages|users|channels|files
```

### 13.7 Notifications

```http
GET   /api/notifications/settings
PATCH /api/notifications/settings
POST  /api/notifications/test
```

### 13.8 Voice

```http
POST /api/voice/direct/{conversationId}/join
POST /api/voice/channels/{channelId}/join
POST /api/voice/rooms/{roomId}/leave
GET  /api/voice/channels/{channelId}/status
```

### 13.9 Admin

```http
GET    /api/admin/users
PATCH  /api/admin/users/{id}
POST   /api/admin/users/{id}/suspend
POST   /api/admin/users/{id}/ban
GET    /api/admin/audit-logs
GET    /api/admin/dashboard
POST   /api/admin/audit-logs/export
```

---

## 14. WebSocket events

### 14.1 Client -> server

```json
{ "type": "typing.start", "channel_id": "..." }
{ "type": "typing.stop", "channel_id": "..." }
{ "type": "presence.update", "status": "online" }
{ "type": "message.ack", "message_id": "..." }
```

### 14.2 Server -> client

```json
{ "type": "message.created", "payload": {} }
{ "type": "message.updated", "payload": {} }
{ "type": "message.deleted", "payload": {} }
{ "type": "reaction.created", "payload": {} }
{ "type": "typing.started", "payload": {} }
{ "type": "presence.changed", "payload": {} }
{ "type": "notification.created", "payload": {} }
{ "type": "voice.room.started", "payload": {} }
{ "type": "voice.room.ended", "payload": {} }
{ "type": "voice.participant.joined", "payload": {} }
{ "type": "voice.participant.left", "payload": {} }
```

---

## 15. Design do MVP

### 15.1 Telas obrigatórias para protótipo

1. Login;
2. Tela principal do chat;
3. Canal com mensagens;
4. DM 1:1;
5. Sala de voz ativa no canal;
6. Chamada de voz 1:1;
7. Upload de arquivo;
8. Arquivo em análise;
9. Busca global;
10. Perfil do usuário;
11. Configurações de notificação;
12. Painel admin;
13. Moderação;
14. Estados de erro/reconexão.

### 15.2 Layout principal

Estrutura:

```text
┌──────────────────────────────────────────────────────────────┐
│ Topbar                                                       │
├──────────────┬──────────────────────────────┬────────────────┤
│ Sidebar      │ Área de conversa             │ Painel direito │
│ canais/DMs   │ mensagens + composer         │ contexto       │
└──────────────┴──────────────────────────────┴────────────────┘
```

### 15.3 Sidebar

Deve conter:

- logo NIC-Labs;
- nome do workspace;
- busca rápida;
- canais;
- DMs;
- botão novo canal;
- status do usuário.

### 15.4 Área de conversa

Deve conter:

- header do canal;
- descrição curta;
- membros online;
- mensagem fixada;
- lista de mensagens;
- composer;
- botões de emoji, anexo e enviar.

### 15.5 Voz no canal

Quando houver chamada ativa:

- banner discreto: “Chamada de voz ativa”;
- botão “Entrar na chamada”;
- participantes com avatar;
- botão mutar;
- botão sair;
- indicador de fala.

### 15.6 Estados de arquivos

- `Em análise`: amarelo discreto;
- `Aprovado`: verde;
- `Bloqueado`: vermelho;
- mensagem clara para bloqueado.

### 15.7 Estados de conexão

- conectado;
- reconectando;
- offline;
- erro de envio;
- mensagem pendente.

---

## 16. Prompt principal para Google Stitch

```text
Crie um protótipo web desktop de alta fidelidade para um MVP chamado NIC-Labs Chat.

Use a identidade visual da NIC-Labs: logo roxa com ícone hexagonal/floral, paleta roxa, preto, branco e cinzas neutros. O visual deve ser corporativo, moderno, limpo e confiável. Evite aparência gamer, excesso de cores ou layout poluído.

O MVP é uma plataforma interna de comunicação corporativa com chat em tempo real, canais, DMs, arquivos, busca, notificações básicas, painel admin e chamadas de voz.

Criar as seguintes telas:

1. Login com logo NIC-Labs, e-mail, senha, botão Entrar, botão Entrar com SSO e link Esqueci minha senha.
2. Tela principal do chat com sidebar de canais/DMs, área de mensagens e painel direito de detalhes.
3. Canal #infraestrutura com mensagens reais, menções, reações, quote-reply e mensagem fixada.
4. DM 1:1 com botão de chamada de voz.
5. Canal com chamada de voz ativa: banner discreto, botão Entrar na chamada, participantes, mute/unmute e sair.
6. Arquivos recentes com status Aprovado, Em análise e Bloqueado.
7. Busca global com filtros Mensagens, Pessoas, Canais e Arquivos.
8. Perfil do usuário com foto, cargo, status, timezone e dispositivos conectados.
9. Configurações de notificações.
10. Painel admin com métricas, usuários, moderação e exportação de logs.

Usar português do Brasil.

A interface deve parecer pronta para apresentação para diretoria: clara, espaçada, consistente e enterprise.
```

---

## 17. Requisitos de qualidade

### 17.1 Performance

MVP deve suportar:

- 200 usuários simultâneos no teste final;
- 2.000 eventos/s no teste de MVP;
- p95 de entrega de mensagens menor que 100 ms on-premise em cenário controlado;
- reconexão WebSocket automática.

### 17.2 Segurança

Obrigatório:

- TLS 1.3;
- headers de segurança;
- proteção XSS;
- proteção CSRF onde aplicável;
- validação server-side;
- rate limiting;
- nenhum secret em plaintext;
- auditoria de ações admin;
- scan de arquivos;
- isolamento de permissões.

### 17.3 Observabilidade

Obrigatório:

- logs estruturados;
- tracing Jaeger;
- métricas Prometheus;
- dashboards Grafana;
- alertas básicos;
- correlação por request ID.

### 17.4 UX

Obrigatório:

- interface responsiva;
- loading states;
- empty states;
- error states;
- estados offline/reconectando;
- mensagens claras;
- navegação previsível.

---

## 18. Uso de IA no desenvolvimento

### 18.1 Regras para Claude Code, Codex e Copilot

Sempre fornecer contexto antes de pedir código:

- serviço em que está trabalhando;
- objetivo da tarefa;
- contratos de API;
- modelo de dados;
- restrições de segurança;
- testes esperados;
- padrões do projeto.

Nunca pedir:

- “crie tudo”;
- “implemente o chat inteiro”;
- “faça do jeito que achar melhor”.

Preferir tarefas pequenas:

- implementar endpoint específico;
- criar migration;
- criar teste unitário;
- revisar race condition;
- gerar componente UI isolado;
- escrever worker idempotente.

### 18.2 Prompt padrão para IA de código

```text
Contexto:
Estamos desenvolvendo o NIC-Labs Chat, um chat interno corporativo em Go + React.

Serviço atual:
[auth-service/chat-service/file-service/etc]

Objetivo:
[descrever tarefa específica]

Requisitos:
[listar requisitos]

Restrições:
- Código simples e testável
- Sem dependências desnecessárias
- Validar erros
- Logs estruturados
- Não vazar dados sensíveis
- Manter compatibilidade com PostgreSQL/Valkey

Entregue:
- Código principal
- Testes unitários
- Breve explicação da decisão
```

### 18.3 Checklist para aceitar código gerado por IA

- [ ] Entendi o código;
- [ ] Não adicionou dependência desnecessária;
- [ ] Não quebrou arquitetura;
- [ ] Tem tratamento de erro;
- [ ] Tem teste;
- [ ] Não expõe segredo;
- [ ] Não cria race condition;
- [ ] Não ignora permissão;
- [ ] Passou no CI;
- [ ] Foi revisado por humano.

---

## 19. Definição de pronto

Uma tarefa só está pronta quando:

- [ ] código implementado;
- [ ] testes passando;
- [ ] revisão feita;
- [ ] CI verde;
- [ ] logs adequados;
- [ ] erros tratados;
- [ ] permissão validada;
- [ ] documentação mínima atualizada;
- [ ] testado manualmente no fluxo principal;
- [ ] sem bug crítico conhecido.

---

## 20. Riscos principais do MVP

### 20.1 Risco: escopo crescer demais

Mitigação:

- congelar escopo do MVP;
- mover extras para V1.0;
- priorizar estabilidade.

### 20.2 Risco: WebSocket instável

Mitigação:

- `-race` no CI;
- testes k6;
- monitorar goroutines;
- reconexão automática;
- pprof.

### 20.3 Risco: voz instável

Mitigação:

- usar LiveKit;
- limitar a voz, sem vídeo;
- testar coturn cedo;
- testar em rede real da empresa;
- monitorar jitter e packet loss.

### 20.4 Risco: notificações falharem

Mitigação:

- outbox PostgreSQL;
- worker idempotente;
- retry;
- deduplicação;
- testes de restart.

### 20.5 Risco: IA gerar código ruim

Mitigação:

- revisão humana;
- testes;
- PRs pequenos;
- não aceitar código não entendido;
- linters e CI.

### 20.6 Risco: UX ruim e baixa adoção

Mitigação:

- protótipo no Stitch;
- UAT com 5-10 pessoas;
- ajustes antes do go-live;
- onboarding simples.

---

## 21. Critérios de aceitação do MVP

- [ ] login funcional;
- [ ] SSO funcional;
- [ ] canais públicos e privados;
- [ ] DMs 1:1;
- [ ] grupos ad-hoc;
- [ ] mensagens em tempo real;
- [ ] presença básica;
- [ ] rich text;
- [ ] reações;
- [ ] menções;
- [ ] quote-reply;
- [ ] fixadas;
- [ ] favoritas;
- [ ] edição;
- [ ] deleção com placeholder;
- [ ] upload de arquivos;
- [ ] preview de arquivos;
- [ ] ClamAV assíncrono;
- [ ] busca PostgreSQL FTS;
- [ ] notificações FCM web;
- [ ] e-mail digest;
- [ ] outbox PostgreSQL;
- [ ] admin RBAC;
- [ ] moderação;
- [ ] dashboard básico;
- [ ] logs de auditoria;
- [ ] chamada de voz 1:1;
- [ ] sala de voz em canal;
- [ ] TLS 1.3;
- [ ] observabilidade;
- [ ] backup básico;
- [ ] load test 200 usuários/2k eventos;
- [ ] UAT interno;
- [ ] go-live assistido.

---

## 22. Roadmap pós-MVP

### 22.1 V1.0 comercial

- E2E via MLS;
- DND/URGENT;
- multi-workspace;
- Tauri;
- PWA offline completo;
- integrações;
- LGPD completa;
- failover automático;
- status page;
- pentest;
- SDKs básicos.

### 22.2 v1.1

- mobile React Native;
- APNs iOS;
- FCM Android;
- melhorias de voz;
- push mobile completo.

### 22.3 V2.0

- IA integrada;
- busca semântica;
- resumo de canais;
- automações;
- analytics avançado;
- white-label comercial;
- marketplace/integrations.

---

## 23. Decisões técnicas já tomadas

| Tema         | Decisão                           |
| ------------ | --------------------------------- |
| Cache/PubSub | Valkey                            |
| Storage      | SeaweedFS, com validação          |
| Secrets      | Sealed Secrets                    |
| Tracing      | Jaeger                            |
| Métricas     | Prometheus + Grafana interno      |
| Voz          | LiveKit + coturn                  |
| Frontend     | React + TypeScript                |
| Backend      | Go                                |
| Banco        | PostgreSQL                        |
| Arquivos     | AES-256 no file-service           |
| Notificações | PostgreSQL outbox + Valkey        |
| Antivírus    | ClamAV assíncrono                 |
| Design       | NIC-Labs roxo, limpo, corporativo |

---

## 24. Decisões pendentes

- ferramenta de migrations definitiva;
- biblioteca Go para WebSocket;
- biblioteca OIDC;
- estratégia final de deploy;
- provedor de e-mail transacional;
- nomes finais de canais iniciais;
- política inicial de retenção;
- limites iniciais de upload;
- layout final aprovado no Stitch;
- fluxo final de onboarding.

---

## 25. Glossário

### CDD

Context Driven Development. Abordagem em que o desenvolvimento é guiado por um documento de contexto rico, para reduzir ambiguidade entre pessoas, ferramentas de IA e decisões técnicas.

### MVP

Minimum Viable Product. Primeira versão funcional para validação real.

### Valkey

Alternativa compatível com Redis, com licença mais adequada para produto comercial.

### SeaweedFS

Storage distribuído compatível com S3 API, usado para arquivos.

### LiveKit

Servidor SFU/WebRTC para áudio, vídeo e mídia em tempo real.

### coturn

Servidor TURN/STUN usado para ajudar WebRTC em redes com NAT/firewall.

### Outbox

Padrão em que eventos são persistidos no banco antes de serem processados por workers, evitando perda.

### Envelope encryption

Modelo em que o serviço criptografa dados antes de enviá-los para o storage.

---

## 26. Regra final de escopo

O MVP deve ser julgado por uma pergunta simples:

> O time consegue usar isso diariamente para conversar por texto, enviar arquivos, receber notificações e falar por voz sem precisar voltar para WhatsApp/Telegram?

Se a resposta for sim, o MVP cumpriu seu objetivo.
