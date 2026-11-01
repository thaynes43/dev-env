package projectcanary_test

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestCapacityDriftStillRefusesCanaryAndPilot(t *testing.T) {
	for _, pilot := range []bool{false, true} {
		name := "canary"
		f := testutil.New(t, "codex", "dev/dev-env")
		original := f.Templates
		validate := func(v *templates.Templates) error { return f.Record.ValidateTemplate(v, testutil.Owner) }
		if pilot {
			name = "pilot"
			p := testutil.NewPilot(t)
			original, validate = p.Templates, p.Policy.ValidateTemplate
		}
		t.Run(name, func(t *testing.T) {
			if err := validate(original); err != nil {
				t.Fatal(err)
			}
			changed := *original
			changed.Home.Size = resource.MustParse("100Gi")
			raw, err := json.Marshal(&changed)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := templates.Parse(map[string]string{templates.Key: string(raw)})
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Revision() != original.Revision() {
				t.Fatal("capacity drift changed the pod revision")
			}
			if validate(parsed) == nil {
				t.Fatal("reviewed capacity freeze accepted drift")
			}
		})
	}
}
