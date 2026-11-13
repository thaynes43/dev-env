package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/summonledger"
)

// The overflow page (DESIGN-064 6.8, "Overflow (N6, R1)", and 5.9's N6 and
// R1; story 793, slice c of 742; coordinator rulings Q3, Q5, Q7, Q9 and Q11
// on item 742 comment 7457 and the answer op793-q-1, not rulings by Tom). A
// refused urgent create has no session to carry a page, so the API counts it
// in the create ledger (urgentRefusals per caller in ConfigMap
// dev-env-summon-creates, story 778), and the pager reads that count. It runs
// inside the pager's tick, with the pager's own client, ledger and Pushover
// sender: no client, lock, transport, goroutine or flag of its own. The keeper
// never writes the create ledger.
//
//   - The read. Each tick, after the pager's ledger and its scan, one Get of
//     the create ledger by name, bounded like the pager's other requests. A
//     ConfigMap that is missing, cannot be read or cannot be decoded as a
//     whole sends no overflow page that tick and logs one fixed line; the
//     pages and the digest go on. One caller's entry that summonledger.Decode
//     refuses makes only that caller wait, with one fixed line a tick; the
//     other callers page (op793-q-1, answer 2).
//   - The page. For each caller whose count is above the count its member in
//     the pager's ledger last recorded (lastPagedCount), at most one overflow
//     event an hour (from lastPagedAt), carrying the caller and the refusals
//     since. The event is a page, or capped when the hourly window is full;
//     either way it takes the count as the new reference, so every refusal is
//     in exactly one event and refusals after it wait for the next.
//   - A count that went down. A caller whose count is below its reference,
//     its key reset, or whose key is gone (an absent key is an empty entry,
//     count 0), gets the reference 0 at once: every refusal its key then
//     holds is new. Its member is kept, and so is its hour.
//   - Once. The member, the dedupe entry and the page record are written in
//     one update before the send, as a page's are; nothing is retried.
//   - The cap. An overflow page counts toward the 6 an hour like any page
//     (ruling Q5): it goes after the drill and before the session pages of
//     the tick, callers by the time of their last refusal, oldest first
//     (op793-q-1, answer 1); past the cap it is capped, counted in the
//     more-events page and listed in the next digest.
const (
	summonEventOverflow = "overflow"
	// summonOverflowEvery is the least time between two overflow events of
	// one caller.
	summonOverflowEvery = time.Hour
	// summonOverflowMaxCallers bounds the callers the overflow member keeps.
	// It is below the store's schema bound (summonPagesMaxCaller), so a keeper
	// of story 782 or 792 still reads it, and it keeps the ledger with every
	// bound filled under the store's byte cap (item 793 comment 7633).
	summonOverflowMaxCallers = 64
	// summonOverflowOwner starts the owner part of an overflow event's
	// dedupe key, overflow-<unix second>-<16 hex of the caller's SHA-256>.
	// No session UID starts so.
	summonOverflowOwner = "overflow-"
)

// summonOverflowCaller is a summoning caller's name as the API names its key:
// its CallerPolicy's name, the summon ServiceAccount <namespace>/<name> with
// the slash replaced by a dot (api/v1alpha1 callerpolicy_types.go), so at
// most 127 characters. Every such name is also a stored name
// (summonStoredName). A key of any other shape is not a summoning caller's and
// is not read: the API counts refusals only for summoning callers, and the
// grants key (summonledger.GrantsKey) never has this shape.
var summonOverflowCaller = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// summonCreatesReader reads the create ledger's data, or fails.
type summonCreatesReader interface {
	read(context.Context) (map[string]string, error)
}

// kubeSummonCreates reads the create ledger by name through the pager's own
// uncached client; the keeper's Role grants get on that one name only.
type kubeSummonCreates struct {
	reader client.Reader
}

