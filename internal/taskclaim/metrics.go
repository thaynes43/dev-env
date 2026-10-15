package taskclaim

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

// Coordination record metrics expose live project counts and lifetime source
// Session bounds. Enabled archive-backed retention can free project capacity
// after eligible records have met the minimum 30-day absence requirement for
// their required Sessions. These series show proximity to the bounds; an
// immediate retry does not free capacity.
const (
	// MetricRecords is the number of records of one kind in one project.
	MetricRecords = "dev_env_coordination_records"
	// MetricRecordsSessionMax is the most records of one kind in one project
	// that any one source Session holds. Records from before the source label
	// count toward the project only, so they are not attributed here.
	MetricRecordsSessionMax = "dev_env_coordination_records_session_max"
	// MetricRecordLimit is the bound Create applies, per kind and scope.
	MetricRecordLimit = "dev_env_coordination_record_limit"
)

// recordPage is the list page size; recordCollectTimeout bounds one scrape.
const (
	recordPage           = 500
	recordCollectTimeout = 10 * time.Second
)

var (
	recordsDesc = prometheus.NewDesc(MetricRecords,
		"Live coordination records of one kind in one project; a new record is refused at the project limit. Space can free only after enabled archive-backed retention succeeds for eligible records, at least 30 days after the required Sessions are absent.",
		[]string{"kind", "project"}, nil)
	recordsSessionMaxDesc = prometheus.NewDesc(MetricRecordsSessionMax,
		"The largest number of live coordination records of one kind attributed to any one source Session in one project; that Session's bound is lifetime (records without a source label are not attributed).",
		[]string{"kind", "project"}, nil)
	recordLimitDesc = prometheus.NewDesc(MetricRecordLimit,
		"Coordination record limits per kind: lifetime per source Session (scope=session), live record count per project (scope=project); immediate retry does not free either bound.",
		[]string{"kind", "scope"}, nil)
)

// RecordKind is one coordination record kind and the bounds its store applies.
type RecordKind struct {
	// Kind is TaskClaim, PublishedContext or InboxMessage.
	Kind string
	// Limits are the effective bounds, as Create applies them.
	Limits Limits
}

// RecordCollector is a prometheus.Collector over the coordination records in
// one namespace. It counts at scrape time with uncached, metadata-only lists,
// the same reads CheckRoom makes: the operator may get, create and list these
// kinds, not watch them, and a cache would hold every record's text.
type RecordCollector struct {
	Reader    client.Reader
	Namespace string
	Kinds     []RecordKind
}

// Describe implements prometheus.Collector.
func (c *RecordCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- recordsDesc
	ch <- recordsSessionMaxDesc
	ch <- recordLimitDesc
}

// Collect implements prometheus.Collector. A kind whose count is unconfirmed
// (a failed list or page) reports no count series for that scrape: never a
// partial count, and never an invalid metric that would also fail the session
// metrics' scrape. Its limit series stay.
func (c *RecordCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), recordCollectTimeout)
	defer cancel()
	for _, k := range c.Kinds {
		ch <- prometheus.MustNewConstMetric(recordLimitDesc, prometheus.GaugeValue, float64(k.Limits.PerSession), k.Kind, "session")
		ch <- prometheus.MustNewConstMetric(recordLimitDesc, prometheus.GaugeValue, float64(k.Limits.PerProject), k.Kind, "project")
		projects, sessionMax, err := c.count(ctx, k.Kind)
		if err != nil {
			continue
		}
		for project, n := range projects {
			ch <- prometheus.MustNewConstMetric(recordsDesc, prometheus.GaugeValue, float64(n), k.Kind, project)
			ch <- prometheus.MustNewConstMetric(recordsSessionMaxDesc, prometheus.GaugeValue, float64(sessionMax[project]), k.Kind, project)
		}
	}
}

// count reads every record of kind that carries a project label, page by page.
func (c *RecordCollector) count(ctx context.Context, kind string) (map[string]int64, map[string]int64, error) {
	projects, sources := map[string]int64{}, map[[2]string]int64{}
	cursor := ""
	for {
		page := &metav1.PartialObjectMetadataList{}
		page.SetGroupVersionKind(coord.GroupVersion.WithKind(kind + "List"))
		if err := c.Reader.List(ctx, page, client.InNamespace(c.Namespace), client.HasLabels{ProjectLabel}, client.Limit(recordPage), client.Continue(cursor)); err != nil {
			return nil, nil, err
		}
		for _, item := range page.Items {
			project := item.Labels[ProjectLabel]
			projects[project]++
			if source := item.Labels[SourceLabel]; source != "" {
				sources[[2]string{project, source}]++
			}
		}
		if page.Continue == "" {
			break
		}
		cursor = page.Continue
	}
	sessionMax := map[string]int64{}
	for key, n := range sources {
		sessionMax[key[0]] = max(sessionMax[key[0]], n)
	}
	return projects, sessionMax, nil
}
