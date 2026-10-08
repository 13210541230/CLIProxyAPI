package modeltrace

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

// Both upstream MIT notices are embedded in distributed CPA binaries.
//
//go:embed data/PLUGIN-LICENSE
var PluginLicense string

const maxWire = 2 * 1024 * 1024
const maxFrame = 256 * 1024

type ModelExecutor interface {
	ExecuteModelStream(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage)
}

// Execute retains the original Codex context and delegates auth selection and
// normal accounting to CPA. Cancellation is explicit; no extra timeout/retry.
func Execute(ctx context.Context, executor ModelExecutor, c Credential, model string, ch Challenge) (Output, error) {
	ctx, cancel := context.WithCancel(handlers.WithManagementCredentialProbe(ctx, c.ID))
	defer cancel()
	body, headers := codexTurn(c.ID, model, "", ch.Prompt)
	raw, err := json.Marshal(body)
	if err != nil {
		return Output{}, err
	}
	stream, errMsg := executor.ExecuteModelStream(ctx, handlers.ModelExecutionRequest{EntryProtocol: "codex", ExitProtocol: "codex", ForcedProvider: "codex", AuthID: c.ID, Model: model, Stream: true, Body: raw, Headers: headers})
	if errMsg != nil {
		return Output{}, executionFailure("model execution rejected", errMsg.StatusCode)
	}
	if stream.StatusCode >= 400 {
		return Output{}, executionFailure("model execution failed", stream.StatusCode)
	}
	out, err := readSDKStream(ctx, stream.Chunks)
	if err != nil && ctx.Err() == nil {
		// Decoder failures are static local messages, never upstream bodies.
		err = &executionDiagnostic{message: err.Error()}
	}
	return out, err
}

// Only this trusted diagnostic type may be persisted by the service. Never
// include upstream error bodies, headers, credential URLs, or request context.
type executionDiagnostic struct{ message string }

func (e *executionDiagnostic) Error() string { return e.message }

func executionFailure(stage string, status int) error {
	if status >= 400 && status <= 599 {
		stage = fmt.Sprintf("%s (HTTP %d)", stage, status)
	}
	return &executionDiagnostic{message: stage}
}

// SDK chunks contain complete Codex SSE lines, not arbitrary HTTP fragments.
func readSDKStream(ctx context.Context, chunks <-chan handlers.ModelExecutionChunk) (Output, error) {
	return readStreamFramed(ctx, chunks, true)
}

// HTTP byte fragments must be accumulated; their prefixes are not complete lines.
func readStream(ctx context.Context, chunks <-chan handlers.ModelExecutionChunk) (Output, error) {
	return readStreamFramed(ctx, chunks, false)
}

func readStreamFramed(ctx context.Context, chunks <-chan handlers.ModelExecutionChunk, sdkLines bool) (Output, error) {
	var out Output
	var pending []byte
	wire := 0
	completed := false
	process := func(frame []byte) error {
		var data []string
		for _, line := range bytes.Split(frame, []byte("\n")) {
			line = bytes.TrimSuffix(line, []byte("\r"))
			if bytes.HasPrefix(line, []byte("data:")) {
				data = append(data, strings.TrimSpace(string(line[5:])))
			}
		}
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		if raw == "[DONE]" {
			return nil
		}
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Output []struct {
					Type    string `json:"type"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Usage struct {
					Input   int64 `json:"input_tokens"`
					Output  int64 `json:"output_tokens"`
					Details struct {
						Reasoning int64 `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return errors.New("invalid model stream event")
		}
		if event.Response.Usage.Input != 0 || event.Response.Usage.Output != 0 {
			out.InputTokens = event.Response.Usage.Input
			out.OutputTokens = event.Response.Usage.Output
			out.ReasoningTokens = event.Response.Usage.Details.Reasoning
		}
		switch event.Type {
		case "response.output_text.delta":
			out.Text += event.Delta
		case "response.completed":
			var text strings.Builder
			for _, item := range event.Response.Output {
				if item.Type == "message" {
					for _, part := range item.Content {
						if part.Type == "output_text" {
							text.WriteString(part.Text)
							if text.Len() > maxText {
								return errors.New("response text exceeds limit")
							}
						}
					}
				}
			}
			// Completed output is authoritative when present, not appended to deltas.
			if text.Len() > 0 {
				out.Text = text.String()
			}
			completed = true
		case "response.failed", "response.incomplete", "error":
			return errors.New("model stream did not complete")
		}
		if len(out.Text) > maxText {
			out.Text = ""
			return errors.New("response text exceeds limit")
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				if len(bytes.TrimSpace(pending)) > 0 {
					if err := process(pending); err != nil {
						return out, err
					}
				}
				if !completed {
					return out, errors.New("model stream closed without completion")
				}
				return out, nil
			}
			if chunk.Err != nil {
				return out, executionFailure("model stream failed", chunk.Err.StatusCode)
			}
			wire += len(chunk.Payload)
			if wire > maxWire {
				return out, errors.New("model stream exceeds limit")
			}
			if sdkLines {
				// SDK payloads contain complete lines (possibly including their
				// terminators). Count raw whitespace, not a trimmed representation.
				for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
					line = bytes.TrimSuffix(line, []byte("\r"))
					if len(line) > maxFrame {
						return out, errors.New("model stream frame exceeds limit")
					}
					if err := process(line); err != nil {
						return out, err
					}
				}
				continue
			}
			pending = append(pending, chunk.Payload...)
			// Normalize after accumulation, including CRLF split across chunks.
			pending = bytes.ReplaceAll(pending, []byte("\r\n"), []byte("\n"))
			for {
				i := bytes.Index(pending, []byte("\n\n"))
				if i < 0 {
					break
				}
				if i > maxFrame {
					return out, errors.New("model stream frame exceeds limit")
				}
				if err := process(pending[:i]); err != nil {
					return out, err
				}
				pending = pending[i+2:]
			}
			if len(pending) > maxFrame {
				return out, errors.New("model stream frame exceeds limit")
			}
		}
	}
}
