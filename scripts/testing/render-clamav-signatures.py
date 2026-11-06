#!/usr/bin/env python3
"""Render a dormant nchat-dev candidate, never apply it or change the live overlay."""
import argparse
import ipaddress
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def render(args):
    if not re.fullmatch(r"[a-z0-9./_-]+@sha256:[a-f0-9]{64}", args.helper_image):
        raise ValueError("a reviewed helper registry digest is required")
    if not re.fullmatch(r"[a-z0-9.-]+", args.storage_class):
        raise ValueError("an explicit RWX StorageClass is required")
    if not 0 < args.max_age <= 31536000 or not re.fullmatch(r"[A-Za-z0-9_/#.-]+", args.approval):
        raise ValueError("explicit age and policy/synthetic approval reference required")
    if not re.fullmatch(r"https://[A-Za-z0-9.-]+(?::443)?(?:/[A-Za-z0-9_/-]*)?", args.mirror):
        raise ValueError("an approved private HTTPS mirror on 443 is required")
    network = ipaddress.ip_network(args.mirror_cidr, strict=True)
    if network.num_addresses != 1:
        raise ValueError("one approved mirror IP (/32 or /128) is required")
    values = {"HELPER_IMAGE": args.helper_image, "STORAGE_CLASS": args.storage_class,
              "MAX_AGE": str(args.max_age), "APPROVAL": args.approval,
              "MIRROR": args.mirror, "MIRROR_CIDR": str(network)}
    with tempfile.TemporaryDirectory(prefix="nchat-signatures-render-") as directory:
        tree = Path(directory)
        shutil.copytree(ROOT / "infra/k8s/base/services/clamav", tree / "base/services/clamav")
        candidate = tree / "candidates/clamav-signatures"
        candidate.mkdir(parents=True)
        for source in (ROOT / "infra/k8s/candidates/clamav-signatures").glob("*.yaml.in"):
            text = source.read_text()
            for name, value in values.items():
                text = text.replace("__" + name + "__", value)
            (candidate / source.name.removesuffix(".in")).write_text(text)
        subprocess.run(["kustomize", "build", str(candidate)], check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for option in ("helper-image", "storage-class", "approval", "mirror", "mirror-cidr"):
        parser.add_argument("--" + option, required=True)
    parser.add_argument("--max-age", required=True, type=int)
    render(parser.parse_args())
