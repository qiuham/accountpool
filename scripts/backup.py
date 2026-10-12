#!/usr/bin/env python3
"""Create a private, offline restore tree. Stop API/worker/browser before use."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
from contextlib import ExitStack


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def linked_copy():
    # Browser engines share large files through hard links; preserve that
    # relationship in the snapshot instead of expanding every engine copy.
    copied_inodes = {}
    def copy_linked(source_file, target_file):
        stat = os.stat(source_file)
        key = (stat.st_dev, stat.st_ino)
        if stat.st_nlink > 1 and key in copied_inodes:
            os.link(copied_inodes[key], target_file)
            return str(target_file)
        result = shutil.copy2(source_file, target_file)
        if stat.st_nlink > 1:
            copied_inodes[key] = target_file
        return result
    return copy_linked


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('backup', 'restore'))
    parser.add_argument('source', type=Path)
    parser.add_argument('destination', type=Path)
    args = parser.parse_args()
    source, destination = args.source.resolve(), args.destination.resolve()
    if destination.exists() or source == destination or source in destination.parents:
        parser.error('destination must be a new directory outside source')
    if args.action == 'restore':
        manifest = json.loads((source / 'manifest.json').read_text())
        for name, expected in manifest.items():
            path = (source / name).resolve()
            if source not in path.parents or not path.is_file() or digest(path) != expected:
                parser.error('backup integrity check failed')
        shutil.copytree(source, destination, symlinks=True, copy_function=linked_copy())
        (destination / 'manifest.json').unlink()
        destination.chmod(0o700)
        print('RESTORE_OK (data only; service not started)')
        return
    with ExitStack() as stack:
        # Locks exclude supported executor and worker writers. Other applications
        # must be stopped by the operator before making a consistent profile copy.
        for relative in ('artifacts/jobs/.executor.lock', 'backend/pool.db.worker.lock'):
            path = source / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            stream = stack.enter_context(path.open('a'))
            fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        destination.mkdir(mode=0o700, parents=True)
        (destination / 'backend').mkdir()
        with sqlite3.connect(f'file:{source / "backend/pool.db"}?mode=ro', uri=True) as src:
            with sqlite3.connect(destination / 'backend/pool.db') as dst:
                src.backup(dst)
                if dst.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                    raise RuntimeError('database integrity check failed')
        copy_linked = linked_copy()
        for relative in ('bundles', '.subsets', 'config.toml', '.env', 'artifacts/jobs/.last-finished'):
            path = source / relative
            if not path.exists():
                continue
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            if path.is_dir():
                shutil.copytree(path, target, symlinks=True, copy_function=copy_linked)
            else:
                shutil.copy2(path, target)
        manifest = {str(p.relative_to(destination)): digest(p)
                    for p in destination.rglob('*') if p.is_file() and not p.is_symlink()}
        (destination / 'manifest.json').write_text(json.dumps(manifest, indent=2))
    print('BACKUP_OK (database integrity: ok)')


if __name__ == '__main__':
    main()
