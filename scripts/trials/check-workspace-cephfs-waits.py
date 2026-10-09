#!/usr/bin/env python3
"""Finite actual-source wait checks: fake clock/PID/proc/index only, no commands.

Run under nice19. No CPU burners, stress tools, busy loops, real Git fixture,
cluster operation or wide/looped tests on shared nodes.
"""
import ast
import contextlib
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import pathlib
import signal
import struct
import subprocess
import sys
from types import SimpleNamespace
from unittest.mock import patch

tree = ast.parse(Path(__file__).with_name('workspace-cephfs-trial.py').read_text())
names = {'bounded_output','status_wait_observation','post_timeout_peer_metadata','run_status_child','run'}
selected = [node for node in tree.body if
            isinstance(node,ast.FunctionDef) and node.name in names or
            isinstance(node,ast.Assign) and any(isinstance(t,ast.Name) and t.id == 'PEER_METADATA_SCRIPT'
                                                for t in node.targets)]

class Clock:
    value = 100
    def monotonic(self):
        return self.value

clock = Clock()
events = []
proc_values = {'syscall':b'262 0xPRIVATE_POINTER /private/argument 0xstack\n',
               'wchan':b'ceph_mdsc_do_request\n'}
def proc_open(path,mode):
    events.append(('procRead',path.rsplit('/',1)[1]))
    assert path.startswith('/proc/987654/') and mode == 'rb'
    value = proc_values[path.rsplit('/',1)[1]]
    if isinstance(value,Exception):
        raise value
    return io.BytesIO(value)

wait_result = None
def waitid(kind,pid,flags):
    assert kind == 1 and pid == 987654 and flags == 7
    events.append(('ownedWithoutReap',))
    if isinstance(wait_result,Exception):
        raise wait_result
    return wait_result

os_mock = SimpleNamespace(uname=lambda:SimpleNamespace(machine='x86_64'),waitid=waitid,
                          P_PID=1,WEXITED=1,WNOHANG=2,WNOWAIT=4)
ns = dict(json=json,os=os_mock,signal=SimpleNamespace(getsignal=lambda _:signal.SIG_DFL,
          SIG_DFL=signal.SIG_DFL,SIGCHLD=signal.SIGCHLD),open=proc_open,time=clock,
          DEADLINE=245,ENV={'GIT_OPTIONAL_LOCKS':'0'},sys=sys,
          ROLE='b',TASK_A=Path('/home/dev/work/cephfs-trial-a'),
          TASK_B=Path('/home/dev/work/cephfs-trial-b'))
exec(compile(ast.Module(body=selected,type_ignores=[]),'<actual-wait-helper>','exec'),ns)
owned = SimpleNamespace(pid=987654,returncode=None)
observation = ns['status_wait_observation'](owned)
assert observation == {'syscallCategory':'metadata','wchanCategory':'cephMetadata'}
assert len(json.dumps(observation).encode()) <= 1024
assert all(secret not in json.dumps(observation) for secret in ['987654','PRIVATE','/private','0x'])
assert events == [('ownedWithoutReap',),('procRead','syscall'),('procRead','wchan')]

# Fixed category coverage; raw unknown symbol/number is never retained.
for raw, category in [(b'0 0xSECRET','read'),(b'257 0xSECRET','open'),
                      (b'217 0xSECRET','directory'),(b'running','running'),
                      (b'-1 0xSECRET','running'),(b'999999 0xSECRET','other')]:
    proc_values.update(syscall=raw,wchan=b'ARBITRARY_PRIVATE_SYMBOL')
    observation = ns['status_wait_observation'](owned)
    assert observation == {'syscallCategory':category,'wchanCategory':'other'}
