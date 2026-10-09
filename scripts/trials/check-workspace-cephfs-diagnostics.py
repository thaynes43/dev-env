#!/usr/bin/env python3
"""Finite actual-source diagnostics checks; no Git fixture or cluster operation.

Run under nice19. Fixed pipe payloads, fake subprocess/clock/CPU counters only;
no CPU burners, stress tools, busy loops or wide/looped tests on shared nodes.
"""
import ast
import json
import os
from pathlib import Path
import re
import resource
import select
import subprocess
import tempfile
import threading
import time
from types import SimpleNamespace

source = Path(__file__).with_name('workspace-cephfs-trial.py').read_text()
tree = ast.parse(source)
nodes = {node.name:node for node in tree.body if isinstance(node,(ast.FunctionDef,ast.ClassDef))}
ns = dict(os=os, Path=Path, re=re, resource=resource, select=select,
          tempfile=tempfile, threading=threading, time=time, json=json,
          DEADLINE=time.monotonic()+5)
selected = ['bounded_output','own_cpu_stat','cpu_delta','StatusTrace','run','record_phase','wip']
exec(compile(ast.Module(body=[nodes[name] for name in selected],type_ignores=[]),
             '<actual-helper-diagnostics>', 'exec'), ns)
# This existing receipt fixture isolates run() from the status Popen ownership
# seam, which has its own finite fake-clock/PID checker.
ns['run_status_child'] = lambda argv,env,timeout,start,measurement,**kwargs: ns['subprocess'].run(
    argv,text=True,capture_output=True,env=env,timeout=timeout,**kwargs)

# Exercise the real pipe reader and native-file cap with a fixed <100KiB input.
trace = ns['StatusTrace']()
native = {'event':'region_enter','category':'index','label':'refresh', 't_abs':0.1,
          'sid':'excluded-host', 'argv':['excluded-argv'],'env':'excluded-value'}
data = (json.dumps(native)+'\n').encode()*500
assert 65536 < len(data) < 100000
assert os.write(trace.write_fd,data) == len(data)
captured = trace.finish()
assert captured['available'] and captured['truncated']
assert captured['retainedBytes'] == captured['byteCap'] == 65536
assert len(captured['events']) == captured['eventCap'] == 48 and captured['omittedEvents'] > 0
assert 'excluded-' not in json.dumps(captured) and trace.file.closed

# The observer stays open while its bounded command owner is still alive, even
# after the helper deadline. Only finish/EOF may close the reader and stop Git's
# writer; a clock-based reader exit would turn the original timeout into SIGPIPE.
ns['DEADLINE'] = time.monotonic()-1
past_deadline = ns['StatusTrace']()
payload = (json.dumps(native)+'\n').encode()
assert os.write(past_deadline.write_fd,payload) == len(payload)
finished = past_deadline.finish()
assert finished['available'] and finished['events'][0]['label'] == 'refresh'
assert past_deadline.file.closed and not past_deadline.thread.is_alive()

# A private retention failure must not close the live writer's pipe. The native
# collector keeps draining/discarding; only EOF/owner finish may end its lifetime.
write_failed = threading.Event()
class FailedRetention:
    def __init__(self):
        self.file = tempfile.TemporaryFile(dir='/tmp',buffering=0)
    def write(self,value):
        write_failed.set()
        raise OSError('injected synthetic retention failure')
    def __getattr__(self,name):
        return getattr(self.file,name)
ns['tempfile'] = SimpleNamespace(TemporaryFile=lambda **kwargs:FailedRetention())
failed_retention = ns['StatusTrace']()
assert os.write(failed_retention.write_fd,payload) == len(payload)
assert write_failed.wait(timeout=2), 'retention failure was not exercised'
assert os.write(failed_retention.write_fd,payload) == len(payload), 'observer closed live writer'
discarded = failed_retention.finish()
assert discarded['available'] is False and discarded['nativeBytes'] == 2*len(payload)
assert discarded['retainedBytes'] == 0 and failed_retention.file.closed
assert not failed_retention.thread.is_alive()
ns['tempfile'] = tempfile

# Exercise the actual early-return cleanup branch with an inert stuck-thread
# stand-in; its read fd is owned and cleaned by this stand-in, never another writer.
stuck = object.__new__(ns['StatusTrace'])
stuck.read_fd, stuck.write_fd = os.pipe()
stuck.file = tempfile.TemporaryFile(dir='/tmp')
stuck.stop = threading.Event()
joins = []
stuck.thread = SimpleNamespace(join=lambda **kwargs:joins.append(kwargs),is_alive=lambda:True)
assert stuck.finish()['available'] is False and stuck.file.closed and stuck.stop.is_set()
assert joins == [{'timeout':0.5}]
os.close(stuck.read_fd)

