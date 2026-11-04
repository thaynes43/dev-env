package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// hashedFields and excludedFields name every leaf of ScheduledWorkSpec by its
// JSON path. A new spec field fails TestSpecHashCoversEveryField until it is
// added to one of them: hashed, or excluded on purpose with DESIGN-054's
// agreement.
var (
	hashedFields = []string{
		"title", "owner", "due", "timeZone",
		"recurrence.freq", "recurrence.byDay", "recurrence.byMonthDay", "recurrence.until", "recurrence.count",
		"notAfter", "mode", "work.template", "work.repo", "work.prompt", "notify", "calendar",
	}
	excludedFields = []string{"editor", "cancelled"}
	// setFields are the list fields that are sets: their order is not
	// hashed. Every list field of the spec is listed here or in
	// orderedFields.
	setFields     = []string{"recurrence.byDay", "notify"}
	orderedFields = []string{}
)

func goldenSpec() v1alpha1.ScheduledWorkSpec {
	until := metav1.NewTime(time.Date(2030, 8, 31, 0, 0, 0, 0, time.UTC))
	return v1alpha1.ScheduledWorkSpec{
		Title: "audit", Owner: "human/dev-env-system/tom", Editor: "human/dev-env-system/tom",
		Due: "2030-06-03T14:00", TimeZone: "America/New_York",
		Recurrence: &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"TH", "MO"}, Until: &until},
		NotAfter:   metav1.NewTime(time.Date(2030, 9, 1, 0, 0, 0, 0, time.UTC)),
		Mode:       v1alpha1.ScheduledWorkHeadless,
		Work:       v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "check <the> trend & report"},
		Notify:     []string{"pushover:phone", "pushover:default"},
		Calendar:   "calendar:haynesnetwork",
	}
}

// The canonical bytes and the hash are pinned: a change to either (a field
// reordered, renamed, newly hashed, an encoder change) voids every stored
// approval, so it must be a deliberate edit of this test.
func TestSpecHashGolden(t *testing.T) {
	const wantInput = `{"title":"audit","owner":"human/dev-env-system/tom","editor":"","due":"2030-06-03T14:00",` +
		`"timeZone":"America/New_York","recurrence":{"freq":"weekly","byDay":["MO","TH"],"until":"2030-08-31T00:00:00Z"},` +
		`"notAfter":"2030-09-01T00:00:00Z","mode":"headless",` +
		`"work":{"template":"ops-audit","repo":"haynes-ops","prompt":"check \u003cthe\u003e trend \u0026 report"},` +
		`"notify":["pushover:default","pushover:phone"],"calendar":"calendar:haynesnetwork"}`
	if got := string(specHashInput(goldenSpec())); got != wantInput {
		t.Errorf("canonical input changed:\n got %s\nwant %s", got, wantInput)
	}
	// The documented encoding is the one SpecHash (scheduledwork.SpecHash)
	// hashes: the restatement below and the alias agree on every fixture.
	for _, s := range []v1alpha1.ScheduledWorkSpec{goldenSpec(), pr903Fixture(), {}} {
		sum := sha256.Sum256(specHashInput(s))
		if got := hex.EncodeToString(sum[:]); got != SpecHash(s) {
			t.Errorf("the documented encoding hashes to %s, SpecHash gives %s", got, SpecHash(s))
		}
	}
	for name, c := range map[string]struct {
		spec v1alpha1.ScheduledWorkSpec
		want string
	}{
		"full":    {goldenSpec(), "6948d7aff591dbeb4d411a910adbdb8542ca404be83cb83f41507645e7594fc9"},
		"minimal": {v1alpha1.ScheduledWorkSpec{}, "fbb4c3580412c3a2febf1110b73a9d043010ab06d088ce296587802a35eb8f5c"},
	} {
		if got := SpecHash(c.spec); got != c.want {
			t.Errorf("%s: SpecHash = %s, want %s", name, got, c.want)
		}
	}
}

// pr903Fixture is the spec of TestSpecHash in #903's
// internal/scheduledwork/dispatch_test.go (4700069), copied as it is.
func pr903Fixture() v1alpha1.ScheduledWorkSpec {
	return v1alpha1.ScheduledWorkSpec{
		Title: "audit", Owner: "human/dev-env-system/tom", Editor: "human/dev-env-system/tom",
		Due: "2030-06-03T14:00", TimeZone: "UTC", NotAfter: metav1.NewTime(time.Date(2030, 9, 1, 0, 0, 0, 0, time.UTC)),
		Mode: v1alpha1.ScheduledWorkHeadless, Recurrence: &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"MO", "TH"}},
		Work:   v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "p"},
		Notify: []string{"pushover:default", "pushover:phone"},
	}
}

// The hash #903's scheduledwork.SpecHash gives on #903's own fixture: #903
// pins no golden value, so this one was computed by #903's function at
// 4700069, extracted unchanged into a throwaway test. A drift of this copy from
// #903's algorithm fails here. After #903 merges, SpecHash becomes an alias of
// scheduledwork.SpecHash and this test guards the alias.
func TestSpecHashPR903Golden(t *testing.T) {
	const want = "f9f2d1185b848b8d8acb50553173415e0fd8b687c18f2cc0c4e2282b456ed788"
	if got := SpecHash(pr903Fixture()); got != want {
		t.Errorf("SpecHash(#903's fixture) = %s, want %s, the hash #903's scheduledwork.SpecHash gives", got, want)
	}
}

