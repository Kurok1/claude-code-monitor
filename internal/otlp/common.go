package otlp

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// attrMap is a working set of attributes from which parsers "take" known keys.
// Whatever remains becomes the row's Attrs leftover.
type attrMap map[string]*commonpb.AnyValue

// newAttrMap layers attributes; later layers override earlier ones. Pass
// (resourceAttrs, pointAttrs) so per-point attributes override resource-level.
func newAttrMap(layers ...[]*commonpb.KeyValue) attrMap {
	m := make(attrMap)
	for _, layer := range layers {
		for _, kv := range layer {
			m[kv.Key] = kv.Value
		}
	}
	return m
}

func (m attrMap) take(key string) (*commonpb.AnyValue, bool) {
	v, ok := m[key]
	if ok {
		delete(m, key)
	}
	return v, ok
}

func (m attrMap) takeString(key string) sql.NullString {
	v, ok := m.take(key)
	if !ok || v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: asString(v), Valid: true}
}

func (m attrMap) takeStringArray(key string) []string {
	v, ok := m.take(key)
	if !ok || v == nil {
		return nil
	}
	return asStringArray(v)
}

func (m attrMap) takeInt64(key string) sql.NullInt64 {
	v, ok := m.take(key)
	if !ok || v == nil {
		return sql.NullInt64{}
	}
	if n, ok := asInt64(v); ok {
		return sql.NullInt64{Int64: n, Valid: true}
	}
	return sql.NullInt64{}
}

func (m attrMap) takeInt32(key string) sql.NullInt32 {
	n := m.takeInt64(key)
	if !n.Valid {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(n.Int64), Valid: true}
}

func (m attrMap) takeFloat64(key string) sql.NullFloat64 {
	v, ok := m.take(key)
	if !ok || v == nil {
		return sql.NullFloat64{}
	}
	if f, ok := asFloat64(v); ok {
		return sql.NullFloat64{Float64: f, Valid: true}
	}
	return sql.NullFloat64{}
}

func (m attrMap) takeBool(key string) sql.NullBool {
	v, ok := m.take(key)
	if !ok || v == nil {
		return sql.NullBool{}
	}
	if b, ok := asBool(v); ok {
		return sql.NullBool{Bool: b, Valid: true}
	}
	return sql.NullBool{}
}

func (m attrMap) leftover() map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = anyValueToJSON(v)
	}
	return out
}

// asString coerces any scalar AnyValue to its string representation.
func asString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	}
	return ""
}

// asInt64 accepts int, double, numeric string, or bool.
func asInt64(v *commonpb.AnyValue) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_IntValue:
		return x.IntValue, true
	case *commonpb.AnyValue_DoubleValue:
		return int64(x.DoubleValue), true
	case *commonpb.AnyValue_StringValue:
		n, err := strconv.ParseInt(strings.TrimSpace(x.StringValue), 10, 64)
		return n, err == nil
	case *commonpb.AnyValue_BoolValue:
		if x.BoolValue {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// asFloat64 accepts double, int, or numeric string.
func asFloat64(v *commonpb.AnyValue) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue, true
	case *commonpb.AnyValue_IntValue:
		return float64(x.IntValue), true
	case *commonpb.AnyValue_StringValue:
		f, err := strconv.ParseFloat(strings.TrimSpace(x.StringValue), 64)
		return f, err == nil
	}
	return 0, false
}

// asBool accepts BoolValue or "true"/"false" string (case-insensitive).
func asBool(v *commonpb.AnyValue) (bool, bool) {
	if v == nil {
		return false, false
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue, true
	case *commonpb.AnyValue_StringValue:
		s := strings.ToLower(strings.TrimSpace(x.StringValue))
		switch s {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func asStringArray(v *commonpb.AnyValue) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.Value.(*commonpb.AnyValue_ArrayValue)
	if !ok || arr.ArrayValue == nil {
		return nil
	}
	out := make([]string, 0, len(arr.ArrayValue.Values))
	for _, vv := range arr.ArrayValue.Values {
		out = append(out, asString(vv))
	}
	return out
}

// anyValueToJSON converts an OTLP AnyValue into a plain Go value suitable for
// json.Marshal. Used to populate the leftover Attrs map.
func anyValueToJSON(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.Values))
		for _, vv := range x.ArrayValue.Values {
			out = append(out, anyValueToJSON(vv))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		out := make(map[string]any, len(x.KvlistValue.Values))
		for _, kv := range x.KvlistValue.Values {
			out[kv.Key] = anyValueToJSON(kv.Value)
		}
		return out
	}
	return nil
}

// extractCommonAttrs pulls the standard public attributes from the map.
// The map is mutated: matched keys are removed so leftover() yields only the
// unknown rest.
func extractCommonAttrs(m attrMap, ts time.Time) (CommonAttrs, error) {
	userID := m.takeString("user.id")
	if !userID.Valid || userID.String == "" {
		return CommonAttrs{}, fmt.Errorf("missing user.id")
	}
	return CommonAttrs{
		Timestamp:       ts,
		SessionID:       m.takeString("session.id"),
		UserID:          userID.String,
		UserAccountUUID: m.takeString("user.account_uuid"),
		UserAccountID:   m.takeString("user.account_id"),
		UserEmail:       m.takeString("user.email"),
		OrganizationID:  m.takeString("organization.id"),
		AppVersion:      m.takeString("app.version"),
		TerminalType:    m.takeString("terminal.type"),
	}, nil
}

func extractEventCommonAttrs(m attrMap, ts time.Time) (EventCommonAttrs, error) {
	common, err := extractCommonAttrs(m, ts)
	if err != nil {
		return EventCommonAttrs{}, err
	}
	return EventCommonAttrs{
		CommonAttrs:        common,
		EventSequence:      m.takeInt64("event.sequence"),
		PromptID:           m.takeString("prompt.id"),
		WorkspaceHostPaths: m.takeStringArray("workspace.host_paths"),
	}, nil
}
