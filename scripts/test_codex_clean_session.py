"""Finite fixtures. No installed client, tmux server or live socket is contacted."""

import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import importlib.util

SCRIPT = Path(__file__).with_name("codex-clean-session.py")
SPEC = importlib.util.spec_from_file_location("clean_session", SCRIPT)
launcher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(launcher)

FAKE = '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
with open(os.environ['FAKE_LOG'], 'a') as out:
    out.write(json.dumps([Path(sys.argv[0]).name, args]) + '\\n')
mode = os.environ.get('FAKE_MODE', '')
if Path(sys.argv[0]).name == 'client':
    if mode == 'unsupported':
        print('old client')
    else:
        print('--remote --no-alt-screen')
    sys.exit(1 if mode == 'client-error' else 0)
if args == ['-V']:
    print('tmux fixture')
    sys.exit(1 if mode == 'tmux-error' else 0)
if args[0] == 'new-session':
    if mode == 'collision':
        print('private task secret', file=sys.stderr)
        sys.exit(1)
    if mode == 'unicode-launch':
        os.write(1, b'\\xff')
        sys.exit(0)
    if mode == 'bad-pane':
        print('not-a-pane')
        sys.exit(0)
    command = args[-1]
    import shlex
    Path(os.environ['FAKE_ARGV']).write_text(json.dumps(shlex.split(command)[1:]))
    print('%7')
    sys.exit(0)
if args[0] == 'display-message':
    if mode == 'unicode-observe':
        os.write(1, b'\\xff')
        sys.exit(0)
    if args[args.index('-t') + 1] != '%7':
        raise SystemExit(78)
    print('1:123' if mode == 'dead' else '0:123')
    sys.exit(1 if mode == 'gone' else 0)
