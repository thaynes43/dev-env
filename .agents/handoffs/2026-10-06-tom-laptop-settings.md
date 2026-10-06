# Handoff: the GitHub settings only Tom can change (2026-10-06)

Two prompts for an agent on Tom's laptop. That agent runs as Tom: his own `gh` login,
and his signed-in browser (Claude in Chrome) for the steps GitHub offers only in the
web UI. The pod's bot has no Administration permission, so it cannot do any of this.

Tom starts the agent with one line:

> Follow part 1 (or part 2) of `.agents/handoffs/2026-10-06-tom-laptop-settings.md`
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

## Part 2: after the first operator publish (KICKOFF B3; DESIGN-001 Q-13, ruled A)

Run this once the pod's agent says `ghcr.io/thaynes43/dev-env-operator` exists.

1. Check it exists: `gh api /users/thaynes43/packages/container/dev-env-operator -q
   .visibility`. If the call returns 404, stop and tell Tom that B3 has not
   published yet.
2. If it says `private`: in the browser, open
   <https://github.com/users/thaynes43/packages/container/dev-env-operator/settings>,
   go to "Danger Zone", choose "Change visibility", pick **Public**, and type the
   package name to confirm. GitHub has no API for this step.
3. Run the call from step 1 again and expect `public`. The pod's agent then checks
   that an anonymous pull works.
