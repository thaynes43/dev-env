#!/usr/bin/env python3
"""Finite actual-helper mocks under nice19; no native compile/child/Git/cluster."""
import ast
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest import mock

source = Path(__file__).with_name('workspace-cephfs-trial.py').read_text()
nodes = {node.name:node for node in ast.parse(source).body
         if isinstance(node,(ast.ClassDef,ast.FunctionDef))}


class MarkerHelperChecks(unittest.TestCase):
    def setUp(self):
        self.events, self.kwargs = [], []
        self.clock = SimpleNamespace(now=1000)
        self.mode, self.exited = 'timeout', None
        self.result, self.operation = 0, 2
        self.snapshots, self.ownership = 0, 0
        self.mapping_valid, self.uncertain_reap = True, False
        self.proc = SimpleNamespace(pid=42, returncode=None)

        def snapshot(pointer, operation):
            self.snapshots += 1
            operation.value = self.operation
            return self.result

        self.library = SimpleNamespace(
            gwm_parent_create=lambda fd: 999 if self.mapping_valid else None,
            gwm_parent_close=lambda pointer: self.events.append('mapClosed'),
            gwm_snapshot=snapshot)
        fake_ctypes = SimpleNamespace(c_uint32=lambda: SimpleNamespace(value=0),byref=lambda value:value)
        self.os = SimpleNamespace(MFD_CLOEXEC=1,MFD_ALLOW_SEALING=2,
            memfd_create=lambda name,flags:101, ftruncate=lambda fd,size:self.events.append(('bytes',size)),
            close=lambda fd:self.events.append('fdClosed'), uname=lambda:SimpleNamespace(machine='x86_64'),
            P_PID=1,WEXITED=2,WNOHANG=4,WNOWAIT=8)

        def waitid(*args):
            self.ownership += 1
            return self.exited

        self.os.waitid = waitid
        self.signal = SimpleNamespace(SIGCHLD=1,SIG_DFL=0,getsignal=lambda value:0)
        fcntl = SimpleNamespace(F_ADD_SEALS=1,F_SEAL_SHRINK=2,F_SEAL_GROW=4,F_SEAL_SEAL=8,
                               fcntl=lambda *args:self.events.append('sealed'))
        owner = self

        class Process:
            def __init__(self):
                self.pid, self.returncode, self.calls = 42, None, 0

            def __enter__(self):
                return self

            def communicate(self, timeout):
                self.calls += 1
                if owner.mode == 'eof':
                    self.returncode = 0
                    return '', ''
                owner.clock.now += timeout
                if owner.mode == 'cancel':
                    raise TimeoutError('fixture cancellation')
                raise subprocess.TimeoutExpired(['git'],timeout,output=b'',stderr=b'')

            def kill(self):
                owner.events.append('kill')

            def wait(self):
                owner.events.append('wait')
                if owner.uncertain_reap:
                    raise TimeoutError('uncertain child wait')
                self.returncode = -9

            def __exit__(self,*args):
                if self.returncode is None:
                    self.wait()

        def popen(argv, **kwargs):
            self.kwargs.append(kwargs)
            self.proc = Process()
            return self.proc

        ns = dict(os=self.os,signal=self.signal,json=json,re=re,fcntl=fcntl,
                  time=SimpleNamespace(monotonic=lambda:self.clock.now),DEADLINE=1145,
                  open=lambda path,mode:io.BytesIO(b'running' if path.endswith('/syscall') else b'wait_woken'),
                  subprocess=SimpleNamespace(Popen=popen,PIPE=object(),
                    TimeoutExpired=subprocess.TimeoutExpired,CompletedProcess=subprocess.CompletedProcess))
        selected = ['GitWaitMarker','status_wait_observation','run_status_child']
        exec(compile(ast.Module(body=[nodes[n] for n in selected],type_ignores=[]),'<actual-marker-helper>','exec'),ns)
        self.marker_class = ns['GitWaitMarker']
        self.marker_class._verified = (fake_ctypes,self.library,{'executable':'/opt/dev-env/trials/git-wait'})
        self.ns = ns

    def run_child(self, enabled=True):
        measurement = {}
        self.measurement = measurement
        env = {'TRIAL_GIT_WAIT_MARKER':'1'} if enabled else {}
        self.ns['run_status_child'](['git','-C','/owned','status','--porcelain=v1','--untracked-files=all'],
            env,15,1000,measurement,pass_fds=(202,),executable='/usr/bin/git')

    def test_exact_one_checkpoint_and_separate_fd_then_genuine_reap_cleanup(self):
        with self.assertRaises(subprocess.TimeoutExpired) as caught:
            self.run_child()
        self.assertEqual(caught.exception.timeout,15)
        self.assertEqual(self.clock.now,1015)
        self.assertEqual((self.ownership,self.snapshots),(1,1))
        self.assertEqual(self.kwargs[0]['pass_fds'],(202,101))
        self.assertEqual(self.kwargs[0]['executable'],'/opt/dev-env/trials/git-wait')
        self.assertEqual(self.kwargs[0]['env']['DEV_ENV_GIT_WAIT_FD'],'101')
        self.assertEqual(self.measurement['waitObservation']['gitOperation'],
                         {'available':True,'scope':'contentOpenWrapper','abiVersion':1})
        self.assertTrue(self.measurement['statusChildReaped'])
        self.assertLess(self.events.index('wait'),self.events.index('mapClosed'))
        self.assertEqual(self.events[-2:],['mapClosed','fdClosed'])
        self.assertEqual(self.marker_class.retained,[])
        receipt=json.dumps(self.measurement['waitObservation'])
        self.assertLessEqual(len(receipt.encode()),1024)
        self.assertNotIn('/owned',receipt)
        self.assertNotIn('pid',receipt.lower())

    def test_default_off_and_early_eof_do_not_sample(self):
        self.mode='eof'
        self.run_child(enabled=False)
        self.assertEqual(self.kwargs[0]['executable'],'/usr/bin/git')
        self.assertEqual(self.kwargs[0]['pass_fds'],(202,))
        self.assertNotIn('diagnosticGit',self.measurement)
        self.assertEqual(self.events,[])
        self.run_child(enabled=True)
        self.assertEqual((self.ownership,self.snapshots),(0,0))
        self.assertEqual(self.events[-2:],['mapClosed','fdClosed'])

    def test_cancellation_reaps_before_close(self):
        self.mode='cancel'
        with self.assertRaises(TimeoutError):
            self.run_child()
        self.assertEqual(self.snapshots,0)
        self.assertEqual(self.events[-4:],['kill','wait','mapClosed','fdClosed'])

    def test_uncertain_reap_retains_marker_without_claiming_stop(self):
        self.uncertain_reap=True
        with self.assertRaises(TimeoutError):
            self.run_child()
        self.assertNotIn('mapClosed',self.events)
        self.assertNotIn('fdClosed',self.events)
        self.assertNotIn('statusChildReaped',self.measurement)
        self.assertEqual(self.measurement['markerCleanup'],'unreapedChildRetained')
        self.assertEqual(len(self.marker_class.retained),1)

    def test_unavailable_reader_states_are_not_retried(self):
        for result in range(1,10):
            marker=self.marker_class()
            self.result=result
            record=marker.snapshot()
            self.assertFalse(record['available'])
            count=self.snapshots
            self.assertEqual(marker.snapshot(),{'available':False,'reason':'alreadySampled'})
            self.assertEqual(self.snapshots,count)
            self.assertLess(len(json.dumps(record)),100)
            marker.close()

    def test_owned_child_gate_refuses_exit_reap_handler_and_unknown_arch(self):
        marker=self.marker_class()
        self.exited=object()
        self.ns['status_wait_observation'](self.proc,marker)
        self.proc.returncode=0
        self.ns['status_wait_observation'](self.proc,marker)
        self.proc.returncode=None
        self.exited=None
        self.signal.getsignal=lambda value:1
        self.ns['status_wait_observation'](self.proc,marker)
        self.signal.getsignal=lambda value:0
        self.os.uname=lambda:SimpleNamespace(machine='unknown')
        self.ns['status_wait_observation'](self.proc,marker)
        self.assertEqual(self.snapshots,0)
        marker.close()

    def test_marker_mapping_failure_prevents_launch_and_closes_fd(self):
        self.mapping_valid=False
        with self.assertRaises(RuntimeError):
            self.run_child()
        self.assertEqual(self.kwargs,[])
        self.assertEqual(self.events[-1],'fdClosed')


