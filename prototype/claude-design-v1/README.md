# Claude Design — Protótipo Visual v1

> **⚠️ Este diretório é referência visual, não código de produção.**
> Não importe, reutilize nem integre nada daqui diretamente em `apps/web` ou qualquer outro pacote do repositório.

## O que é isso

Protótipo de UI/UX do NChat gerado no [Claude Design](https://claude.ai/design), exportado como HTML/CSS/JS estático.
Serve como referência visual para futuras tasks de frontend.

## Estrutura

```
claude-design-v1/
├── nic-chat/          # Telas do protótipo (HTML + CSS + JS + assets)
│   ├── assets/        # Logos e ícones (nic-labs-icon.png, nic-labs-logo.png)
│   ├── tokens.css     # Design tokens (cores, tipografia, espaçamento, sombras)
│   ├── partials.js    # Componentes parciais compartilhados entre telas
│   └── *.html         # Telas individuais (ver lista abaixo)
├── screenshots/       # Capturas de tela de estados selecionados
└── uploads/           # Uploads originais do Claude Design (screens + código bruto)
```

## Telas cobertas

| Arquivo                    | Tela                                 |
| -------------------------- | ------------------------------------ |
| `login.html`               | Login                                |
| `shell.html`               | Shell principal (layout base)        |
| `canal.html`               | Canal de mensagens                   |
| `dm.html`                  | DM (mensagens diretas / 1-to-1)      |
| `arquivos.html`            | Arquivos compartilhados              |
| `busca.html`               | Busca global                         |
| `chamada.html`             | Chamada de voz/vídeo (shell)         |
| `chamada-audio-1to1.html`  | Chamada de áudio 1-to-1              |
| `chamada-audio-grupo.html` | Chamada de áudio em grupo            |
| `perfil.html`              | Perfil do usuário                    |
| `seguranca.html`           | Segurança da conta                   |
| `sessoes.html`             | Sessões ativas                       |
| `notificacoes.html`        | Configurações de notificações        |
| `admin.html`               | Painel administrativo                |
| `admin-usuarios.html`      | Admin — gestão de usuários           |
| `admin-canais.html`        | Admin — gestão de canais             |
| `admin-auditoria.html`     | Admin — auditoria                    |
| `estados.html`             | Estados de UI (vazio, erro, loading) |
| `index.html`               | Índice navegável das telas           |

## Assets e logos

- `nic-chat/assets/nic-labs-icon.png` — Ícone da Nic Labs
- `nic-chat/assets/nic-labs-logo.png` — Logo completo da Nic Labs

## Design tokens

`nic-chat/tokens.css` define as variáveis CSS do sistema de design:
cores primárias, neutros, tipografia, espaçamentos, bordas e sombras.

## Como usar como referência

1. Abra `nic-chat/index.html` em um browser para navegar entre as telas.
2. Inspecione `tokens.css` para extrair o sistema de cores e tipografia.
3. Use as telas HTML como especificação visual — **não como código reutilizável**.

## O que **não** fazer com este protótipo

- ❌ Importar componentes JSX/HTML em `apps/web`
- ❌ Tratar `tokens.css` como arquivo de produção sem revisão
- ❌ Integrar a tela de login com o fluxo de autenticação diretamente
- ❌ Adicionar dependências baseadas nos imports deste protótipo

## Implementação futura

Para transformar este protótipo em código de produção:

1. **Extrair design tokens** de `tokens.css` para o sistema de design oficial (ex: Tailwind config ou CSS custom properties auditadas).
2. **Recriar componentes em React/TypeScript** a partir das telas HTML como especificação visual — não como base de código.
3. **Seguir ADRs** de stack e arquitetura do repositório.
4. **Não copiar HTML inline** — os estilos do protótipo são intencionalmente autocontidos para visualização.

---

_Gerado em: 2026-05-28 | Ferramenta: Claude Design | Versão: v1_
