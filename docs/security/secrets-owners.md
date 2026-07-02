# Secrets Owners

| Secret                        | Namespace     | Owner         | Rotation cadence             | Notes                                                                                                                                                 |
| ----------------------------- | ------------- | ------------- | ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| nchat-secrets                 | nchat         | Dev B / Infra | manual on change or incident | app shared dev/staging placeholder                                                                                                                    |
| nchat-staging-tls             | nchat-staging | Dev B / Infra | before expiry or incident    | staging TLS secret                                                                                                                                    |
| VALKEY_URL (in nchat-secrets) | nchat         | Dev B / Infra | manual on change or incident | Chat-service Valkey connection string (mention label cache + WS broadcast); may embed credentials, sourced from Sealed Secrets/vault, never hardcoded |
