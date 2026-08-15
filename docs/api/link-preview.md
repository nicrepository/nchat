# File-service: preview de links externos por Open Graph (RF-10)

Preview server-side de um link colado pelo usuario. O file-service busca a
pagina, le os metadados Open Graph e devolve **apenas texto normalizado**.

Nao e um proxy: nenhum corpo, header ou status remoto chega ao cliente. Nao e um
renderizador: nao ha browser, JavaScript, CSS, imagem, iframe, fonte ou qualquer
subresource -- e uma unica requisicao HTTP controlada pelo documento HTML.

Fora de escopo: deteccao de URL dentro da mensagem, renderizacao do card no
frontend, persistencia do preview e fetch da imagem do `og:image`.

## Habilitacao

A rota existe sempre. Enquanto `FILE_LINK_PREVIEW_ENABLED=false` (default) ela
responde `503 service_unavailable` -- nunca `404`, para que uma configuracao
incompleta nao seja confundida com rota inexistente.

O default e desligado porque a feature faz o servico abrir conexoes de saida
para hosts nomeados pelo usuario. Habilitar exige `AUTH_JWT_HMAC_SECRET`: a rota
e autenticada. Nao exige banco, storage nem chave de cifragem, entao ela roda com
`FILE_UPLOADS_ENABLED=false`.

Configuracao em `services/file-service/.env.example`:
`FILE_LINK_PREVIEW_ENABLED`, `FILE_LINK_PREVIEW_TIMEOUT_SECONDS` (default 5,
maximo 30) e `FILE_LINK_PREVIEW_CACHE_TTL_SECONDS` (default 900, maximo 86400).
O restante -- teto do corpo, limite de redirects, tamanho do cache, tipos aceitos
-- e constante: um deployment que pudesse alarga-los enfraqueceria o sandbox.

## Contrato

| Metodo | Rota publica              | Descricao                       |
| ------ | ------------------------- | ------------------------------- |
| POST   | `/api/files/link-preview` | metadados Open Graph de um link |

`POST` e nao `GET` com a URL na query string: o valor e fornecido pelo usuario e
aponta para terceiros, entao mante-lo no corpo o mantem fora de access logs,
referrers e caches intermediarios. O metodo tambem descreve o que a rota e -- uma
acao que provoca uma requisicao de saida.

```bash
curl -X POST https://nchat.local:8443/api/files/link-preview \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/artigo"}'
```

Resposta `200`:

```json
{
  "data": {
    "url": "https://example.com/artigo",
    "title": "Titulo da pagina",
    "description": "Resumo da pagina",
    "imageUrl": "https://cdn.example.com/card.png",
    "siteName": "Example"
  }
}
```

Campos ausentes na pagina sao omitidos. `url` e a forma canonica **do que foi
pedido**, nunca o destino final de um redirect: um card que afirmasse endereco
diferente do link que o usuario ve seria uma primitiva de phishing.

Origem dos campos: `og:title`, `og:description`, `og:image` (ou `og:image:url`)
e `og:site_name`. Se a pagina nao declara `og:title` ou `og:description`, valem
`<title>` e `<meta name="description">` como fallback. Tags duplicadas: a
primeira vence.

### Erros

| Status | Codigo                   | Quando                                               |
| ------ | ------------------------ | ---------------------------------------------------- |
| `400`  | `bad_request`            | corpo invalido, ou URL vazia/malformada/longa demais |
| `400`  | `url_not_allowed`        | esquema, porta ou destino recusado pela politica     |
| `403`  | `malicious_url`          | link recusado pelo Safe Browsing (RF-21)             |
| `401`  | `unauthorized`           | access token ausente ou invalido                     |
| `404`  | `preview_not_available`  | a pagina foi lida e nao tem metadado exibivel        |
| `415`  | `unsupported_media_type` | a resposta remota nao e `text/html`                  |
| `429`  | `rate_limited`           | orcamento por usuario esgotado (`Retry-After`)       |
| `502`  | `upstream_unavailable`   | o site nao pode ser alcancado ou lido                |
| `503`  | `link_check_unavailable` | veredito Safe Browsing indisponivel (RF-21)          |
| `503`  | `service_unavailable`    | feature desabilitada ou dependencia ausente          |
| `504`  | `upstream_timeout`       | o site nao respondeu dentro do orcamento             |

As mensagens sao texto fixo. Uma recusa nunca diz **qual** destino foi recusado,
nem repete endereco, hostname ou mensagem do servidor remoto: a diferenca entre
"recusado" e "recusado porque resolveu para tal faixa" e um mapa da rede interna.

## Safe Browsing (RF-21)

Quando `FILE_LINK_SAFETY_ENABLED=true`, o veredito da **URL canonica completa**
-- scheme, host, path e query, sem fragmento -- e consultado **antes** do fetch
Open Graph. URL condenada, veredito indisponivel, ou veredito ainda inexistente:
a pagina nunca e requisitada. Buscar primeiro e perguntar depois seria renderizar
a pagina de phishing.

A chave e a URL e nao o host, e essa distincao e o ponto: um preview renderiza
uma _pagina_, entao decidi-lo pela reputacao do dominio que a hospeda liberava
todo caminho naquele dominio.

URL sem veredito nao e liberacao e tambem nao e fetch: o file-service registra um
job duravel e responde `202 link_check_pending`. Um worker proprio submete o scan
a Cloudflare, guarda o UUID e faz o polling, entao repetir o preview da mesma URL
nao gera scan novo -- e um restart nao perde o que ja estava em andamento.

