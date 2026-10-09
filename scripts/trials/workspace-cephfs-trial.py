#!/usr/bin/env python3
"""PRIVATE bounded synthetic cross-node trial. Never run outside reviewed fixture Jobs.

Two peers own separate task worktrees. Their coordination is a test harness, NOT
platform ownership or fencing. No network remotes, auth, models or user repos.
"""
import errno
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import select
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time

ROLE = os.environ.get('TRIAL_ROLE')
assert ROLE in {'a', 'b', 'reconnect'}, 'reviewed role required'
assert os.environ.get('TRIAL_CONTAINER') == 'disposable-cephfs', 'fixture-only'
START = time.monotonic()
DEADLINE = START + (145 if ROLE in {'a', 'b'} else 40)
REPOS = Path('/home/dev/repos')
CODEX = Path('/home/dev/codex')
WORK = Path('/home/dev/work')
CONTROL = REPOS / '.cephfs-trial'
REF = REPOS / 'cephfs-fixture'
ANCHOR = CODEX / 'cephfs-fixture'
TASK_A = WORK / 'cephfs-trial-a'
TASK_B = WORK / 'cephfs-trial-b'
ADMIN_LOCK = REF / '.git' / 'fixture-admin.lock'
RUN_ID = 'workspace-cephfs-trial-20261009-v1'
PROCESSES = []
MEASUREMENTS = []
STARTUP_METADATA = {}
DIAGNOSTIC_METADATA = {}
PHASES = {}
STAT_SAMPLES = set()
ENV = dict(os.environ)
ENV.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null',
           GIT_TERMINAL_PROMPT='0', GIT_OPTIONAL_LOCKS='0', GIT_AUTHOR_NAME='CephFS fixture',
           GIT_COMMITTER_NAME='CephFS fixture',
           GIT_AUTHOR_EMAIL='fixture@example.invalid',
           GIT_COMMITTER_EMAIL='fixture@example.invalid', LC_ALL='C')

def shutdown(signum, frame):
    for proc in PROCESSES:
        if proc.poll() is None:
            os.killpg(proc.pid, signal.SIGTERM)
    raise TimeoutError('fixture deadline or termination')
signal.signal(signal.SIGALRM, shutdown)
signal.signal(signal.SIGTERM, shutdown)
signal.alarm(145 if ROLE in {'a', 'b'} else 40)

def remaining():
    value = DEADLINE - time.monotonic()
    if value <= 0:
        raise TimeoutError('fixture deadline')
    return value

def bounded_output(value):
    if value is None:
        return ''
    if isinstance(value, bytes):
        value = value.decode('utf-8', errors='replace')
    return value[:400]

def own_cpu_stat():
    # Resolve this process's cgroup through its mount root. Reading the root
    # cpu.stat by assumption can accidentally measure the whole node instead.
    try:
        member = next(line[3:] for line in Path('/proc/self/cgroup').read_text().splitlines()
                      if line.startswith('0::'))
        for line in Path('/proc/self/mountinfo').read_text().splitlines():
            left, right = line.split(' - ', 1)
            if right.split()[0] != 'cgroup2':
                continue
            fields = left.split()
            relative = Path(member).relative_to(fields[3])
            directory = Path(fields[4]) / relative
            values = {}
            for entry in (directory/'cpu.stat').read_text()[:4096].splitlines():
                key, value = entry.split()
                if key in {'usage_usec','user_usec','system_usec','nr_periods',
                           'nr_throttled','throttled_usec'}:
                    values[key] = int(value)
            assert 'usage_usec' in values
            return {'values':values, 'identity':str(directory),
                    'cpuMax':(directory/'cpu.max').read_text()[:80].strip()}
    except (OSError, ValueError, StopIteration, AssertionError):
        pass
    return None

def cpu_delta(child_before, child_after, before, after):
    value = {'childUserSeconds':child_after.ru_utime-child_before.ru_utime,
             'childSystemSeconds':child_after.ru_stime-child_before.ru_stime}
    if before and after and before['identity'] == after['identity']:
        delta = {key:after['values'][key]-number for key,number in before['values'].items()
                 if key in after['values']}
        value['ownCgroupV2'] = {'available':all(number >= 0 for number in delta.values()),
                               'cpuMax':after['cpuMax'], 'delta':delta}
    else:
        value['ownCgroupV2'] = {'available':False, 'reason':'own cgroup v2 counters unavailable'}
    return value