proc_values.update(syscall=PermissionError('private error'),wchan=b'0')
unavailable = ns['status_wait_observation'](owned)
assert unavailable['syscallReason'] == 'permissionDenied'
assert unavailable['wchanReason'] == 'runningOrHidden'
assert 'private error' not in json.dumps(unavailable)
proc_values.update(syscall=b'4 '+b'X'*1024,wchan=FileNotFoundError())
unavailable = ns['status_wait_observation'](owned)
assert unavailable['syscallReason'] == 'capped' and unavailable['wchanReason'] == 'exitedOrMissing'

# Reaping/exit races and unexpected child ownership never read a numeric proc path.
events.clear()
owned.returncode = 0
assert ns['status_wait_observation'](owned)['reason'] == 'alreadyReaped' and not events
owned.returncode = None
wait_result = SimpleNamespace(si_pid=owned.pid)
assert ns['status_wait_observation'](owned)['reason'] == 'exitedBeforeObservation'
wait_result = ChildProcessError()
assert ns['status_wait_observation'](owned)['reason'] == 'childOwnershipUnavailable'
assert not any(e[0] == 'procRead' for e in events)
ns['signal'].getsignal = lambda _:signal.SIG_IGN
assert ns['status_wait_observation'](owned)['reason'] == 'uncertainReaper'
ns['signal'].getsignal = lambda _:signal.SIG_DFL
wait_result = None
proc_values.update(syscall=b'0 0xdiscarded',wchan=b'folio_wait_bit_common')

argv = ['git','-C',str(ns['TASK_A']),'status','--porcelain=v1','--untracked-files=all']
actions = []
class Process:
    pid = owned.pid
    returncode = None
    def __enter__(self):
        return self
    def __exit__(self,*args):
        if self.returncode is None:
            self.wait()
    def communicate(self,timeout):
        events.append(('communicate',timeout))
        action = actions.pop(0)
        if action == 'timeout':
            clock.value += timeout
            raise subprocess.TimeoutExpired(argv,timeout,output=b'partial',stderr=b'bounded-error')
        if isinstance(action,BaseException):
            clock.value += 1
            raise action
        clock.value += action
        self.returncode = 0
        events.append(('reapedAtEOF',))
        return ('M  src/000.txt\n?? new.txt\n','')
    def kill(self):
        events.append(('kill',))
    def wait(self):
        self.returncode = -9
        events.append(('reap',))
        return self.returncode

def popen(actual,**kwargs):
    assert actual == argv and kwargs['text'] and kwargs['pass_fds'] == (7,)
    assert kwargs['env']['GIT_OPTIONAL_LOCKS'] == '0'
    events.append(('spawn',))
    return Process()

ns['subprocess'] = SimpleNamespace(Popen=popen,PIPE=subprocess.PIPE,
    TimeoutExpired=subprocess.TimeoutExpired,CompletedProcess=subprocess.CompletedProcess)
class Trace:
    write_fd = 7
    def finish(self):
        events.append(('traceFinish',))
        return {'available':True,'events':[]}
ns.update(StatusTrace=Trace,MEASUREMENTS=[],remaining=lambda:ns['DEADLINE']-clock.value,
          own_cpu_stat=lambda:None,cpu_delta=lambda *args:events.append(('cpuSnapshot',)) or {},
          resource=SimpleNamespace(getrusage=lambda _:None,RUSAGE_CHILDREN=0))
def peer_sample(actual):
    assert actual == argv and ('reap',) in events
    assert events[-1] == ('traceFinish',)
    events.append(('peerSample',))
    clock.value += 0.75
    return {'available':False,'reason':'metadataTimeout'}
real_peer_sampler = ns['post_timeout_peer_metadata']
ns['post_timeout_peer_metadata'] = peer_sample

# Early EOF creates no proc observation, timer thread or post-status warmup.
actions[:] = [0.25]
events.clear()
clock.value = 100
assert ns['run'](argv).stdout == 'M  src/000.txt\n?? new.txt\n'
assert not any(e[0] in {'ownedWithoutReap','procRead','peerSample'} for e in events)
assert ns['MEASUREMENTS'][-1]['waitObservation']['reason'] == 'completedBeforeCheckpoint'

