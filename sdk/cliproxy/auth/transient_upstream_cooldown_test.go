package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestManager_MarkResult_TransientUpstreamNoCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	cases := []struct {
		name string
		err  *Error
	}{
		{name: "500 empty stream", err: &Error{HTTPStatus: http.StatusInternalServerError, Message: "empty_stream: upstream stream closed before first payload"}},
		{name: "502 bad gateway", err: &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream stream ended with incomplete SSE data frame"}},
		{name: "503 service unavailable", err: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: `{"error":{"code":"server_is_overloaded"}}`}},
		{name: "504 gateway timeout", err: &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "stream error: stream disconnected before completion"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-transient-" + tc.name, Provider: "openai-compatible"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			model := "mimo-v2.5"
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    tc.err,
			})

			assertNoCooldown(t, m, auth.ID, model)
		})
	}
}

func TestTransientUpstreamExemptionRequiresStatus(t *testing.T) {
	// A body-only message with no HTTP status must not accidentally avoid
	// cooldown through the transient path.
	err := &Error{Message: "upstream stream ended with incomplete SSE data frame"}
	if isTransientUpstreamResultError(err) {
		t.Fatal("transient upstream classification must require an HTTP status code")
	}
}

func TestTransientUpstreamExemptionDoesNotBypassForceCooldown(t *testing.T) {
	err := &Error{HTTPStatus: http.StatusBadGateway, Code: ErrorCodeForceCooldown, Message: "quota exhausted"}
	if shouldSkipCredentialCooldown(err) {
		t.Fatal("force_cooldown must never be skipped even for a 502")
	}
}
