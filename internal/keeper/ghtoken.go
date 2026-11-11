package keeper

import (
	"context"
	"maps"
	"slices"
	"strings"

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
// grant also logs permissionsNotGranted, which is built from the configured
// set. A granted permission is logged only when its name is in that set and
// its level is read, write or admin. A response entry whose name is outside
// the set is dropped, and the line carries permissionsDropped when that count
// is not zero. The token, the key and the request headers are not fields.
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
			granted, dropped := grantedPermissions(app.Permissions, tok.Permissions)
			fields := []any{
				"repositorySelection", redactGitHubText(tok.RepositorySelection),
				"permissions", granted,
			}
			if dropped != 0 {
				fields = append(fields, "permissionsDropped", dropped)
			}
			if missing := acceptedPermissions(app.MissingPermissions(tok.Permissions)); len(missing) > 0 {
				fields = append(fields, "permissionsNotGranted", missing)
			}
			return Refreshed{
				Value:     newSecretValue(tok.Token.Reveal() + "\n"),
				ExpiresAt: tok.ExpiresAt,
				Fields:    fields,
			}, nil
		},
	}
}

// grantedPermissions returns the response entries that may be logged, and how
// many response names were outside configured. A logged entry's name is one
// the keeper asked for, and its level is read, write or admin. A configured
// name with another level is left out of the log and out of the count. Names
// are not passed through the credential scrubber, so a long configured
// permission stays visible.
func grantedPermissions(configured, granted map[string]string) ([]string, int) {
	logged := make([]string, 0, len(granted))
	dropped := 0
	for _, name := range slices.Sorted(maps.Keys(granted)) {
		level := granted[name]
		if _, ok := configured[name]; !ok {
			dropped++
			continue
		}
		if !permissionLevel[level] {
			continue
		}
		logged = append(logged, name+":"+level)
	}
	return logged, dropped
}

// acceptedPermissions keeps name:level entries whose name and level are ones
// ParsePermissions would accept. permissionsNotGranted is built from the
// configured set and passes through here. Response permissions do not.
func acceptedPermissions(entries []string) []string {
	if len(entries) == 0 {
		return entries
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, level, ok := strings.Cut(entry, ":")
		if !ok || !permissionName.MatchString(name) || !permissionLevel[level] {
			continue
		}
		out = append(out, name+":"+level)
	}
	return out
}
