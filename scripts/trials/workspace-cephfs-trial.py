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
import shutil
import signal
import subprocess
import sys
import tarfile
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

def run(argv, *, expected=0, env=None, timeout=15):
    start = time.monotonic()
    result = subprocess.run(argv, text=True, capture_output=True,
                            env=env or ENV, timeout=min(timeout, remaining()))
    MEASUREMENTS.append({'operation': ' '.join(argv[:3]),
                         'seconds': time.monotonic()-start, 'exit': result.returncode})
    if expected is not None:
        assert result.returncode == expected, 'fixture command unexpected exit'
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
    write_json(CONTROL / (name + '.json'), value or {'role':ROLE, 'elapsed':time.monotonic()-START})

def wait(name, timeout=30):
    end = min(DEADLINE, time.monotonic()+timeout)
    path = CONTROL / (name + '.json')
    while time.monotonic() < end:
        if path.exists():
            return json.loads(path.read_text())
        # Bounded coordination wait, not a CPU/load loop.
        time.sleep(0.2)
    raise TimeoutError('peer did not complete ' + name)

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
    return {'status':status, 'fileHash':digest(path/'src'/'000.txt'),
            'indexFileSHA256':digest(index),
            'head':git(path,'rev-parse','HEAD').stdout.strip(), 'commonDir':common(path)}

def receipt(checks, metadata):
    assert max([m['seconds'] for m in MEASUREMENTS] or [0]) <= 15
    result = {'runID':RUN_ID, 'role':ROLE, 'podUID':os.environ['TRIAL_POD_UID'],
              'node':os.environ['TRIAL_NODE'], 'elapsedSeconds':time.monotonic()-START,
              'mounts':mounted_paths(), 'checks':checks, 'metadata':metadata,
              'commands':MEASUREMENTS, 'caps':size_cap(),
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
    inputs = wait('inputs')
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
    {'a':role_a,'b':role_b,'reconnect':role_reconnect}[ROLE]()
except BaseException as error:
    for proc in PROCESSES:
        if proc.poll() is None:
            os.killpg(proc.pid,signal.SIGTERM)
    print(json.dumps({'runID':RUN_ID,'role':ROLE,'result':'FAIL',
                      'errorType':type(error).__name__,
                      'reason':str(error)[:160],'elapsedSeconds':time.monotonic()-START}),flush=True)
    raise SystemExit(1)
