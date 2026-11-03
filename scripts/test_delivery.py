import base64
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import update_homebrew as brew
import release_artifacts as release


class DeliveryTests(unittest.TestCase):
    def test_formula_uses_verified_platform_hashes(self):
        hashes = {f'fbhook_1.2.3_{system}_{arch}.tar.gz': 'a' * 64
                  for system in ('darwin', 'linux') for arch in ('amd64', 'arm64')}
        formula = brew.formula('darron/fbhook', 'v1.2.3', hashes, 'b' * 40)
        self.assertIn('class Fbhook < Formula', formula)
        self.assertIn('bin.install "fbhook"', formula)
        self.assertIn('license "MIT"', formula)
        self.assertIn('skip_clean "bin/fbhook"', formula)
        self.assertIn('fbhook #{version} sha=' + 'b' * 40 + ' clean', formula)
        self.assertEqual(formula.count('sha256 "' + 'a' * 64 + '"'), 4)
        with self.assertRaises(ValueError):
            brew.formula('darron/fbhook', 'v1.2.3', {}, 'b' * 40)

    def test_update_refuses_downgrade_from_generated_formula_without_writing(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            remote = root / 'remote'
            remote.mkdir()
            tag = 'v1.2.2'
            for os_name in ('darwin', 'linux'):
                for arch in ('amd64', 'arm64'):
                    archive = remote / release.archive_name(tag, os_name, arch)
                    archive.write_bytes(f'{os_name}-{arch}'.encode())
            release.write_checksums(remote, tag)
            old_hashes = {release.archive_name('v1.2.3', os_name, arch): 'b' * 64
                          for os_name in ('darwin', 'linux') for arch in ('amd64', 'arm64')}
            existing = brew.formula('darron/fbhook', 'v1.2.3', old_hashes, 'c' * 40)
            writes = []

            def command(args, **_):
                if args[1:3] == ['release', 'view']:
                    return json.dumps(dict(tagName=tag, isDraft=False, isPrerelease=False))
                if args[1] == 'api':
                    return 'a' * 40
                raise AssertionError(args)

            def run(args, **kwargs):
                if args[1:3] == ['release', 'download']:
                    for path in remote.iterdir():
                        shutil.copyfile(path, Path(args[args.index('--dir') + 1]) / path.name)
                    return subprocess.CompletedProcess(args, 0)
                if args[:2] == ['gh', 'api'] and '--method' not in args:
                    return subprocess.CompletedProcess(
                        args, 0,
                        stdout=json.dumps({'content': base64.b64encode(existing.encode()).decode(), 'sha': 'old'}),
                        stderr='')
                if '--method' in args:
                    writes.append(args)
                    raise AssertionError('downgrade attempted a tap write')
                raise AssertionError(args)

            with patch.object(brew.release, 'command', side_effect=command), \
                    patch.object(brew.subprocess, 'run', side_effect=run):
                with self.assertRaises(ValueError):
                    brew.update('darron/fbhook', tag, 'darron/homebrew-tap')
            self.assertEqual(writes, [])


if __name__ == '__main__':
    unittest.main()
