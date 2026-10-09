#!/usr/bin/env python3
"""Finite source-recipe mocks only, nice19: no network, child or native compile."""
import hashlib
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
import sys
from unittest import mock

sys.dont_write_bytecode = True

recipe = Path(__file__).resolve().parents[2] / 'images/git-wait/prepare-source.py'
spec = importlib.util.spec_from_file_location('git_wait_source_recipe', recipe)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SourceRecipeChecks(unittest.TestCase):
    def test_source_root_creates_missing_parent_but_refuses_existing_target(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-root-mock-') as directory:
            target=Path(directory)/'missing-parent/native'
            with mock.patch.object(sys,'argv',['recipe',str(target)]), \
                    mock.patch.object(module,'download',side_effect=RuntimeError('mock first download')) as download:
                with self.assertRaisesRegex(RuntimeError,'mock first download'):
                    module.main()
                self.assertTrue((target/'artifacts').is_dir())
                self.assertEqual(download.call_count,1)
                with self.assertRaises(FileExistsError):
                    module.main()
                self.assertEqual(download.call_count,1)

    def test_download_is_one_bounded_read_and_rejects_wrong_pin(self):
        reads = []
        calls = []

        class Stream(io.BytesIO):
            def read(self, size):
                reads.append(size)
                return super().read(size)

        def opened(url, timeout):
            calls.append((url, timeout))
            return Stream(b'abc')

        original = module.urllib.request.urlopen
        module.urllib.request.urlopen = opened
        try:
            with tempfile.TemporaryDirectory(prefix='git-wait-source-mock-') as directory:
                path = Path(directory) / 'artifact'
                with self.assertRaises(ValueError):
                    module.download('https://example.invalid/source', path, 2)
                self.assertFalse(path.exists())
                with self.assertRaises(ValueError):
                    module.download('https://example.invalid/source', path, 3, '0' * 64)
                self.assertFalse(path.exists())
                with self.assertRaises(ValueError):
                    module.download('https://example.invalid/source', path, 3, expected_size=2)
                self.assertFalse(path.exists())
                self.assertEqual(module.download('https://example.invalid/source', path, 3,
                                                hashlib.sha256(b'abc').hexdigest(), 3), b'abc')
        finally:
            module.urllib.request.urlopen = original
        self.assertEqual(reads, [3, 4, 4, 4])
        self.assertEqual(len(calls), 4)  # One request per independent fixture, no retry.
        self.assertTrue(all(timeout == 30 for _, timeout in calls))

    def test_dsc_checksum_set_is_exact_and_bounded_by_pinned_artifacts(self):
        body = b'Version: 1:2.39.5-0+deb12u3\nChecksums-Sha256:\n ' + b'a' * 64 + \
            b' 12 git_2.39.5.orig.tar.xz\n ' + b'b' * 64 + b' 3 git.debian.tar.xz\nFiles:\n'
        parsed = module.checksums(body)
        self.assertEqual(parsed['git_2.39.5.orig.tar.xz'], ('a' * 64, 12))
        with self.assertRaises(ValueError):
            module.checksums(body.replace(b'git.debian.tar.xz', b'../escape'))
        with self.assertRaises(ValueError):
            module.checksums(body.replace(b'git.debian.tar.xz', b'git_2.39.5.orig.tar.xz'))
        with self.assertRaises(ValueError):
            module.checksums(body.replace(b'Checksums-Sha256:', b'Absent:'))

    def test_archive_refuses_escape_and_special_files(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-archive-mock-') as directory:
            root = Path(directory)
            for index, (name, kind, link) in enumerate([
                    ('../escape', tarfile.REGTYPE, ''),
                    ('root/link', tarfile.SYMTYPE, '../../escape'),
                    ('root/device', tarfile.CHRTYPE, '')]):
                archive = root / (str(index) + '.tar')
                with tarfile.open(archive, 'w') as stream:
                    entry = tarfile.TarInfo(name)
                    entry.type, entry.linkname = kind, link
                    stream.addfile(entry)
                output = root / ('out' + str(index))
                output.mkdir()
                with self.assertRaises(ValueError):
                    module.extract(archive, output)
                self.assertEqual(list(output.iterdir()), [])
            archive = root / 'valid.tar'
            with tarfile.open(archive, 'w') as stream:
                entry = tarfile.TarInfo('root/tiny')
                entry.size = 1
                stream.addfile(entry, io.BytesIO(b'x'))
            output = root / 'valid'
            output.mkdir()
            module.extract(archive, output)
            self.assertEqual((output / 'root/tiny').read_bytes(), b'x')


if __name__ == '__main__':
    unittest.main()