# One checkpoint only, then the exact original remainder. Duration/CPU/Trace2
# precede the optional metadata child; retained partial output and timeout stay 15s.
actions[:] = ['timeout','timeout']
events.clear()
clock.value = 100
try:
    ns['run'](argv)
except subprocess.TimeoutExpired as error:
    assert error.timeout == 15 and error.output == b'partial' and error.stderr == b'bounded-error'
else:
    raise AssertionError('original timeout lost')
record = ns['MEASUREMENTS'][-1]
assert record['seconds'] == record['effectiveTimeoutSeconds'] == 15 and clock.value == 115.75
assert record['statusChildReaped'] and record['postTimeoutPeerMetadata']['reason'] == 'metadataTimeout'
assert [e for e in events if e[0] == 'communicate'] == [('communicate',5),('communicate',10)]
assert sum(e[0] == 'ownedWithoutReap' for e in events) == 1
assert events[-5:] == [('kill',),('reap',),('cpuSnapshot',),('traceFinish',),('peerSample',)]

# An interruption of the optional sampler is visible without rewriting the
# already-complete timed-out status measurement or swallowing the helper signal.
interrupted_sample = TimeoutError('fixture deadline or termination')
def sample_interrupted(actual):
    raise interrupted_sample
ns['post_timeout_peer_metadata'] = sample_interrupted
clock.value = 100
events.clear()
actions[:] = ['timeout','timeout']
try:
    ns['run'](argv)
except TimeoutError as error:
    assert error is interrupted_sample
else:
    raise AssertionError('post-timeout interruption lost')
assert ns['MEASUREMENTS'][-1]['postTimeoutPeerMetadata']['reason'] == 'helperInterrupted'
assert ns['MEASUREMENTS'][-1]['errorType'] == 'TimeoutExpired'
ns['post_timeout_peer_metadata'] = peer_sample

# An unproven reap never grants sampling authority, even if wait returned.
saved_wait = Process.wait
Process.wait = lambda self:events.append(('unprovenWait',))
clock.value = 100
events.clear()
actions[:] = ['timeout','timeout']
try:
    ns['run'](argv)
except subprocess.TimeoutExpired:
    pass
assert not ns['MEASUREMENTS'][-1]['statusChildReaped']
assert 'postTimeoutPeerMetadata' not in ns['MEASUREMENTS'][-1] and ('peerSample',) not in events
Process.wait = saved_wait

# Short caller/global budget cannot reach the checkpoint or run any proc read.
ns['DEADLINE'] = 103
clock.value = 100
events.clear()
actions[:] = ['timeout']
try:
    ns['run'](argv)
except subprocess.TimeoutExpired as error:
    assert error.timeout == 3
assert not any(e[0] in {'ownedWithoutReap','procRead'} for e in events)
assert ns['MEASUREMENTS'][-1]['effectiveTimeoutSeconds'] == 3

# Helper cancellation keeps #139's generic interruption receipt and kills/reaps;
# it never becomes a per-command timeout or permits post-timeout metadata.
ns['DEADLINE'] = 245
clock.value = 100
events.clear()
interrupted = TimeoutError('fixture deadline or termination')
actions[:] = [interrupted]
try:
    ns['run'](argv)
except TimeoutError as error:
    assert error is interrupted
else:
    raise AssertionError('interruption lost')
assert ns['MEASUREMENTS'][-1]['errorType'] == 'TimeoutError'
assert ('kill',) in events and ('reap',) in events and ('peerSample',) not in events

# An alarm inside proc acquisition remains cancellation, never "unavailable".
clock.value = 100
events.clear()
actions[:] = ['timeout']
proc_values['syscall'] = interrupted
try:
    ns['run'](argv)
except TimeoutError as error:
    assert error is interrupted
else:
    raise AssertionError('proc read swallowed the helper interruption')
