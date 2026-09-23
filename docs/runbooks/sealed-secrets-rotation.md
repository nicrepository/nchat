# Sealed Secrets Rotation Runbook

## When to rotate

Rotate a secret when any of these events occurs:

- Security incident.
- Owner or responsible person changes.
- Certificate or credential expiration.
- Suspected exposure.
- Environment or cluster key changes.

## Manual rotation steps

1. Create or update the unsealed Secret manifest locally under `infra/k8s/secrets/unsealed/`.
2. Seal it with strict scope using `scripts/secrets/sealed-secrets-seal.sh`.
3. Commit only the generated SealedSecret under `infra/k8s/secrets/sealed/`.
4. Apply the SealedSecret with `kubectl` or a future Flux/ArgoCD flow.
5. Validate the generated Kubernetes Secret exists without printing its value.
6. Restart workloads that require the new value.
7. Record the rotation date, reason, and operator in the relevant ticket or change log.

## Pending: nchat-link-safety (RF-21)

Every supported overlay — `k3s-dev`, `k3s-staging` and `nchat-dev-server` —
enables `CHAT_LINK_SAFETY_ENABLED` and `FILE_LINK_SAFETY_ENABLED`, and both
services refuse to start without the credentials. So this Secret must be sealed
**before** the next deploy of any of them, or chat-service and file-service will
CrashLoopBackOff. That is the intended failure: enabled with
no checker would accept every link unchecked.

The template is versioned; the ciphertext is not, because sealing needs real
credentials for both providers.

### Adding the Google Web Risk key to an already-sealed Secret (issue #928)

`nchat-dev` already holds a valid `nchat-link-safety` SealedSecret with the four
Cloudflare keys. Issue #928 added a fifth,
`CHAT_LINK_SAFETY_GOOGLE_WEBRISK_API_KEY`, and **this repository cannot generate
its ciphertext**: sealing requires the plaintext key, which lives outside Git.
The committed SealedSecret is therefore unchanged and is missing that one key,
so chat-service will refuse to start with `CHAT_LINK_SAFETY_ENABLED=true` until
an operator performs the steps below. That refusal is the intended failure —
starting with only the fallback configured is the pre-#928 arrangement, in which
ordinary links stay permanently unverified.

`kubeseal` merges a single key into an existing SealedSecret without needing the
other four, so nothing already sealed has to be re-entered:

```
# WEBRISK_KEY is read into the environment out of band -- never as a shell
# argument, which would reach the history file and the process list.
read -rs WEBRISK_KEY

printf '%s' "$WEBRISK_KEY" | kubeseal \
  --raw \
  --namespace nchat-dev \
  --name nchat-link-safety \
  --scope strict \
  --controller-namespace kube-system \
  --controller-name sealed-secrets

unset WEBRISK_KEY
```

Paste the single-line ciphertext `--raw` prints into
`infra/k8s/secrets/sealed/nchat-dev/nchat-link-safety.yaml` under
`spec.encryptedData` as:

```
    CHAT_LINK_SAFETY_GOOGLE_WEBRISK_API_KEY: <ciphertext>
```

Keep the existing four entries exactly as they are. `--scope strict` is
required: the other entries were sealed with it, and a mixed-scope SealedSecret
fails to decrypt.

At Google, restrict the key to the Web Risk API only and to this deployment's
egress addresses before sealing it. An unrestricted key is spendable by anybody
who obtains it.

### Sealing the Secret from scratch (a new environment)

Steps 1-3 above, with:

```
cp infra/k8s/secrets/templates/nchat-link-safety.template.yaml \
   infra/k8s/secrets/unsealed/nchat-link-safety.yaml
# fill in the five values: the Google Web Risk key for chat-service, and the
# Cloudflare account id and token, which are the same in both the CHAT_* and
# FILE_* keys. Then:
scripts/secrets/sealed-secrets-seal.sh \
  infra/k8s/secrets/unsealed/nchat-link-safety.yaml \
  infra/k8s/secrets/sealed/nchat-dev/nchat-link-safety.yaml \
  nchat-dev
```

Add the generated file to `infra/k8s/secrets/sealed/nchat-dev/kustomization.yaml`
and delete the unsealed copy.

Note that only chat-service reads the Web Risk key. file-service names the two
`FILE_*` Cloudflare keys individually rather than mounting this Secret with
`envFrom`, so rotating the Google key never requires restarting it.

