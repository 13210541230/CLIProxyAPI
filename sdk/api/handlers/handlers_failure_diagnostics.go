package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type requestFailureDetails struct {
	code    string
	typeID  string
	summary string
}

func requestFailureDetailsFromError(err error, body []byte) requestFailureDetails {
	if len(body) == 0 {
		var terminatedErr *coreexecutor.RequestTerminatedError
		if errors.As(err, &terminatedErr) && terminatedErr != nil {
			body = terminatedErr.Body
		}
	}
	if len(body) > 0 {
		details := clienterror.ExtractUpstreamErrorDetails(string(body))
		if details.Code != "" || details.Type != "" || details.Summary != "" {
			return requestFailureDetails{code: details.Code, typeID: details.Type, summary: details.Summary}
		}
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		details := clienterror.ExtractUpstreamErrorDetails(authErr.Message)
		if authErr.Code != "" {
			details.Code = authErr.Code
		}
		return requestFailureDetails{code: details.Code, typeID: details.Type, summary: details.Summary}
	}
	if err == nil {
		return requestFailureDetails{}
	}
	details := clienterror.ExtractUpstreamErrorDetails(err.Error())
	return requestFailureDetails{code: details.Code, typeID: details.Type, summary: details.Summary}
}

func publishAccountPoolSelectionFailureUsage(ctx context.Context, providers []string, model string, stream bool, err error) {
	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil || authErr.HTTPStatus < 500 {
		return
	}
	switch strings.TrimSpace(authErr.Code) {
	case "account_busy", "account_pool_unavailable", "account_pool_disabled":
	default:
		return
	}
	provider := ""
	if len(providers) > 0 {
		provider = strings.TrimSpace(providers[0])
	}
	reporter := helps.NewUsageReporter(ctx, provider, model, nil)
	reporter.SetStream(stream)
	reporter.PublishFailure(ctx, err)
}
