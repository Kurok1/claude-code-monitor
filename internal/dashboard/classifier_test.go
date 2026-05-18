package dashboard

import (
	"testing"

	"github.com/kuroky/claude-code-monitor/internal/config"
)

func TestClassifier_BuiltinClaude(t *testing.T) {
	c, err := NewClassifier(nil)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	cases := []struct {
		in, want string
	}{
		{"claude-opus-4-7", "opus-4.7"},
		{"claude-opus-4-7[1m]", "opus-4.7"},      // [1m] context suffix stripped
		{"claude-opus-4-1-20250805", "opus-4.1"}, // date snapshot stripped
		{"claude-sonnet-4-6", "sonnet-4.6"},
		{"claude-haiku-4-5-20251001", "haiku-4.5"},
		{"Claude-Opus-4-7", "opus-4.7"}, // case-insensitive
	}
	for _, tc := range cases {
		if got := c.Classify(tc.in); got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifier_ThirdPartyPassthrough(t *testing.T) {
	c, _ := NewClassifier(nil)
	cases := []string{
		"deepseek-v3",
		"deepseek-r1",
		"gpt-4o",
		"gemini-2.0-flash",
		"qwen-max",
	}
	for _, in := range cases {
		if got := c.Classify(in); got != in {
			t.Errorf("Classify(%q) = %q, want raw passthrough", in, got)
		}
	}
}

func TestClassifier_UserRulesOverrideBuiltin(t *testing.T) {
	c, err := NewClassifier([]config.ModelGroupRule{
		// First rule wins: collapse all opus versions into one bucket.
		{Pattern: `(?i)^claude-opus-.*`, Group: "opus"},
		// Merge deepseek variants.
		{Pattern: `(?i)^deepseek-.*`, Group: "deepseek"},
		// Capture-group reference: gpt-4o → gpt-4
		{Pattern: `(?i)^gpt-(\d+).*`, Group: "gpt-$1"},
	})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	cases := []struct {
		in, want string
	}{
		{"claude-opus-4-7[1m]", "opus"},      // user rule beats built-in
		{"claude-opus-4-1-20250805", "opus"}, // user rule beats built-in
		{"claude-sonnet-4-6", "sonnet-4.6"},  // falls through to built-in
		{"deepseek-v3", "deepseek"},
		{"deepseek-r1", "deepseek"},
		{"gpt-4o", "gpt-4"},
		{"gpt-3.5-turbo", "gpt-3"},
		{"qwen-max", "qwen-max"}, // no rule matches → raw passthrough
	}
	for _, tc := range cases {
		if got := c.Classify(tc.in); got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifier_EmptyInput(t *testing.T) {
	c, _ := NewClassifier(nil)
	if got := c.Classify(""); got != "" {
		t.Errorf("Classify(\"\") = %q, want empty", got)
	}
}

func TestClassifier_BadRegex(t *testing.T) {
	_, err := NewClassifier([]config.ModelGroupRule{
		{Pattern: `[invalid`, Group: "x"},
	})
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
}