# Resolve namespaced and host-style own-cgroup paths, never an assumed root.
files = {'/proc/self/cgroup':'0::/parent/own\n',
         '/proc/self/mountinfo':'1 0 0:1 /parent /sys/fs/cgroup rw - cgroup2 cgroup rw\n',
         '/sys/fs/cgroup/own/cpu.stat':'usage_usec 100\nnr_throttled 2\nthrottled_usec 30\n',
         '/sys/fs/cgroup/own/cpu.max':'25000 100000\n'}
class FakePath:
    def __init__(self,path):
        self.path = str(path)
    def read_text(self):
        return files[self.path]
    def relative_to(self,path):
        return Path(self.path).relative_to(path)
    def __truediv__(self,path):
        return FakePath(Path(self.path)/path)
    def __str__(self):
        return self.path
ns['Path'] = FakePath
before = ns['own_cpu_stat']()
assert before['identity'] == '/sys/fs/cgroup/own' and before['values']['usage_usec'] == 100
files['/proc/self/mountinfo'] = '1 0 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n'
files['/sys/fs/cgroup/parent/own/cpu.stat'] = files['/sys/fs/cgroup/own/cpu.stat']
files['/sys/fs/cgroup/parent/own/cpu.max'] = '25000 100000\n'
assert ns['own_cpu_stat']()['identity'] == '/sys/fs/cgroup/parent/own'
files['/proc/self/cgroup'] = '1:cpu:/own\n'
assert ns['own_cpu_stat']() is None
ns['Path'] = Path

argv = ['git','-C','/synthetic/task','status','--porcelain=v1','--untracked-files=all']
class FakeTrace:
    write_fd = 7
    finishes = 0
    def finish(self):
        self.finishes += 1
        return {'available':True,'events':[{'event':'region_enter','label':'refresh'}]}
trace_mock = FakeTrace()
ns['StatusTrace'] = lambda:trace_mock
ns['ENV'] = {'GIT_OPTIONAL_LOCKS':'0'}
ns['remaining'] = lambda:3
ns['MEASUREMENTS'] = []
ns['time'] = SimpleNamespace(monotonic=lambda:next(clock))
clock = iter([100,103])
child = iter([SimpleNamespace(ru_utime=1,ru_stime=2),SimpleNamespace(ru_utime=1.25,ru_stime=2.5)])
ns['resource'] = SimpleNamespace(RUSAGE_CHILDREN=resource.RUSAGE_CHILDREN,
                                getrusage=lambda which:next(child))
after = {'identity':before['identity'],'cpuMax':before['cpuMax'],
         'values':{'usage_usec':200,'nr_throttled':4,'throttled_usec':90}}
cgroups = iter([before,after])
ns['own_cpu_stat'] = lambda:next(cgroups)
error = subprocess.TimeoutExpired(argv,3,output=b'partial',stderr=None)
def fake_run(actual,**kwargs):
    assert actual == argv and kwargs['timeout'] == 3
    assert kwargs['env']['GIT_OPTIONAL_LOCKS'] == '0'
    assert kwargs['env']['GIT_TRACE2_EVENT'] == '/dev/fd/7'
    assert kwargs['env']['GIT_TRACE2_CONFIG_PARAMS'] == kwargs['env']['GIT_TRACE2_ENV_VARS'] == ''
    assert kwargs['pass_fds'] == (7,)
    raise error
ns['subprocess'] = SimpleNamespace(run=fake_run,TimeoutExpired=subprocess.TimeoutExpired)
try:
    ns['run'](argv)
except subprocess.TimeoutExpired as observed:
    assert observed is error
else:
    raise AssertionError('timeout was replaced')
record = ns['MEASUREMENTS'][0]
assert record['seconds'] == record['effectiveTimeoutSeconds'] == 3 and record['exit'] is None
assert record['cpu']['childUserSeconds'] == 0.25 and record['cpu']['childSystemSeconds'] == 0.5
assert record['cpu']['ownCgroupV2']['delta'] == {'usage_usec':100,'nr_throttled':2,'throttled_usec':60}
assert record['trace2']['events'][0]['label'] == 'refresh' and trace_mock.finishes == 1
clock = iter([100,100.25])
child = iter([SimpleNamespace(ru_utime=0,ru_stime=0),SimpleNamespace(ru_utime=0.1,ru_stime=0.05)])
cgroups = iter([before,after])
ns['remaining'] = lambda:145
normal = SimpleNamespace(returncode=0,stdout='M  src/000.txt\n?? new.txt\n',stderr='')
def successful_run(actual,**kwargs):
    assert actual == argv and kwargs['timeout'] == 15 and kwargs['pass_fds'] == (7,)
    return normal
