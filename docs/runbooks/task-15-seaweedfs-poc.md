# TASK-15 — PoC SeaweedFS

## Status

Script criado, ambiente preparado com segundo volume server via profile `seaweed-replication`.

## Objetivo

Validar SeaweedFS para upload/download, integridade, latência e replicação básica no ambiente dev local.

## Ambiente

- Docker Compose local
- SeaweedFS master
- SeaweedFS volume (primário)
- SeaweedFS volume-2 via profile `seaweed-replication`
- SeaweedFS filer
- SeaweedFS S3 gateway

## Comando

```bash
make poc-seaweedfs
# ou
pnpm poc:seaweedfs
```

Para subir o segundo volume (replicação):

```bash
docker compose --env-file infra/compose/.env.dev \
  -f infra/compose/compose.dev.yml \
  --profile seaweed-replication \
  up -d seaweed-master seaweed-volume seaweed-volume-2 seaweed-filer seaweed-s3
```

## O que é validado

- master/filer respondem (HTTP smoke)
- upload pequeno via filer (`POST /poc/small.txt`)
- download pequeno via filer (`GET /poc/small.txt`)
- checksum SHA-256 pequeno
- upload grande controlado (default 10 MiB, override `SEAWEEDFS_POC_LARGE_MB`)
- download grande controlado
- checksum SHA-256 grande
- latência básica upload/download (ms)
- `dir/assign?replication=001` retorna fid com replicação solicitada
- `dir/lookup?volumeId=<vid>` valida número de locations (≥2 quando volume-2 disponível)

## Resultados

Os resultados são gerados em `poc-results/seaweedfs/` com:

- `<timestamp>-summary.md` — tabela de resultados, latências e limitações
- `<timestamp>-metrics.json` — métricas estruturadas

Os resultados **não são versionados** (gitignored).

## Critérios de aceite

- Upload/download pequeno passa
- Upload/download maior passa
- Checksums batem
- Latência é registrada
- Replicação básica comprovada via lookup (≥2 locations) quando volume-2 está ativo;
  ou marcada como `limited` com documentação explícita quando volume-2 não está rodando

## Variáveis de ambiente relevantes

| Variável                       | Default | Descrição                                              |
| ------------------------------ | ------- | ------------------------------------------------------ |
| `SEAWEEDFS_MASTER_HOST_PORT`   | `9333`  | Porta do master no host                                |
| `SEAWEEDFS_FILER_HOST_PORT`    | `8888`  | Porta do filer no host                                 |
| `SEAWEEDFS_VOLUME_2_HOST_PORT` | `8089`  | Porta do segundo volume no host                        |
| `SEAWEEDFS_REPLICATION`        | `000`   | Replicação do filer (use `001` para PoC com 2 volumes) |
| `SEAWEEDFS_POC_LARGE_MB`       | `10`    | Tamanho do arquivo grande em MiB                       |

> **Nota:** `SEAWEEDFS_REPLICATION=001` no filer exige que dois volume servers estejam
> registrados no master. Caso contrário, o filer pode rejeitar uploads. Na PoC, a
> replicação é validada diretamente via `dir/assign?replication=001` na API do master,
> independente da configuração do filer.

## Limitações

- Não é benchmark final de produção
- Não testa falha de nó completa (kill de volume server)
- Não testa backup/restore
- Não testa ClamAV
- Não testa preview de arquivos
- Não testa carga concorrente real
- replication=001 requer dois volume servers; se ausentes, resultado é `limited`
- Não integra com file-service ainda

## Próximos passos

1. Teste de falha de nó (kill seaweed-volume, verificar acesso via volume-2)
2. Backup/restore de volumes
3. Teste com arquivos maiores (100 MiB+)
4. Teste de concorrência (uploads paralelos)
5. Integração com file-service Go
6. Decisão final ao fim do Sprint 3

## Definition of Done

- [x] Script de PoC criado (`scripts/poc/seaweedfs-poc.sh`)
- [x] Compose suporta segundo volume (`seaweed-volume-2`, profile `seaweed-replication`)
- [ ] Upload/download validado (execução local)
- [ ] Checksums validados (execução local)
- [ ] Latência registrada (execução local)
- [ ] Replicação básica validada (execução local)
- [x] Runbook criado
- [ ] make ci passa
- [x] PR aberto
