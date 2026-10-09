#!/usr/bin/env python3
"""Finite in-process manifest checks; no children, builds, Git or cluster calls."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import runpy
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
COMMIT = 'a'*40
BASE = ('ghcr.io/thaynes43/dev-env:2.9.1@sha256:'
        'a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf')
NATIVE = 'ghcr.io/thaynes43/dev-env:git-wait-sha-'+COMMIT+'@sha256:'+'b'*64


class RenderChecks(unittest.TestCase):
    def render(self, directory, extra):
        argv = ['renderer','--output-dir',directory,*extra]
        with mock.patch.object(sys,'argv',argv), contextlib.redirect_stdout(io.StringIO()):
            runpy.run_path(str(HERE/'render-workspace-cephfs-trial.py'),run_name='__main__')
        return {role:json.loads((Path(directory)/('workspace-cephfs-trial-'+role+'.job.json')).read_text())
                for role in ['a','b','reconnect']}

    def test_default_off_and_native_manifest_only_select_original_a_b_status(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-render-') as directory:
            plain = self.render(directory,['--source-commit',COMMIT])
            native = self.render(directory,['--source-commit',COMMIT,'--git-wait-image',NATIVE])
        helper_hash = hashlib.sha256((HERE/'workspace-cephfs-trial.py').read_bytes()).hexdigest()
        for role in ['a','b','reconnect']:
            old, new = plain[role],native[role]
            oldpod,newpod = old['spec']['template']['spec'],new['spec']['template']['spec']
            self.assertEqual(oldpod['containers'][0]['image'],BASE)
            self.assertEqual(newpod['initContainers'][0]['image'],BASE)
            self.assertEqual(newpod['restartPolicy'],'Never')
            self.assertEqual(new['spec']['backoffLimit'],0)
            self.assertEqual(new['spec']['activeDeadlineSeconds'],180 if role in {'a','b'} else 60)
            self.assertEqual(newpod['containers'][0]['resources']['limits']['cpu'],'250m')
            self.assertEqual(newpod['initContainers'][0]['resources']['limits']['cpu'],'100m')
            self.assertEqual(oldpod['containers'][0]['args'],newpod['containers'][0]['args'])
            self.assertNotIn('TRIAL_GIT_WAIT_MARKER',[value['name'] for value in oldpod['containers'][0]['env']])
            if role == 'reconnect':
                self.assertEqual(new,old)
                continue
            container = newpod['containers'][0]
            values = {value['name']:value.get('value') for value in container['env']}
            self.assertEqual(container['image'],NATIVE)
            self.assertEqual(values['TRIAL_GIT_WAIT_MARKER'],'1')
            self.assertEqual(values['TRIAL_HELPER_SOURCE_COMMIT'],COMMIT)
            self.assertEqual(values['TRIAL_HELPER_SHA256'],helper_hash)
            self.assertEqual(new['metadata']['annotations']['dev-env.haynesops.com/trial-source-sha256'],helper_hash)
            # Removing only the opt-in image and identity env must restore the
            # entire existing Pod/Job spec, including volumes, security and caps.
            container['image']=BASE
            container['env']=[value for value in container['env']
                              if value['name'] not in {'TRIAL_GIT_WAIT_MARKER','TRIAL_HELPER_SOURCE_COMMIT','TRIAL_HELPER_SHA256'}]
            self.assertEqual(new,old)

    def test_mismatched_fixture_source_refuses_before_any_manifest_write(self):
        with tempfile.TemporaryDirectory(prefix='git-wait-render-refusal-') as directory:
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as caught:
                self.render(directory,['--source-commit','c'*40,'--git-wait-image',NATIVE])
            self.assertEqual(caught.exception.code,2)
            self.assertEqual(list(Path(directory).iterdir()),[])


if __name__ == '__main__':
    unittest.main()