func (k *kubeSummonCreates) read(ctx context.Context) (map[string]string, error) {
	gctx, cancel := context.WithTimeout(ctx, noticeStoreTimeout)
	defer cancel()
	var cm corev1.ConfigMap
	if err := k.reader.Get(gctx, types.NamespacedName{Namespace: summonledger.Namespace, Name: summonledger.Name}, &cm); err != nil {
		return nil, errSummonPages
	}
	return cm.Data, nil
}

// summonOverflowKey is one overflow event's dedupe key. The second and the
// caller's hash make it new for every event: a caller has at most one an
// hour.
func summonOverflowKey(caller string, at time.Time) string {
	return fmt.Sprintf("%s%d-%s/%s", summonOverflowOwner, at.Unix(), summonOverflowHash(caller), summonEventOverflow)
}

func summonOverflowHash(caller string) string {
	sum := sha256.Sum256([]byte(caller))
	return hex.EncodeToString(sum[:8])
}

// summonOverflowEntryKey reports whether a dedupe key is an overflow event's.
func summonOverflowEntryKey(key string) bool {
	owner, event, _ := cutSummonKey(key)
	return event == summonEventOverflow && strings.HasPrefix(owner, summonOverflowOwner)
}

// overflowJobs reads the create ledger and lists this tick's overflow events:
// one for each caller whose count is above its reference and whose last
// overflow event is at least an hour old. It lowers, in memory, the reference
// of a caller whose count went down or whose key is gone, and reports whether
// it changed the ledger. It logs each fixed line at most once a tick.
func (p *summonPager) overflowJobs(ctx context.Context, l *summonPageLedger, now time.Time) ([]summonJob, bool) {
	if p.creates == nil {
		return nil, false
	}
	data, err := p.creates.read(ctx)
	if err != nil {
		p.log.Error(nil, "summon overflow: the create ledger is unavailable")
		return nil, false
	}
	st := &l.State
	callers := make([]string, 0, len(data))
	for caller := range data {
		if caller != summonledger.GrantsKey && summonOverflowCaller.MatchString(caller) {
			callers = append(callers, caller)
		}
	}
	sort.Strings(callers)
	var jobs []summonJob
	changed, unreadable, ahead := false, false, false
	for _, caller := range callers {
		e, err := summonledger.Decode(data[caller], true)
		if err != nil {
			unreadable = true // no value from the entry is logged
			continue
		}
		o, kept := st.Overflow[caller]
		if e.UrgentRefusals < o.LastPagedCount {
			// The key was reset: every refusal it holds is new (item 3).
			o.LastPagedCount = 0
			st.Overflow[caller] = o
			changed = true
		}
		if e.UrgentRefusals <= o.LastPagedCount {
			continue
		}
		if kept && o.LastPagedAt > now.Unix() {
			ahead = true
			continue
		}
		if kept && now.Before(time.Unix(o.LastPagedAt, 0).Add(summonOverflowEvery)) {
			continue // carried by the next event
		}
		var at time.Time
		if e.LastUrgentRefusal != nil {
			at = *e.LastUrgentRefusal
		}
		jobs = append(jobs, summonJob{key: summonOverflowKey(caller, now), event: summonEventOverflow, at: at,
			caller: caller, refused: e.UrgentRefusals, since: e.UrgentRefusals - o.LastPagedCount})
	}
	// A caller whose key is gone holds no refusals: an absent key is an empty
	// entry. Its reference falls to 0 and its member, with its hour, is kept
	// until the bound needs the room (summonPageState.overflowRoom).
	for caller, o := range st.Overflow {
		if _, present := data[caller]; !present && o.LastPagedCount != 0 {
			o.LastPagedCount = 0
			st.Overflow[caller] = o
			changed = true
		}
	}
	if unreadable {
		p.log.Error(nil, "summon overflow: a caller's create ledger entry is unreadable; that caller waits")
	}
	if ahead {
		p.log.Error(nil, "summon overflow ledger time is ahead of the keeper's clock")
	}
	return jobs, changed
}

