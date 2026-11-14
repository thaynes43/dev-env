package summonledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// MaxSuffix is the last name suffix a create may take: a re-fired key's
// requested name, then -2 to -99 (DESIGN-064 6.4), and the platform's filing
// name the same way (6.6).
const MaxSuffix = 99

// RequestHash is what a repeat of a key must match, and what a charge's hash
// holds (DESIGN-064 5.9, the story 778 fix round 1 correction, F2): mode,
// lane, the requested name and the entry's priority, not data. Binding
// priority keeps a charge from moving queues on a retry after a policy edit.
// A re-fire with fresher data returns the session already running and does
// not change its brief. The requested name, not the name the session took,
// so the repeat of a create that took a suffix still matches.
func RequestHash(mode, lane, requested string, priority v1alpha1.SummonPriority) string {
	sum := sha256.Sum256([]byte("dev-env.summon.request.v2\x00" + mode + "\x00" + lane + "\x00" + requested + "\x00" + string(priority)))
	return hex.EncodeToString(sum[:])
}

// NameSequence is every name a create for requested may take, in order:
// requested, then -2 to -99.
func NameSequence(requested string) []string {
	all := make([]string, 0, MaxSuffix)
	all = append(all, requested)
	for i := 2; i <= MaxSuffix; i++ {
		all = append(all, fmt.Sprintf("%s-%d", requested, i))
	}
	return all
}

// requestedNames are the requested names a charge bound to name can come
// from: name itself, and, when name ends in a suffix -2 to -99, the name
// before it (NameSequence).
func requestedNames(name string) []string {
	out := []string{name}
	i := strings.LastIndexByte(name, '-')
	if i <= 0 {
		return out
	}
	n, err := strconv.Atoi(name[i+1:])
	if err == nil && n >= 2 && n <= MaxSuffix && strconv.Itoa(n) == name[i+1:] {
		out = append(out, name[:i])
	}
	return out
}

// FilingID is the dispatch id of the platform's filing for the remediation
// whose UID is uid (DESIGN-064 5.9 "The create ledger" item 2, 6.6 step 2):
// filing:<uid>. A caller's create never draws one: its ids are 32 hex
// characters.
func FilingID(uid types.UID) string {
	return "filing:" + string(uid)
}