class StatusTrace:
    """Drain native Trace2 under the command deadline; retain at most 64KiB.

    The blocking pipe reader never generates load and stops when Git exits or
    the command is killed. /tmp is private; no trace writes touch CephFS.
    """
    def __init__(self):
        self.read_fd, self.write_fd = os.pipe()
        self.file = tempfile.TemporaryFile(dir='/tmp',buffering=0)
        self.stop = threading.Event()
        self.total = 0
        self.kept = 0
        self.error = False
        self.drain_truncated = False
        self.thread = threading.Thread(target=self.collect, daemon=True)
        self.thread.start()

    def collect(self):
        drained_after_stop = 0
        try:
            # Only the command owner stops this reader, after subprocess.run
            # has terminated/reaped Git. Closing at the global deadline can
            # SIGPIPE a still-running Git before its original timeout arrives.
            while True:
                if not select.select([self.read_fd], [], [], 0.1)[0]:
                    if self.stop.is_set():
                        break
                    continue
                chunk = os.read(self.read_fd, 8192)
                if not chunk:
                    break
                self.total += len(chunk)
                retained = chunk[:max(0, 65536-self.kept)]
                if not self.error:
                    try:
                        self.file.write(retained)
                        self.kept += len(retained)
                    except (OSError, ValueError):
                        # Retention is an observer. Keep draining/discarding
                        # until Git's owner stops it; never SIGPIPE live Git
                        # because the private diagnostic file failed.
                        self.error = True
                if self.stop.is_set():
                    drained_after_stop += len(chunk)
                    if drained_after_stop >= 65536:
                        self.drain_truncated = True
                        break
        except (OSError, ValueError):
            self.error = True
        finally:
            os.close(self.read_fd)

    def finish(self):
        os.close(self.write_fd)
        self.stop.set()
        self.thread.join(timeout=0.5)
        if self.thread.is_alive():
            self.file.close()
            return {'available':False, 'reason':'bounded Trace2 collector did not finish'}
        try:
            self.file.seek(0)
            raw = self.file.read(65536)
        except (OSError, ValueError):
            self.error = True
            raw = b''
        finally:
            self.file.close()
        events = []
        event_count = 0
        for line in raw.splitlines():
            try:
                native = json.loads(line)
            except (ValueError, UnicodeDecodeError):
                continue
            kind = native.get('event')
            if kind not in {'region_enter','region_leave','data','exit'}:
                continue
            event = {'event':kind}
            # No SID/hostname, arbitrary config/env values, paths or raw argv.
            for key in ['category','label','key']:
                item = native.get(key)
                if isinstance(item,str) and re.fullmatch(r'[A-Za-z0-9_./-]{1,80}',item) and not item.startswith('/'):
                    event[key] = item
            for key in ['t_abs','t_rel','nesting','code']:
                if isinstance(native.get(key),(int,float)):
                    event[key] = native[key]
            if kind == 'data':
                item = str(native.get('value',''))
                if not re.fullmatch(r'[0-9]+(?:\.[0-9]+)?',item):
                    continue
                event['value'] = float(item) if '.' in item else int(item)
            event_count += 1
            events.append(event)
            events = events[-48:]
        return {'available':not self.error and self.total > 0,
                'nativeBytes':self.total, 'retainedBytes':self.kept,
                'byteCap':65536, 'truncated':self.total > self.kept,
                'drainTruncated':self.drain_truncated,
                'eventCap':48, 'omittedEvents':max(0,event_count-48), 'events':events}

class GitWaitMarker:
    """Fixture-only 64-byte parent-owned memfd; same-ABI C reads atomic storage.

    Initialization reads private immutable image files only. There are no marker
    writes to CephFS, reader child, sampling thread or raw-memory Python reads.
    """
    _verified = None
    retained = []
    executable = '/opt/dev-env/trials/git-wait'
    reader = '/opt/dev-env/trials/libgit-wait-reader.so'
    fields = ['sourceCommit','baseImage','debianVersion','upstreamCommit',
              'debianDSCSHA256','debianOrigSHA256','debianOverlaySHA256',
              'debianSeriesSHA256','markerPatchSHA256','markerSourceSHA256',
              'markerHeaderSHA256','buildOptionsSHA256','executableSHA256',
              'readerSHA256','helperSHA256','statMacros','postLinkStrip',
              'executableByteCap','readerByteCap']

    @classmethod
    def interface(cls):
        if cls._verified is None:
            import ctypes
            with open('/opt/dev-env/trials/git-wait-provenance.json','rb') as stream:
                raw = stream.read(4097)
            assert len(raw) <= 4096
            record = json.loads(raw)
            assert record['abiVersion'] == 1 and record['platform'] == 'linux-amd64'
            assert record['artifactVerification'] == 'hostedArtifactVerified'
            assert record['debianVersion'] == '1:2.39.5-0+deb12u3'
            assert record['baseImage'] == ('ghcr.io/thaynes43/dev-env:2.9.1@sha256:'
                'a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf')
            assert record['executable'] == cls.executable and record['reader'] == cls.reader
            assert record['debianPatchCount'] == 7 and not record['historicalCompilerEquivalence']
            assert re.fullmatch('[0-9a-f]{40}',record['sourceCommit'])
            assert record['sourceCommit'] == os.environ.get('TRIAL_HELPER_SOURCE_COMMIT')
            assert re.fullmatch('[0-9a-f]{40}',record['upstreamCommit'])
            assert record['helperSHA256'] == os.environ.get('TRIAL_HELPER_SHA256')
            assert record['postLinkStrip'] == '--strip-unneeded'
            assert record['executableByteCap'] == 8*1024*1024
            assert record['readerByteCap'] == 256*1024
            assert record['statMacros'].keys() == {'USE_STDEV','USE_NSEC'}
            assert all(type(value) is bool for value in record['statMacros'].values())
            for field in cls.fields:
                if field.endswith('SHA256'):
                    assert re.fullmatch('[0-9a-f]{64}',record[field])
            for path, field, cap in [(cls.executable,'executableSHA256',8*1024*1024),
                                     (cls.reader,'readerSHA256',256*1024)]:
                with open(path,'rb') as stream:
                    content = stream.read(cap+1)
                assert len(content) <= cap and hashlib.sha256(content).hexdigest() == record[field]
            library = ctypes.CDLL(cls.reader)
            library.gwm_parent_create.argtypes = [ctypes.c_int]
            library.gwm_parent_create.restype = ctypes.c_void_p
            library.gwm_parent_close.argtypes = [ctypes.c_void_p]
            library.gwm_parent_close.restype = None
            library.gwm_snapshot.argtypes = [ctypes.c_void_p,ctypes.POINTER(ctypes.c_uint32)]
            library.gwm_snapshot.restype = ctypes.c_int
            metadata = {field:record[field] for field in cls.fields}
            metadata['executable'] = cls.executable
            metadata['markerBytes'] = 64
            assert len(json.dumps(metadata).encode()) <= 4096
            cls._verified = (ctypes,library,metadata)
        return cls._verified

    def __init__(self):
        self.fd, self.mapping, self.library = None, None, None
        self.sampled = False
        try:
            self.ctypes, self.library, self.metadata = self.interface()
            self.fd = os.memfd_create('git-wait',os.MFD_CLOEXEC | os.MFD_ALLOW_SEALING)
            os.ftruncate(self.fd,64)
            fcntl.fcntl(self.fd,fcntl.F_ADD_SEALS,
                        fcntl.F_SEAL_SHRINK | fcntl.F_SEAL_GROW | fcntl.F_SEAL_SEAL)
            self.mapping = self.library.gwm_parent_create(self.fd)
            if not self.mapping:
                raise RuntimeError('native marker mapping unavailable')
        except BaseException:
            self.close()
            raise

    def snapshot(self):
        if self.sampled:
            return {'available':False,'reason':'alreadySampled'}
        self.sampled = True
        operation = self.ctypes.c_uint32()
        result = self.library.gwm_snapshot(self.mapping,self.ctypes.byref(operation))
        scopes = {1:'refreshLstat',2:'contentOpenWrapper',3:'smallFileContentRead'}
        if result == 0 and operation.value in scopes:
            return {'available':True,'scope':scopes[operation.value],'abiVersion':1}
        reasons = {1:'missing',2:'unsupported',3:'invalidABI',4:'transitionInProgress',
                   5:'changedDuringRead',6:'sequenceWrap',7:'writerOverlapOrFault',8:'idle'}
        return {'available':False,'reason':reasons.get(result,'unsupportedResult')}

    def close(self):
        if self.mapping is not None:
            self.library.gwm_parent_close(self.mapping)
            self.mapping = None
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None


