# Handoff: the GitHub settings only Tom can change (2026-10-06)

Three prompts for an agent on Tom's laptop. That agent runs as Tom: his own `gh` login,
and his signed-in browser (Claude in Chrome) for the steps GitHub offers only in the
web UI. The pod's bot has no Administration permission, so it cannot do any of this.

Tom starts the agent with one line:

> Follow part 1 (or part 2, or part 3) of `.agents/handoffs/2026-10-06-tom-laptop-settings.md`
> in thaynes43/dev-env.

Running a part is Tom's yes to exactly the changes it lists. Do nothing else.

## Rules for the laptop agent

- Change only what the part lists. For anything else, ask Tom first.
- Never print, paste or commit a private key, token or secret value. Send a key
  straight from its file into `gh secret set`, then delete the file.
- Report each step as done, already set, or failed with the error text.

## Part 1: now (before KICKOFF B4 and B5)

1. **Allow auto-merge** on the repo (Renovate's `platformAutomerge` needs it):
   `gh api -X PATCH repos/thaynes43/dev-env -F allow_auto_merge=true`, then check
   with `gh api repos/thaynes43/dev-env -q .allow_auto_merge` (expect `true`).
2. **GHCR access for the agent image.** In the browser, open
   <https://github.com/users/thaynes43/packages/container/dev-env/settings>. Under
   "Manage Actions access", add the repository `thaynes43/dev-env` with the role
   **Write**. Leave the package's visibility and its other entries as they are.
3. **Renovate.** Open <https://github.com/settings/installations>, configure the
   Mend Renovate app, and make sure its repository access includes
   `thaynes43/dev-env`. If it covers all repositories, nothing to do.
4. **The release-please App (DESIGN-001 Q-14, ruled A).**
   1. Create a GitHub App at <https://github.com/settings/apps/new>:
      name `thaynes43-dev-env-release` (any free name works; report the one used),
      homepage `https://github.com/thaynes43/dev-env`, webhook **off**, and
      "Only on this account". Repository permissions: Contents **Read and write**,
      Pull requests **Read and write**, Issues **Read and write** (release-please
      creates its `autorelease:` labels), Metadata **Read-only**. Nothing else.
   2. Install it on `thaynes43/dev-env` only ("Only select repositories").
   3. Store the App id as a repo variable:
      `gh variable set RELEASE_APP_ID --repo thaynes43/dev-env --body <app id>`.
   4. Generate a private key. Ask Tom whether to save a copy in 1Password first. Then
      `gh secret set RELEASE_APP_PRIVATE_KEY --repo thaynes43/dev-env < <key file>`
      and delete the downloaded `.pem`.
   5. Check: `gh variable list --repo thaynes43/dev-env` shows `RELEASE_APP_ID`, and
      `gh secret list --repo thaynes43/dev-env` shows `RELEASE_APP_PRIVATE_KEY`
      (the value is never shown).
5. **Bridge until step 4 is done** (found on 2026-10-06: the first release-please run
   failed with "GitHub Actions is not permitted to create or approve pull requests",
   because without the App the workflow uses `GITHUB_TOKEN`). Allow Actions to open
   PRs: `gh api -X PUT repos/thaynes43/dev-env/actions/permissions/workflow
   -F can_approve_pull_request_reviews=true` (this one field only; the default
   workflow permissions stay as they are). Skip this step if step 4 is done in the
   same run. Check with `gh api repos/thaynes43/dev-env/actions/permissions/workflow`
   (expect `can_approve_pull_request_reviews: true`). The release PR then opens, but
   starts no CI until it is closed and reopened (CLAUDE.md "Releases"). With the App
   in place this setting is no longer needed and may be switched back off.

## Part 2: after the first operator publish (KICKOFF B3; DESIGN-001 Q-13, ruled A)

Run this once the pod's agent says `ghcr.io/thaynes43/dev-env-operator` exists.

1. Give `gh` the package scope (a default login lacks it, and the call below then
   fails with 403 instead of 404): `gh auth refresh -h github.com -s read:packages`.
   Then check it exists: `gh api /users/thaynes43/packages/container/dev-env-operator -q
   .visibility`. If the call returns 404, stop and tell Tom that B3 has not
   published yet.
2. If it says `private`: in the browser, open
   <https://github.com/users/thaynes43/packages/container/dev-env-operator/settings>,
   go to "Danger Zone", choose "Change visibility", pick **Public**, and type the
   package name to confirm. GitHub has no API for this step.
3. Run the call from step 1 again and expect `public`. The pod's agent then checks
   that an anonymous pull works.

## Part 3: the Protect Main ruleset (after `CI - Success` has reported once)

Run this once the pod's agent says the `CI - Success` check has run on a PR (KICKOFF
B2). Before that, the check does not exist and the ruleset would block every merge
forever. It is the one required check; the Claude review stays advisory and is never
added.

1. Check that the check has reported:
   `gh api repos/thaynes43/dev-env/commits/main/check-runs -q '.check_runs[].name'`
   lists `CI - Success`. If it does not, stop and tell Tom. (The workflow also runs on
   push to main, so the first merge after B2 reports it. Any PR's head commit works
   too.)
2. Check that no ruleset exists yet: `gh api repos/thaynes43/dev-env/rulesets -q
   '.[].name'` prints nothing. If `Protect Main` is already there, skip to step 4.
3. Create it. The ruleset applies to the default branch, blocks deletion and
   force-push, requires linear history, requires a pull request with 0 approvals, and
   requires the status check `CI - Success` from GitHub Actions (integration id
   15368). "Require branches to be up to date" stays **off**
   (`strict_required_status_checks_policy: false`), so parallel agent PRs do not re-run
   CI after every merge. There are no bypass actors. Write the body to a file and post
   it:

```bash
cat > /tmp/protect-main.json <<'JSON'
{
  "name": "Protect Main",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {
    "ref_name": {
      "include": ["~DEFAULT_BRANCH"],
      "exclude": []
    }
  },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": false
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": false,
        "do_not_enforce_on_create": false,
        "required_status_checks": [
          { "context": "CI - Success", "integration_id": 15368 }
        ]
      }
    }
  ]
}
JSON
gh api -X POST repos/thaynes43/dev-env/rulesets --input /tmp/protect-main.json
rm /tmp/protect-main.json
```

   **If GitHub's response or the UI says the ruleset will not be enforced on a private
   repo on Tom's plan** (the API answers 403 with "Upgrade to GitHub Pro or make this
   repository public", or the ruleset page shows a not-enforced banner), stop and tell
   Tom. That is DESIGN-001 Q-12, and the pod's agent asks it. Do not make the repo
   public, and do not change the plan.
4. Read it back: `gh api repos/thaynes43/dev-env/rulesets -q '.[] | select(.name ==
   "Protect Main") | .id'` gives the id; then `gh api
   repos/thaynes43/dev-env/rulesets/<id>`. Check that `enforcement` is `active`,
   `conditions.ref_name.include` is `["~DEFAULT_BRANCH"]`, the rules are `deletion`,
   `non_fast_forward`, `required_linear_history`, `pull_request` (0 approvals) and
   `required_status_checks` with `CI - Success`, integration id 15368 and
   `strict_required_status_checks_policy` false. Report each as matches or differs.
