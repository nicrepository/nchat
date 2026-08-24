# Admin Console — integrações seguras (issue #582)

Runbook da tela `/integrations`: o que ela mostra, o que cada diagnóstico
realmente faz, o que este deployment consegue verificar e o que ele
deliberadamente não verifica.

O contrato HTTP está em [`../api/admin-endpoints.md`](../api/admin-endpoints.md),
seção 8. A autorização está em
[`../security/rbac-matrix.md`](../security/rbac-matrix.md). O inventário de
configuração está em [`../security/config-inventory.md`](../security/config-inventory.md).

---

## 1. O que a tela responde

Duas perguntas, nesta ordem: **o que a plataforma sabe sobre esta integração
agora**, e **o que acontece quando a gente tenta de verdade**.

A primeira é respondida pela coleta passiva da issue #581 — abrir a página não
contata nada. A segunda é o diagnóstico ativo, e ele só roda quando um operador
aperta o botão.

Cada integração tem sempre as mesmas quatro seções, na mesma ordem: **estado**,
**configuração**, **teste/diagnóstico**, **histórico**. Uma lista de variáveis
de ambiente agrupadas por prefixo é exatamente o que esta issue existe para
substituir.

## 2. A tela não escreve configuração

Nenhuma chave de integração é classe A. Todas vêm do ConfigMap versionado em Git
ou de um Sealed Secret, então alterá-las é commit + rollout ou o runbook de
rotação ([`sealed-secrets-rotation.md`](sealed-secrets-rotation.md)).

Não existe campo "substituir secret", e isso não é uma lacuna: não existe
backend de secret que a Admin API possa escrever, e um campo desses ou gravaria
um valor que ninguém lê ou empurraria credencial de cluster para um processo que
não pode tê-la. Uma credencial aparece como `Configurado` / `Não configurado` e
nada mais.

O registry em `services/admin-service/internal/domain/integration.go` apenas
**nomeia** chaves que já existem no catálogo da issue #580;
`ValidateIntegrationRegistry` recusa uma que não exista. É isso que impede a tela
de virar um segundo modelo de configuração.

## 3. O que pode e o que não pode ser diagnosticado

| Integração | Diagnóstico | Etapas                                              |
| ---------- | ----------- | --------------------------------------------------- |
| Keycloak   | sim         | DNS, TCP, TLS, discovery, issuer, JWKS, credencial¹ |
| SMTP       | sim         | DNS, TCP, TLS, credencial, prontidão (+ entrega²)   |
| LiveKit    | sim         | DNS, TCP, TLS, credencial, prontidão                |
| ClamAV     | sim         | DNS, TCP, prontidão (`PING`/`VERSION`)              |
| SeaweedFS  | sim         | DNS, TCP, TLS, prontidão                            |
| TURN       | **não**     | nenhuma variável da plataforma nomeia o servidor    |
| Link Scan  | **não**     | a credencial é escopada a chat-service/file-service |

¹ Reportada como **não executada**, com o motivo: validar o client sem executar
uma autenticação real não é algo que o protocolo ofereça, e um diagnóstico que
logasse alguém colocaria um evento de login na trilha do provedor a cada clique.

² Só quando o e-mail de teste é acionado. Um diagnóstico comum nunca entrega
nada.

As duas linhas sem diagnóstico mostram o motivo no console, em vez de um botão
ausente. Inventar um alvo é exatamente o que esta superfície existe para evitar,
e é por isso que o motivo é dado em vez de omitido.

### Por que TURN não tem diagnóstico

O coturn é configurado dentro do próprio LiveKit
(`infra/compose/livekit/livekit.yaml.template`) e no compose de desenvolvimento.
Nenhum workload do NChat recebe uma variável que nomeie o servidor TURN, então
não há alvo em `os.Environ()` deste pod. Quando o TURN passar a ser nomeado por
configuração de plataforma, a linha ganha um adapter — e a mudança obriga a
declarar a política de rede dele primeiro, porque o registry recusa uma
integração que declare política sem diagnóstico.

