#!/usr/bin/env python3
"""Finite source-recipe mocks only, nice19: no network, child or native compile."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import sys
import re
from unittest import mock

sys.dont_write_bytecode = True

recipe = Path(__file__).resolve().parents[2] / 'images/git-wait/prepare-source.py'
spec = importlib.util.spec_from_file_location('git_wait_source_recipe', recipe)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
build_spec = importlib.util.spec_from_file_location('git_wait_build_recipe',recipe.with_name('build.py'))
build_module = importlib.util.module_from_spec(build_spec)
build_spec.loader.exec_module(build_module)


class SourceRecipeChecks(unittest.TestCase):
    def test_actual_docker_base_arg_binds_both_stages_and_workflows_never_override(self):
        dockerfile=recipe.with_name('Dockerfile').read_text()
        defaults=re.findall(r'^ARG BASE_IMAGE=(.*)$',dockerfile,re.MULTILINE)
        self.assertEqual(defaults,[module.LOCK['baseImage']])
        stages=re.findall(r'^FROM ([^\n]+)$',dockerfile,re.MULTILINE)
        self.assertEqual(stages,['${BASE_IMAGE} AS builder','${BASE_IMAGE}'])
        builder=dockerfile.split('FROM ${BASE_IMAGE} AS builder',1)[1].split('FROM ${BASE_IMAGE}',1)[0]
        self.assertIn('\nARG BASE_IMAGE\n',builder)
        self.assertEqual(builder.count('env BASE_IMAGE="${BASE_IMAGE}" nice'),2)
        workflows=recipe.parents[2]/'.github/workflows'
        for name in ['ci.yml','publish-git-wait.yml']:
            self.assertNotIn('BASE_IMAGE=',(workflows/name).read_text())

    def test_changed_or_missing_actual_base_refuses_before_download_or_compile(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-base-refusal-') as directory:
            root=Path(directory)
            for actual in ['',module.LOCK['baseImage'].replace(':2.9.1@',':unreviewed@')]:
                with mock.patch.dict(module.os.environ,BASE_IMAGE=actual), \
                        mock.patch.object(sys,'argv',['recipe',str(root/'source')]), \
                        mock.patch.object(module,'download') as download:
                    with self.assertRaisesRegex(ValueError,'actual build base'):
                        module.main()
                    download.assert_not_called()
                    self.assertFalse((root/'source').exists())
                with mock.patch.dict(build_module.os.environ,BASE_IMAGE=actual), \
                        mock.patch.object(sys,'argv',['build',str(root/'source'),str(root/'out')]), \
                        mock.patch.object(build_module,'run') as compile_call:
                    with self.assertRaisesRegex(ValueError,'actual build base'):
                        build_module.main()
                    compile_call.assert_not_called()
                    self.assertFalse((root/'out').exists())

    def test_exact_actual_base_reaches_only_mocked_first_build_call(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-base-admission-') as directory:
            root=Path(directory)
            (root/'source-provenance.json').write_text(json.dumps({'baseImage':module.LOCK['baseImage']}))
            with mock.patch.dict(build_module.os.environ,BASE_IMAGE=module.LOCK['baseImage'],COMMIT='a'*40), \
                    mock.patch.object(sys,'argv',['build',str(root),str(root/'out')]), \
                    mock.patch.object(build_module,'run',side_effect=RuntimeError('mock first make')) as compile_call:
                with self.assertRaisesRegex(RuntimeError,'mock first make'):
                    build_module.main()
                self.assertEqual(compile_call.call_count,1)

    def test_prepared_provenance_mismatch_refuses_before_build(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-prepared-base-refusal-') as directory:
            root=Path(directory)
            (root/'source-provenance.json').write_text(json.dumps({'baseImage':'unverified'}))
            with mock.patch.dict(build_module.os.environ,BASE_IMAGE=module.LOCK['baseImage'],COMMIT='a'*40), \
                    mock.patch.object(sys,'argv',['build',str(root),str(root/'out')]), \
                    mock.patch.object(build_module,'run') as compile_call:
                with self.assertRaisesRegex(ValueError,'prepared source provenance'):
                    build_module.main()
                compile_call.assert_not_called()
                self.assertFalse((root/'out').exists())

    def test_source_root_creates_missing_parent_but_refuses_existing_target(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-root-mock-') as directory:
            target=Path(directory)/'missing-parent/native'
            with mock.patch.dict(module.os.environ,BASE_IMAGE=module.LOCK['baseImage']), \
                    mock.patch.object(sys,'argv',['recipe',str(target)]), \
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

    def test_only_pinned_internal_relnotes_link_is_retained_after_regular_files(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-relnotes-mock-') as directory:
            root=Path(directory)
            archive=root/'allowed.tar'
            with tarfile.open(archive,'w') as stream:
                link=tarfile.TarInfo('root/RelNotes')
                link.type,link.linkname=tarfile.SYMTYPE,'Documentation/RelNotes/2.39.5.txt'
                stream.addfile(link)  # Link deliberately precedes its declared target.
                target=tarfile.TarInfo('root/Documentation/RelNotes/2.39.5.txt')
                target.size=1
                stream.addfile(target,io.BytesIO(b'x'))
            output=root/'output'
            output.mkdir()
            module.extract(archive,output)
            self.assertTrue((output/'root/RelNotes').is_symlink())
            self.assertEqual((output/'root/RelNotes').read_bytes(),b'x')
            copied=root/'copied'
            module.shutil.copytree(output/'root',copied,symlinks=True)
            self.assertTrue((copied/'RelNotes').is_symlink())
            self.assertEqual((copied/'RelNotes').read_bytes(),b'x')

    def test_relnotes_exception_refuses_wrong_missing_nonregular_and_colliding_inventory(self):
        literal='Documentation/RelNotes/2.39.5.txt'
        link=('root/RelNotes',tarfile.SYMTYPE,literal)
        target=('root/'+literal,tarfile.REGTYPE,'')
        # Fixed tiny inventories, not archive fuzzing or a wide test loop.
        cases=[
            [('root/RelNotes',tarfile.SYMTYPE,'Documentation/RelNotes/2.39.4.txt'),target],
            [link],
            [link,('root/'+literal,tarfile.DIRTYPE,'')],
            [link,target,('root/Documentation',tarfile.REGTYPE,'')],
            [link,target,('root/RelNotes/descendant',tarfile.REGTYPE,'')],
            [link,target,('root/RelNotes',tarfile.REGTYPE,'')],
            [link,target,('root/other-link',tarfile.SYMTYPE,literal)],
            [('root/RelNotes',tarfile.LNKTYPE,literal),target],
            [link,target,('another-root/tiny',tarfile.REGTYPE,'')],
        ]
        with tempfile.TemporaryDirectory(prefix='git-wait-relnotes-refusal-') as directory:
            root=Path(directory)
            for index,entries in enumerate(cases):
                archive=root/(str(index)+'.tar')
                with tarfile.open(archive,'w') as stream:
                    for name,kind,destination in entries:
                        entry=tarfile.TarInfo(name)
                        entry.type,entry.linkname=kind,destination
                        entry.size=1 if entry.isfile() else 0
                        stream.addfile(entry,io.BytesIO(b'x') if entry.isfile() else None)
                output=root/('out'+str(index))
                output.mkdir()
                with self.assertRaises(ValueError):
                    module.extract(archive,output)
                self.assertEqual(list(output.iterdir()),[])  # Whole validation precedes mutation.

    def test_extract_refuses_preexisting_destination_state(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-destination-mock-') as directory:
            root=Path(directory)
            archive=root/'source.tar'
            with tarfile.open(archive,'w') as stream:
                entry=tarfile.TarInfo('root/tiny')
                entry.size=1
                stream.addfile(entry,io.BytesIO(b'x'))
            output=root/'output'
            output.mkdir()
            (output/'preserved').write_bytes(b'unchanged')
            with self.assertRaises(ValueError):
                module.extract(archive,output)
            self.assertEqual(sorted(path.name for path in output.iterdir()),['preserved'])
            self.assertEqual((output/'preserved').read_bytes(),b'unchanged')


if __name__ == '__main__':
    unittest.main()
