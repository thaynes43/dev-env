package apiserver

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// admitThreadHost precedes generic profile/ancestry allowances on both routes.
// Creation spec and authenticated pod account are separate requirements: a
// raised workbench pod and a patched profile label cannot supply creation rights.
func (s *Server) admitThreadHost(ctx context.Context, c *caller, host *v1alpha1.AgentSession, pod *corev1.Pod, coordinator bool) error {
	resolved := s.codexHostProfile(ctx, host.Spec.Profile)
	if err := s.admitCodexHost(ctx, host, resolved, false); err != nil {
		return err
	}
	reserved := s.CodexWorkbenchHosts.Name != "" && host.Name == s.CodexWorkbenchHosts.Name
	podWorkbench := pod != nil && pod.Spec.ServiceAccountName == templates.WorkbenchServiceAccount
	privileged := reserved || resolved.Workbench() || podWorkbench
	if !privileged {
		if coordinator {
			return forbidden("coordinator intent requires the operator-named workbench host; no fallback")
		}
		return nil
	}
	if !coordinator {
		return forbidden("this host is reserved for an explicit coordinator launch")
	}
	if !reserved || !resolved.Workbench() || !podWorkbench {
		return forbidden("the coordinator host account does not match its declaration")
	}
	if c.kind == kindHuman || c.kind == kindClient {
		return nil
	}
	if s.isWorkbenchSession(c) && c.session != nil && c.pod != nil && c.pod.Spec.ServiceAccountName == templates.WorkbenchServiceAccount && s.codexHostProfile(ctx, c.session.Spec.Profile).Workbench() {
		return nil
	}
	return newError(http.StatusForbidden, apiv1.CodeForbidden, "coordinator threads require Tom, a trusted client, or a workbench-by-creation session with a workbench pod")
}