raise SystemExit(77)
'''


class LauncherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="clean-launch-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo ' $(touch SHOULD_NOT_EXIST)"
        self.repo.mkdir()
        self.sock = socket.socket(socket.AF_UNIX)
        self.addCleanup(self.sock.close)
        self.socket_path = self.root / "daemon.sock"
        self.sock.bind(str(self.socket_path))  # bind only; no listening/connection
        self.task = self.root / "task"
        self.text = "--resume\nTask 'quoted' $HOME `touch SHOULD_NOT_EXIST` ; $(false)\nUnicode: café\n"
        self.task.write_text(self.text)
        for name in ("client", "tmux"):
            path = self.root / name
            path.write_text(FAKE)
            path.chmod(0o700)
        self.log = self.root / "log"
        self.args = ["--client", str(self.root / "client"), "--tmux", str(self.root / "tmux"),
                     "--repo", str(self.repo), "--socket", str(self.socket_path),
                     "--model", "gpt-6.1-sol", "--effort", "medium", "--name", "task-unique-1",
                     "--task-file", str(self.task), "--observe-seconds", "0"]
        self.env = dict(os.environ, XDG_STATE_HOME=str(self.root / "state"), FAKE_LOG=str(self.log), FAKE_ARGV=str(self.root / "argv"))

    def invoke(self, extra=(), mode="", stdin=None):
        result = subprocess.run([sys.executable, str(SCRIPT), *self.args, *extra],
                                input=stdin, capture_output=True, text=True,
                                env=dict(self.env, FAKE_MODE=mode), timeout=10)
        return result.returncode, json.loads(result.stdout)

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_exact_fresh_command_and_quoting(self):
        code, receipt = self.invoke()
        self.assertEqual(code, 0)
        self.assertEqual(receipt['state'], 'client-running-unverified')
        argv = json.loads((self.root / 'argv').read_text())
        self.assertEqual(argv, [str(self.root / 'client'), '--remote', 'unix://' + str(self.socket_path),
                               '-C', str(self.repo), '-m', 'gpt-6.1-sol', '-c',
                               'model_reasoning_effort="medium"', '--no-alt-screen', '--', self.text])
        command = self.calls()[2][1][-1]
        # Actually parse the shell command with a harmless argv printer. This
        # catches expansion/injection that shlex.split alone would not expose.
        printer = self.root / 'argv printer'
        printer.write_text('#!/usr/bin/env python3\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n')
        printer.chmod(0o700)
        command = command.replace(shlex.quote(argv[0]), shlex.quote(str(printer)), 1)
        parsed = subprocess.run(['/bin/sh', '-c', command], capture_output=True, text=True,
                                cwd=self.root, timeout=5, check=True)
        self.assertEqual(json.loads(parsed.stdout), argv[1:])
        self.assertFalse((self.root / 'SHOULD_NOT_EXIST').exists())
        self.assertNotIn(self.text, json.dumps(receipt))

    def test_stdin_task_and_dry_run_invoke_no_tools(self):
        code, receipt = self.invoke(['--task-file', '-', '--dry-run'], stdin=self.text)
        self.assertEqual((code, receipt['state']), (0, 'dry-run'))
        self.assertEqual(self.calls(), [])

    def test_missing_inputs_and_executables(self):
        for options in (['--socket', str(self.root / 'missing')], ['--socket', str(self.task)],
                        ['--repo', 'relative'], ['--client', str(self.root / 'absent')],
                        ['--tmux', str(self.root / 'absent')], ['--name', 'x:1'],
                        ['--model=--resume'], ['--observe-seconds', 'nan'],
                        ['--observe-seconds', '6']):
            with self.subTest(options=options):
                code, _ = self.invoke(options)
                self.assertEqual(code, 2)
                self.assertEqual(self.calls(), [])

    def test_task_bounds_encoding_and_nul(self):
        for raw in (b'', b'   ', b'x' * 16385, b'bad\0task', b'\xff'):
            with self.subTest(raw=raw[:20]):
                self.task.write_bytes(raw)
                code, _ = self.invoke()
                self.assertEqual(code, 2)
                self.assertEqual(self.calls(), [])

    def test_unsupported_and_failed_probes_do_not_launch(self):
        for mode in ('unsupported', 'client-error', 'tmux-error'):
            with self.subTest(mode=mode):
                self.log.unlink(missing_ok=True)
                code, _ = self.invoke(mode=mode)
                self.assertEqual(code, 2)
                self.assertFalse(any(args[0] == 'new-session' for _, args in self.calls()))

    def test_collision_and_early_exit_never_retry_or_inject(self):
        for mode, expected in (('collision', 'launch-unconfirmed'), ('dead', 'client-not-running'),
                               ('gone', 'client-not-running'), ('unicode-launch', 'launch-unconfirmed'),
                               ('unicode-observe', 'launch-unconfirmed'), ('bad-pane', 'launch-unconfirmed')):
            with self.subTest(mode=mode):
                self.log.unlink(missing_ok=True)
                (self.root / "state/codex-clean-session/task-unique-1").unlink(missing_ok=True)
                code, receipt = self.invoke(mode=mode)
                self.assertEqual((code, receipt['state']), (3, expected))
                self.assertEqual(sum(args[0] == 'new-session' for _, args in self.calls()), 1)
                self.assertNotIn('secret', json.dumps(receipt))
                self.assertTrue(all(args[0] in ('--help', '-V', 'new-session', 'display-message')
                                    for _, args in self.calls()))

    def test_consumed_name_never_launches_twice(self):
        self.assertEqual(self.invoke()[0], 0)
        code, receipt = self.invoke()
        self.assertEqual((code, receipt['state']), (2, 'name-consumed'))
        self.assertEqual(sum(args[0] == 'new-session' for _, args in self.calls()), 1)
        self.assertEqual(self.invoke(['--dry-run'])[1]['state'], 'name-consumed')

    def test_timeout_after_launch_is_unconfirmed(self):
        import argparse
        args = argparse.Namespace(client=str(self.root / 'client'), tmux=str(self.root / 'tmux'),
                                  repo=str(self.repo), socket=str(self.socket_path), model='gpt-6.1-sol',
                                  effort='medium', name='task-unique', task_file=str(self.task),
                                  observe_seconds=0, dry_run=False)
        for phase in ('new-session', 'display-message'):
            calls = []
            def fake_run(argv):
                calls.append(argv)
                if argv[1] == phase:
                    raise subprocess.TimeoutExpired(argv, 5)
                return subprocess.CompletedProcess(argv, 0, '%7' if argv[1] == 'new-session' else '--remote --no-alt-screen', '')
            with self.subTest(phase=phase), mock.patch.dict(os.environ, XDG_STATE_HOME=str(self.root / phase)), mock.patch.object(launcher, 'run', fake_run):
                with self.assertRaises(launcher.Refusal) as caught:
                    launcher.launch(args)
                self.assertEqual(caught.exception.state, 'launch-unconfirmed')
                self.assertEqual(sum(call[1] == 'new-session' for call in calls), 1)


if __name__ == '__main__':
    unittest.main()
