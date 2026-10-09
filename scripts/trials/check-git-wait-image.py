#!/usr/bin/env python3
"""HOSTED fixture-image check only, under nice19: tiny, serial, finite, no load.

Never build or run this in the shared pod. No cluster/auth/provider/model calls.
Compare exactly three status commands on one tiny WIP fixture. One separate owned
sleeping test child reaches the existing 5s checkpoint then is killed and reaped.
This proves local interfaces/functional behavior, not Ceph cause/performance.
"""
import ast
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tarfile
import tempfile
import time

ROOT = Path('/opt/dev-env/trials')
with (ROOT / 'git-wait-provenance.json').open('rb') as stream:
    raw = stream.read(4097)
assert len(raw) <= 4096
PROVENANCE = json.loads(raw)
assert PROVENANCE['artifactVerification'] == 'hostedArtifactVerified'
assert PROVENANCE['debianVersion'] == '1:2.39.5-0+deb12u3'
assert PROVENANCE['platform'] == 'linux-amd64' and PROVENANCE['debianPatchCount'] == 7
assert not PROVENANCE['historicalCompilerEquivalence']
assert PROVENANCE['postLinkStrip'] == '--strip-unneeded'
assert PROVENANCE['executableByteCap'] == 8 * 1024 * 1024
assert PROVENANCE['readerByteCap'] == 256 * 1024
assert PROVENANCE['stripTool'].startswith('GNU strip ')
for path, field, cap in [(ROOT / 'git-wait', 'executableSHA256', 8*1024*1024),
                         (ROOT / 'git-control', 'controlSHA256', 8*1024*1024),
                         (ROOT / 'libgit-wait-reader.so', 'readerSHA256', 256*1024),
                         (ROOT / 'checks/workspace-cephfs-trial.py', 'helperSHA256', 256*1024),
                         (Path('/usr/bin/git'), 'baseSystemGitSHA256', 8*1024*1024)]:
    with path.open('rb') as stream:
        content = stream.read(cap+1)
    assert 0 < len(content) <= cap
    assert hashlib.sha256(content).hexdigest() == PROVENANCE[field]
assert len(json.dumps(PROVENANCE).encode()) <= 4096
# Retain license text and the complete corresponding source, including the
# actual native marker and declared options, in this same distributed artifact.
with Path('/usr/share/licenses/git-wait/COPYING').open('rb') as stream:
    license_text = stream.read(65537)
assert 0 < len(license_text) <= 65536 and b'GNU GENERAL PUBLIC LICENSE' in license_text
with Path('/usr/share/doc/dev-env/THIRD_PARTY.md').open('rb') as stream:
    third_party = stream.read(65537)
assert len(third_party) <= 65536 and b'git-wait-source.tar.gz' in third_party
required = {'git-wait-source/git/COPYING', 'git-wait-source/git/debian/patches/series',
            'git-wait-source/recipe/marker.c', 'git-wait-source/recipe/marker.h',
            'git-wait-source/recipe/marker-test.c', 'git-wait-source/recipe/git-marker.patch',
            'git-wait-source/build-options.json', 'git-wait-source/provenance.json'}
retained = {}
with tarfile.open(ROOT / 'git-wait-source.tar.gz', 'r|gz') as archive:
    members = total = 0
    for entry in archive:
        members += 1
        total += entry.size
        assert members <= 12000 and total <= 128*1024*1024
        if entry.name in required:
            assert entry.isfile() and entry.name not in retained and entry.size <= 256*1024
            retained[entry.name] = archive.extractfile(entry).read(256*1024+1)
assert retained.keys() == required
assert retained['git-wait-source/git/COPYING'] == license_text
assert json.loads(retained['git-wait-source/provenance.json']) == PROVENANCE
options = json.loads(retained['git-wait-source/build-options.json'])
assert hashlib.sha256('\n'.join(options).encode()).hexdigest() == PROVENANCE['buildOptionsSHA256']
for name, field in [('marker.c','markerSourceSHA256'), ('marker.h','markerHeaderSHA256'),
                    ('git-marker.patch','markerPatchSHA256')]:
    assert hashlib.sha256(retained['git-wait-source/recipe/'+name]).hexdigest() == PROVENANCE[field]
# Exercise the actual rendered helper interface, not a parallel ctypes setup.
# CI supplies the expected exact source commit and helper digest from checkout;
# the image's own provenance cannot supply its expected identity to itself.
helper = ROOT / 'checks/workspace-cephfs-trial.py'
node = next(node for node in ast.parse(helper.read_text()).body
            if isinstance(node, ast.ClassDef) and node.name == 'GitWaitMarker')
namespace = dict(os=os, json=json, re=re, hashlib=hashlib, fcntl=fcntl)
exec(compile(ast.Module(body=[node], type_ignores=[]), '<actual-marker-interface>', 'exec'), namespace)
GitWaitMarker = namespace['GitWaitMarker']
_, _, metadata = GitWaitMarker.interface()
assert metadata['sourceCommit'] == os.environ['TRIAL_HELPER_SOURCE_COMMIT']
assert metadata['helperSHA256'] == os.environ['TRIAL_HELPER_SHA256']
assert 0 < (ROOT / 'git-control').stat().st_size <= PROVENANCE['executableByteCap']
env = {'PATH':'/usr/bin:/bin', 'LC_ALL':'C', 'GIT_CONFIG_NOSYSTEM':'1',
       'GIT_CONFIG_GLOBAL':'/dev/null', 'GIT_TERMINAL_PROMPT':'0', 'GIT_OPTIONAL_LOCKS':'0',
       'GIT_AUTHOR_NAME':'Synthetic fixture', 'GIT_COMMITTER_NAME':'Synthetic fixture',
       'GIT_AUTHOR_EMAIL':'fixture@example.invalid', 'GIT_COMMITTER_EMAIL':'fixture@example.invalid'}