// Editor and cancelled are not hashed, a set's order is not, and SpecHash
// leaves its argument as it was.
func TestSpecHashIgnoresEditorCancelledAndSetOrder(t *testing.T) {
	base := goldenSpec()
	h := SpecHash(base)
	if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("hash %q is not 64 lowercase hex", h)
	}
	same := *base.DeepCopy()
	same.Editor, same.Cancelled = "session/s/u", true
	same.Notify = []string{"pushover:default", "pushover:phone"}
	same.Recurrence.ByDay = []string{"MO", "TH"}
	if got := SpecHash(same); got != h {
		t.Errorf("editor, cancelled or a set's order changed the hash: %s, want %s", got, h)
	}
	if base.Notify[0] != "pushover:phone" || base.Recurrence.ByDay[0] != "TH" {
		t.Error("SpecHash sorted its argument's sets")
	}
	absent := *base.DeepCopy()
	absent.Notify, absent.Calendar, absent.Recurrence = nil, "", nil
	empty := *absent.DeepCopy()
	empty.Notify = []string{}
	if SpecHash(absent) != SpecHash(empty) {
		t.Error("an absent and an empty notify hash differently")
	}
}

type leaf struct {
	path  string
	index []int
}

var timeType = reflect.TypeFor[metav1.Time]()

// leaves lists the JSON-path leaves of a struct type: a nested struct or a
// pointer to one is walked, a time is a leaf.
func leaves(t reflect.Type, prefix string, index []int) []leaf {
	var out []leaf
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			panic("ScheduledWorkSpec field " + f.Name + " has no JSON name")
		}
		path, idx := prefix+name, append(slices.Clone(index), i)
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != timeType {
			out = append(out, leaves(ft, path+".", idx)...)
			continue
		}
		out = append(out, leaf{path, idx})
	}
	return out
}

// field returns the leaf at index of the struct v points to, allocating
// nil pointers on the way.
func field(v reflect.Value, l leaf) reflect.Value {
	for _, i := range l.index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v
}

// mutate changes the leaf at index of the struct v points to.
func mutate(t *testing.T, v reflect.Value, l leaf) {
	t.Helper()
	v = field(v, l)
	later := metav1.NewTime(time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC))
	switch {
	case v.Type() == timeType:
		v.Set(reflect.ValueOf(later))
	case v.Type() == reflect.PointerTo(timeType):
		v.Set(reflect.ValueOf(&later))
	case v.Kind() == reflect.String:
		v.SetString(v.String() + "x")
	case v.Kind() == reflect.Bool:
		v.SetBool(!v.Bool())
	case v.Kind() == reflect.Int32:
		v.SetInt(v.Int() + 1)
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.String:
		v.Set(reflect.Append(v, reflect.ValueOf("zz-added")))
	default:
		t.Fatalf("%s: no mutation for %s; teach mutate this kind", l.path, v.Type())
	}
}

// Every spec field is hashed or explicitly excluded: a field added to the
// struct fails here until it is listed, and each listed field is checked by
// changing it alone.
func TestSpecHashCoversEveryField(t *testing.T) {
	all := leaves(reflect.TypeFor[v1alpha1.ScheduledWorkSpec](), "", nil)
	var paths []string
	for _, l := range all {
		paths = append(paths, l.path)
	}
	want := slices.Concat(hashedFields, excludedFields)
	if !slices.Equal(slices.Sorted(slices.Values(paths)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("ScheduledWorkSpec's fields are %v; hashedFields and excludedFields list %v: put each new field in one", paths, want)
	}
	base := goldenSpec()
	h := SpecHash(base)
	for _, l := range all {
		s := *base.DeepCopy()
		mutate(t, reflect.ValueOf(&s).Elem(), l)
		changed := SpecHash(s) != h
		switch excluded := slices.Contains(excludedFields, l.path); {
		case excluded && changed:
			t.Errorf("%s is excluded, but an edit of it changed the hash", l.path)
		case !excluded && !changed:
			t.Errorf("an edit of %s kept the hash", l.path)
		}
	}
}

// A set field's order is not hashed: reversing each one alone keeps the hash.
// Every list field is classified, so a new list field fails here until it is
// declared a set or ordered.
func TestSpecHashSetReorderInvariance(t *testing.T) {
	var lists []string
	probe := v1alpha1.ScheduledWorkSpec{}
	for _, l := range leaves(reflect.TypeFor[v1alpha1.ScheduledWorkSpec](), "", nil) {
		if field(reflect.ValueOf(&probe).Elem(), l).Kind() == reflect.Slice {
			lists = append(lists, l.path)
		}
	}
	if want := slices.Concat(setFields, orderedFields); !slices.Equal(slices.Sorted(slices.Values(lists)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("the spec's list fields are %v; setFields and orderedFields list %v", lists, want)
	}
	base := goldenSpec()
	h := SpecHash(base)
	for _, l := range leaves(reflect.TypeFor[v1alpha1.ScheduledWorkSpec](), "", nil) {
		if !slices.Contains(setFields, l.path) {
			continue
		}
		s := *base.DeepCopy()
		v := field(reflect.ValueOf(&s).Elem(), l)
		if v.Len() < 2 {
			t.Fatalf("%s: the golden spec needs two values to reorder", l.path)
		}
		reversed := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			reversed.Index(i).Set(v.Index(v.Len() - 1 - i))
		}
		v.Set(reversed)
		if got := SpecHash(s); got != h {
			t.Errorf("reversing %s changed the hash: %s, want %s", l.path, got, h)
		}
	}
}

// specHashInput restates the canonical encoding SpecHash's documentation
// gives; TestSpecHashGolden checks that it is what SpecHash hashes.
func specHashInput(spec v1alpha1.ScheduledWorkSpec) []byte {
	s := *spec.DeepCopy()
	s.Editor, s.Cancelled = "", false
	slices.Sort(s.Notify)
	if s.Recurrence != nil {
		slices.Sort(s.Recurrence.ByDay)
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}
