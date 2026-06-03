---
name: NIC Chat Design System
colors:
  surface: "#f9f9ff"
  surface-dim: "#d0daef"
  surface-bright: "#f9f9ff"
  surface-container-lowest: "#ffffff"
  surface-container-low: "#eff3ff"
  surface-container: "#e6eeff"
  surface-container-high: "#dee9fd"
  surface-container-highest: "#d9e3f7"
  on-surface: "#121c2a"
  on-surface-variant: "#4a4455"
  inverse-surface: "#273140"
  inverse-on-surface: "#ebf1ff"
  outline: "#7b7486"
  outline-variant: "#ccc3d7"
  surface-tint: "#7331df"
  primary: "#5300b7"
  on-primary: "#ffffff"
  primary-container: "#6d28d9"
  on-primary-container: "#dac5ff"
  inverse-primary: "#d3bbff"
  secondary: "#675971"
  on-secondary: "#ffffff"
  secondary-container: "#efdcf9"
  on-secondary-container: "#6d5f78"
  tertiary: "#6000a5"
  on-tertiary: "#ffffff"
  tertiary-container: "#7d21cb"
  on-tertiary-container: "#e3c2ff"
  error: "#ba1a1a"
  on-error: "#ffffff"
  error-container: "#ffdad6"
  on-error-container: "#93000a"
  primary-fixed: "#ebddff"
  primary-fixed-dim: "#d3bbff"
  on-primary-fixed: "#250059"
  on-primary-fixed-variant: "#5b00c5"
  secondary-fixed: "#efdcf9"
  secondary-fixed-dim: "#d2c0dc"
  on-secondary-fixed: "#22172c"
  on-secondary-fixed-variant: "#4f4259"
  tertiary-fixed: "#f0dbff"
  tertiary-fixed-dim: "#ddb7ff"
  on-tertiary-fixed: "#2c0051"
  on-tertiary-fixed-variant: "#6900b3"
  background: "#f9f9ff"
  on-background: "#121c2a"
  surface-variant: "#d9e3f7"
typography:
  display-lg:
    fontFamily: Inter
    fontSize: 48px
    fontWeight: "700"
    lineHeight: 56px
    letterSpacing: -0.02em
  headline-lg:
    fontFamily: Inter
    fontSize: 32px
    fontWeight: "600"
    lineHeight: 40px
    letterSpacing: -0.01em
  headline-md:
    fontFamily: Inter
    fontSize: 24px
    fontWeight: "600"
    lineHeight: 32px
  headline-sm:
    fontFamily: Inter
    fontSize: 20px
    fontWeight: "600"
    lineHeight: 28px
  body-lg:
    fontFamily: Inter
    fontSize: 18px
    fontWeight: "400"
    lineHeight: 28px
  body-md:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: "400"
    lineHeight: 24px
  body-sm:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: "400"
    lineHeight: 20px
  label-md:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: "500"
    lineHeight: 16px
    letterSpacing: 0.02em
  headline-lg-mobile:
    fontFamily: Inter
    fontSize: 28px
    fontWeight: "600"
    lineHeight: 36px
rounded:
  sm: 0.125rem
  DEFAULT: 0.25rem
  md: 0.375rem
  lg: 0.5rem
  xl: 0.75rem
  full: 9999px
spacing:
  unit: 4px
  xs: 4px
  sm: 8px
  md: 16px
  lg: 24px
  xl: 32px
  gutter: 24px
  margin-mobile: 16px
  margin-desktop: 40px
---

## Brand & Style

O sistema de design foca na confiabilidade e eficiência corporativa para o ambiente de laboratório e comunicação interna. A estética é centrada no estilo **Corporate / Modern**, priorizando clareza visual, estrutura organizada e um tom profissional adequado para apresentações executivas.

O objetivo é transmitir segurança e inovação através de uma interface limpa, onde o uso da cor roxa é intencional e focado em ações e hierarquia, evitando excessos decorativos. A experiência deve ser fluida e direta, facilitando a troca de informações rápidas entre colaboradores.

## Colors

A paleta é fundamentada no **Vibrant Purple** como cor de ação principal. O **Dark Purple** é reservado para elementos estruturais e tipografia de alto contraste, enquanto o **Lilac** atua em estados de hover e variações sutis.

