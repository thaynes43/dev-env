package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const TaskClaimMaxRequestBytes = 4096

// TaskClaimClient exposes only fixed in-session claim operations. Ownership is
// derived by the server; the client cannot supply another owner or a raw route.
type TaskClaimClient struct{ api *InboxClient }

func NewTaskClaimClient(s Settings) (*TaskClaimClient, error) {
	c, err := NewInboxClient(s)
	if err != nil {
		return nil, errors.New("task claim API identity or TLS unavailable")
	}
	return &TaskClaimClient{api: c}, nil
}

func ValidTaskClaimIdentity(i apiv1.TaskClaimIdentity) bool {
	return taskclaim.ValidIdentity(coord.TaskIdentity{Project: i.Project, Repository: i.Repository, TaskKey: i.TaskKey})
}

func ValidTaskClaimPage(project string, limit int, cursor string) bool {
	return projectcatalog.ValidName(project) && limit >= 1 && limit <= 100 && len(cursor) <= 4096 && utf8.ValidString(cursor) && !strings.ContainsAny(cursor, "\x00\r\n")
}

func ValidTaskClaimRead(project, name, uid string) bool {
	return projectcatalog.ValidName(project) && taskclaim.ValidName(name) && (uid == "" || protocol.ValidKubeUID(uid))
}

func (c *TaskClaimClient) Create(ctx context.Context, identity apiv1.TaskClaimIdentity) ([]byte, int, error) {
	if !ValidTaskClaimIdentity(identity) {
		return nil, 0, errors.New("invalid explicit task identity")
	}
	body, err := json.Marshal(apiv1.CreateTaskClaimRequest{Identity: identity})
	if err != nil || len(body) > TaskClaimMaxRequestBytes {
		return nil, 0, errors.New("invalid bounded task claim request")
	}
	data, status, err := c.api.doBounded(ctx, http.MethodPost, apiv1.TaskClaimsPath, body, "task claim", TaskClaimMaxRequestBytes)
	if err != nil {
		return nil, status, err
	}
	if status == 200 || status == 201 || status == 409 {
		var result apiv1.TaskClaimResult
		if json.Unmarshal(data, &result) != nil || !validClaimView(result.Claim, identity.Project) || result.Claim.Identity.TaskKey != identity.TaskKey || (status == 201 && result.Outcome != "Created") || (status == 200 && result.Outcome != "Confirmed") || (status == 409 && result.Outcome != "Conflict") || (status != 409 && result.Claim.Identity != identity) {
			return nil, status, errors.New("task claim reply has unconfirmed ownership metadata")
		}
	} else if status/100 == 2 {
		return nil, status, errors.New("task claim create returned an unexpected success status")
	}
	return data, status, nil
}
func (c *TaskClaimClient) List(ctx context.Context, project string, limit int, cursor string) ([]byte, int, error) {
	if !ValidTaskClaimPage(project, limit, cursor) {
		return nil, 0, errors.New("invalid task claim page")
	}
	q := url.Values{"project": {project}, "limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("continue", cursor)
	}
	data, status, err := c.api.doBounded(ctx, http.MethodGet, apiv1.TaskClaimsPath+"?"+q.Encode(), nil, "task claim", TaskClaimMaxRequestBytes)
	if err != nil {
		return nil, status, err
	}
	if status/100 == 2 {
		var page apiv1.TaskClaimList
		if status != 200 || json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > limit || !ValidTaskClaimPage(project, limit, page.Continue) {
			return nil, status, errors.New("task claim list returned an unconfirmed metadata page")
		}
		for _, claim := range page.Items {
			if !validClaimView(claim, project) {
				return nil, status, errors.New("task claim list returned an unconfirmed ownership identity")
			}
		}
	}
	return data, status, nil
}
func (c *TaskClaimClient) Get(ctx context.Context, project, name, uid string) ([]byte, int, error) {
	if !ValidTaskClaimRead(project, name, uid) {
		return nil, 0, errors.New("invalid task claim identity fence")
	}
	q := url.Values{"project": {project}}
	if uid != "" {
		q.Set("uid", uid)
	}
	data, status, err := c.api.doBounded(ctx, http.MethodGet, apiv1.TaskClaimsPath+"/"+name+"?"+q.Encode(), nil, "task claim", TaskClaimMaxRequestBytes)
	if err != nil {
		return nil, status, err
	}
	if status/100 == 2 {
		var claim apiv1.TaskClaim
		if status != 200 || json.Unmarshal(data, &claim) != nil || !validClaimView(claim, project) || claim.Name != name || (uid != "" && claim.UID != uid) {
			return nil, status, errors.New("task claim get returned an unconfirmed ownership identity")
		}
	}
	return data, status, nil
}

func validClaimView(claim apiv1.TaskClaim, project string) bool {
	return claim.Identity.Project == project && ValidTaskClaimIdentity(claim.Identity) && taskclaim.ValidName(claim.Name) && protocol.ValidKubeUID(claim.UID) && projectcatalog.ValidName(claim.Owner.Namespace) && projectcatalog.ValidName(claim.Owner.Name) && protocol.ValidKubeUID(claim.Owner.UID)
}
