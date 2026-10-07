package apiserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// apiError is a refusal on its way to the client: the HTTP status and the error
// document. Handlers return it; serve writes it.
type apiError struct {
	status int
	body   apiv1.Error
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.status, e.body.Code, e.body.Message)
}

func newError(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, body: apiv1.Error{Code: code, Message: fmt.Sprintf(format, args...)}}
}

func badRequest(format string, args ...any) *apiError {
	return newError(http.StatusBadRequest, apiv1.CodeBadRequest, format, args...)
}

func forbidden(format string, args ...any) *apiError {
	return newError(http.StatusForbidden, apiv1.CodeForbidden, format, args...)
}

func notFound(format string, args ...any) *apiError {
	return newError(http.StatusNotFound, apiv1.CodeNotFound, format, args...)
}

func internal(format string, args ...any) *apiError {
	return newError(http.StatusInternalServerError, apiv1.CodeInternal, format, args...)
}

// invalid is a 422 naming the fields at fault.
func invalid(fields ...apiv1.FieldError) *apiError {
	msgs := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Field == "" {
			msgs = append(msgs, f.Message)
			continue
		}
		msgs = append(msgs, f.Field+": "+f.Message)
	}
	e := newError(http.StatusUnprocessableEntity, apiv1.CodeInvalid, "%s", strings.Join(msgs, "; "))
	e.body.Fields = fields
	return e
}

func fieldError(field, format string, args ...any) apiv1.FieldError {
	return apiv1.FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// fromKubeError turns an error from the Kubernetes API server into a refusal.
// An Invalid from the AgentSession schema (D-39) becomes a 422 whose fields carry
// the schema's own messages, renamed from spec.<field> to the request's names, so
// the API reports the CEL rules rather than restating them.
func fromKubeError(err error, what string) *apiError {
	return kubeError(err, what, requestField)
}

// fromGrantKubeError is fromKubeError for AccessGrants: an Invalid from their
// schema (D-54) names the fields of a grant request (grantRequestField).
func fromGrantKubeError(err error, what string) *apiError {
	return kubeError(err, what, grantRequestField)
}

// kubeError is fromKubeError with rename turning an object's field path into the
// request's field name.
func kubeError(err error, what string, rename func(string) string) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "%s: the Kubernetes API server could not be reached: %v", what, err)
	}
	st := status.Status()
	switch {
	case apierrors.IsInvalid(err):
		return invalid(statusCauses(st, rename)...)
	case apierrors.IsNotFound(err):
		return notFound("%s: not found", what)
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), apierrors.IsTooManyRequests(err), apierrors.IsServiceUnavailable(err):
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "%s: the Kubernetes API server is busy: %s", what, st.Message)
	default:
		// Forbidden here is the operator's own RBAC, not the caller's: a
		// deployment fault, not a refusal of the request.
		return internal("%s: the Kubernetes API server refused the operator: %s", what, st.Message)
	}
}

// statusCauses lists an Invalid status's causes as request fields.
func statusCauses(st metav1.Status, rename func(string) string) []apiv1.FieldError {
	var out []apiv1.FieldError
	if st.Details != nil {
		for _, c := range st.Details.Causes {
			out = append(out, apiv1.FieldError{Field: rename(c.Field), Message: c.Message})
		}
	}
	if len(out) == 0 {
		out = append(out, apiv1.FieldError{Field: "", Message: st.Message})
	}
	return out
}

// requestField renames an AgentSession field path to the request's name:
// spec.limits.timeout is limits.timeout, metadata.name is name.
func requestField(path string) string {
	switch {
	case strings.HasPrefix(path, "spec."):
		return strings.TrimPrefix(path, "spec.")
	case path == "spec":
		return ""
	case strings.HasPrefix(path, "metadata.name"):
		return "name"
	}
	return path
}

// grantRequestField renames an AccessGrant field path to a grant request's
// name: spec.kube.namespaces[0] is namespaces[0], spec.egress.ports[0].port is
// ports[0].port, spec.credential.name is credential, and a rule on spec, kube or
// egress as a whole names no field.
func grantRequestField(path string) string {
	p := requestField(path)
	switch {
	case p == "kube", p == "egress":
		return ""
	case strings.HasPrefix(p, "kube."):
		return strings.TrimPrefix(p, "kube.")
	case strings.HasPrefix(p, "egress."):
		return strings.TrimPrefix(p, "egress.")
	case p == "credential", strings.HasPrefix(p, "credential."):
		return "credential"
	}
	return p
}

// writeJSON writes v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, e *apiError) {
	if e.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dev-env-operator"`)
	}
	writeJSON(w, e.status, apiv1.ErrorResponse{Error: e.body})
}
