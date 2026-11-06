#!/usr/bin/env python3
"""Opt-in filesystem qualification. Run on approved mounts on two distinct nodes.

Never discovers clusters or operates on the signature store. All writes are in a
new .nchat-rwx-<run> directory; no cleanup action or recursive deletion exists.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import time


def emit(event, **values):
    print(json.dumps(dict(event=event, **values)), flush=True)


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def locked(root, shared):
    descriptor = os.open(root / ".publisher.lock", os.O_RDONLY if shared else os.O_RDWR)
    try:
        fcntl.flock(descriptor, (fcntl.LOCK_SH if shared else fcntl.LOCK_EX) | fcntl.LOCK_NB)
    except OSError:
        os.close(descriptor)
        raise
    return os.fdopen(descriptor, "r" if shared else "r+")


def publish(root, sequence):
    name = "generation-" + str(sequence)
    candidate = root / ("candidate-" + str(sequence))
    candidate.mkdir()  # Never overwrite a prior attempt or another run.
    hashes = {}
    for part in ("main", "daily", "bytecode"):
        data = (str(sequence) + ":" + part).encode()
        with (candidate / part).open("xb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        hashes[part] = hashlib.sha256(data).hexdigest()
    sync_directory(candidate)
    candidate.rename(root / name)
    sync_directory(root)
    with (root / "state.next").open("x") as output:
        json.dump(dict(generation=name, hashes=hashes), output)
        output.flush()
        os.fsync(output.fileno())
    (root / "state.next").replace(root / "state.json")
    sync_directory(root)


def observe(root, seconds):
    state = json.loads((root / "state.json").read_text())
    selected = root / state["generation"]
    emit("snapshot_selected", generation=state["generation"])
    time.sleep(seconds)  # Hold SH across selection, copy/verification and acknowledgement.
    for part, expected in state["hashes"].items():
        if hashlib.sha256((selected / part).read_bytes()).hexdigest() != expected:
            raise RuntimeError("partial or inconsistent publication")
    emit("snapshot_verified", generation=state["generation"])


def prune(root):
    active = json.loads((root / "state.json").read_text())["generation"]
    for directory in root.glob("generation-*"):
        if directory.name == active:
            continue
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError("unexpected qualification generation")
        for part in ("main", "daily", "bytecode"):
            (directory / part).unlink()
        directory.rmdir()
    sync_directory(root)


def run(args):
    root = args.root / (".nchat-rwx-" + args.run)
    if args.action == "init":
        root.mkdir(mode=0o770)
        (root / ".publisher.lock").touch(mode=0o660, exist_ok=False)
        sync_directory(root)
        emit("initialized")
        return
    if root.is_symlink() or not root.is_dir():
        raise ValueError("unknown qualification directory")
    with locked(root, args.shared):
        emit("lock_acquired", shared=args.shared)
        if args.action == "hold":
            time.sleep(args.seconds)
        elif args.action == "publish":
            publish(root, args.sequence)
        elif args.action == "snapshot":
            observe(root, args.seconds)
        elif args.action == "prune":
            prune(root)
    emit("completed")


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--run", required=True)
    parser.add_argument("--action", choices=("init", "hold", "probe", "publish", "snapshot", "prune"), required=True)
    parser.add_argument("--shared", action="store_true")
    parser.add_argument("--seconds", type=float, default=0)
    parser.add_argument("--sequence", type=int, default=1)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{32}", args.run) or not 0 <= args.seconds <= 3600 or not 1 <= args.sequence <= 10000:
        parser.error("invalid qualification bounds")
    if args.action in ("publish", "prune", "init") and args.shared:
        parser.error("writes require an exclusive lock")
    if args.action == "snapshot" and not args.shared:
        parser.error("snapshot must exercise a shared reader")
    return args


if __name__ == "__main__":
    try:
        run(arguments())
    except BlockingIOError:
        emit("lock_conflict")
        raise SystemExit(2)
    except (OSError, ValueError, RuntimeError):
        emit("qualification_failed")
        raise SystemExit(1)
