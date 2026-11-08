#!/usr/bin/env python3
"""Static packaging contract for the two MIT Cursor ACP helpers.

This does not build the agent image, download Cursor, or start a provider.
Image execution stays in hosted CI through images/agent/smoke-test.sh.
"""
import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HELPER_DIR = "/usr/local/lib/dev-env-cursor-acp"
SESSION = "cursor-acp-session.py"
CHECKER = "cursor-acp-smoke-check.py"
ARCHIVE = "https://downloads.cursor.com/lab/2026.10.22-27dcf5f/linux/x64/agent-cli-package.tar.gz"
ARCHIVE_SHA256 = "85296944e4600a83c4b764f578d1502bf9c416db200d33a593e8f3ecabfdc294"


class ImagePackagingTests(unittest.TestCase):
    def text(self, relative):
        return (ROOT / relative).read_text(encoding="utf-8")

    def test_helpers_are_image_owned_and_ci_rebuilds_them(self):
        docker = self.text("images/agent/Dockerfile")
        self.assertIn(
            "COPY scripts/%s scripts/%s %s/" % (SESSION, CHECKER, HELPER_DIR),
            docker,
        )
        self.assertIn("chmod 0755 %s/%s %s/%s" % (HELPER_DIR, SESSION, HELPER_DIR, CHECKER), docker)
        self.assertNotIn("downloads.cursor.com", docker)
        self.assertNotIn("/opt/dev-env/scripts/%s" % SESSION, docker)
        self.assertNotIn("/home/dev/%s" % SESSION, docker)
        agent = self.text(".github/workflows/ci.yml")
        start = agent.index("agent_image:")
        block = agent[start:agent.index("\n  lint:", start)]
        self.assertIn("- 'scripts/%s'" % SESSION, block)
        self.assertIn("- 'scripts/%s'" % CHECKER, block)
        publish = self.text(".github/workflows/publish-agent.yml")
        self.assertNotIn("\n    paths:", publish)
        self.assertIn("scripts/%s" % SESSION, publish)
        self.assertIn("scripts/%s" % CHECKER, publish)

    def test_smoke_checks_paths_help_and_empty_refusal(self):
        smoke = self.text("images/agent/smoke-test.sh")
        self.assertIn("--read-only", smoke)
        self.assertIn("--network none", smoke)
        self.assertIn("--tmpfs /home/dev:rw,uid=1000,gid=1000,size=256m", smoke)
        self.assertIn(HELPER_DIR, smoke)
        self.assertIn("%s --help" % SESSION, smoke)
        self.assertIn("%s -" % CHECKER, smoke)
        self.assertIn("empty-input", smoke)
        self.assertIn('test "$code" = 2', smoke)
        self.assertIn("not protocol or task success", smoke)
        self.assertNotIn("downloads.cursor.com", smoke)

    def test_notices_leave_the_vendor_client_unbundled(self):
        notice = self.text("images/THIRD_PARTY.md")
        self.assertIn(SESSION, notice)
        self.assertIn(CHECKER, notice)
        self.assertIn("Not bundled", notice)
        self.assertIn("https://cursor.com/terms-of-service", notice)
        docs = self.text("docs/cursor-acp-session.md")
        self.assertIn(HELPER_DIR, docs)
        self.assertIn(ARCHIVE, docs)
        self.assertIn(ARCHIVE_SHA256, docs)
        self.assertIn("2026.10.22-27dcf5f", docs)
        self.assertIn("read-only", docs)
        self.assertIn("does not install", docs)

    def test_source_help_and_empty_refusal_do_not_launch_a_provider(self):
        session = ROOT / "scripts" / SESSION
        checker = ROOT / "scripts" / CHECKER
        help_run = subprocess.run(
            [sys.executable, "-I", "-B", str(session), "--help"],
            text=True, capture_output=True, timeout=5, check=False)
        self.assertEqual(help_run.returncode, 0, help_run.stderr)
        self.assertIn("--client", help_run.stdout)
        self.assertNotIn("cursor-agent", help_run.stderr)
        empty = subprocess.run(
            [sys.executable, "-I", "-B", str(checker), "-"],
            input="", text=True, capture_output=True, timeout=5, check=False)
        self.assertEqual(empty.returncode, 2, empty.stdout)
        self.assertEqual(json.loads(empty.stdout), {"event": "refused", "reason": "empty-input"})
        no_help = subprocess.run(
            [sys.executable, "-I", "-B", str(checker), "--help"],
            text=True, capture_output=True, timeout=5, check=False)
        self.assertEqual(no_help.returncode, 2)
        self.assertNotIn("usage:", no_help.stdout.lower())


if __name__ == "__main__":
    unittest.main()
