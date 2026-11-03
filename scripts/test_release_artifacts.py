import hashlib
import json
import shutil
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import release_artifacts as release


class ReleaseArtifactsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.output = self.root / "dist"
        self.output.mkdir()
        self.binary = self.root / "fbhook"
        self.binary.write_bytes(b"test binary\x00\xff")
        self.license = self.root / "README.md"
        self.license.write_text("MIT fixture\n")

    def bundle(self):
        for os_name in ("darwin", "linux"):
            for arch in ("amd64", "arm64"):
                release.package(self.binary, self.license, self.output, "v0.9.0", os_name, arch)
        release.write_checksums(self.output, "v0.9.0")

    def test_archives_contain_only_executable_cli_and_license(self):
        self.bundle()
        archive = self.output / "fbhook_0.9.0_linux_arm64.tar.gz"
        with tarfile.open(archive) as source:
            self.assertEqual(source.getnames(), ["fbhook", "README.md"])
            self.assertEqual(source.extractfile("fbhook").read(), b"test binary\x00\xff")
            self.assertEqual(source.getmember("fbhook").mode, 0o755)
            self.assertEqual(source.getmember("README.md").mode, 0o644)
        files = release.verify_bundle(self.output, "v0.9.0")
        self.assertEqual(len(files), 9)
        expected = hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n"
        self.assertEqual(archive.with_name(archive.name + ".sha256").read_text(), expected)
        self.assertIn(expected, (self.output / "SHA256SUMS").read_text())

    def test_packaging_is_repeatable(self):
        self.bundle()
        first = {p.name: p.read_bytes() for p in self.output.iterdir()}
        self.bundle()
        self.assertEqual({p.name: p.read_bytes() for p in self.output.iterdir()}, first)

    def test_refuses_tampered_missing_extra_and_symlink_assets(self):
        for mode in ("archive", "individual", "combined", "missing", "extra", "symlink"):
            with self.subTest(mode=mode):
                self.bundle()
                archive = self.output / "fbhook_0.9.0_linux_arm64.tar.gz"
                if mode == "archive":
                    archive.write_bytes(b"changed")
                elif mode == "individual":
                    archive.with_name(archive.name + ".sha256").write_text("wrong")
                elif mode == "combined":
                    (self.output / "SHA256SUMS").write_text("wrong")
                elif mode == "missing":
                    archive.unlink()
                elif mode == "extra":
                    (self.output / "unwanted").write_text("extra")
                else:
                    archive.unlink()
                    archive.symlink_to(self.binary)
                with self.assertRaises(ValueError):
                    release.verify_bundle(self.output, "v0.9.0")
                for path in self.output.iterdir():
                    path.unlink()

    def test_rejects_unsafe_metadata_and_unsupported_targets(self):
        for tag in ("0.9.0", "v01.0.0", "v0.9.0-rc1", "../../main", "v0.9.0;pwd", "v0.9.0\n"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.validate_metadata(tag, "a" * 40)
        for revision in ("a" * 39, "A" * 40, "z" * 40, "a" * 40 + " -X main.other=x"):
            with self.subTest(revision=revision), self.assertRaises(ValueError):
                release.validate_metadata("v0.9.0", revision)
        with self.assertRaises(ValueError):
            release.package(self.binary, self.license, self.output, "v0.9.0", "windows", "amd64")
        self.assertEqual(list(self.output.iterdir()), [])

    def test_source_version_must_match_tag(self):
        (self.root / "feedback.go").write_text('package opentunnel\nconst Version = "0.8.0"\n')
        with self.assertRaises(ValueError):
            release.validate_source(self.root, "v0.9.0")
        (self.root / "feedback.go").write_text('package opentunnel\nconst Version = "0.9.0"\n')
        release.validate_source(self.root, "v0.9.0")

    def test_retry_uploads_only_missing_identical_assets(self):
        self.bundle()
        files = release.verify_bundle(self.output, "v0.9.0")
        existing = self.root / "existing"
        existing.mkdir()
        (existing / files[0].name).write_bytes(files[0].read_bytes())
        pending = release.missing_assets(files, existing)
        self.assertEqual(len(pending), 8)
        self.assertNotIn(files[0], pending)
        (existing / files[0].name).write_bytes(b"different published bytes")
        with self.assertRaises(ValueError):
            release.missing_assets(files, existing)

    def publish_fixture(self, details, revision="a" * 40, remote=None, allow_upload=True):
        """Replace only the GitHub CLI boundary; use real bundles and downloads."""
        calls = []

        def query(args, **kwargs):
            calls.append(args)
            if args[1:3] == ["release", "view"]:
                return json.dumps(details)
            if args[1] == "api":
                return revision
            raise AssertionError("unexpected GitHub query: " + repr(args))

        def mutation(args, **kwargs):
            calls.append(args)
            if args[1:3] == ["release", "download"]:
                name = args[args.index("--pattern") + 1]
                destination = Path(args[args.index("--dir") + 1]) / name
                shutil.copyfile(remote / name, destination)
            elif args[1:3] == ["release", "upload"]:
                if not allow_upload:
                    raise AssertionError("unsafe upload before rejection")
            else:
                raise AssertionError("unexpected GitHub mutation: " + repr(args))

        with patch.object(release, "command", side_effect=query), \
                patch.object(release.subprocess, "run", side_effect=mutation):
            release.publish(self.output, "v0.9.0", "a" * 40, "owner/repository")
        return calls

    def test_publisher_downloads_existing_assets_and_uploads_only_missing_without_clobber(self):
        self.bundle()
        files = release.verify_bundle(self.output, "v0.9.0")
        remote = self.root / "remote"
        remote.mkdir()
        for path in files[:2]:
            shutil.copyfile(path, remote / path.name)
        details = dict(tagName="v0.9.0", isDraft=False, isPrerelease=False,
                       assets=[dict(name=path.name) for path in files[:2]] + [dict(name="unrelated.txt")])
        calls = self.publish_fixture(details, remote=remote)
        downloads = [args for args in calls if args[1:3] == ["release", "download"]]
        self.assertEqual({args[args.index("--pattern") + 1] for args in downloads},
                         {path.name for path in files[:2]})
        uploads = [args for args in calls if args[1:3] == ["release", "upload"]]
        self.assertEqual(len(uploads), 1)
        self.assertNotIn("--clobber", uploads[0])
        self.assertEqual(uploads[0][6:], [str(path) for path in files[2:]])
        for path in files[2:]:
            shutil.copyfile(path, remote / path.name)
        details["assets"] = [dict(name=path.name) for path in files]
        calls = self.publish_fixture(details, remote=remote)
        self.assertFalse(any(args[1:3] == ["release", "upload"] for args in calls))

    def test_publisher_refuses_draft_prerelease_mismatched_tag_and_moved_revision(self):
        self.bundle()
        stable = dict(tagName="v0.9.0", isDraft=False, isPrerelease=False, assets=[])
        for change, revision in ((dict(isDraft=True), "a" * 40),
                                 (dict(isPrerelease=True), "a" * 40),
                                 (dict(tagName="v0.8.0"), "a" * 40),
                                 ({}, "b" * 40)):
            with self.subTest(change=change, revision=revision), self.assertRaises(ValueError):
                self.publish_fixture(dict(stable, **change), revision=revision, allow_upload=False)

    def test_publisher_refuses_conflicting_asset_before_any_upload(self):
        self.bundle()
        remote = self.root / "remote"
        remote.mkdir()
        name = "fbhook_0.9.0_linux_arm64.tar.gz"
        (remote / name).write_bytes(b"different published bytes")
        details = dict(tagName="v0.9.0", isDraft=False, isPrerelease=False,
                       assets=[dict(name=name)])
        with self.assertRaises(ValueError):
            self.publish_fixture(details, remote=remote, allow_upload=False)


if __name__ == "__main__":
    unittest.main()
