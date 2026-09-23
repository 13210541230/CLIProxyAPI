package pluginhost

import (
	"context"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const schedulerReasonLimit = 256

func (h *Host) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	provider := schedulerRequestProvider(req)
	exclusive, ownerID, ownerConflict := h.exclusiveScheduler(provider)
	if exclusive {
		if errIdentity := validateSchedulerCallerHash(req.Options.Metadata); errIdentity != nil {
			return pluginapi.SchedulerPickResponse{}, true, errIdentity
		}
	}
	record := h.schedulerRecordForRequest(provider, exclusive, ownerID, ownerConflict)
	if record == nil {
		if exclusive {
			// An owner that is still loaded but no longer declares scheduler
			// capability for this provider (e.g. the account pool was switched
			// off) has relinquished its exclusive claim; fall back to built-in
			// scheduling. A genuinely missing/failed owner keeps failing closed.
			if !ownerConflict && h.ownerRelinquishedScheduler(ownerID, provider) {
				return pluginapi.SchedulerPickResponse{}, false, nil
			}
			return pluginapi.SchedulerPickResponse{}, true, schedulerUnavailableError()
		}
		return pluginapi.SchedulerPickResponse{}, false, nil
	}

	resp, handled, errPick := h.callScheduler(ctx, *record, req)
	if errPick != nil {
		if exclusive {
			return pluginapi.SchedulerPickResponse{}, true, schedulerUnavailableError()
		}
		return resp, handled, errPick
	}
	if !handled {
		if exclusive {
			return pluginapi.SchedulerPickResponse{}, true, schedulerUnavailableError()
		}
		return resp, false, nil
	}

	resp, valid, reason := normalizeSchedulerResponse(resp, req)
	if !valid {
		log.WithField("plugin_id", record.id).Warnf("pluginhost: scheduler returned invalid response: %s", reason)
		if exclusive {
			return pluginapi.SchedulerPickResponse{}, true, schedulerUnavailableError()
		}
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	if resp.Decision == pluginapi.SchedulerDecisionReject {
		return resp, true, schedulerDecisionError(resp)
	}
	return resp, true, nil
}

func (h *Host) HasScheduler() bool {
	return h.schedulerRecord() != nil
}

func (h *Host) SchedulerWantsAcrossPriorities() bool {
	record := h.schedulerRecord()
	if record == nil {
		return false
	}
	return schedulerWantsAcrossPriorities(record.plugin.Capabilities)
}

func (h *Host) schedulerRecord() *capabilityRecord {
	return h.schedulerRecordForRequest("", false, "", false)
}

func (h *Host) schedulerRecordForRequest(provider string, exclusive bool, ownerID string, ownerConflict bool) *capabilityRecord {
	if h == nil || ownerConflict {
		return nil
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		if exclusive {
			if record.id != ownerID || !schedulerSupportsProvider(record.plugin, provider) {
				continue
			}
		}
		copyRecord := record
		return &copyRecord
	}
	return nil
}

func (h *Host) callScheduler(ctx context.Context, record capabilityRecord, req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, handled bool, err error) {
	scheduler := record.plugin.Capabilities.Scheduler
	if h == nil || scheduler == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "Scheduler.Pick", recovered)
			resp = pluginapi.SchedulerPickResponse{}
			handled = false
			err = nil
		}
	}()

	req.Plugin = record.meta
	resp, errPick := scheduler.Pick(ctx, req)
	if errPick != nil {
		log.WithField("plugin_id", record.id).WithError(errPick).Warn("pluginhost: scheduler rejected auth pick")
		return pluginapi.SchedulerPickResponse{}, true, errPick
	}
	return resp, true, nil
}

func normalizeSchedulerResponse(resp pluginapi.SchedulerPickResponse, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, string) {
	resp.Decision = pluginapi.SchedulerDecision(strings.ToLower(strings.TrimSpace(string(resp.Decision))))
	resp.AuthID = strings.TrimSpace(resp.AuthID)
	resp.DelegateBuiltin = strings.TrimSpace(resp.DelegateBuiltin)
	resp.ErrorCode = strings.TrimSpace(resp.ErrorCode)
	resp.Reason = boundedSchedulerReason(resp.Reason)

	// Empty Decision is the legacy response shape. It is converted locally so
	// older non-exclusive plugins retain their existing ABI behavior.
	if resp.Decision == "" {
		if !resp.Handled {
			return pluginapi.SchedulerPickResponse{}, false, "unhandled legacy response"
		}
		switch {
		case resp.AuthID != "":
			resp.Decision = pluginapi.SchedulerDecisionSelected
		case resp.DelegateBuiltin != "":
			resp.Decision = pluginapi.SchedulerDecisionDelegateBuiltin
		default:
			return pluginapi.SchedulerPickResponse{}, false, "missing auth id or delegate"
		}
	}

	switch resp.Decision {
	case pluginapi.SchedulerDecisionSelected:
		if resp.AuthID == "" {
			return pluginapi.SchedulerPickResponse{}, false, "selected decision missing auth id"
		}
		if !schedulerCandidateExists(req.Candidates, resp.AuthID) {
			return pluginapi.SchedulerPickResponse{}, false, "unknown auth id"
		}
		resp.Handled = true
		return resp, true, ""
	case pluginapi.SchedulerDecisionDelegateBuiltin:
		if !validSchedulerBuiltin(resp.DelegateBuiltin) {
			return pluginapi.SchedulerPickResponse{}, false, "unknown delegate"
		}
		resp.Handled = true
		return resp, true, ""
	case pluginapi.SchedulerDecisionReject:
		if resp.ErrorCode == "" {
			return pluginapi.SchedulerPickResponse{}, false, "reject decision missing error code"
		}
		if resp.HTTPStatus < 400 || resp.HTTPStatus > 599 {
			return pluginapi.SchedulerPickResponse{}, false, "reject decision has invalid http status"
		}
		resp.Handled = true
		return resp, true, ""
	default:
		return pluginapi.SchedulerPickResponse{}, false, "unknown scheduler decision"
	}
}

