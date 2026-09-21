package accountpool

import (
	"net/http"
	"net/url"
)

// schedulerPickRequest mirrors the host SchedulerPickRequest wire shape.
type schedulerPickRequest struct {
	Provider   string
	Providers  []string
	Model      string
	Stream     bool
	Options    schedulerOptions
	Candidates []schedulerAuthCandidate
}

type schedulerOptions struct {
	Headers  map[string][]string
	Metadata map[string]any
}

type schedulerAuthCandidate struct {
	ID         string
	Provider   string
	Priority   int
	Status     string
	Attributes map[string]string
	Metadata   map[string]any
}

// schedulerPickResponse mirrors the host SchedulerPickResponse wire shape.
type schedulerPickResponse struct {
	Decision        string
	AuthID          string
	DelegateBuiltin string
	ErrorCode       string
	HTTPStatus      int
	Retryable       bool
	Reason          string
	Handled         bool
}

// managementRequest is the private JSON-equivalent of pluginapi.ManagementRequest.
type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

// managementResponse is the private JSON-equivalent of pluginapi.ManagementResponse.
type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Public aliases exposed to the ABI bridge while keeping the wire types internal.
type SchedulerPickRequest = schedulerPickRequest
type SchedulerPickResponse = schedulerPickResponse
type ManagementRequest = managementRequest
type ManagementResponse = managementResponse

// AdmitResult is the outcome of gating an already-selected request.
type AdmitResult struct {
	Terminate  bool
	StatusCode int
	Body       []byte
}

const (
	// Metadata key names injected by the CPA host.
	metadataKeyHash        = "quota_key_hash"
	metadataSelectedAuthID = "selected_auth_id"
)

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}