assert ns['MEASUREMENTS'][-1]['errorType'] == 'TimeoutError'
assert ('reap',) in events and ('peerSample',) not in events
proc_values['syscall'] = b'0 0xdiscarded'

# Observation cost is charged to the original deadline, including slow launch.
clock.value = 104
events.clear()
actions[:] = ['timeout',0.25]
measurement = {}
def slow_observation(process):
    clock.value += 0.4
    return {'syscallCategory':'read','wchanCategory':'other'}
real_observer = ns['status_wait_observation']
ns['status_wait_observation'] = slow_observation
ns['run_status_child'](argv,ns['ENV'],15,100,measurement,pass_fds=(7,))
assert [e for e in events if e[0] == 'communicate'] == [('communicate',1),('communicate',9.599999999999994)]
assert measurement['waitObservation']['elapsedSeconds'] == 5.400000000000006
ns['status_wait_observation'] = real_observer

# Reconnect retains its old subprocess.run path and gets no new wait observer.
def reconnect_run(actual,**kwargs):
    assert actual == argv and kwargs['timeout'] == 15 and kwargs['pass_fds'] == (7,)
    events.append(('unchangedReconnectRun',))
    clock.value += 0.25
    return subprocess.CompletedProcess(actual,0,'M  src/000.txt\n','')
ns['subprocess'].run = reconnect_run
ns['ROLE'] = 'reconnect'
clock.value = 100
events.clear()
assert ns['run'](argv).returncode == 0
assert events == [('unchangedReconnectRun',),('cpuSnapshot',),('traceFinish',)]
assert 'waitObservation' not in ns['MEASUREMENTS'][-1]
ns['ROLE'] = 'b'

# One-second metadata budget is global, not a fresh allowance after expiry.
ns['post_timeout_peer_metadata'] = real_peer_sampler
metadata_calls = []
def metadata_run(actual,**kwargs):
    metadata_calls.append((actual,kwargs))
    assert actual[:2] == [sys.executable,'-c'] and actual[3] == 'cephfs-trial-a'
    assert kwargs['timeout'] == 1
    clock.value += 0.2
    return SimpleNamespace(returncode=0,stdout='{"available":false,"samples":[],"reason":"missing"}')
ns['subprocess'].run = metadata_run
clock.value = 244.001
assert real_peer_sampler(argv)['reason'] == 'insufficientOriginalHelperBudget' and not metadata_calls
clock.value = 244
assert real_peer_sampler(argv)['reason'] == 'missing' and len(metadata_calls) == 1
assert real_peer_sampler(['git','-C',str(ns['TASK_B'])])['reason'] == 'notPeerStatus'
def metadata_failed(actual,**kwargs):
    clock.value += 0.25
    return SimpleNamespace(returncode=1,stdout='discarded raw private content')
ns['subprocess'].run = metadata_failed
clock.value = 200
failed = real_peer_sampler(argv)
assert failed['reason'] == 'childFailedOrOversized' and failed['seconds'] == 0.25
assert 'private content' not in json.dumps(failed)
def metadata_timeout(actual,**kwargs):
    assert kwargs['timeout'] == 1
    clock.value += 1
    raise subprocess.TimeoutExpired(actual,1)
ns['subprocess'].run = metadata_timeout
clock.value = 200
assert real_peer_sampler(argv)['reason'] == 'metadataTimeout' and clock.value == 201
def metadata_interrupted(actual,**kwargs):
    raise interrupted
ns['subprocess'].run = metadata_interrupted
clock.value = 200
try:
    real_peer_sampler(argv)
except TimeoutError as error:
    assert error is interrupted
else:
    raise AssertionError('metadata read swallowed original helper cancellation')