## Web Push: nchat-webpush (#862)

Web Push stays off until an operator does all of this for one environment.
Nothing in the repository enables it: the Deployment mounts `nchat-webpush` as
optional, and `NOTIFICATION_WORKER_ENABLED` defaults to `false`.

1. **Generate one VAPID pair for the environment, once.** Into the ignored
   unsealed copy, never to the terminal:

   ```
   cp infra/k8s/secrets/templates/nchat-webpush.template.yaml \
      infra/k8s/secrets/unsealed/nchat-webpush.yaml
   umask 077
   openssl ecparam -name prime256v1 -genkey -noout -out /tmp/vapid.pem
   PRIV="$(openssl ec -in /tmp/vapid.pem -outform DER 2>/dev/null | tail -c +8 | head -c 32 | base64 | tr '+/' '-_' | tr -d '=\n')"
   PUB="$(openssl ec -in /tmp/vapid.pem -pubout -outform DER 2>/dev/null | tail -c 65 | base64 | tr '+/' '-_' | tr -d '=\n')"
   sed -i "s|NOTIFICATION_VAPID_PUBLIC_KEY: \"\"|NOTIFICATION_VAPID_PUBLIC_KEY: \"$PUB\"|; s|NOTIFICATION_VAPID_PRIVATE_KEY: \"\"|NOTIFICATION_VAPID_PRIVATE_KEY: \"$PRIV\"|" \
     infra/k8s/secrets/unsealed/nchat-webpush.yaml
   shred -u /tmp/vapid.pem; unset PRIV PUB
   ```

   Fill `NOTIFICATION_VAPID_SUBJECT` with the operating team's `mailto:` or
   `https:` contact and set `metadata.namespace`. Seal with steps 1-3 above
   (`nchat-dev` for nchat-dev-server), add the file to the environment's sealed
   kustomization and delete the unsealed copy.

2. **Keep the pair.** Every browser subscription is bound to the public key.
   Redeploys and restarts keep it because it lives in the Secret; a new pair
   orphans every subscription until each browser visits again and re-subscribes.
   Rotate only on compromise, and never copy one environment's pair to another.

3. **Egress to the push services is versioned.** `nchat-default-deny-egress`
   covers notification-service; `nchat-allow-notification-webpush-egress` in
   `infra/k8s/components/least-privilege-network-policies` gives it TCP/443 to
   the public internet only, minus the private, cluster, link-local and reserved
   ranges, in every overlay that composes that component (nchat-dev-server and
   k3s-prod). `pnpm k8s:ci` validates its selector, direction, port and
   exclusions, and fails on any `0.0.0.0/0` outside the three authorised
   policies. Confirm it is applied before enabling the worker:
   `kubectl -n <namespace> get networkpolicy nchat-allow-notification-webpush-egress`.
   The sender refuses private destinations at dial time as well; the policy is
   the outer layer.

4. **Enable the worker.** `NOTIFICATION_WORKER_ENABLED: "true"` in the
   environment's ConfigMap, then restart notification-service. The outbox backlog
   written while the worker was off is not replayed as pushes: anything older than
   `NOTIFICATION_PUSH_TTL_SECONDS` (4h default) expires without a provider call.

5. **Verify without printing a key.**
   - notification-service `/readyz` is 200. An absent, malformed or mismatched
     pair fails it and names the variable, never the value.
   - Authenticated `GET /api/notifications/push/config` returns a non-null
     `vapid_public_key`. `null` means the worker is off or its configuration is
     not ready, and the browser reports "not configured". A `503
push_delivery_unavailable` means it is configured but not running — the
     same fact `/readyz` reports — and the browser offers to try again.
   - Perfil > Notificações shows "Notificações do navegador estão ativadas" after
     "Ativar" in a real browser.

Rollback: set `NOTIFICATION_WORKER_ENABLED` back to `false`. Browsers report
"not configured" on their next visit; subscriptions are kept, so re-enabling with
the same pair needs no re-subscription. Do not delete the Secret to roll back.

## Prohibitions

- Do not commit the original unsealed Secret.
- Do not commit private keys.
- Do not use `cluster-wide` scope without approved exception.
- Do not paste sensitive values into issues, PRs, docs, logs, terminals, or CI output.
