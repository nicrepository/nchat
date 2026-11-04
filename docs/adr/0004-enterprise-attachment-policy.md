# ADR-0004 — Política versionada e inspeção de anexos empresariais

## Status

Proposed — implementação local da issue #1128, sujeita às revisões independentes.

## Contexto

Identificação por 512 bytes não separa Office/ZIP/CFBF nem valida dados textuais.
A ampliação precisa conservar quarentena, autorização, criptografia, original e
compatibilidade de persistência. O endpoint original também convertia áudio.

## Decisão

Usar catálogo tipado v1 no file-service e classificadores pequenos, com budgets
fixos. Inspecionar o objeto cifrado por leitura autenticada antes da finalização,
e revalidar a política no worker AV antes da liberação. Manter os estados atuais;
persistir versão/formato e proteger `clean` por constraint que recusa aprovação
de novos uploads por workers antigos. Não criar parser CFBF, DSL, nova fila ou
storage plaintext. Famílias sem inspeção suficiente continuam não liberadas.

O endpoint original conserva bytes e nome nativo; MP3 é representação explícita
em rota separada. Previews continuam objetos derivados cifrados independentes.
Não persistir SHA-256 de plaintext: binding/tamanho/chunks já são autenticados;
SHA-256 no corpus de integração prova fidelidade sem expor hashes corporativos.

## Consequências

Há uma leitura adicional limitada no upload e outra revalidação no scan.
Documentos ativos legítimos podem ser recusados pelo perfil passivo. ZIP tem
restrições conservadoras de nesting/metadata/folhas. Rollout exige expansão de
schema antes da escrita e baseline de leitores atualizado, especialmente para
o contrato de áudio. Flags novas começam desligadas.

Design, alternativas, riscos, matriz N/N+1 e testes estão em
[Anexos empresariais](../architecture/enterprise-attachment-policy.md).
Suporte efetivo e referências por produto SAP estão na
[matriz de formatos](../api/supported-file-types.md).
