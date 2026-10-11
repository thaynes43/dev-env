package apiv1

import (
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

const SessionDirectoryPath = "/v1/session-directory"

// These views expose only the directory's deliberately whitelisted metadata.
type DirectoryCurrent struct {
	SourceState           string                  `json:"sourceState"`
	PlacementState        string                  `json:"placementState"`
	ObservedAt            time.Time               `json:"observedAt"`
	SourceResourceVersion string                  `json:"sourceResourceVersion,omitempty"`
	Witness               *coord.DirectoryWitness `json:"witness,omitempty"`
	StopState             string                  `json:"stopState"`
}
type SessionDirectory struct {
	Name            string                       `json:"name"`
	UID             string                       `json:"uid"`
	ResourceVersion string                       `json:"resourceVersion"`
	Original        coord.SessionDirectorySpec   `json:"original"`
	Retained        coord.SessionDirectoryStatus `json:"retained"`
	Current         DirectoryCurrent             `json:"current"`
}
type SessionDirectoryList struct {
	Items           []SessionDirectory `json:"items"`
	ResourceVersion string             `json:"resourceVersion"`
	Continue        string             `json:"continue,omitempty"`
}
type DirectoryContextReference struct {
	Name            string                    `json:"name"`
	UID             string                    `json:"uid"`
	ResourceVersion string                    `json:"resourceVersion"`
	Kind            string                    `json:"kind"`
	Key             string                    `json:"key"`
	ContentSHA256   string                    `json:"contentSHA256"`
	ByteCount       int32                     `json:"byteCount"`
	Origin          string                    `json:"origin"`
	Completeness    string                    `json:"completeness"`
	PublishedAt     time.Time                 `json:"publishedAt"`
	DeclaredThread  *DirectoryThreadAssertion `json:"declaredThread,omitempty"`
}

// An assertion retains publisher provenance; it proves no provider restoration.
type DirectoryThreadAssertion struct {
	ConversationID  string    `json:"conversationID"`
	Invocation      string    `json:"invocation"`
	SourceNamespace string    `json:"sourceNamespace"`
	SourceUID       string    `json:"sourceUID"`
	PodName         string    `json:"podName"`
	PodUID          string    `json:"podUID"`
	PublishedAt     time.Time `json:"publishedAt"`
}
type DirectoryContextReferences struct {
	Items                    []DirectoryContextReference `json:"items"`
	DirectoryResourceVersion string                      `json:"directoryResourceVersion"`
	Continue                 string                      `json:"continue,omitempty"`
}