def status_wait_observation(process, marker=None):
    # Only this calling parent can reap its status child. No poll/wait or timer
    # thread runs between the communicate checkpoint and these proc reads.
    # A just-exited, unreaped child may be unavailable, but cannot be PID-reused.
    result = {'syscallCategory':'unavailable', 'wchanCategory':'unavailable'}
    if marker is not None:
        result['gitOperation'] = {'available':False,'reason':'childOwnershipUnavailable'}
    if process.returncode is not None:
        return dict(result, reason='alreadyReaped')
    if signal.getsignal(signal.SIGCHLD) != signal.SIG_DFL:
        return dict(result, reason='uncertainReaper')
    try:
        # Prove it remains our unreaped child without consuming its exit status.
        exited = os.waitid(os.P_PID,process.pid,os.WEXITED | os.WNOHANG | os.WNOWAIT)
    except TimeoutError:
        raise
    except (ChildProcessError,OSError):
        return dict(result, reason='childOwnershipUnavailable')
    if exited is not None:
        return dict(result, reason='exitedBeforeObservation')
    if os.uname().machine != 'x86_64':
        return dict(result, reason='unsupportedArchitecture')
    if marker is not None:
        # ONE same-ABI atomic snapshot after this parent's exact unreaped-child
        # gate. No retry, raw memory decoding, PID/proc polling or reader child.
        result['gitOperation'] = marker.snapshot()
    categories = {
        'metadata':{4,5,6,21,89,191,192,193,194,195,196,262,332},
        'open':{2,257,437}, 'read':{0,17,19,295,327},
        'directory':{78,217}, 'memoryMap':{9,25}, 'futex':{202},
    }
    for name, cap in [('syscall',1024),('wchan',128)]:
        try:
            with open('/proc/%d/%s' % (process.pid,name),'rb') as stream:
                raw = stream.read(cap)
            if len(raw) == cap:
                result[name+'Reason'] = 'capped'
                continue
            value = raw.decode('ascii').strip()
            if name == 'syscall':
                # Discard all arguments, register addresses and pointers.
                token = value.split()[0]
                if token == 'running':
                    result['syscallCategory'] = 'running'
                elif token == '-1':
                    result['syscallCategory'] = 'blockedOutsideSyscall'
                else:
                    number = int(token)
                    result['syscallCategory'] = next(
                        (key for key, numbers in categories.items() if number in numbers),'other')
            elif value in {'0',''}:
                result['wchanReason'] = 'runningOrHidden'
            elif value.startswith(('ceph_mdsc_','__ceph_mdsc_')):
                result['wchanCategory'] = 'cephMetadata'
            elif value.startswith(('ceph_','__ceph_')):
                result['wchanCategory'] = 'cephOther'
            elif value.startswith(('folio_wait','wait_on_page','io_schedule')):
                result['wchanCategory'] = 'pageOrIOWait'
            elif value in {'wait_woken','schedule','schedule_timeout','futex_wait_queue'}:
                result['wchanCategory'] = 'wait'
            else:
                result['wchanCategory'] = 'other'
        except TimeoutError:
            # The helper's alarm/termination signal must still abort the owner.
            raise
        except PermissionError:
            result[name+'Reason'] = 'permissionDenied'
        except FileNotFoundError:
            result[name+'Reason'] = 'exitedOrMissing'
        except (OSError,ValueError,IndexError,UnicodeError):
            result[name+'Reason'] = 'unavailable'
    assert len(json.dumps(result).encode()) <= 1024
    return result

