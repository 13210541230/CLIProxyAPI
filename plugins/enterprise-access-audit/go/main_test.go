package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONDispatchWiresInterceptAndCompletion(t *testing.T) {
	root := t.TempDir()
	yaml := "data_dir: " + filepath.ToSlash(root) + "\ndatabase_path: " + filepath.ToSlash(filepath.Join(root, "audit.sqlite")) + "\n"
	configYAML := base64.StdEncoding.EncodeToString([]byte(yaml))
	registrationRaw, err := handleMethod("plugin.register", []byte(`{"schema_version":2,"config_yaml":"`+configYAML+`"}`))
	if err != nil {
		t.Fatalf("register error = %v", err)
	}
	var registrationEnvelope envelope
	if err := json.Unmarshal(registrationRaw, &registrationEnvelope); err != nil || !registrationEnvelope.OK {
		t.Fatalf("registration envelope = %s, error=%v", registrationRaw, err)
	}
	request := `{"RequestID":"dispatch-1","SourceFormat":"openai","RequestedModel":"allowed","Body":"eyJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGkifV19","Metadata":{"request_path":"/v1/chat/completions","quota_key_hash":"deadbeef"}}`
	interceptRaw, err := handleMethod("request.intercept_before", []byte(request))
	if err != nil {
		t.Fatalf("intercept error = %v", err)
	}
	var interceptEnvelope envelope
	if err := json.Unmarshal(interceptRaw, &interceptEnvelope); err != nil || !interceptEnvelope.OK {
		t.Fatalf("intercept envelope = %s, error=%v", interceptRaw, err)
	}
	completionRaw, err := handleMethod("request.complete", []byte(`{"RequestID":"dispatch-1","SourceFormat":"openai","Model":"allowed","Outcome":"succeeded","StatusCode":200,"Metadata":{"request_path":"/v1/chat/completions","quota_key_hash":"deadbeef"}}`))
	if err != nil || !strings.Contains(string(completionRaw), `"ok":true`) {
		t.Fatalf("completion = %s, error=%v", completionRaw, err)
	}
	_, _ = handleMethod("plugin.shutdown", nil)
}