// overflowSpare is the member the bound may drop to make room: one whose
// reference is 0 (its caller's key is gone, or was reset to zero) and whose
// last overflow event is at least an hour old, the oldest first. Dropping it
// changes nothing: a caller with no member counts from 0, and its hour has
// passed. "" when there is none.
func (s summonPageState) overflowSpare(now time.Time) string {
	spare, at := "", int64(0)
	for caller, o := range s.Overflow {
		if o.LastPagedCount != 0 || now.Before(time.Unix(o.LastPagedAt, 0).Add(summonOverflowEvery)) {
			continue
		}
		if spare == "" || o.LastPagedAt < at || (o.LastPagedAt == at && caller < spare) {
			spare, at = caller, o.LastPagedAt
		}
	}
	return spare
}

// overflowRoom reports whether caller has a member, or can get one: the
// member is below its bound, or holds a spare one.
func (s summonPageState) overflowRoom(caller string, now time.Time) bool {
	if _, ok := s.Overflow[caller]; ok {
		return true
	}
	return len(s.Overflow) < summonOverflowMaxCallers || s.overflowSpare(now) != ""
}

// markOverflow takes an overflow event's count as its caller's reference, in
// memory, with the event's time; it drops a spare member first when the
// member is at its bound. Other jobs are left alone.
func (s *summonPageState) markOverflow(j summonJob, now time.Time) {
	if j.event != summonEventOverflow {
		return
	}
	if _, ok := s.Overflow[j.caller]; !ok && len(s.Overflow) >= summonOverflowMaxCallers {
		if spare := s.overflowSpare(now); spare != "" {
			delete(s.Overflow, spare)
		}
	}
	s.Overflow[j.caller] = summonOverflowEntry{LastPagedCount: j.refused, LastPagedAt: now.Unix()}
}

// overflowCallers maps the hash in an overflow event's key to its caller,
// for a capped event's digest line: the dedupe entry holds no name, so the
// ledger's largest member stays within the byte cap (item 793 comment 7633).
func (s summonPageState) overflowCallers() map[string]string {
	out := make(map[string]string, len(s.Overflow))
	for caller := range s.Overflow {
		out[summonOverflowHash(caller)] = caller
	}
	return out
}

// summonOverflowLineCaller is the caller a capped overflow event's digest
// line names: the member whose hash its key carries, or unknown when the
// bound has dropped that member.
func summonOverflowLineCaller(owner string, callers map[string]string) string {
	if i := strings.LastIndexByte(owner, '-'); i >= 0 {
		if caller, ok := callers[owner[i+1:]]; ok {
			return summonStoredName(caller)
		}
	}
	return "unknown"
}

// renderOverflow is the overflow page's fixed template and its content-free
// record (ruling Q9): the fixed title, the caller's name and the count. The
// caller's name is shown only as a stored name (summonStoredName); the one
// link is the console's sessions list.
func (p *summonPager) renderOverflow(j summonJob) (Notice, pages.Page) {
	caller := summonStoredName(j.caller)
	n := Notice{Priority: 0, URL: p.origin + summonConsolePath, URLTitle: "Open dev-env sessions",
		Title: "[dev-env] " + summonEventOverflow}
	n.Message = fmt.Sprintf("[dev-env] %s: %d urgent session %s from caller %s %s refused and not yet paged. "+
		"Its queue of urgent sessions was full or its creates for the hour were spent, so no session was queued for %s.\n"+
		"At most one overflow page goes for a caller in an hour; refusals after this one wait for the next.",
		summonEventOverflow, j.since, summonPlural(int(min(j.since, 2)), "create", "creates"), caller,
		summonPlural(int(min(j.since, 2)), "was", "were"), summonPlural(int(min(j.since, 2)), "it", "them"))
	rec := pages.Page{Key: summonRecordKey(j.key), Sender: summonPageSender, Source: summonPageSource, Priority: 0,
		Title: n.Title, Correlation: caller,
		Body: fmt.Sprintf("caller %s, %d urgent %s refused", caller, j.since, summonPlural(int(min(j.since, 2)), "create", "creates"))}
	return n, rec
}
