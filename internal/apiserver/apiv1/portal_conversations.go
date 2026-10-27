package apiv1

import "github.com/thaynes43/dev-env/internal/agentd/protocol"

// PortalConversationPrefix is a dedicated action, not a session metadata read.
const PortalConversationPrefix = "/v1/portal/sessions/"

// PortalConversationResponse binds one projection to the resolved target.
type PortalConversationResponse struct {
	SessionName string                      `json:"sessionName"`
	SessionUID  string                      `json:"sessionUID"`
	PodUID      string                      `json:"podUID,omitempty"`
	Projection  protocol.PortalConversation `json:"projection"`
}
