package keeper

import (
	"context"
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/types"
)

// The gh token's Secret and key (D-13). Session pods mount the Secret as a
// directory at /creds, so the token is /creds/gh_token, which agentd's gh wrapper
// and git's credential helper read at every call (D-40, D-42).
const (
	DefaultGHTokenSecret    = "dev-env-gh-token"
	DefaultOpsGHTokenSecret = "dev-env-ops-gh-token"
	// DefaultOpsGitHubAppDir is the mount of the haynes-ops-bot App's files.
	// The keeper reads it only when --enable-ops-gh-token is set.
	DefaultOpsGitHubAppDir = "/etc/dev-env-keeper/github-ops-bot"
	GHTokenKey             = "gh_token"
)

// GitHubTokenJob mints an installation token of app into secret's gh_token. The
// file ends in a newline, as v1's /creds/gh_token did; every reader takes it
// through `$(cat ...)`, which drops it. Both App loops use this Job. A full
// grant logs the permissions and repository selection GitHub returned. A short
// grant also logs permissionsNotGranted. The token, the key and the request
// headers are not fields.
func GitHubTokenJob(name string, app *GitHubApp, secret types.NamespacedName) Job {
	return Job{
		Name:   name,
		Secret: secret,
		Key:    GHTokenKey,
		Refresh: func(ctx context.Context) (Refreshed, error) {
			tok, err := app.Mint(ctx)
			if err != nil {
				return Refreshed{}, err
			}
			fields := []any{
				"repositorySelection", redactGitHubText(tok.RepositorySelection),
				"permissions", redactGitHubList(permissionList(tok.Permissions)),
			}
			if missing := app.MissingPermissions(tok.Permissions); len(missing) > 0 {
				fields = append(fields, "permissionsNotGranted", redactGitHubList(missing))
			}
			return Refreshed{
				Value:     newSecretValue(tok.Token.Reveal() + "\n"),
				ExpiresAt: tok.ExpiresAt,
				Fields:    fields,
			}, nil
		},
	}
}

// permissionList renders a permission set as sorted "name:level" strings.
func permissionList(p map[string]string) []string {
	out := make([]string, 0, len(p))
	for _, name := range slices.Sorted(maps.Keys(p)) {
		out = append(out, name+":"+p[name])
	}
	return out
}
