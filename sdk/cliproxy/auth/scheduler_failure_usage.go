package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// schedulerFailureExecutorType marks usage records that never reached an
// executor: the request failed while a plugin-owned scheduler was choosing a
// credential. Executors publish their own records, so without this the whole
// scheduling-failure class is invisible in request monitoring.
const schedulerFailureExecutorType = "scheduler"

// schedulerPolicyUnavailableCode is the plugin scheduler's fail-closed code.
const schedulerPolicyUnavailableCode = "policy_unavailable"

// publishSchedulerPickFailure publishes a result-only zero-token failure record
// when the plugin scheduler failed closed. Other pick failures stay unpublished
// so their existing handling is unchanged.
func publishSchedulerPickFailure(ctx context.Context, provider string, providers []string, model string, opts cliproxyexecutor.Options, errPick error) {
	record, ok := schedulerPickFailureRecord(ctx, provider, providers, model, opts, errPick)
	if !ok {
		return
	}
	coreusage.PublishRecord(ctx, record)
}

// schedulerFailureBody renders the pick error as the JSON envelope the usage
// pipeline parses for error code/type/summary, so monitoring shows the reason
// instead of an empty failure row.
func schedulerFailureBody(errPick error) string {
	message := strings.TrimSpace(errPick.Error())
	code := schedulerPolicyUnavailableCode
	if typed, ok := errPick.(*Error); ok && typed != nil {
		if trimmed := strings.TrimSpace(typed.Code); trimmed != "" {
			code = trimmed
		}
		message = strings.TrimSpace(typed.Message)
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    code,
			"type":    "server_error",
			"message": message,
		},
	})
	if errMarshal != nil {
		return message
	}
	return string(payload)
}

// schedulerPickFailureRecord builds the monitoring record for a fail-closed
// pick. It returns ok=false for every other failure class.
func schedulerPickFailureRecord(ctx context.Context, provider string, providers []string, model string, opts cliproxyexecutor.Options, errPick error) (coreusage.Record, bool) {
	if errPick == nil {
		return coreusage.Record{}, false
	}
	code := ""
	if typed, ok := errPick.(*Error); ok && typed != nil {
		code = strings.TrimSpace(typed.Code)
	}
	if code != schedulerPolicyUnavailableCode {
		return coreusage.Record{}, false
	}
	resolved := strings.ToLower(strings.TrimSpace(provider))
	if resolved == "" || resolved == "mixed" {
		for _, candidate := range providers {
			if candidate = strings.ToLower(strings.TrimSpace(candidate)); candidate != "" {
				resolved = candidate
				break
			}
		}
	}
	clientMeta := logging.GetClientRequestMetadata(ctx)
	return coreusage.Record{
		Provider:        resolved,
		ExecutorType:    schedulerFailureExecutorType,
		Model:           strings.TrimSpace(model),
		APIKey:          coreusage.APIKeyFromContext(ctx),
		SessionID:       clientMeta.SessionID,
		ParentSessionID: clientMeta.ParentSessionID,
		ReasoningEffort: coreusage.ReasoningEffortFromContext(ctx),
		ServiceTier:     coreusage.ServiceTierFromContext(ctx),
		Generate:        coreusage.GenerateFlag(true),
		Stream:          opts.Stream,
		RequestedAt:     time.Now(),
		Failed:          true,
		Fail: coreusage.Failure{
			StatusCode: http.StatusServiceUnavailable,
			Body:       schedulerFailureBody(errPick),
		},
	}, true
}