### Por que Link Scan não tem diagnóstico

A credencial do provedor é um Secret montado **apenas** por chat-service e
file-service, por decisão da RF-21. O `admin-service` não a recebe. Um teste
daqui gastaria a quota de um terceiro para verificar conectividade e não provaria
nada sobre o pipeline que realmente verifica links. O estado dessa integração
continua vindo do Health Center, como `disabled` ou `unknown`.

## 4. Segurança dos diagnósticos

O diagnóstico é o único lugar do `admin-service` em que um clique de operador
causa conexão de saída. Três camadas, e cada uma cobre o que as outras não
cobrem.

**O alvo vem do ambiente deste pod.** Nenhum handler, corpo, header ou parâmetro
alcança um endereço de discagem. Não existe campo de URL, host ou porta em lugar
nenhum da tela — o cliente escolhe _qual_ integração contatar, nunca _o que_ ela
é.

**O scheme é conferido antes de a URL ser usada.** Só `http` e `https` para alvo
em forma de URL; `host:porta` puro para os demais. Um ConfigMap com `file://` ou
`gopher://` produz erro de configuração, não requisição. URL com credencial
embutida é recusada em vez de higienizada.

**O endereço efetivamente discado é conferido no socket.** A verificação roda no
`Control` do dialer, depois da resolução e imediatamente antes do `connect(2)`.
É a única camada sem janela entre checar e conectar, e é onde DNS rebinding
morre em vez de virar discussão.

Recusado para toda integração, sem opt-out:

- link-local (`169.254.0.0/16`, `fe80::/10`) — a faixa de metadata de nuvem:
  `169.254.169.254` na AWS e no Azure, e o que `metadata.google.internal` resolve
  no GCP;
- endereço não especificado, multicast e broadcast.

**Não** recusado: RFC 1918, unique-local e loopback. Toda dependência do NChat é
um serviço de cluster, então uma regra "sem endereço privado" quebraria o
deployment real sem fazer nada contra a faixa que importa.

Além disso, em todas:

- **nenhum redirect é seguido.** Uma dependência que responde `302` é reportada
  como alcançável e nada mais;
- no OIDC, o `jwks_uri` que o provedor devolve precisa ter o mesmo scheme, host
  **e porta** do issuer. Dois serviços num host são duas origens;
- verificação TLS nunca é relaxada. Não existe `InsecureSkipVerify` no pacote e
  não existe configuração que possa introduzir um;
- corpo remoto é drenado ou lido sob teto de bytes e reduzido a campos
  conhecidos. O operador recebe `tls_error`, nunca uma cadeia de certificados,
  um hostname interno ou um stack trace.

## 5. O e-mail de teste

O destino **não é um parâmetro**. É o endereço da própria conta administrativa
autenticada, lido da sessão pelo servidor. Não existe campo de destinatário na
tela nem no corpo da requisição, então o pior que uma sessão roubada consegue é
mandar e-mail para a própria vítima. Essa decisão sozinha é todo o controle
anti-relay, e é por isso que não foi preciso inventar allowlist de destinos.

A mensagem é fixa, marcada `Auto-Submitted: auto-generated`, não carrega dado da
plataforma, exige confirmação explícita no console e é limitada a **um envio por
minuto por administrador**.

O relay usado é o mesmo do notification-service, com o mesmo `SMTP_TLS_MODE`. Se
o modo for `none` e houver usuário configurado, a etapa de credencial **falha**:
o `net/smtp` recusa PLAIN sobre conexão não cifrada e este código não contorna
isso. Um relay que só aceita senha em texto claro é um achado, não uma
configuração a acomodar.

## 6. Limites

| O quê                    | Orçamento                                          |
| ------------------------ | -------------------------------------------------- |
| Diagnóstico              | 6/min, burst 3, por **administrador × integração** |
| E-mail de teste          | 1/min, sem burst, por administrador                |
| Diagnósticos simultâneos | 2 por pod, recusado em vez de enfileirado          |
| Execução inteira         | teto de 20 s; cada etapa tem seu próprio timeout   |