E um controle **distinto** da politica abaixo e nao substitui nenhuma parte
dela: o Safe Browsing responde se _aquela URL_ e maliciosa, a politica de destino
responde se este backend pode abrir a conexao para aquele endereco. Um destino recusado pela politica continua
recusado sem o provedor ser consultado.

Contrato completo, cache, granularidade e configuracao em
[link-safety.md](./link-safety.md).

## Seguranca

O destino e julgado pelo **endereco IP que a conexao vai usar**, nunca pelo
hostname. O dialer resolve o nome, verifica todas as respostas e conecta a um
endereco que ja aceitou -- nao existe janela entre a checagem e o uso, entao DNS
rebinding nao tem onde acontecer. Um nome que responde com varios enderecos e
recusado se **qualquer um** deles nao for publico.

A politica recusa destinos nao publicos em IPv4 e IPv6 -- incluindo as formas em
que um endereco IPv4 viaja disfarcado dentro de um IPv6 -- e com isso recusa
tambem os metadata services de nuvem e qualquer hostname ou alias que resolva
para eles. As faixas exatas estao em
`services/file-service/internal/linkpreview/policy.go`; nao sao repetidas aqui.

Alem disso:

- apenas `http` e `https`, apenas a porta default do esquema (o que impede a rota
  de virar scanner de portas indireto), e nunca credenciais embutidas na URL;
- cada redirect e uma nova conexao pelo mesmo dialer, entao a politica inteira
  vale de novo em cada salto; o numero de saltos e limitado;
- proxies do ambiente sao ignorados: um proxy resolveria e conectaria no lugar
  do servico, que e justamente a decisao que o dialer existe para tomar;
- TLS e verificado normalmente, contra o hostname original. Nada e desabilitado;
- allowlist de Content-Type: so `text/html`, decidido a partir do header antes de
  ler o corpo;
- o corpo e lido atraves de um limite, aplicado sobre os bytes **descomprimidos**
  (uma bomba de compressao expande ate o limite e para), e o parser encerra em
  `<body>`;
- timeouts curtos e finitos por fase -- conexao, handshake TLS, headers de
  resposta -- alem do orcamento total.

### Metadados sao input nao confiavel

`title`, `description` e `siteName` sao dados, nunca markup. O backend nao gera
HTML a partir deles: eles saem como strings JSON, com UTF-8 invalido descartado,
espacos em branco colapsados (inclusive quebras de linha) e tamanho truncado por
runes. O cliente deve renderiza-los como texto -- nunca com
`dangerouslySetInnerHTML`.

`imageUrl` e resolvido contra a pagina e aceito so como `http`/`https`, o que
impede `javascript:`, `data:` e `file:` de chegarem ao navegador. **Ele nao e
buscado pelo servidor**: um segundo fetch guiado por valor que a pagina remota
controla reintroduziria o SSRF que o resto da feature remove.

## Cache

Em processo, com TTL finito e numero maximo de entradas. A chave e a URL
canonica completa (esquema e host minusculos, fragmento removido, query
preservada), entao duas URLs distintas nunca compartilham entrada.

Falhas terminais tambem sao cacheadas, por um minuto: sem isso um cliente
repetindo uma URL morta pagaria um timeout e um socket novo a cada tentativa, que
e o unico desfecho que custa recursos reais ao servico. Cancelamento do chamador
nao e cacheado nem contado -- nao e uma resposta sobre a URL.

Duas limitacoes conhecidas, ambas aceitas:

- o cache e por processo, entao N replicas buscam a mesma URL ate N vezes. O
  alternativo seria o Valkey que o servico so opcionalmente tem, e fazer a
  feature depender de um bus do qual ela nao precisa seria um trade pior;
- nao ha coalescencia de requisicoes em voo. Se M clientes pedem a **mesma** URL
  nova dentro da janela de uma busca (~centenas de ms), saem M requisicoes ao
  mesmo site em vez de uma. O caso dominante -- previews pedidos conforme as
  mensagens renderizam -- e sequencial e ja e absorvido pelo cache. Isso e
  gentileza com o site remoto, nao um controle de seguranca: o volume continua
  limitado pelo rate limit por usuario e a politica SSRF nao e afetada.
  `golang.org/x/sync/singleflight` resolveria, ao custo de uma busca
  compartilhada cujo cancelamento passa a ser de todos; se o fan-out aparecer em
  producao, e essa a mudanca.

## Abuso

A rota e a unica do servico em que o chamador decide quanto trabalho de saida
acontece, entao ela e autenticada e tem orcamento por usuario por minuto,
separado do orcamento de upload -- previews nao podem consumir a cota que protege
uploads. O limiter e o mesmo mecanismo in-process ja usado pelo upload e carrega
o mesmo teto conhecido: N replicas concedem N orcamentos. O cache e o que evita
que o caso comum -- o mesmo link visto por todo mundo em um canal -- chegue a
rede.

## Observabilidade

Metrica: `nchat_file_link_previews_total{result}`, com o conjunto fechado `hit`,
`success`, `invalid_url`, `blocked`, `unsupported_content_type`, `timeout`,
`upstream_error`, `no_metadata`, `malicious`, `safety_unavailable`. `blocked`
subindo merece alerta: sao chamadores apontando o fetcher para destinos que a
politica recusa. `malicious` e `safety_unavailable` sao RF-21 -- veja tambem
`nchat_url_safety_checks_total{result}` em [link-safety.md](./link-safety.md).

A URL nunca e label -- e fornecida pelo chamador e ilimitada, entao um cliente
hostil criaria uma serie por request. Ela tambem nao e logada: a rota nao emite
log proprio, so as metricas e o log HTTP transversal, que registra o template da
rota e nunca o corpo.
