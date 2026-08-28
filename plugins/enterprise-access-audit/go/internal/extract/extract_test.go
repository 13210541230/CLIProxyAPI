package extract

import "testing"

func TestExtractPhaseOneFixtures(t *testing.T) {
	tests := []struct {
		name, format, path, body, wantText, wantReason string
		wantAvailable                                  bool
	}{
		{"chat user only", "openai", "/v1/chat/completions", `{"messages":[{"role":"system","content":"secret"},{"role":"user","content":"hello"},{"role":"assistant","content":"answer"}]}`, "hello", "", true},
		{"chat latest user only", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"old user"},{"role":"assistant","content":"old answer"},{"role":"user","content":"latest user"}]}`, "latest user", "", true},
		{"completion array", "openai", "/v1/completions", `{"prompt":["first","second"]}`, "second", "", true},
		{"numeric prompt", "openai", "/v1/completions", `{"prompt":[12,13],"secret":"do not persist"}`, "", "numeric_prompt", false},
		{"responses string input", "openai-response", "/v1/responses", `{"input":"question"}`, "question", "", true},
		{"responses message", "openai-response", "/v1/responses", `{"instructions":"system","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"ignore"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}]}`, "question", "", true},
		{"responses input text", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}]}`, "question", "", true},
		{"responses latest user only", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"latest question"}]}]}`, "latest question", "", true},
		{"responses assistant input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"assistant","text":"assistant secret"}]}`, "", "no_user_text", false},
		{"responses developer input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"developer","text":"developer secret"}]}`, "", "no_user_text", false},
		{"responses system input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"system","text":"system secret"}]}`, "", "no_user_text", false},
		{"responses missing role input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","text":"missing role secret"}]}`, "", "no_user_text", false},
		{"responses unknown role input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"unknown","text":"unknown role secret"}]}`, "", "no_user_text", false},
		{"chat missing type object", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"text":"missing type secret"}]}]}`, "", "no_user_text", false},
		{"chat unknown type text", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"image","text":"image secret"}]}]}`, "", "no_user_text", false},
		{"claude missing type object", "claude", "/v1/messages", `{"messages":[{"role":"user","content":[{"text":"missing type secret"}]}]}`, "", "no_user_text", false},
		{"claude latest user only", "claude", "/v1/messages", `{"messages":[{"role":"user","content":"old question"},{"role":"assistant","content":"old answer"},{"role":"user","content":"latest question"}]}`, "latest question", "", true},
		{"claude unknown type text", "claude", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_use","text":"tool secret"}]}]}`, "", "no_user_text", false},
		{"gemini parts", "gemini", "/v1beta/models/gemini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"},{"type":"image","text":"image secret"},{"inlineData":{"data":"secret"}}]}]}`, "hello", "", true},
		{"gemini latest user only", "gemini", "/v1beta/models/gemini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"old question"}]},{"role":"model","parts":[{"text":"old answer"}]},{"role":"user","parts":[{"text":"latest question"}]}]}`, "latest question", "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Extract(test.format, test.path, []byte(test.body))
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			if got.Text != test.wantText || got.TextAvailable != test.wantAvailable || got.TextUnavailableReason != test.wantReason {
				t.Fatalf("Extract() = %#v, want text=%q available=%v reason=%q", got, test.wantText, test.wantAvailable, test.wantReason)
			}
		})
	}
}

func TestExtractMalformedNeverReturnsRawBody(t *testing.T) {
	got, err := Extract("openai", "/v1/chat/completions", []byte(`{"messages":`))
	if err == nil || got.Text != "" || got.TextAvailable {
		t.Fatalf("malformed result = %#v, error=%v", got, err)
	}
}
