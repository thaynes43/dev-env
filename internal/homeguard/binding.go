// Package homeguard validates a pinned original HOME without reading its contents.
package homeguard

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const Annotation = "dev-env.haynesops.com/original-home-binding"
const Env = "AGENTD_ORIGINAL_HOME_BINDING"
const RescueFinalizer = "dev-env.haynesops.com/rescue"

var ErrUnsafe = errors.New("original HOME binding is unavailable, changed or unsafe; preserve HOME")

type Binding struct {
	Namespace  string `json:"namespace"`
	Session    string `json:"session"`
	SessionUID string `json:"sessionUID"`
	PVCUID     string `json:"pvcUID"`
	PV         string `json:"pv"`
	PVUID      string `json:"pvUID"`
}

func Parse(raw string) (*Binding, error) {
	if len(raw) > 4096 {
		return nil, ErrUnsafe
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	var b Binding
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF || b.Namespace == "" || b.Session == "" || b.SessionUID == "" || b.PVCUID == "" || b.PV == "" || b.PVUID == "" {
		return nil, ErrUnsafe
	}
	for _, s := range []string{b.Namespace, b.Session, b.SessionUID, b.PVCUID, b.PV, b.PVUID} {
		if len(s) > 253 || strings.ContainsAny(s, "/\\\x00\r\n\t ") || strings.HasPrefix(s, "-") {
			return nil, ErrUnsafe
		}
	}
	return &b, nil
}
func (b Binding) String() string { raw, _ := json.Marshal(b); return string(raw) }
func (b Binding) Claim() string  { return "home-" + b.Session }

// Select keeps an admitted binding effective even if the startup flag is later
// omitted. A same-name replacement Session is refused, never silently unguarded.
func Select(config *Binding, namespace, name, uid string, annotations map[string]string) (*Binding, error) {
	var b *Binding
	if raw, exists := annotations[Annotation]; exists {
		var err error
		b, err = Parse(raw)
		if err != nil {
			return nil, err
		}
	}
	if config != nil && config.Namespace == namespace && config.Session == name {
		if b != nil && *b != *config {
			return nil, ErrUnsafe
		}
		b = config
	}
	if b != nil && (b.Namespace != namespace || b.Session != name || b.SessionUID != uid) {
		return nil, ErrUnsafe
	}
	return b, nil
}

type metadata struct {
	Annotations       map[string]string `json:"annotations"`
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	DeletionTimestamp *string           `json:"deletionTimestamp"`
	Finalizers        []string          `json:"finalizers"`
	OwnerReferences   []struct {
		APIVersion, Kind, Name, UID string
		Controller                  bool
	} `json:"ownerReferences"`
}
type object struct {
	Metadata metadata `json:"metadata"`
	Spec     struct {
		Agent, Mode                            string
		Workspace, ProjectCanary, PrivatePilot any
		OperatingMode                          string
		VolumeName                             string
		ClaimRef                               *struct{ APIVersion, Kind, Namespace, Name, UID string }
	} `json:"spec"`
	Status struct {
		Phase          string
		ArchivedAt     *string
		PrivateHistory any
	} `json:"status"`
}

func has(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Verify checks real API object metadata. The rescue and PV protection finalizers
// anchor identity; these observations alone are not an atomic mount/start fence.
func (b Binding) Verify(session, pvc, pv []byte, running bool) error {
	var s, c, v object
	if json.Unmarshal(session, &s) != nil || json.Unmarshal(pvc, &c) != nil || json.Unmarshal(pv, &v) != nil {
		return ErrUnsafe
	}
	if s.Metadata.Namespace != b.Namespace || s.Metadata.Name != b.Session || s.Metadata.UID != b.SessionUID || s.Metadata.DeletionTimestamp != nil || s.Status.ArchivedAt != nil || s.Spec.Agent != "codex" || s.Spec.Mode != "remote" || s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil || s.Metadata.Annotations["dev-env.haynesops.com/task-budget-uid"] != "" || s.Status.PrivateHistory != nil {
		return ErrUnsafe
	}
	if running && s.Spec.OperatingMode != "" && s.Spec.OperatingMode != "Running" {
		return ErrUnsafe
	}
	if c.Metadata.Namespace != b.Namespace || c.Metadata.Name != b.Claim() || c.Metadata.UID != b.PVCUID || c.Metadata.DeletionTimestamp != nil || c.Status.Phase != "Bound" || c.Spec.VolumeName != b.PV || !has(c.Metadata.Finalizers, RescueFinalizer) {
		return ErrUnsafe
	}
	owners := 0
	for _, o := range c.Metadata.OwnerReferences {
		if o.Controller {
			owners++
			if o.APIVersion != "dev-env.haynesops.com/v1alpha1" || o.Kind != "AgentSession" || o.Name != b.Session || o.UID != b.SessionUID {
				return ErrUnsafe
			}
		}
	}
	if owners != 1 {
		return ErrUnsafe
	}
	ref := v.Spec.ClaimRef
	if v.Metadata.Name != b.PV || v.Metadata.UID != b.PVUID || v.Metadata.DeletionTimestamp != nil || v.Status.Phase != "Bound" || !has(v.Metadata.Finalizers, "kubernetes.io/pv-protection") || ref == nil || ref.APIVersion != "v1" || ref.Kind != "PersistentVolumeClaim" || ref.Namespace != b.Namespace || ref.Name != b.Claim() || ref.UID != b.PVCUID {
		return ErrUnsafe
	}
	return nil
}
