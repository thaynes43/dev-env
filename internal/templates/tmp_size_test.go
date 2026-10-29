package templates

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestTmpSizeLimit(t *testing.T) {
	const legacyRevision = "2.0.0-fffc40c6b4" // Captured before DESIGN-035.
	for _, value := range []string{"", "null", "1Gi", "8Gi", "16Gi", "64Gi", "17179869184"} {
		t.Run("value="+value, func(t *testing.T) {
			doc := example(t)
			if value != "" {
				doc = strings.Replace(doc, "  M:\n", "  M:\n    tmpSizeLimit: "+value+"\n", 1)
			}
			tmpl, err := parse(t, doc)
			if err != nil {
				t.Fatal(err)
			}
			m, err := tmpl.Size("")
			if err != nil {
				t.Fatal(err)
			}
			if value == "" || value == "null" {
				if m.TmpSizeLimit != nil || tmpl.Revision() != legacyRevision {
					t.Fatalf("default materialized or legacy revision changed: limit=%v revision=%s", m.TmpSizeLimit, tmpl.Revision())
				}
				return
			}
			if m.TmpSizeLimit == nil || m.TmpSizeLimit.Cmp(resource.MustParse(value)) != 0 {
				t.Fatalf("tmpSizeLimit = %v, want %s", m.TmpSizeLimit, value)
			}
			if tmpl.Revision() == legacyRevision {
				t.Fatal("an explicit tmpSizeLimit did not change the revision")
			}
			if tmpl.Sizes[v1alpha1.SizeS].TmpSizeLimit != nil || tmpl.Sizes[v1alpha1.SizeL].TmpSizeLimit != nil {
				t.Fatal("setting M changed another class's configuration")
			}
		})
	}
}

func TestTmpSizeLimitRefusesInvalidValues(t *testing.T) {
	for _, value := range []string{"0", "-1Gi", "512Mi", "1073741823", "65Gi", "68719476737", "100000000000000000000Gi", "bogus"} {
		t.Run(value, func(t *testing.T) {
			doc := strings.Replace(example(t), "  M:\n", "  M:\n    tmpSizeLimit: "+value+"\n", 1)
			_, err := parse(t, doc)
			if err == nil {
				t.Fatalf("accepted invalid tmpSizeLimit %q", value)
			}
			if value != "bogus" {
				q := resource.MustParse(value)
				want := fmt.Sprintf("sizes.M.tmpSizeLimit: %s is outside 1Gi to 64Gi", q.String())
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error does not identify size, field and rejected value: %v; want %q", err, want)
				}
			}
		})
	}
}
