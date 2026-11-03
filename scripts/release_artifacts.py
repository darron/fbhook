#!/usr/bin/env python3
"""Build CLI-only stable release archives and publish without replacing assets."""

import argparse
import gzip
import hashlib
import io
import json
import os
import platform
import re
import subprocess
import tarfile
import tempfile
from pathlib import Path

TARGETS = (("darwin", "amd64"), ("darwin", "arm64"), ("linux", "amd64"), ("linux", "arm64"))
TAG = r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"


def validate_metadata(tag, revision):
    if not re.fullmatch(TAG, tag):
        raise ValueError("release must be a stable vMAJOR.MINOR.PATCH tag")
    if not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise ValueError("release revision must be a full lowercase Git SHA")


def validate_source(source, tag):
    matches = re.findall(r'^const Version = "([^"\n]+)"$', (source / "feedback.go").read_text(), re.MULTILINE)
    if matches != [tag[1:]]:
        raise ValueError("source Version does not match release tag")


def archive_name(tag, os_name, arch):
    validate_metadata(tag, "0" * 40)
    if (os_name, arch) not in TARGETS:
        raise ValueError("unsupported release target")
    return "fbhook_{}_{}_{}.tar.gz".format(tag[1:], os_name, arch)


def package(binary, license_path, output, tag, os_name, arch):
    destination = output / archive_name(tag, os_name, arch)
    # Fixed metadata avoids changing archives solely because of build time or uid.
    with destination.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as archive:
            for name, path, mode in (("fbhook", binary, 0o755), ("README.md", license_path, 0o644)):
                data = path.read_bytes()
                info = tarfile.TarInfo(name)
                info.size, info.mode = len(data), mode
                archive.addfile(info, io.BytesIO(data))


def checksum_line(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.name + "\n"


def archive_paths(output, tag):
    return sorted(output / archive_name(tag, os_name, arch) for os_name, arch in TARGETS)


def write_checksums(output, tag):
    lines = []
    for path in archive_paths(output, tag):
        line = checksum_line(path)
        path.with_name(path.name + ".sha256").write_text(line)
        lines.append(line)
    (output / "SHA256SUMS").write_text("".join(lines))


def verify_bundle(output, tag):
    archives = archive_paths(output, tag)
    files = archives + [p.with_name(p.name + ".sha256") for p in archives] + [output / "SHA256SUMS"]
    if set(output.iterdir()) != set(files) or any(p.is_symlink() or not p.is_file() for p in files):
        raise ValueError("release bundle has missing, extra or unsafe files")
    lines = [checksum_line(p) for p in archives]
    if (output / "SHA256SUMS").read_text() != "".join(lines):
        raise ValueError("combined release checksums do not match")
    for path, line in zip(archives, lines):
        if path.with_name(path.name + ".sha256").read_text() != line:
            raise ValueError("individual release checksum does not match")
    return files


def missing_assets(files, existing):
    pending = []
    for path in files:
        remote = existing / path.name
        if not remote.exists():
            pending.append(path)
        elif remote.is_symlink() or not remote.is_file() or remote.read_bytes() != path.read_bytes():
            raise ValueError("published asset differs; refusing to replace " + path.name)
    return pending


def command(args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def build(source, output, tag, revision):
    validate_metadata(tag, revision)
    source, output = source.resolve(), output.resolve()
    validate_source(source, tag)
    if command(["git", "rev-parse", "HEAD"], cwd=source) != revision:
        raise ValueError("source checkout does not match release revision")
    if command(["git", "status", "--porcelain"], cwd=source):
        raise ValueError("release source checkout must be clean")
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory() as tmp:
        binary = Path(tmp) / "fbhook"
        for os_name, arch in TARGETS:
            env = dict(os.environ, CGO_ENABLED="0", GOOS=os_name, GOARCH=arch)
            subprocess.run([
                "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags",
                "-s -w -X main.buildRevision={} -X main.buildState=clean".format(revision),
                "-o", str(binary), "./cmd/fbhook",
            ], cwd=source, env=env, check=True)
            metadata = command(["go", "version", "-m", str(binary)])
            for setting in ("GOOS=" + os_name, "GOARCH=" + arch, "CGO_ENABLED=0"):
                if setting not in metadata.split():
                    raise ValueError("binary build metadata lacks " + setting)
            native_os = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
            native_arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
            if (os_name, arch) == (native_os, native_arch):
                if command([str(binary), "version"]) != "fbhook {} sha={} clean".format(tag[1:], revision):
                    raise ValueError("built CLI version metadata does not match release")
                subprocess.run([str(binary), "help"], check=True, stdout=subprocess.DEVNULL)
            package(binary, source / "README.md", output, tag, os_name, arch)
            print("Built " + archive_name(tag, os_name, arch), flush=True)
    write_checksums(output, tag)
    verify_bundle(output, tag)


def publish(output, tag, revision, repository):
    validate_metadata(tag, revision)
    files = verify_bundle(output, tag)
    details = json.loads(command(["gh", "release", "view", tag, "--repo", repository,
                                  "--json", "tagName,isDraft,isPrerelease,assets"]))
    if details["tagName"] != tag or details["isDraft"] or details["isPrerelease"]:
        raise ValueError("release must be published and stable")
    actual = command(["gh", "api", "repos/{}/commits/{}".format(repository, tag), "--jq", ".sha"])
    if actual != revision:
        raise ValueError("release tag moved after building")
    names = {asset["name"] for asset in details["assets"]}
    with tempfile.TemporaryDirectory() as tmp:
        existing = Path(tmp)
        for path in files:
            if path.name in names:
                subprocess.run(["gh", "release", "download", tag, "--repo", repository,
                                "--pattern", path.name, "--dir", str(existing)], check=True)
        pending = missing_assets(files, existing)
        if pending:
            subprocess.run(["gh", "release", "upload", tag, "--repo", repository,
                            *[str(path) for path in pending]], check=True)
        print("Published {} assets; {} already identical".format(len(pending), len(files) - len(pending)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("build", "verify", "publish"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source", type=Path)
    parser.add_argument("--repo")
    args = parser.parse_args()
    validate_metadata(args.tag, args.revision)
    if args.operation == "build":
        if args.source is None:
            parser.error("build requires --source")
        build(args.source, args.output, args.tag, args.revision)
    elif args.operation == "publish":
        if not args.repo or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo):
            parser.error("publish requires --repo owner/repository")
        publish(args.output, args.tag, args.revision, args.repo)
    else:
        verify_bundle(args.output, args.tag)


if __name__ == "__main__":
    main()