# One post-timeout metadata child, never a warmup. It reads the exact synthetic
# linked-worktree index (bounded SHA-1 v2/v3 format) and two fixed peer paths.
# No Git command or descendants, raw index bytes, arbitrary paths or PID output.
# Format: https://github.com/git/git/blob/v2.39.5/Documentation/gitformat-index.txt
PEER_METADATA_SCRIPT = r'''
import hashlib,json,os,struct,sys
from pathlib import Path
task=sys.argv[1]
assert task in {'cephfs-trial-a','cephfs-trial-b'}
base=Path('/home/dev/work')/task
index=Path('/home/dev/repos/cephfs-fixture/.git/worktrees')/task/'index'
result={'available':False,'samples':[],'indexByteCap':65536,'sampleCap':2}
try:
    with (base/'.git').open('rb') as stream:
        pointer=stream.read(513)
    assert pointer == ('gitdir: '+str(index.parent)+'\n').encode()
    with index.open('rb') as stream:
        data=stream.read(65537)
    assert 32 <= len(data) <= 65536
    assert data[:4] == b'DIRC' and hashlib.sha1(data[:-20]).digest() == data[-20:]
    version,count=struct.unpack_from('!II',data,4)
    assert version in {2,3} and count <= 2048
    offset=12
    tuples={}
    for _ in range(count):
        begin=offset
        assert offset+62 <= len(data)-20
        fields=struct.unpack_from('!10I',data,offset)
        flags=struct.unpack_from('!H',data,offset+60)[0]
        offset+=62
        if flags & 0x4000:
            assert version == 3 and offset+2 <= len(data)-20
            offset+=2
        end=data.index(b'\0',offset,len(data)-20)
        name=data[offset:end]
        if name in {b'src/000.txt',b'src/001.txt'}:
            assert name not in tuples and not flags & 0x3000
            tuples[name]=dict(zip(['ctimeSeconds','ctimeNs','mtimeSeconds','mtimeNs',
                                  'device','inodeLow32','mode','uid','gid','size'],fields))
        offset=begin+((end+1-begin+7)//8)*8
        assert offset <= len(data)-20
    while offset < len(data)-20:
        assert offset+8 <= len(data)-20
        signature,length=struct.unpack_from('!4sI',data,offset)
        # Split/sparse/unknown mandatory extensions cannot be interpreted here.
        assert b'A' <= signature[:1] <= b'Z'
        offset+=8+length
        assert offset <= len(data)-20
    for name in ['src/000.txt','src/001.txt']:
        sample={'path':name,'index':tuples.get(name.encode())}
        try:
            st=(base/name).lstat()
            sample['stat']={'device':st.st_dev,'inodeLow32':st.st_ino & 0xffffffff,
                            'mode':st.st_mode,'uid':st.st_uid,'gid':st.st_gid,
                            'size':st.st_size,'mtimeNs':st.st_mtime_ns,'ctimeNs':st.st_ctime_ns}
        except FileNotFoundError:
            sample['statReason']='missing'
        except OSError:
            sample['statReason']='unavailable'
        result['samples'].append(sample)
    result['available']=all(s.get('index') is not None and 'stat' in s for s in result['samples'])
except FileNotFoundError:
    result['reason']='missing'
except PermissionError:
    result['reason']='permissionDenied'
except (OSError,ValueError,AssertionError,struct.error):
    result['reason']='invalidOrUnavailable'
print(json.dumps(result,sort_keys=True))
'''

def post_timeout_peer_metadata(argv):
    result = {'available':False, 'samples':[], 'budgetSeconds':1}
    peer = TASK_B if ROLE == 'a' else TASK_A if ROLE == 'b' else None
    if peer is None or argv[2] != str(peer):
        return dict(result, reason='notPeerStatus')
    start = time.monotonic()
    if DEADLINE-start < 1:
        return dict(result, reason='insufficientOriginalHelperBudget')
    try:
        sampled = subprocess.run([sys.executable,'-c',PEER_METADATA_SCRIPT,peer.name],
                                 text=True,capture_output=True,env=ENV,
                                 timeout=min(1,DEADLINE-start))
        if sampled.returncode != 0 or len(sampled.stdout.encode()) > 4096:
            result['reason'] = 'childFailedOrOversized'
        else:
            result.update(json.loads(sampled.stdout))
    except subprocess.TimeoutExpired:
        # subprocess.run kills and genuinely reaps its own metadata child.
        result['reason'] = 'metadataTimeout'
    except TimeoutError:
        raise
    except (OSError,ValueError,TypeError):
        result['reason'] = 'unavailable'
    finally:
        result['seconds'] = time.monotonic()-start
    return result

def run_status_child(argv, env, timeout, start, measurement, **kwargs):
    # One sleeping communicate checkpoint, not polling or a second Git command.
    # This parent is the only reaper; the pipe collector never owns this PID.
    measurement['waitObservation'] = {'syscallCategory':'unavailable',
                                     'wchanCategory':'unavailable','reason':'checkpointNotReached'}
    marker, process = None, None
    try:
        if env.get('TRIAL_GIT_WAIT_MARKER') == '1':
            marker = GitWaitMarker()
            measurement['diagnosticGit'] = marker.metadata
            env = dict(env,DEV_ENV_GIT_WAIT_FD=str(marker.fd))
            kwargs['pass_fds'] = (*kwargs.get('pass_fds',()),marker.fd)
            kwargs['executable'] = marker.executable
        with subprocess.Popen(argv,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,
                              env=env,**kwargs) as process:
            deadline = min(DEADLINE,start+timeout)
            try:
                until_checkpoint = min(deadline,start+5)-time.monotonic()
                try:
                    stdout, stderr = process.communicate(timeout=max(0,until_checkpoint))
                    measurement['waitObservation']['reason'] = 'completedBeforeCheckpoint'
                except subprocess.TimeoutExpired:
                    if deadline-time.monotonic() <= 0:
                        raise
                    measurement['waitObservation'] = (status_wait_observation(process,marker)
                        if marker is not None else status_wait_observation(process))
                    measurement['waitObservation']['elapsedSeconds'] = time.monotonic()-start
                    assert len(json.dumps(measurement['waitObservation']).encode()) <= 1024
                    stdout, stderr = process.communicate(timeout=max(0,deadline-time.monotonic()))
            except subprocess.TimeoutExpired as error:
                process.kill()
                process.wait()  # Reaping precedes every optional peer sample.
                measurement['statusChildReaped'] = process.returncode is not None
                raise subprocess.TimeoutExpired(argv,timeout,output=error.output,stderr=error.stderr) from None
            except BaseException:
                process.kill()
                # Popen.__exit__ reaps even a helper alarm/cancellation; no samples.
                raise
            return subprocess.CompletedProcess(argv,process.returncode,stdout,stderr)
    finally:
        if marker is not None:
            if process is None or process.returncode is not None:
                marker.close()
            else:
                # A stuck/uncertain child is not proof of stop. Retain its private
                # map/FD until helper exit; never sample/reassign/claim cleanup.
                GitWaitMarker.retained.append(marker)
                measurement['markerCleanup'] = 'unreapedChildRetained'