Por administrador e integração, e não por IP: dois operadores investigando a
mesma falha não podem se limitar mutuamente, e um operador segurando o botão não
pode transformar o console em scanner. Estourar o orçamento é `429` com
`Retry-After`.

O diagnóstico é cancelado junto com a requisição que o pediu — o oposto da coleta
de health, que é compartilhada e por isso desacoplada. Sair da página interrompe
o trabalho de saída.

## 7. Auditoria

Todo diagnóstico e todo e-mail de teste geram uma linha, tenham sido bem
sucedidos, recusados ou falhado.

- ação: `admin.integration.diagnose` ou `admin.integration.smtp.test_email`;
- recurso: `admin.integration:<id>`;
- metadata, por allowlist: `integration`, `outcome` e `failed_stage` quando
  alguma falhou.

Nenhum alvo, resposta, credencial ou destinatário entra na trilha. O
destinatário em particular é redundante: a coluna de ator já identifica de quem
era a caixa de entrada.

## 8. Busca de configurações

A busca em `/configuration` indexa **somente metadata**: rótulo, descrição,
chave, seção, serviço responsável e nome da variável.

Nenhum valor é indexado, e isso é propriedade de segurança e não simplificação.
Uma busca que casasse em valores confirmaria um palpite: digite um token
suspeito e um acerto diz que ele está certo. Credenciais nunca chegam ao console
como valor, mas uma forma mascarada ou derivada vazaria do mesmo jeito.

Acentos são dobrados, então `configuracao` encontra `Configuração`. Todas as
palavras do termo precisam casar, em qualquer ordem. Os cartões de integração
linkam para `/configuration?q=<nome>`, que abre a tela com o termo já aplicado e
as seções recolhidas expandidas.

## 9. Diagnóstico rápido

| Sintoma no console                        | O que verificar                                                          |
| ----------------------------------------- | ------------------------------------------------------------------------ |
| Todas as etapas "não executada"           | O pod não recebe a variável que nomeia o endpoint. Cheque o ConfigMap.   |
| DNS falha                                 | O nome não resolve no cluster, ou resolve só para link-local.            |
| TLS falha com `tls_error`                 | Certificado fora da cadeia de confiança do pod. Não há como desligar.    |
| Issuer falha                              | O Keycloak declara um issuer diferente de `OIDC_ISSUER_URL`.             |
| JWKS falha por origem                     | O provedor aponta as chaves para outro host/porta. É recusado.           |
| Credencial falha no LiveKit               | `LIVEKIT_API_KEY`/`LIVEKIT_API_SECRET` não são os que o servidor aceita. |
| `429` no botão                            | Orçamento por administrador, ou o pod já roda dois diagnósticos.         |
| Botão desabilitado                        | Falta `admin.integrations.manage`.                                       |
| Configuração vazia com aviso de permissão | Falta `admin.config.read`, que é concedida separadamente.                |

## 10. Fora de escopo

- **Webhooks (#499).** Não existe implementação no repositório — nem migration,
  nem serviço, nem rota. A #582 não inventou o subsistema para preencher a tela;
  quando a #499 existir, o Admin Console reusa o modelo real dela.
- **Teste EICAR no ClamAV.** Não foi introduzido. Escrever um padrão
  reconhecidamente malicioso no daemon coloca um hit de assinatura no log de
  segurança do ambiente, e ensinar operador a ignorar essa linha é pior que não
  ter o teste.
- **Room de teste no LiveKit.** O diagnóstico lista salas com um token de
  `roomList` válido por 30 segundos. Não cria nada, então não há órfã para
  limpar.
- **Capacidade do storage.** O SeaweedFS expõe estatística de volume só no
  master, que este pod não recebe. Um número derivado de outra coisa seria
  inventado, e um operador planejaria em cima dele.
- **Editor de YAML, painel Kubernetes, terminal remoto, gestão de firewall.**
