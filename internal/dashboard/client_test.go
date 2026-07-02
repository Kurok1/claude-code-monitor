/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */

package dashboard

import "testing"

func TestParseClient(t *testing.T) {
	cases := []struct {
		raw     string
		want    Client
		wantErr bool
	}{
		{"", ClientAll, false},
		{"all", ClientAll, false},
		{"claude", ClientClaude, false},
		{"codex", ClientCodex, false},
		{"Claude", "", true},
		{"gpt", "", true},
	}
	for _, c := range cases {
		got, err := ParseClient(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseClient(%q): want error, got %q", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseClient(%q) = %q, %v; want %q", c.raw, got, err, c.want)
		}
	}
}

func TestClientArms(t *testing.T) {
	if !ClientAll.includesClaude() || !ClientAll.includesCodex() {
		t.Error("ClientAll must include both arms")
	}
	if !ClientClaude.includesClaude() || ClientClaude.includesCodex() {
		t.Error("ClientClaude must include claude only")
	}
	if ClientCodex.includesClaude() || !ClientCodex.includesCodex() {
		t.Error("ClientCodex must include codex only")
	}
}