def run(argv, *, expected=0, env=None, timeout=15):
    trace = None
    command_env = env or ENV
    kwargs = {}
    if argv[0] == 'git':
        kwargs['executable'] = '/usr/bin/git'
    if argv[:2] == ['git','-C'] and argv[3:] == ['status','--porcelain=v1','--untracked-files=all']:
        trace = StatusTrace()
        command_env = dict(command_env, GIT_TRACE2_EVENT='/dev/fd/'+str(trace.write_fd),
                           GIT_TRACE2_CONFIG_PARAMS='', GIT_TRACE2_ENV_VARS='')
        kwargs['pass_fds'] = (trace.write_fd,)
        child_before = resource.getrusage(resource.RUSAGE_CHILDREN)
        cgroup_before = own_cpu_stat()
    start = time.monotonic()
    measurement = {'operation':' '.join(argv[:3]), 'argv':argv, 'expectedExit':expected}
    effective_timeout = None
    try:
        effective_timeout = min(timeout, remaining())
        if trace and ROLE in {'a','b'}:
            result = run_status_child(argv,command_env,effective_timeout,start,measurement,**kwargs)
        else:
            result = subprocess.run(argv, text=True, capture_output=True,
                                    env=command_env, timeout=effective_timeout, **kwargs)
    except subprocess.TimeoutExpired as error:
        measurement.update(seconds=time.monotonic()-start, exit=None, errorType='TimeoutExpired',
                           effectiveTimeoutSeconds=effective_timeout,
                           stdout=bounded_output(error.stdout), stderr=bounded_output(error.stderr))
        MEASUREMENTS.append(measurement)
        raise
    except Exception as error:
        # The helper's signal handler raises TimeoutError. Retain the in-flight
        # command before finally attaches its bounded Trace2/CPU diagnostics.
        measurement.update(seconds=time.monotonic()-start, exit=None,
                           errorType=type(error).__name__, effectiveTimeoutSeconds=effective_timeout)
        MEASUREMENTS.append(measurement)
        raise
    else:
        measurement.update(seconds=time.monotonic()-start, exit=result.returncode)
        MEASUREMENTS.append(measurement)
    finally:
        if trace:
            measurement['cpu'] = cpu_delta(child_before, resource.getrusage(resource.RUSAGE_CHILDREN),
                                           cgroup_before, own_cpu_stat())
            measurement['trace2'] = trace.finish()
            # CPU/Trace2 and command duration belong only to the original Git,
            # not the optional metadata child. Generic interruptions never sample.
            if measurement.get('errorType') == 'TimeoutExpired' and measurement.get('statusChildReaped'):
                try:
                    measurement['postTimeoutPeerMetadata'] = post_timeout_peer_metadata(argv)
                except TimeoutError:
                    measurement['postTimeoutPeerMetadata'] = {
                        'available':False,'samples':[],'reason':'helperInterrupted'}
                    raise
    if expected is not None:
        if result.returncode != expected:
            # Every command here targets only the synthetic local fixture. Keep
            # bounded diagnostics so a failed trial does not hide the actual call.
            measurement['stderr'] = bounded_output(result.stderr)
            measurement['stdout'] = bounded_output(result.stdout)
            raise AssertionError('fixture command unexpected exit: expected %s, actual %s' %
                                 (expected, result.returncode))
    return result

def git(path, *args, **kwargs):
    return run(['git', '-C', str(path), *args], **kwargs)

def write_json(path, value):
    temporary = path.with_name(path.name + '.' + ROLE + '.tmp')
    with temporary.open('w') as stream:
        json.dump(value, stream, sort_keys=True)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)

def flag(name, value=None):
    value = value or {'role':ROLE, 'elapsed':time.monotonic()-START}
    write_json(CONTROL / (name + '.json'), value)
    record_phase(name, value, 'publish')

def record_phase(name, value, action):
    if name in {'inputs','build-done'}:
        PHASES.setdefault(name, {'action':action,'elapsedSeconds':time.monotonic()-START,
                                'value':value})

def wait(name, timeout=30):
    end = min(DEADLINE, time.monotonic()+timeout)
    path = CONTROL / (name + '.json')
    while time.monotonic() < end:
        if path.exists():
            value = json.loads(path.read_text())
            record_phase(name, value, 'observe')
            return value
        # Bounded coordination wait, not a CPU/load loop.
        time.sleep(0.2)
    raise TimeoutError('peer did not complete ' + name)