def git(repo, args, executable='/usr/bin/git', **kwargs):
    result = subprocess.run(['git', '-C', str(repo), *args], executable=executable,
                            env=env, text=True, capture_output=True, timeout=15, **kwargs)
    assert result.returncode == 0 and len(result.stdout.encode()) <= 4096 and \
        len(result.stderr.encode()) <= 4096
    return result


with tempfile.TemporaryDirectory(prefix='git-wait-equivalence-', dir='/tmp') as directory:
    repo = Path(directory)
    git(repo, ['init', '-b', 'main'])
    (repo / 'a.txt').write_text('synthetic base\n')
    (repo / 'b.txt').write_text('unchanged synthetic base\n')
    git(repo, ['add', '.'])
    git(repo, ['commit', '-m', 'synthetic base'])
    (repo / 'a.txt').write_text('synthetic staged WIP\n')
    git(repo, ['add', 'a.txt'])
    (repo / 'a.txt').write_text('synthetic unstaged WIP\n')
    (repo / 'untracked.txt').write_text('synthetic untracked WIP\n')
    # One changed-stat/unchanged-content path exercises refresh content checking.
    os.utime(repo / 'b.txt', ns=(1_700_000_000_000_000_000, 1_700_000_000_000_000_000))
    names = ['a.txt', 'b.txt', 'untracked.txt', '.git/index', '.git/HEAD', '.git/refs/heads/main']
    before = {name:(repo / name).read_bytes() for name in names}
    outputs = []
    for executable in ['/usr/bin/git', str(ROOT / 'git-control'), str(ROOT / 'git-wait')]:
        owned_marker = GitWaitMarker()
        fd = owned_marker.fd
        try:
            command_env = dict(env, DEV_ENV_GIT_WAIT_FD=str(fd))
            result = subprocess.run(['git', '-C', str(repo), 'status', '--porcelain=v1',
                                     '--untracked-files=all'], executable=executable,
                                    env=command_env, pass_fds=(fd,), text=True,
                                    capture_output=True, timeout=15)
            assert result.returncode == 0 and not result.stderr and len(result.stdout.encode()) <= 1024
            outputs.append(result.stdout)
            assert {name:(repo / name).read_bytes() for name in names} == before
        finally:
            owned_marker.close()
    assert outputs == ['MM a.txt\n?? untracked.txt\n'] * 3
assert not repo.exists()

# One finite constructor/atfork check; a child cannot publish another writer.
owned_marker = GitWaitMarker()
fd = owned_marker.fd
try:
    checked = subprocess.run([str(ROOT / 'marker-test'), '--fork-disabled'],
                             env=dict(env, DEV_ENV_GIT_WAIT_FD=str(fd)), pass_fds=(fd,),
                             text=True, capture_output=True, timeout=5)
    assert checked.returncode == 0 and not checked.stdout and not checked.stderr
finally:
    owned_marker.close()

# One actual sleeping child; no stress, operation loop or public PID. The original
# command deadline stays 5.5s; kill/reap safety may exceed a syscall's timer.
owned_marker = GitWaitMarker()
fd = owned_marker.fd
process = None
try:
    started = time.monotonic()
    with subprocess.Popen([str(ROOT / 'marker-test'), '--owned-sleep'],
                          env=dict(env, DEV_ENV_GIT_WAIT_FD=str(fd)), pass_fds=(fd,),
                          text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
        try:
            process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            assert process.returncode is None and signal.getsignal(signal.SIGCHLD) == signal.SIG_DFL
            assert os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None
            assert owned_marker.snapshot() == {
                'available':True, 'scope':'contentOpenWrapper', 'abiVersion':1}
            try:
                process.communicate(timeout=max(0, started + 5.5 - time.monotonic()))
            except subprocess.TimeoutExpired:
                process.kill()
                stdout, stderr = process.communicate()
                assert process.returncode is not None and not stdout and not stderr
            else:
                raise AssertionError('sleeping fixture must reach original deadline')
        else:
            raise AssertionError('sleeping fixture must reach one checkpoint')
    try:
        os.waitpid(process.pid, os.WNOHANG)
    except ChildProcessError:
        pass
    else:
        raise AssertionError('child was not genuinely reaped')
finally:
    if process is not None and process.returncode is None:
        process.kill()
        process.wait()
    owned_marker.close()
print(json.dumps({'result':'PASS', 'scope':'hosted tiny fixture only', 'statusCommands':3,
                  'checkpoints':1, 'markerBytes':64, 'publicByteCap':4096,
                  'checks':['same-source control/instrumented/distro output',
                            'actual helper provenance, artifact caps and native interface',
                            'exact WIP/ref/index bytes and status argv flags',
                            'same-ABI atomic sample under owned unreaped proof',
                            'original deadline followed by genuine stop/reap']}))
