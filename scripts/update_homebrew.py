#!/usr/bin/env python3
"""Verify published assets and update the tap with an optimistic GitHub write."""
import argparse
import base64
import json
import os
import re
import subprocess
import tempfile
from pathlib import Path
import release_artifacts as release

VERSION = re.compile(r'^  version "(\d+\.\d+\.\d+)"$', re.MULTILINE)
RELEASE_URL = re.compile(
    r'^      url "https://github\.com/[^/]+/[^/]+/releases/download/'
    r'(v\d+\.\d+\.\d+)/fbhook_(\d+\.\d+\.\d+)_(darwin|linux)_(amd64|arm64)\.tar\.gz"$',
    re.MULTILINE)


def existing_formula_tag(text):
    """Return one safe stable version from legacy metadata or all release URLs."""
    versions = VERSION.findall(text)
    urls = RELEASE_URL.findall(text)
    if urls:
        if any(tag[1:] != filename_version for tag, filename_version, _, _ in urls):
            raise ValueError('formula release URL version does not match filename')
        tags = {tag for tag, filename_version, _, _ in urls if tag[1:] == filename_version}
        platforms = {(os_name, arch) for _, _, os_name, arch in urls}
        if (len(urls) != 4 or len(tags) != 1
                or platforms != {('darwin', 'amd64'), ('darwin', 'arm64'),
                                 ('linux', 'amd64'), ('linux', 'arm64')}):
            raise ValueError('unrecognized release formula version')
        tag = tags.pop()
        release.validate_metadata(tag, '0' * 40)
        if versions != [] and versions != [tag[1:]]:
            raise ValueError('formula has mixed versions')
        return tag
    if len(versions) != 1:
        raise ValueError('formula has no unambiguous version')
    tag = 'v' + versions[0]
    release.validate_metadata(tag, '0' * 40)
    return tag


def formula(repo, tag, hashes, revision):
    release.validate_metadata(tag, revision)
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repo):
        raise ValueError('invalid repository')
    lines = ['class Fbhook < Formula', '  desc "Shared feedback intake and issue delivery"',
             f'  homepage "https://github.com/{repo}"', '  license "MIT"']
    for platform, ruby_platform in (('darwin', 'macos'), ('linux', 'linux')):
        lines.append(f'  on_{ruby_platform} do')
        for arch, ruby_arch in (('arm64', 'arm'), ('amd64', 'intel')):
            name = release.archive_name(tag, platform, arch)
            digest = hashes.get(name, '')
            if not re.fullmatch(r'[a-f0-9]{64}', digest):
                raise ValueError('missing verified archive checksum')
            lines.extend([f'    on_{ruby_arch} do',
                          f'      url "https://github.com/{repo}/releases/download/{tag}/{name}"',
                          f'      sha256 "{digest}"', '    end'])
        lines.append('  end')
    lines.extend(['', '  # Preserve the exact executable published on GitHub.',
                  '  skip_clean "bin/fbhook"', '', '  def install', '    bin.install "fbhook"',
                  '    prefix.install "THIRD_PARTY_NOTICES.txt"', '  end', '  test do',
                  f'    assert_equal "fbhook #{{version}} sha={revision} clean\\n", shell_output("#{{bin}}/fbhook version")',
                  '    assert_match "shared feedback", shell_output("#{bin}/fbhook help")',
                  '    assert_path_exists prefix/"THIRD_PARTY_NOTICES.txt"',
                  '  end', 'end', ''])
    return '\n'.join(lines)


def update(repo, tag, tap):
    release.validate_metadata(tag, '0' * 40)
    for name in (repo, tap):
        if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', name):
            raise ValueError('invalid repository')
    metadata = json.loads(release.command(['gh', 'release', 'view', tag, '--repo', repo,
                                         '--json', 'tagName,isDraft,isPrerelease']))
    if metadata != dict(tagName=tag, isDraft=False, isPrerelease=False):
        raise ValueError('published stable release required')
    revision = release.command(['gh', 'api', f'repos/{repo}/commits/{tag}', '--jq', '.sha'])
    release.validate_metadata(tag, revision)
    with tempfile.TemporaryDirectory() as tmp:
        directory = Path(tmp)
        subprocess.run(['gh', 'release', 'download', tag, '--repo', repo, '--pattern', 'fbhook_*',
                        '--pattern', 'SHA256SUMS', '--dir', tmp], check=True)
        release.verify_bundle(directory, tag)
        hashes = {p.name: release.hashlib.sha256(p.read_bytes()).hexdigest()
                  for p in release.archive_paths(directory, tag)}
    content = formula(repo, tag, hashes, revision)
    env = dict(os.environ)
    if os.environ.get('HOMEBREW_TAP_TOKEN'):
        env['GH_TOKEN'] = os.environ['HOMEBREW_TAP_TOKEN']
    endpoint = f'repos/{tap}/contents/Formula/fbhook.rb'
    result = subprocess.run(['gh', 'api', endpoint], env=env, text=True, capture_output=True)
    sha = None
    if result.returncode == 0:
        current = json.loads(result.stdout)
        text = base64.b64decode(current['content']).decode()
        if text == content:
            print('Homebrew formula already identical.')
            return
        previous = existing_formula_tag(text)
        if tuple(map(int, previous[1:].split('.'))) > tuple(map(int, tag[1:].split('.'))):
            raise ValueError('refusing to downgrade the existing formula')
        sha = current['sha']
    elif '404' not in result.stderr:
        raise RuntimeError('cannot read existing Homebrew formula')
    payload = {'message': f'Update fbhook to {tag}',
               'content': base64.b64encode(content.encode()).decode()}
    if sha:
        payload['sha'] = sha
    subprocess.run(['gh', 'api', '--method', 'PUT', endpoint, '--input', '-'],
                   env=env, input=json.dumps(payload), text=True, check=True, stdout=subprocess.DEVNULL)
    print('Homebrew formula updated with verified release hashes.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', required=True)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--tap', default='darron/homebrew-tap')
    args = parser.parse_args()
    update(args.repo, args.tag, args.tap)