- **Primária (#6D28D9):** Usada para botões principais, ícones ativos e branding.
- **Neutras:** O **Graphite Gray** é a base para textos de corpo e labels. O uso de **Light Gray** para fundos de áreas laterais (sidebars) e superfícies secundárias garante a separação visual sem a necessidade de bordas pesadas.
- **Gradients:** Use gradientes lineares sutis (ex: de #6D28D9 para #A855F7) exclusivamente em estados selecionados de navegação ou cards de métricas de destaque.

## Typography

Utilizamos a família **Inter** por sua legibilidade excepcional em telas e caráter técnico e neutro.

- **Hierarquia:** Títulos utilizam pesos `600` (Semi Bold) para garantir autoridade visual.
- **Corpo de texto:** O peso `400` (Regular) é o padrão para mensagens e descrições.
- **Labels:** Para identificadores pequenos e badges, o peso `500` (Medium) com leve espaçamento entre letras (letter-spacing) assegura que o conteúdo seja legível mesmo em tamanhos reduzidos.
- **Idioma:** Todo o conteúdo textual deve seguir as normas gramaticais do Português do Brasil (PT-BR).

## Layout & Spacing

O sistema utiliza um modelo de **Grid Fluido** baseado em múltiplos de 4px para manter a consistência matemática.

- **Desktop:** Grid de 12 colunas com margens de 40px. A área de chat central é expansível, enquanto as barras laterais possuem larguras fixas (ex: 280px para navegação).
- **Mobile:** Grid de 4 colunas com margens de 16px. Componentes como tabelas devem transitar para modelos de lista ou permitir scroll horizontal.
- **Ritmo Vertical:** Use 16px (md) para separação entre mensagens e 24px (lg) para separação entre seções ou cards de métricas.

## Elevation & Depth

Para manter o aspecto "Enterprise", a profundidade é comunicada através de **Tonal Layers** e bordas suaves, em vez de sombras pesadas.

- **Nível 0 (Fundo):** Light Gray (#F8FAFC) para o canvas principal.
- **Nível 1 (Superfícies):** White (#FFFFFF) para cards e área de chat, com uma borda fina de 1px em #E2E8F0.
- **Sombra Sutil:** Sombras são permitidas apenas em menus suspensos (dropdowns) e modais, utilizando um desfoque (blur) amplo e opacidade muito baixa (ex: 4-8%) para simular elevação natural sem poluir a interface.

## Shapes

Adotamos a escala **Soft** (0.25rem / 4px) para refletir precisão e seriedade.

- **Botões e Inputs:** 4px de raio de canto (border-radius).
- **Cards e Modais:** 8px (rounded-lg) para suavizar grandes áreas de conteúdo sem perder o rigor profissional.
- **Avatares:** Devem ser circulares para contraste visual com os elementos retilíneos da interface.

## Components

### Avatares e Status

Os avatares devem ser circulares. O indicador de status (online, away, offline) deve ser um círculo pequeno de 10px posicionado no canto inferior direito do avatar, com uma borda branca de 2px para separação visual.

### Botões

- **Primário:** Fundo #6D28D9, texto branco. Sem gradiente no estado padrão, apenas no hover de forma muito sutil.
- **Secundário:** Borda 1px #6D28D9, texto #6D28D9, fundo transparente.
- **Terciário:** Sem bordas, texto #374151.

### Cards de Métricas

Fundo branco, borda fina cinza, título em `label-md` (cinza) e valor principal em `headline-md` (roxo). Devem ser limpos, sem ícones desnecessários.

### Tabelas

Linhas com altura de 52px, cabeçalho com fundo #F8FAFC e texto em caixa alta (uppercase). Use divisores horizontais sutis em #F1F5F9.

### Badges e Chips

Utilize fundos com 10% de opacidade da cor principal e texto com 100% da cor para um visual "tonal" elegante e legível (ex: Badge Lilac com texto Dark Purple).

### Estados Vazios (Empty States)

Devem ser centralizados, utilizando ilustrações minimalistas em tons de cinza e roxo claro, seguidos por um título claro e uma chamada para ação (CTA) em português (ex: "Nenhuma conversa encontrada").
