package extract

import "testing"

func TestExtractPhaseOneFixtures(t *testing.T) {
	tests := []struct {
		name, format, path, body, wantText, wantReason string
		wantAvailable                                  bool
	}{
		{"chat history ending assistant is not a new turn", "openai", "/v1/chat/completions", `{"messages":[{"role":"system","content":"secret"},{"role":"user","content":"hello"},{"role":"assistant","content":"answer"}]}`, "", "no_new_user_turn", false},
		{"chat latest user only", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"old user"},{"role":"assistant","content":"old answer"},{"role":"user","content":"latest user"}]}`, "latest user", "", true},
		{"chat skips harness iteration without new user turn", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"§1689§ 用户真正输入\n<ctx-search-hint>内部提示</ctx-search-hint>"},{"role":"assistant","content":"answer"},{"role":"user","content":"<system-reminder>iteration metadata</system-reminder>"}]}`, "", "no_new_user_turn", false},
		{"chat skips escaped framework notification", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"&lt;background-task-notification&gt;&lt;status&gt;failed&lt;/status&gt;&lt;/background-task-notification&gt;"}]}`, "", "no_new_user_turn", false},
		{"chat strips unmarked framework suffix", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"actual question\n<system-reminder>iteration metadata</system-reminder>"}]}`, "actual question", "", true},
		{"completion array", "openai", "/v1/completions", `{"prompt":["first","second"]}`, "second", "", true},
		{"numeric prompt", "openai", "/v1/completions", `{"prompt":[12,13],"secret":"do not persist"}`, "", "numeric_prompt", false},
		{"responses string input", "openai-response", "/v1/responses", `{"input":"question"}`, "question", "", true},
		{"responses message", "openai-response", "/v1/responses", `{"instructions":"system","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"ignore"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}]}`, "question", "", true},
		{"responses input text", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}]}`, "question", "", true},
		{"responses latest user only", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"latest question"}]}]}`, "latest question", "", true},
		{"responses history ending assistant is not a new turn", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]}]}`, "", "no_new_user_turn", false},
		{"responses history ending tool output is not a new turn", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]},{"type":"function_call_output","call_id":"call-1","output":"tool output"}]}`, "", "no_new_user_turn", false},
		{"responses skips framework input without a new turn", "openai-response", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"§1700§ actual question\n<system-reminder>ignore</system-reminder>"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"<background-task-notification>done</background-task-notification>"}]}]}`, "", "no_new_user_turn", false},
		{"responses assistant input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"assistant","text":"assistant secret"}]}`, "", "no_new_user_turn", false},
		{"responses developer input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"developer","text":"developer secret"}]}`, "", "no_new_user_turn", false},
		{"responses system input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"system","text":"system secret"}]}`, "", "no_new_user_turn", false},
		{"responses missing role input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","text":"missing role secret"}]}`, "", "no_new_user_turn", false},
		{"responses unknown role input text", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"unknown","text":"unknown role secret"}]}`, "", "no_new_user_turn", false},
		{"chat missing type object", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"text":"missing type secret"}]}]}`, "", "no_user_text", false},
		{"chat unknown type text", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"type":"image","text":"image secret"}]}]}`, "", "no_user_text", false},
		{"claude missing type object", "claude", "/v1/messages", `{"messages":[{"role":"user","content":[{"text":"missing type secret"}]}]}`, "", "no_user_text", false},
		{"claude latest user only", "claude", "/v1/messages", `{"messages":[{"role":"user","content":"old question"},{"role":"assistant","content":"old answer"},{"role":"user","content":"latest question"}]}`, "latest question", "", true},
		{"claude history ending assistant is not a new turn", "claude", "/v1/messages", `{"messages":[{"role":"user","content":"old question"},{"role":"assistant","content":"old answer"}]}`, "", "no_new_user_turn", false},
		{"claude unknown type text", "claude", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_use","text":"tool secret"}]}]}`, "", "no_user_text", false},
		{"gemini parts", "gemini", "/v1beta/models/gemini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"},{"type":"image","text":"image secret"},{"inlineData":{"data":"secret"}}]}]}`, "hello", "", true},
		{"gemini latest user only", "gemini", "/v1beta/models/gemini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"old question"}]},{"role":"model","parts":[{"text":"old answer"}]},{"role":"user","parts":[{"text":"latest question"}]}]}`, "latest question", "", true},
		{"gemini history ending model is not a new turn", "gemini", "/v1beta/models/gemini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"old question"}]},{"role":"model","parts":[{"text":"old answer"}]}]}`, "", "no_new_user_turn", false},
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
