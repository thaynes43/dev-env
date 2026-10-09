#!/usr/bin/env python3
"""One finite native-Git fd smoke check on a tiny private temporary fixture.

Run under nice19. No Ceph/cluster, remotes, auth, models, load loops, CPU burners,
stress tools or wide/looped tests. Exactly one status command; all data removed.
"""
import ast
import json
import os
from pathlib import Path
import re
import resource
import select
import signal
import subprocess
import tempfile
import threading
import time

source = Path(__file__).with_name('workspace-cephfs-trial.py').read_text()
tree = ast.parse(source)
nodes = {node.name:node for node in tree.body if isinstance(node,(ast.FunctionDef,ast.ClassDef))}
deadline = time.monotonic()+40
def remaining():
    seconds = deadline-time.monotonic()
    if seconds <= 0:
        raise TimeoutError('local smoke global deadline')
    return seconds
env = {'PATH':os.environ['PATH'],'LC_ALL':'C','GIT_CONFIG_NOSYSTEM':'1',
       'GIT_CONFIG_GLOBAL':'/dev/null','GIT_TERMINAL_PROMPT':'0','GIT_OPTIONAL_LOCKS':'0',
       'GIT_AUTHOR_NAME':'Synthetic fixture','GIT_COMMITTER_NAME':'Synthetic fixture',
       'GIT_AUTHOR_EMAIL':'fixture@example.invalid','GIT_COMMITTER_EMAIL':'fixture@example.invalid'}
ns = dict(os=os,Path=Path,re=re,resource=resource,select=select,signal=signal,subprocess=subprocess,
          tempfile=tempfile,threading=threading,time=time,json=json,DEADLINE=deadline,
          remaining=remaining,ENV=env,MEASUREMENTS=[],ROLE='b',
          TASK_A=Path('/home/dev/work/cephfs-trial-a'),TASK_B=Path('/home/dev/work/cephfs-trial-b'))
selected = ['bounded_output','own_cpu_stat','cpu_delta','StatusTrace','status_wait_observation',
            'post_timeout_peer_metadata','run_status_child','run']
exec(compile(ast.Module(body=[nodes[name] for name in selected],type_ignores=[]),
             '<actual-helper-native-trace>', 'exec'), ns)
traces = []
trace_class = ns['StatusTrace']
def capture_trace():
    trace = trace_class()
    traces.append(trace)
    return trace
ns['StatusTrace'] = capture_trace
run = ns['run']
version = ns['bounded_output'](run(['git','--version','--build-options']).stdout)
with tempfile.TemporaryDirectory(prefix='cephfs-native-smoke-',dir='/tmp') as directory:
    repo = Path(directory)
    run(['git','-C',directory,'init','-b','main'])
    (repo/'src').mkdir()
    (repo/'src'/'000.txt').write_text('synthetic base\n')
    (repo/'src'/'001.txt').write_text('unchanged synthetic file\n')
    run(['git','-C',directory,'add','src'])
    run(['git','-C',directory,'commit','-m','synthetic base'])
    (repo/'src'/'000.txt').write_text('synthetic staged WIP\n')
    run(['git','-C',directory,'add','src/000.txt'])
    (repo/'untracked.txt').write_text('synthetic untracked WIP\n')
    before = {name:(repo/name).read_bytes() for name in ['src/000.txt','src/001.txt','untracked.txt','.git/index']}
    result = run(['git','-C',directory,'status','--porcelain=v1','--untracked-files=all'])
    assert result.stdout == 'M  src/000.txt\n?? untracked.txt\n' and len(result.stdout) <= 400
    assert {name:(repo/name).read_bytes() for name in before} == before
    record = ns['MEASUREMENTS'][-1]
    trace = record['trace2']
    assert record['exit'] == 0 and record['seconds'] < 15 and remaining() > 0
    assert trace['available'] and 0 < trace['retainedBytes'] <= trace['byteCap'] == 65536
    assert len(trace['events']) <= trace['eventCap'] == 48
    assert any(event['event'] in {'region_enter','region_leave'} for event in trace['events'])
    assert any(event.get('label') == 'refresh' for event in trace['events'])
    assert all('argv' not in event and 'sid' not in event for event in trace['events'])
    assert sum(command['argv'][3:4] == ['status'] for command in ns['MEASUREMENTS']) == 1
    assert len(traces) == 1 and traces[0].file.closed and not traces[0].thread.is_alive()
    for fd in [traces[0].read_fd,traces[0].write_fd]:
        try:
            os.fstat(fd)
        except OSError:
            pass
        else:
            raise AssertionError('native trace fd leaked')
assert not repo.exists(), 'private local fixture cleanup failed'
print(json.dumps({'result':'PASS','gitVersionBuild':version,'statusCalls':1,
    'statusSeconds':record['seconds'],'commandCapSeconds':15,'globalCapSeconds':40,
    'traceRetainedBytes':trace['retainedBytes'],'traceByteCap':trace['byteCap'],
    'phaseEvents':len(trace['events']),'eventCap':trace['eventCap'],
    'checks':['native Git emits phase events through actual pass_fds+/dev/fd',
              'exact staged/untracked output and index/file bytes preserved',
              'native trace file/fds/thread and tiny temporary repository cleaned'],
    'scope':'local native fd smoke only; no CephFS performance or cluster acceptance'}))