def startup_barrier():
    # Separate mount/container startup skew from the 30s lock-phase budget. No
    # lock is held until both peers are actually executing the helper.
    CONTROL.mkdir(exist_ok=True)
    peer = 'b' if ROLE == 'a' else 'a'
    flag('ready-' + ROLE, {'podUID':os.environ['TRIAL_POD_UID'],
                          'node':os.environ['TRIAL_NODE']})
    start = time.monotonic()
    STARTUP_METADATA['budgetSeconds'] = 60
    try:
        ready = wait('ready-' + peer, timeout=60)
    finally:
        STARTUP_METADATA['seconds'] = time.monotonic()-start
        STARTUP_METADATA['remainingHelperSeconds'] = max(0, DEADLINE-time.monotonic())
    assert STARTUP_METADATA['seconds'] <= 60, 'peer startup barrier exceeded 60s'
    assert ready['podUID'] != os.environ['TRIAL_POD_UID']
    assert ready['node'] != os.environ['TRIAL_NODE'], 'peers require different nodes'
    STARTUP_METADATA.update(peerPodUID=ready['podUID'], peerNode=ready['node'])

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

def common(path):
    result = git(path, 'rev-parse', '--git-common-dir').stdout.strip()
    return str((path / result).resolve()) if not result.startswith('/') else result

def mounted_paths():
    facts = []
    mount_types = {}
    for line in Path('/proc/self/mountinfo').read_text().splitlines():
        left, right = line.split(' - ', 1)
        mount_types[left.split()[4]] = right.split()[0]
    for path in [REPOS, CODEX, WORK]:
        assert str(path.resolve()) == str(path), 'paths must be real, not aliases'
        assert path.is_dir() and str(path) in mount_types, 'shared mount missing'
        assert mount_types[str(path)] == 'ceph', 'kernel CephFS mount required'
        facts.append({'path':str(path), 'fsType':mount_types[str(path)],
                      'device':path.stat().st_dev, 'inode':path.stat().st_ino})
    assert len({fact['device'] for fact in facts}) == 1, 'different shared filesystems'
    assert json.loads((REPOS/'.fixture-identity.json').read_text())['runID'] == RUN_ID
    return facts

def size_cap():
    files = 0
    logical_bytes = 0
    for root in [REPOS, CODEX, WORK]:
        for path in root.rglob('*'):
            files += 1
            if path.is_file():
                logical_bytes += path.stat().st_size
    assert files <= 2048, 'fixture inode cap exceeded'
    assert logical_bytes <= 16*1024*1024, 'fixture bytes cap exceeded'
    return {'entries':files, 'logicalBytes':logical_bytes,
            'entryCap':2048, 'logicalByteCap':16*1024*1024}

def wip(path):
    status = git(path, 'status', '--porcelain=v1', '--untracked-files=all').stdout
    index = Path(git(path,'rev-parse','--git-path','index').stdout.strip())
    if not index.is_absolute():
        index = path / index
    phase = 'postFileIO' if 'build-done' in PHASES else 'initialWIP'
    sample_key = (phase,str(path))
    if ROLE in {'a','b'} and sample_key not in STAT_SAMPLES:
        STAT_SAMPLES.add(sample_key)
        assert len(STAT_SAMPLES) <= 3, 'diagnostic sample cap exceeded'
        samples = []
        for name in ['src/000.txt','src/001.txt']:
            stat = (path/name).stat()
            samples.append({'path':name,'device':stat.st_dev,'inode':stat.st_ino,
                            'mode':stat.st_mode,'size':stat.st_size,
                            'mtimeNs':stat.st_mtime_ns,'ctimeNs':stat.st_ctime_ns})
        debug = git(path,'ls-files','--debug','--','src/000.txt','src/001.txt').stdout
        DIAGNOSTIC_METADATA.setdefault('indexStatSamples',[]).append(
            {'task':path.name,'phase':phase,'indexDebug':bounded_output(debug),
             'indexDebugTruncated':len(debug) > 400, 'stat':samples})
    return {'status':status, 'fileHash':digest(path/'src'/'000.txt'),
            'indexFileSHA256':digest(index),
            'head':git(path,'rev-parse','HEAD').stdout.strip(), 'commonDir':common(path)}

def receipt(checks, metadata):
    assert max([m['seconds'] for m in MEASUREMENTS] or [0]) <= 15
    assert STARTUP_METADATA.get('seconds', 0) <= 60
    result = {'runID':RUN_ID, 'role':ROLE, 'podUID':os.environ['TRIAL_POD_UID'],
              'node':os.environ['TRIAL_NODE'], 'elapsedSeconds':time.monotonic()-START,
              'mounts':mounted_paths(), 'checks':checks, 'metadata':metadata,
              'commands':MEASUREMENTS, 'startup':STARTUP_METADATA, 'caps':size_cap(),
              'diagnostics':DIAGNOSTIC_METADATA, 'phases':PHASES,
              'scope':'synthetic storage/Git mechanics only; no platform fencing, real agent, auth, household/device latency or outage-recovery acceptance'}
    write_json(CONTROL/('result-'+ROLE+'.json'), result)
    print(json.dumps(result, sort_keys=True), flush=True)