ns['subprocess'].run = successful_run
assert ns['run'](argv) is normal
assert ns['MEASUREMENTS'][1]['seconds'] == 0.25 and ns['MEASUREMENTS'][1]['exit'] == 0
assert trace_mock.finishes == 2
clock = iter([100,101])
child = iter([SimpleNamespace(ru_utime=0,ru_stime=0),SimpleNamespace(ru_utime=0.01,ru_stime=0.02)])
cgroups = iter([before,after])
interrupted = TimeoutError('fixture deadline or termination')
def interrupted_run(actual,**kwargs):
    assert actual == argv and kwargs['timeout'] == 15 and kwargs['pass_fds'] == (7,)
    raise interrupted
ns['subprocess'].run = interrupted_run
try:
    ns['run'](argv)
except TimeoutError as observed:
    assert observed is interrupted
else:
    raise AssertionError('helper interruption was replaced')
record = ns['MEASUREMENTS'][2]
assert len(ns['MEASUREMENTS']) == 3 and record['argv'] == argv
assert record['seconds'] == 1 and record['effectiveTimeoutSeconds'] == 15
assert record['exit'] is None and record['errorType'] == 'TimeoutError'
assert record['cpu']['childUserSeconds'] == 0.01 and record['cpu']['childSystemSeconds'] == 0.02
assert record['trace2']['events'][0]['label'] == 'refresh' and trace_mock.finishes == 3
unavailable = ns['cpu_delta'](SimpleNamespace(ru_utime=0,ru_stime=0),
                            SimpleNamespace(ru_utime=0,ru_stime=0),None,None)
assert unavailable['ownCgroupV2']['available'] is False

# The exact WIP result stays independent of diagnostic metadata, which is sampled
# only after status success and once per task/phase. Bulk durations survive failure.
ns.update(PHASES={},STAT_SAMPLES=set(),DIAGNOSTIC_METADATA={},ROLE='a',START=100,
          time=SimpleNamespace(monotonic=lambda:101),digest=lambda path:'fixed-hash',
          common=lambda path:'/synthetic/common')
stat = SimpleNamespace(st_dev=1,st_ino=2,st_mode=33188,st_size=4,st_mtime_ns=5,st_ctime_ns=6)
class StatPath(FakePath):
    name = 'task'
    def __truediv__(self,path):
        return StatPath(Path(self.path)/path)
    def stat(self):
        return stat
calls = []
def git(path,*args):
    calls.append(args)
    values = {'status':'M  src/000.txt\n?? new.txt\n','rev-parse':
              '/synthetic/index' if args[-1] == 'index' else 'fixed-head',
              'ls-files':'src/000.txt\n  ctime: 1:2\n'}
    return SimpleNamespace(stdout=values[args[0]])
ns['git'] = git
first = ns['wip'](StatPath('/synthetic/task'))
assert calls[0] == ('status','--porcelain=v1','--untracked-files=all')
assert first['status'] == 'M  src/000.txt\n?? new.txt\n'
assert len(ns['DIAGNOSTIC_METADATA']['indexStatSamples']) == 1
assert ns['wip'](StatPath('/synthetic/task')) == first
assert len(ns['DIAGNOSTIC_METADATA']['indexStatSamples']) == 1
ns['record_phase']('build-done',{'installArchiveSeconds':0.5},'observe')
assert ns['wip'](StatPath('/synthetic/task')) == first
assert len(ns['DIAGNOSTIC_METADATA']['indexStatSamples']) == 2
assert ns['PHASES']['build-done']['value']['installArchiveSeconds'] == 0.5
print(json.dumps({'result':'PASS','checks':['64KiB native Trace2/48 sanitized events',
    'past-global-deadline reader preserves writer until owner finish',
    'retention write failure discards without closing live writer',
    'join-timeout branch closes native temporary file',
    'own cgroup resolution and unavailable result','original timeout/argv/global cap unchanged',
    'child CPU and own throttling deltas survive timeout','exact staged/untracked WIP unchanged',
    'original helper interruption retains in-flight command/CPU/Trace2 and rethrows',
    'two-path stat samples deduplicated after status','bulk phase durations retained'],
    'runtime':'fixed pipe payload and mocks only; no Git fixture or cluster'}))
