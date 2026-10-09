#!/usr/bin/env python3
"""Finite inert check of the actual helper functions and outer failure payload.

No real subprocess, filesystem fixture, alarm, credentials or cluster operation.
"""
import ast
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace

source = Path(__file__).with_name('workspace-cephfs-trial.py').read_text()
tree = ast.parse(source)
functions = {node.name: node for node in tree.body if isinstance(node, ast.FunctionDef)}
failure_handler = tree.body[-1].handlers
failure_handler[0].body.insert(0, ast.Expr(value=ast.Call(
    func=ast.Name(id='capture_timeout',ctx=ast.Load()),
    args=[ast.Name(id='error',ctx=ast.Load())], keywords=[])))
argv = ['git', '-C', '/synthetic/task', 'status', '--porcelain=v1']

def namespace(fake_run, clock, budget=145):
    return {'subprocess':SimpleNamespace(run=fake_run, TimeoutExpired=subprocess.TimeoutExpired),
            'time':SimpleNamespace(monotonic=lambda:next(clock)), 'remaining':lambda:budget,
            'ENV':{}, 'MEASUREMENTS':[], 'PROCESSES':[], 'STARTUP_METADATA':{},
            'RUN_ID':'synthetic', 'ROLE':'b', 'START':100, 'DEADLINE':245,
            'os':SimpleNamespace(environ={'TRIAL_POD_UID':'synthetic-pod', 'TRIAL_NODE':'worker'}),
            'json':json}

def timeout_payload(stdout, stderr, budget=145):
    cap = min(15, budget)
    error = subprocess.TimeoutExpired(argv, cap, output=stdout, stderr=stderr)
    def fake_run(actual_argv, **kwargs):
        assert actual_argv == argv and kwargs['timeout'] == cap
        assert kwargs['text'] is True and kwargs['capture_output'] is True
        raise error
    ns = namespace(fake_run, iter([100, 100+cap, 100+cap, 100+cap]), budget)
    printed = []
    def capture_timeout(observed):
        assert observed is error, 'wrapper replaced the original timeout'
    ns['capture_timeout'] = capture_timeout
    ns['print'] = lambda output, **kwargs:printed.append(json.loads(output))
    body = [functions['bounded_output'], functions['run'],
            ast.Try(body=[ast.Expr(value=ast.Call(func=ast.Name(id='run',ctx=ast.Load()),
                       args=[ast.Name(id='argv',ctx=ast.Load())],
                       keywords=[ast.keyword(arg='expected',value=ast.Constant(value=73))]))],
                    handlers=failure_handler, orelse=[], finalbody=[])]
    ns['argv'] = argv
    try:
        exec(compile(ast.fix_missing_locations(ast.Module(body=body,type_ignores=[])),
                     '<actual-helper-timeout-and-failure>', 'exec'), ns)
    except SystemExit as exited:
        assert exited.code == 1
    else:
        raise AssertionError('original timeout did not reach the failure handler')
    payload = printed[0]
    record = payload['commands'][0]
    assert payload['errorType'] == 'TimeoutExpired' and len(payload['commands']) == 1
    assert record['argv'] == argv and record['expectedExit'] == 73 and record['exit'] is None
    assert record['errorType'] == 'TimeoutExpired' and record['effectiveTimeoutSeconds'] == cap
    assert record['seconds'] == cap and payload['elapsedSeconds'] == cap
    assert isinstance(record['stdout'], str) and isinstance(record['stderr'], str)
    assert len(record['stdout']) <= 400 and len(record['stderr']) <= 400
    return record

partial = timeout_payload(b'\xff' + b'x'*600, 'é'*600)
assert partial['stdout'].startswith('\ufffd') and len(partial['stdout']) == 400
assert partial['stderr'] == 'é'*400
empty = timeout_payload(None, None)
assert empty['stdout'] == empty['stderr'] == ''
clipped = timeout_payload('partial', b'partial', budget=3)
assert clipped['effectiveTimeoutSeconds'] == clipped['seconds'] == 3

result = SimpleNamespace(returncode=73, stdout='', stderr='')
def normal_run(actual_argv, **kwargs):
    assert actual_argv == argv and kwargs['timeout'] == 15
    return result
ns = namespace(normal_run, iter([100, 100.25]))
exec(compile(ast.Module(body=[functions['bounded_output'],functions['run']],type_ignores=[]),
             '<actual-helper-normal-status>', 'exec'), ns)
assert ns['run'](argv, expected=73) is result
assert ns['MEASUREMENTS'][0]['exit'] == ns['MEASUREMENTS'][0]['expectedExit'] == 73
assert ns['MEASUREMENTS'][0]['seconds'] == 0.25
print(json.dumps({'result':'PASS', 'checks':['15s timeout reaches actual failure payload',
    'exact argv/expected/null exit/effective cap/elapsed retained',
    'bytes/str/None normalized and capped at400characters',
    'remaining global budget still clips timeout', 'normal expected status unchanged'],
    'runtime':'mock subprocess and fake clock only; no cluster fixture'}))