def role_a():
    assert not REF.exists(), 'a fresh empty trial claim is required'
    CONTROL.mkdir(exist_ok=True)
    git(REPOS, 'init', '-b', 'main', str(REF))
    (REF/'src').mkdir()
    for index in range(128):
        content = (('fixture source %03d\n' % index).encode()*256)[:4096]
        (REF/'src'/('%03d.txt' % index)).write_bytes(content)
    (REF/'.gitignore').write_text('.fixture-build/' + chr(10))
    git(REF,'add','src','.gitignore')
    git(REF,'commit','-m','fixture base')
    initial = git(REF,'rev-parse','HEAD').stdout.strip()
    with (REF/'src'/'000.txt').open('a') as stream:
        stream.write('pinned fixture source\n')
    git(REF,'commit','-am','fixture pinned source')
    pinned = git(REF,'rev-parse','HEAD').stdout.strip()
    git(REF,'update-ref','refs/heads/race',initial)
    git(REF,'worktree','add','--detach',str(ANCHOR),pinned)
    flag('seed', {'initial':initial,'pinned':pinned,'commonDir':str(REF/'.git'),
                  'anchorCommonDir':common(ANCHOR)})

    with ADMIN_LOCK.open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        (CONTROL/'mkdir-lock').mkdir()
        flag('locks-held')
        wait('locks-blocked')
        (CONTROL/'mkdir-lock').rmdir()
        fcntl.flock(lock, fcntl.LOCK_UN)
    flag('locks-released')
    wait('locks-reacquired')

    # Actual Git update-ref holds its own ref lock while its prepared hook waits.
    hook = REF/'.git'/'hooks'/'reference-transaction'
    hook.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys, time
if sys.argv[1] == 'prepared' and os.environ.get('TRIAL_HOLD_REF') == '1':
    control = pathlib.Path('/home/dev/repos/.cephfs-trial')
    temporary = control/'git-held.tmp'
    with temporary.open('w') as stream:
        json.dump({'actualGitPreparedHook':True},stream)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary,control/'git-held.json')
    end = time.monotonic()+30
    while time.monotonic()<end:
        if (control/'git-continue.json').exists():
            sys.exit(0)
        time.sleep(0.2)
    sys.exit(1)
