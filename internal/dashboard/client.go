/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.3.0
 */

package dashboard

import "fmt"

// Client selects which telemetry family a query covers. Wire values are the
// `client` query parameter on every /api endpoint; empty means all.
type Client string

const (
	ClientAll    Client = "all"
	ClientClaude Client = "claude"
	ClientCodex  Client = "codex"
)

// ParseClient validates the raw query parameter. Empty string → ClientAll.
func ParseClient(raw string) (Client, error) {
	switch raw {
	case "", "all":
		return ClientAll, nil
	case "claude":
		return ClientClaude, nil
	case "codex":
		return ClientCodex, nil
	}
	return "", fmt.Errorf("invalid client %q: want all|claude|codex", raw)
}

func (c Client) includesClaude() bool { return c == ClientAll || c == ClientClaude }
func (c Client) includesCodex() bool  { return c == ClientAll || c == ClientCodex }
