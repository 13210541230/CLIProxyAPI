package modeltrace

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestModelTraceStreamDeltaCompletedAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		want   string
	}{
		{"delta", []string{`{"type":"response.output_text.delta","delta":"1,2,"}`, `{"type":"response.output_text.delta","delta":"3"}`, `{"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1}}}}`}, "1,2,3"},
		{"complete", []string{`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"4,5,6"}]}],"usage":{"input_tokens":12,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1}}}}`}, "4,5,6"},
		{"no-double", []string{`{"type":"response.output_text.delta","delta":"4,5,6"}`, `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"4,5,6"}]}],"usage":{"input_tokens":12,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1}}}}`}, "4,5,6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := make(chan handlers.ModelExecutionChunk, 1000)
			for _, e := range tc.events {
				wire := "data: " + e + "\r\n\r\n"
				for i := 0; i < len(wire); i += 7 {
					chunks <- handlers.ModelExecutionChunk{Payload: []byte(wire[i:min(i+7, len(wire))])}
				}
			}
			close(chunks)
			out, err := readStream(context.Background(), chunks)
			if err != nil || out.Text != tc.want || out.InputTokens != 12 || out.OutputTokens != 3 || out.ReasoningTokens != 1 {
				t.Fatal(out, err)
			}
		})
	}
}

// Codex's native SDK emits complete SSE lines without newline delimiters.
func TestModelTraceStreamAcceptsNativeSDKLines(t *testing.T) {
	chunks := make(chan handlers.ModelExecutionChunk, 7)
	for _, line := range []string{
		`: keepalive`,
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"synthetic"}}`,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"1,2,3"}`,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"1,2,3"}]}],"usage":{"input_tokens":12,"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1}}}}`,
	} {
		chunks <- handlers.ModelExecutionChunk{Payload: []byte(line)}
	}
	close(chunks)
	out, err := readSDKStream(context.Background(), chunks)
	if err != nil || out.Text != "1,2,3" || out.InputTokens != 12 || out.OutputTokens != 3 || out.ReasoningTokens != 1 {
		t.Fatal(out, err)
	}
}

func TestModelTraceHTTPFragmentsDoNotPromoteCommentOrEventText(t *testing.T) {
	for _, prefix := range []string{": ", "event: "} {
		for _, split := range []int{1, len(prefix)} {
			for _, kind := range []string{"response.completed", "response.output_text.delta", "response.failed"} {
				t.Run(fmt.Sprintf("%q/%d/%s", prefix, split, kind), func(t *testing.T) {
					wire := prefix + fmt.Sprintf(`data: {"type":%q,"delta":"comment-only"}`, kind) + "\n\n"
					chunks := make(chan handlers.ModelExecutionChunk, 3)
					chunks <- handlers.ModelExecutionChunk{Payload: []byte(wire[:split])}
					chunks <- handlers.ModelExecutionChunk{Payload: []byte(wire[split:])}
					if kind != "response.completed" {
						chunks <- handlers.ModelExecutionChunk{Payload: []byte("data: {\"type\":\"response.completed\"}\n\n")}
					}
					close(chunks)
					out, err := readStream(context.Background(), chunks)
					if kind == "response.completed" {
						if err == nil || !strings.Contains(err.Error(), "without completion") {
							t.Fatal("promoted comment/event content to completion", out, err)
						}
					} else if err != nil || out.Text != "" {
						t.Fatal("processed comment/event content", out, err)
					}
				})
			}
		}
	}
}

func TestModelTraceSDKLineSizeIncludesWhitespace(t *testing.T) {
	for _, prefix := range []string{":", "event: synthetic", `data: {"type":"response.created"}`} {
		for _, leading := range []bool{false, true} {
			for _, size := range []int{maxFrame, maxFrame + 1} {
				t.Run(fmt.Sprintf("%s/leading=%t/size=%d", prefix, leading, size), func(t *testing.T) {
					padding := strings.Repeat(" ", size-len(prefix))
					line := prefix + padding
					if leading {
						line = padding + prefix
					}
					chunks := make(chan handlers.ModelExecutionChunk, 2)
					chunks <- handlers.ModelExecutionChunk{Payload: []byte(line)}
					chunks <- handlers.ModelExecutionChunk{Payload: []byte(`data: {"type":"response.completed"}`)}
					close(chunks)
					_, err := readSDKStream(context.Background(), chunks)
					if size > maxFrame {
						if err == nil || !strings.Contains(err.Error(), "frame exceeds limit") {
							t.Fatal("accepted oversized SDK line", err)
						}
					} else if err != nil {
						t.Fatal("rejected boundary SDK line", err)
					}
				})
			}
		}
	}
}

func TestModelTraceSDKFailureCompletionAndLimits(t *testing.T) {
	for _, line := range []string{
		`data: {"type":"response.failed"}`,
		`data: {"type":"response.incomplete"}`,
		`data: [DONE]`,
		`data: {"type":"response.output_text.delta","delta":"1"}`,
		fmt.Sprintf(`data: {"type":"response.output_text.delta","delta":%q}`, strings.Repeat("x", maxText+1)),
	} {
		chunks := make(chan handlers.ModelExecutionChunk, 1)
		chunks <- handlers.ModelExecutionChunk{Payload: []byte(line)}
		close(chunks)
		if _, err := readSDKStream(context.Background(), chunks); err == nil {
			t.Fatal("accepted unsuccessful SDK stream", line[:min(len(line), 50)])
		}
	}
	chunks := make(chan handlers.ModelExecutionChunk, 12)
	for i := 0; i <= maxWire/maxFrame; i++ {
		chunks <- handlers.ModelExecutionChunk{Payload: []byte(":" + strings.Repeat("x", maxFrame-1))}
	}
	close(chunks)
	if _, err := readSDKStream(context.Background(), chunks); err == nil || !strings.Contains(err.Error(), "stream exceeds limit") {
		t.Fatal("accepted oversized SDK wire", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readSDKStream(ctx, make(chan handlers.ModelExecutionChunk)); err == nil {
		t.Fatal("ignored SDK cancellation")
	}
}

func TestModelTraceStreamLimitsCancellationAndErrors(t *testing.T) {
	for _, event := range []string{`{"type":"response.failed","response":{"error":{"message":"sensitive"}}}`, `{"type":"response.incomplete"}`, `not json`, fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, strings.Repeat("x", maxText+1))} {
		chunks := make(chan handlers.ModelExecutionChunk, 1)
		chunks <- handlers.ModelExecutionChunk{Payload: []byte("data: " + event + "\n\n")}
		close(chunks)
		if _, err := readStream(context.Background(), chunks); err == nil {
			t.Fatal("accepted invalid", event[:min(len(event), 50)])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readStream(ctx, make(chan handlers.ModelExecutionChunk)); err == nil {
		t.Fatal("ignored cancellation")
	}
}