func schedulerDecisionError(resp pluginapi.SchedulerPickResponse) error {
	message := resp.Reason
	if message == "" {
		message = resp.ErrorCode
	}
	return &coreauth.Error{
		Code:       resp.ErrorCode,
		Message:    message,
		Retryable:  resp.Retryable,
		HTTPStatus: resp.HTTPStatus,
	}
}

func validateSchedulerCallerHash(metadata map[string]any) error {
	value, ok := metadata["quota_key_hash"]
	if !ok {
		return &coreauth.Error{Code: "identity_missing", Message: "canonical caller identity is missing", Retryable: false, HTTPStatus: 401}
	}
	hash, ok := value.(string)
	if !ok || len(strings.TrimSpace(hash)) != 8 {
		return &coreauth.Error{Code: "identity_missing", Message: "canonical caller identity is invalid", Retryable: false, HTTPStatus: 400}
	}
	for _, char := range strings.TrimSpace(hash) {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return &coreauth.Error{Code: "identity_missing", Message: "canonical caller identity is invalid", Retryable: false, HTTPStatus: 400}
		}
	}
	return nil
}

func schedulerUnavailableError() error {
	return &coreauth.Error{
		Code:       "policy_unavailable",
		Message:    "exclusive scheduler policy is unavailable",
		Retryable:  true,
		HTTPStatus: 503,
	}
}

func boundedSchedulerReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > schedulerReasonLimit {
		return reason[:schedulerReasonLimit]
	}
	return reason
}

func schedulerRequestProvider(req pluginapi.SchedulerPickRequest) string {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "" && provider != "mixed" {
		return provider
	}
	for _, candidate := range req.Providers {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "codex" {
			return candidate
		}
	}
	return provider
}

func (h *Host) exclusiveScheduler(provider string) (bool, string, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if h == nil || provider == "" {
		return false, "", false
	}
	h.mu.Lock()
	owners := append([]string(nil), h.exclusiveOwners[provider]...)
	h.mu.Unlock()
	if len(owners) == 0 {
		return false, "", false
	}
	if len(owners) != 1 {
		return true, "", true
	}
	return true, owners[0], false
}

func schedulerSupportsProvider(plugin pluginapi.Plugin, provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if plugin.Capabilities.Scheduler == nil {
		return false
	}
	for _, candidate := range plugin.Capabilities.SchedulerExclusiveProviders {
		if strings.ToLower(strings.TrimSpace(candidate)) == provider {
			return true
		}
	}
	return false
}

// ownerRelinquishedScheduler reports whether the exclusive owner plugin is
// loaded but no longer declares a scheduler for the provider, meaning its
// exclusive claim has been released (as opposed to an owner that failed to
// load, which must keep failing closed).
func (h *Host) ownerRelinquishedScheduler(ownerID, provider string) bool {
	if h == nil || ownerID == "" {
		return false
	}
	for _, record := range h.activeRecords() {
		if record.id != ownerID {
			continue
		}
		// Only an owner that dropped its scheduler capability entirely has
		// relinquished the claim. An owner still declaring a scheduler for a
		// different scope keeps failing closed so configuration mismatches
		// stay visible instead of silently falling back.
		return record.plugin.Capabilities.Scheduler == nil
	}
	return false
	return false
}

func schedulerCandidateExists(candidates []pluginapi.SchedulerAuthCandidate, authID string) bool {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == authID {
			return true
		}
	}
	return false
}

func validSchedulerBuiltin(delegate string) bool {
	switch delegate {
	case pluginapi.SchedulerBuiltinRoundRobin, pluginapi.SchedulerBuiltinFillFirst:
		return true
	default:
		return false
	}
}
