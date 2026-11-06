# Binary-only installer. clamd and FreshClam still run in the official pinned image.
FROM scratch
COPY --chmod=0500 --chown=100:101 clamav-signatures /clamav-signatures
USER 100:101
ENTRYPOINT ["/clamav-signatures"]