''')
    hook.chmod(0o755)
    held_env = dict(ENV, TRIAL_HOLD_REF='1')
    held_start = time.monotonic()
    proc = subprocess.Popen(['git','-C',str(REF),'update-ref','refs/heads/race',pinned],
                            env=held_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True, start_new_session=True)
    PROCESSES.append(proc)
    wait('git-held')
    assert (REF/'.git'/'refs'/'heads'/'race.lock').exists()
    wait('git-blocked')
    flag('git-continue')
    proc.communicate(timeout=min(10,remaining()))
    assert proc.returncode == 0, 'holding actual Git transaction failed'
    held_elapsed = time.monotonic()-held_start
    assert held_elapsed <= 35, 'held Git transaction exceeded its separate budget'
    flag('git-released')
    wait('git-reacquired')
    assert git(REF,'rev-parse','race').stdout.strip() == pinned

    # Two actual worktree administrative calls, serialized by a fixture flock.
    with ADMIN_LOCK.open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX | fcntl.LOCK_NB)
        git(REF,'worktree','add','-b','fixture/task-a',str(TASK_A),pinned)
        flag('admin-a-held')
        wait('admin-b-blocked')
        fcntl.flock(lock,fcntl.LOCK_UN)
    flag('admin-a-released')
    wait('admin-b-added')
    with (TASK_A/'src'/'000.txt').open('a') as stream:
        stream.write('private writer a staged WIP\n')
    git(TASK_A,'add','src/000.txt')
    flag('wip-a', wip(TASK_A))
    wait('wip-b')

    inputs = TASK_A/'.fixture-build'/'inputs'
    inputs.mkdir(parents=True)
    start = time.monotonic()
    for index in range(256):
        content = (('fixture dependency %03d\n' % index).encode()*512)[:8192]
        with (inputs/('%03d.dat' % index)).open('wb') as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
    elapsed = time.monotonic()-start
    assert elapsed <= 30, 'bounded source write phase exceeded 30s'
    flag('inputs', {'count':256,'bytes':sum(p.stat().st_size for p in inputs.iterdir()),
                    'sourceWriteSeconds':elapsed})
    wait('build-done',timeout=40)
    assert wip(TASK_A) == wait('wip-a')
    assert wip(TASK_B) == wait('wip-b')
    assert common(TASK_A) == common(TASK_B) == str(REF/'.git')
    git(REF,'fsck','--no-dangling')
    wait('b-complete')
    flag('final-state', {'pinned':pinned, 'a':wip(TASK_A),'b':wip(TASK_B),
                         'anchor':git(ANCHOR,'rev-parse','HEAD').stdout.strip(),
                         'build':wait('build-done'),
                         'heldGitTransactionSeconds':held_elapsed,
                         'heldGitTransactionBudgetSeconds':35,'hookWaitBudgetSeconds':30})
    receipt({'flockPeerExclusion':True,'mkdirPeerExclusion':True,
             'actualGitRefLockCollision':True,'serializedWorktreeAdd':True,
             'linkedPaths':True,'distinctWritersWIPPreserved':True,'gitFsck':True},
            wait('final-state'))

def role_b():
    CONTROL.mkdir(exist_ok=True)
    seed = wait('seed')
    assert seed['commonDir'] == common(ANCHOR) == str(REF/'.git')
    assert git(ANCHOR,'rev-parse','HEAD').stdout.strip() == seed['pinned']
    wait('locks-held')
    run(['flock','-n','-E','73',str(ADMIN_LOCK),'true'],expected=73)
    try:
        (CONTROL/'mkdir-lock').mkdir()
    except FileExistsError:
        pass
    else:
        raise AssertionError('cross-node mkdir did not exclude peer')
    flag('locks-blocked')
    wait('locks-released')
    run(['flock','-w','5',str(ADMIN_LOCK),'true'])
    (CONTROL/'mkdir-lock').mkdir()
    (CONTROL/'mkdir-lock').rmdir()
    flag('locks-reacquired')
    wait('git-held')
    blocked = git(REF,'update-ref','refs/heads/race',seed['pinned'],seed['initial'],expected=None)
    assert blocked.returncode != 0 and 'cannot lock ref' in blocked.stderr
    flag('git-blocked', {'exit':blocked.returncode,'gitRejectedHeldRefLock':True})
    wait('git-released')
    git(REF,'update-ref','refs/heads/race',seed['initial'])
    git(REF,'update-ref','refs/heads/race',seed['pinned'])
    flag('git-reacquired')
    wait('admin-a-held')
    argv = ['git','-C',str(REF),'worktree','add','-b','fixture/task-b',str(TASK_B),seed['pinned']]
    run(['flock','-n','-E','73',str(ADMIN_LOCK),*argv],expected=73)
    assert not TASK_B.exists()
    flag('admin-b-blocked')
    wait('admin-a-released')
    run(['flock','-w','5',str(ADMIN_LOCK),*argv])
    flag('admin-b-added')
    with (TASK_B/'src'/'000.txt').open('a') as stream:
        stream.write('private writer b staged WIP\n')
    git(TASK_B,'add','src/000.txt')
    flag('wip-b',wip(TASK_B))
    wait('wip-a')
    inputs = wait('inputs',timeout=40)
    installed = TASK_B/'.fixture-build'/'installed'
    installed.mkdir(parents=True)
    start = time.monotonic()
    file_times = []
    for index in range(256):
        one = time.monotonic()
        source = TASK_A/'.fixture-build'/'inputs'/('%03d.dat' % index)
        target = installed/source.name
        temporary = target.with_suffix('.tmp')
        shutil.copyfile(source,temporary)
        with temporary.open('rb') as stream:
            os.fsync(stream.fileno())
        os.replace(temporary,target)
        assert digest(source) == digest(target)
        file_times.append(time.monotonic()-one)
    archive = TASK_B/'.fixture-build'/'bundle.tar'
    with tarfile.open(archive,'w') as bundle:
        for path in sorted(installed.iterdir()):
            bundle.add(path,arcname=path.name,recursive=False)
    with archive.open('rb') as stream:
        os.fsync(stream.fileno())
    elapsed = time.monotonic()-start
    assert elapsed <= 30, 'bounded install/archive phase exceeded 30s'
    build = {'inputs':inputs,'installedFiles':256,'archiveBytes':archive.stat().st_size,
             'archiveSHA256':digest(archive),'installArchiveSeconds':elapsed,
             'fileCopyRenameP95Seconds':sorted(file_times)[242],
             'fileCopyRenameMaxSeconds':max(file_times)}
    flag('build-done',build)
    assert wip(TASK_A) == wait('wip-a')
    assert wip(TASK_B) == wait('wip-b')
    receipt({'flockPeerExclusionAndRelease':True,'mkdirPeerExclusionAndRelease':True,
             'actualGitRefLockCollisionAndRelease':True,'serializedWorktreeAdd':True,
             'linkedPaths':True,'boundedInstallArchive':True,'peerWIPUnchanged':True},build)
    flag('b-complete')

def role_reconnect():
    final = wait('final-state',timeout=5)
    a = json.loads((CONTROL/'result-a.json').read_text())
    b = json.loads((CONTROL/'result-b.json').read_text())
    assert a['node'] != b['node'], 'initial peers must be on distinct nodes'
    assert os.environ['TRIAL_POD_UID'] not in {a['podUID'],b['podUID']}
    assert wip(TASK_A) == final['a'] and wip(TASK_B) == final['b']
    assert git(ANCHOR,'rev-parse','HEAD').stdout.strip() == final['pinned']
    assert digest(TASK_B/'.fixture-build'/'bundle.tar') == final['build']['archiveSHA256']
    run(['flock','-w','5',str(ADMIN_LOCK),'true'])
    (CONTROL/'mkdir-lock').mkdir()
    (CONTROL/'mkdir-lock').rmdir()
    git(REF,'update-ref','refs/heads/reconnect-check',final['pinned'])
    assert git(REF,'rev-parse','reconnect-check').stdout.strip() == final['pinned']
    git(REF,'fsck','--no-dangling')
    receipt({'completedFixturePodReplacement':True,'linkedPathsPreserved':True,
             'wipIndexHeadPreserved':True,'archiveHashPreserved':True,
             'flockMkdirAndGitLockReusable':True,'gitFsck':True},final)

try:
    mounted_paths()
    if ROLE in {'a', 'b'}:
        startup_barrier()
    DIAGNOSTIC_METADATA['gitVersionBuild'] = bounded_output(run(['git','--version','--build-options']).stdout)
    {'a':role_a,'b':role_b,'reconnect':role_reconnect}[ROLE]()
except BaseException as error:
    for proc in PROCESSES:
        if proc.poll() is None:
            os.killpg(proc.pid,signal.SIGTERM)
    print(json.dumps({'runID':RUN_ID,'role':ROLE,'result':'FAIL',
                      'podUID':os.environ['TRIAL_POD_UID'],
                      'node':os.environ['TRIAL_NODE'],'commands':MEASUREMENTS,
                      'startup':STARTUP_METADATA,
                      'diagnostics':DIAGNOSTIC_METADATA, 'phases':PHASES,
                      'remainingGlobalBudgetSeconds':max(0,DEADLINE-time.monotonic()),
                      'errorType':type(error).__name__,
                      'reason':str(error)[:160],'elapsedSeconds':time.monotonic()-START}),flush=True)
    raise SystemExit(1)
