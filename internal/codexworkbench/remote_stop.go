package codexworkbench

import (
	"errors"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// MaxRemoteStopHosts bounds the opt-in to individually named proof hosts.
const MaxRemoteStopHosts = 16

// RemoteStopHosts changes only the next pod built for an exact named host.
// The zero value preserves the legacy stop for every session.
type RemoteStopHosts struct {
	Namespace string
	names     []string
}

func ParseRemoteStopHosts(raw, namespace string) (RemoteStopHosts, error) {
	p := RemoteStopHosts{Namespace: namespace}
	if raw == "" {
		return p, nil
	}
	names := strings.Split(raw, ",")
	if len(names) > MaxRemoteStopHosts {
		return RemoteStopHosts{}, errors.New("codex-remote-stop-hosts exceeds the 16 host limit")
	}
	for _, name := range names {
		if len(validation.IsDNS1123Label(name)) != 0 || slices.Contains(p.names, name) {
			return RemoteStopHosts{}, errors.New("codex-remote-stop-hosts requires unique exact DNS session names, without whitespace or wildcards")
		}
		p.names = append(p.names, name)
	}
	return p, nil
}

func (p RemoteStopHosts) Names(s *v1alpha1.AgentSession) bool {
	return s != nil && IsHost(s) && p.Namespace != "" && s.Namespace == p.Namespace && slices.Contains(p.names, s.Name)
}
