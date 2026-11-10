// Package usagetotals is the operator's leader-only counters of task usage
// that remain after a Session is deleted (DESIGN-059, OpenProject #524).
// The totals live in Prometheus. The leader adds a Session, then writes
// AnnotationCounted. A ready Session that finished more than 168 hours
// earlier is marked skipped and not added. A restart skips a Session that
// already carries any mark.
package usagetotals