# Execute the literal metadata child with in-memory index/proc-like files only.
def index_bytes(version=2):
    body = b'DIRC'+struct.pack('!II',version,3)
    for name in sorted([b'src/000.txt',b'src/001.txt',b'PRIVATE_UNRETAINED_PATH']):
        entry = struct.pack('!10I',1,2,3,4,5,6,33188,1000,1000,4096)+b'\0'*20
        entry += struct.pack('!H',len(name))+name+b'\0'
        body += entry+b'\0'*((-len(entry)) % 8)
    return body+hashlib.sha1(body).digest()

base = '/home/dev/work/cephfs-trial-a'
gitdir = '/home/dev/repos/cephfs-fixture/.git/worktrees/cephfs-trial-a'
files = {base+'/.git':('gitdir: '+gitdir+'\n').encode(),gitdir+'/index':index_bytes()}
stats = {base+'/'+name:SimpleNamespace(st_dev=7,st_ino=2**40+6,st_mode=33188,
          st_uid=1000,st_gid=1000,st_size=4096,st_mtime_ns=3000000004,st_ctime_ns=1000000002)
         for name in ['src/000.txt','src/001.txt']}
class MemoryPath:
    def __init__(self,path):
        self.path = str(path)
    def __truediv__(self,name):
        return MemoryPath(PurePosixPath(self.path)/name)
    @property
    def parent(self):
        return MemoryPath(PurePosixPath(self.path).parent)
    def __str__(self):
        return self.path
    def open(self,mode):
        assert mode == 'rb'
        if self.path not in files:
            raise FileNotFoundError()
        return io.BytesIO(files[self.path])
    def lstat(self):
        if self.path not in stats:
            raise FileNotFoundError()
        return stats[self.path]

def literal_metadata():
    output = io.StringIO()
    with patch.object(pathlib,'Path',MemoryPath),patch.object(sys,'argv',['metadata','cephfs-trial-a']),contextlib.redirect_stdout(output):
        exec(compile(ns['PEER_METADATA_SCRIPT'],'<literal-metadata-child>','exec'),{})
    return json.loads(output.getvalue())

sample = literal_metadata()
assert sample['available'] and len(sample['samples']) == 2
assert sample['samples'][0]['stat']['inodeLow32'] == sample['samples'][0]['index']['inodeLow32'] == 6
assert 'PRIVATE' not in json.dumps(sample) and '/home/' not in json.dumps(sample)
files[gitdir+'/index'] = index_bytes(3)
assert literal_metadata()['available']
body = index_bytes()[:-20]+b'TREE'+struct.pack('!I',0)
files[gitdir+'/index'] = body+hashlib.sha1(body).digest()
assert literal_metadata()['available']
body = index_bytes()[:-20]+b'link'+struct.pack('!I',0)
files[gitdir+'/index'] = body+hashlib.sha1(body).digest()
assert literal_metadata()['reason'] == 'invalidOrUnavailable'
files[gitdir+'/index'] = index_bytes(4)
assert literal_metadata()['reason'] == 'invalidOrUnavailable'
files[gitdir+'/index'] = index_bytes()[:-1]+b'X'
assert literal_metadata()['reason'] == 'invalidOrUnavailable'
files[gitdir+'/index'] = index_bytes()
del stats[base+'/src/001.txt']
sample = literal_metadata()
assert not sample['available'] and sample['samples'][1]['statReason'] == 'missing'
files[base+'/.git'] = b'gitdir: /private/foreign\n'
assert literal_metadata()['reason'] == 'invalidOrUnavailable'
print(json.dumps({'result':'PASS','checks':['single owned unreaped PID checkpoint, exit/reap races',
    'fixed categories, permission/unknown/cap redaction','EOF and exact timeout flags/output',
    'cancellation kills/reaps and retains generic interruption','original command/global budgets',
    'status CPU/Trace2 precede post-timeout sample','one-second metadata gate, two fixed index/stat tuples',
    'literal index checksum/version/missing/foreign safeguards'],
    'runtime':'fake clock/PID/proc/index only; no command, Git or cluster operation'}))