class MarkerInterfaceChecks(unittest.TestCase):
    def setUp(self):
        self.binary, self.reader = b'synthetic executable', b'synthetic reader'
        digest = hashlib.sha256
        self.commit, self.helper = 'a'*40, 'b'*64
        self.record = {field:'c'*64 for field in [
            'debianDSCSHA256','debianOrigSHA256','debianOverlaySHA256',
            'debianSeriesSHA256','markerPatchSHA256','markerSourceSHA256',
            'markerHeaderSHA256','buildOptionsSHA256']}
        self.record.update(abiVersion=1,platform='linux-amd64',
            artifactVerification='hostedArtifactVerified',debianVersion='1:2.39.5-0+deb12u3',
            baseImage=('ghcr.io/thaynes43/dev-env:2.9.1@sha256:'
                'a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf'),
            executable='/opt/dev-env/trials/git-wait',
            reader='/opt/dev-env/trials/libgit-wait-reader.so',debianPatchCount=7,
            historicalCompilerEquivalence=False,sourceCommit=self.commit,
            upstreamCommit='d'*40,helperSHA256=self.helper,
            statMacros={'USE_STDEV':False,'USE_NSEC':True},postLinkStrip='--strip-unneeded',
            executableByteCap=8*1024*1024,readerByteCap=256*1024,
            executableSHA256=digest(self.binary).hexdigest(),readerSHA256=digest(self.reader).hexdigest())
        self.reads, self.loads = [], []
        self.oversized = None

    def interface(self, record, env=None):
        owner = self

        class Oversized:
            def __len__(self):
                return 8*1024*1024+1

        class File:
            def __init__(self, path):
                self.path = path
            def __enter__(self):
                return self
            def __exit__(self,*args):
                pass
            def read(self,cap):
                owner.reads.append((self.path,cap))
                if self.path == owner.oversized:
                    return Oversized()
                return { '/opt/dev-env/trials/git-wait-provenance.json':json.dumps(record).encode(),
                         record['executable']:owner.binary,record['reader']:owner.reader}[self.path][:cap]

        library = SimpleNamespace(gwm_parent_create=lambda fd:None,
            gwm_parent_close=lambda pointer:None,gwm_snapshot=lambda pointer,operation:None)
        def load(path):
            self.loads.append(path)
            return library
        fake_ctypes = SimpleNamespace(CDLL=load,c_int=object(),c_void_p=object(),
                                      c_uint32=object(),POINTER=lambda value:object())
        expected = {'TRIAL_HELPER_SOURCE_COMMIT':self.commit,'TRIAL_HELPER_SHA256':self.helper}
        ns = dict(os=SimpleNamespace(environ=expected if env is None else env),json=json,re=re,
                  hashlib=hashlib,open=lambda path,mode:File(path))
        exec(compile(ast.Module(body=[nodes['GitWaitMarker']],type_ignores=[]),
                     '<actual-marker-interface>','exec'),ns)
        with mock.patch.dict(sys.modules,ctypes=fake_ctypes):
            return ns['GitWaitMarker'].interface()

    def test_actual_interface_accepts_exact_declared_artifacts(self):
        _, _, metadata = self.interface(self.record)
        self.assertEqual(metadata['sourceCommit'],self.commit)
        self.assertEqual(len(self.loads),1)
        self.assertEqual([cap for _,cap in self.reads],[4097,8*1024*1024+1,256*1024+1])
        self.assertLessEqual(len(json.dumps(metadata).encode()),4096)

    def test_provenance_and_expected_manifest_mismatches_refuse_before_native_load(self):
        for field,value in [('baseImage','unverified'),('sourceCommit','e'*40),
                            ('helperSHA256','f'*64),('postLinkStrip','undeclared'),
                            ('executableByteCap',16*1024*1024),('executableSHA256','0'*64)]:
            record=dict(self.record)
            record[field]=value
            with self.assertRaises(AssertionError):
                self.interface(record)
        with self.assertRaises(AssertionError):
            self.interface(self.record,{})
        self.assertEqual(self.loads,[])

    def test_oversized_executable_refuses_before_hash_or_native_load(self):
        self.oversized=self.record['executable']
        with self.assertRaises(AssertionError):
            self.interface(self.record)
        self.assertEqual(self.loads,[])


if __name__ == '__main__':
    unittest.main()
