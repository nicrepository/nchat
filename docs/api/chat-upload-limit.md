# Chat Service — Limite de upload (RF-32)

> **Issue:** [#458](https://github.com/nicrepository/nchat/issues/458) — Limite
> de upload configuravel.
> **Scope:** o tamanho maximo de um anexo, configuravel por workspace.
> O enforcement vive no file-service; ver
> [file-attachments.md](./file-attachments.md).

Base URL resolvida em runtime via `VITE_CHAT_API_BASE_URL` (default:
`/api/chat`). O endpoint vive sob esse prefixo pelo mesmo motivo do anti-spam:
e o unico que os gateways encaminham para o chat-service.

---

## Endpoints

| Method | Path                                              | Auth                                           |
| ------ | ------------------------------------------------- | ---------------------------------------------- |
| GET    | `/api/chat/workspaces/{workspaceID}/upload-limit` | Bearer JWT + sessao ativa + admin do workspace |
| PATCH  | `/api/chat/workspaces/{workspaceID}/upload-limit` | Bearer JWT + sessao ativa + admin do workspace |

Os dois verbos exigem que o chamador seja **`owner` ou `admin` ativo do
workspace nomeado no path**. O ID do path nunca e confiado: o handler verifica a
membership e o `UPDATE` verifica de novo na mesma statement, entao um chamador
que administra outro workspace nao altera nada. Falta de papel e workspace de
outra pessoa recebem o mesmo `403`.

### Response body (os dois verbos)

```json
{
  "data": {
    "workspace_id": "…",
    "max_upload_bytes": 262144000,
    "min": 1048576,
    "max": 536870912
  }
}
```

`min` e `max` sao devolvidos para que o cliente valide e renderize contra os
limites do servidor em vez de reafirma-los.

### Request body (PATCH)

```json
{ "max_upload_bytes": 104857600 }
```

Apenas este campo e aceito. Sao rejeitados com `400`: campos desconhecidos, nao
inteiros, decimais, `null`, corpos acima de 64 KiB, valores fora de
`[min, max]` e **valores que nao sejam multiplos exatos de 1048576 bytes
(1 MiB)**.

**Nenhum valor invalido e corrigido, arredondado, truncado ou limitado
silenciosamente** — ele e recusado. `1572864` (1,5 MiB) nao vira 1 MiB nem
2 MiB: e um `400`.

### Status codes

| Code | Quando                                                            |
| ---- | ----------------------------------------------------------------- |
| 200  | Sucesso                                                           |
| 400  | Workspace ID malformado, corpo malformado, ou valor fora da faixa |
| 401  | Sem usuario autenticado                                           |
| 403  | Chamador nao administra este workspace                            |
| 404  | Workspace inexistente                                             |
| 500  | Falha de persistencia (nenhum detalhe interno no corpo)           |
| 503  | Workspace settings nao conectado                                  |

---

## Semantica

| Propriedade   | Valor                                                                                             |
| ------------- | ------------------------------------------------------------------------------------------------- |
| Escopo        | um limite por **workspace**, aplicado a anexos de canal e de DM                                   |
| Unidade       | bytes na API; editado e exibido em **MiB** inteiros (1 MiB = 1048576 bytes)                       |
| Default       | **262144000** (250 MiB) — o "250 MB" do RF-32                                                     |
| Minimo        | **1048576** (1 MiB) — nunca 0; um limite zero desabilitaria anexos por um controle de tamanho     |
| Maximo        | **536870912** (512 MiB) — nao existe valor "ilimitado"                                            |
| Granularidade | multiplo exato de 1048576 bytes; qualquer outro valor e rejeitado                                 |
| Storage       | `chat.workspaces.max_upload_bytes`, `NOT NULL`, `CHECK` de intervalo **e** de multiplo (`000020`) |

Este e o tamanho **maximo de um arquivo**, nao uma quota de armazenamento: nada
aqui limita quantos arquivos um workspace acumula.

### Por que somente MiB inteiros

A tela administrativa edita um numero inteiro de MiB. Um valor armazenado como
1572864 bytes nao poderia ser mostrado nesse campo sem ser alterado, e um
"salvar" comum passaria a gravar um limite que o administrador nunca editou.
Recusar o valor e a unica resposta nao destrutiva, entao a regra vive em
`uploadpolicy.Valid` e e repetida pelo `CHECK` da coluna.

Se um valor fora dessa regra existir na coluna, a tela entra em estado de
configuracao invalida: mostra o valor em bytes, nao renderiza o formulario e nao
oferece caminho de submit. A correcao e explicita, no banco -- nada e ajustado
automaticamente.

## Propagacao

Nao ha cache e nao ha invalidacao a coordenar.

O file-service le `max_upload_bytes` na **mesma consulta que autoriza o
destino** do upload, uma vez por request, antes do primeiro byte. Uma alteracao
administrativa vale para o proximo upload, sem restart e sem TTL. Um upload ja
em andamento termina sob a politica com que comecou — a politica e resolvida uma
unica vez, entao nao ha janela em que o limite mude no meio de uma transferencia.

O limite efetivo tambem e publicado em `GET /api/chat/sidebar`
(`workspace.max_upload_bytes`) para qualquer membro ativo, porque o cliente
precisa dele para avisar antes de iniciar um upload. E numero de politica, nao
capacidade: conhecer ou editar esse valor no cliente nao concede nada, o
file-service rele a coluna a cada upload.

## Limitacoes conhecidas

- **Sem trilha de auditoria administrativa.** O chat-service nao tem
  infraestrutura de auditoria admin e o RF-32 nao adicionou uma. Quem alterou o
  limite, e de quanto para quanto, nao e registrado. Mesma limitacao registrada
  para o RF-19.
- **O gateway aplica apenas um teto tecnico estatico** (536879104 bytes = 512 MiB
  - 8 KiB de overhead multipart), por meio de um upload guard nginx que limita o
    corpo enquanto o transmite. Ele nao conhece e nao aplica esta politica por
    workspace, que continua sendo aplicada exclusivamente pelo file-service. O
    middleware `buffering` do Traefik foi rejeitado por ler o corpo inteiro antes
    da autenticacao. Ver "Gateway" em [file-attachments.md](./file-attachments.md).
- **Uploads simultaneos sao limitados no cluster** por vagas em advisory locks do
  PostgreSQL, adquiridas depois da autorizacao e antes do primeiro byte. Isso e
  independente desta politica de tamanho; ver "Uploads simultaneos" em
  [file-attachments.md](./file-attachments.md).
