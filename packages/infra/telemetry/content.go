package telemetry

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const defaultContentBytes = 32 << 10

var contentSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:sk-(?:lf-)?|pk-lf-|xox[baprs]-|gh[pousr]_)[a-z0-9_-]+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9+/=._-]+`),
	regexp.MustCompile(`(?i)(?:[a-z0-9_]*(?:password|passwd|secret|api_key|apikey|access_token|refresh_token|authorization|cookie))\s*[=:]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}]+)`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`https?://[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
	regexp.MustCompile(`(?i)data:[^\s"']+`),
}

// ContentRecorder reads disclosure policy once when a profile is constructed.
// Disabled tracing does no reflection, serialization, or content scanning.
// This is intentionally independent of prompts and the canonical transcript.
func ContentRecorder() func(trace.Span, string, any) {
	enabled, _ := strconv.ParseBool(os.Getenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"))
	if !enabled {
		return nil
	}
	limit := defaultContentBytes
	if value, err := strconv.Atoi(os.Getenv("OTEL_INSTRUMENTATION_GENAI_CONTENT_MAX_BYTES")); err == nil && value >= 512 && value <= 256<<10 {
		limit = value
	}
	// Exact operator credentials complement structural and token redaction.
	var replacements []string
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if sensitiveField(key) && len(value) >= 8 {
			replacements = append(replacements, value, "[redacted]")
		}
	}
	replacer := strings.NewReplacer(replacements...)
	return func(span trace.Span, field string, value any) {
		if !span.IsRecording() || (field != "input" && field != "output") {
			return
		}
		budget := contentBudget{remaining: limit, limit: limit, nodes: 1024, secrets: replacer}
		clean := budget.clean(reflect.ValueOf(value), 0)
		encoded, err := json.Marshal(clean)
		if err != nil {
			return
		}
		if len(encoded) > limit {
			budget.truncated = true
			// Keep the attribute valid JSON even when escaping expands its size.
			preview := string(encoded)
			for len(encoded) > limit {
				preview = utf8Prefix(preview, len(preview)/2)
				encoded, _ = json.Marshal(map[string]any{"truncated": true, "preview": preview})
			}
		}
		span.SetAttributes(attribute.String("langfuse.observation."+field, string(encoded)), attribute.Bool("langfuse.observation.metadata."+field+"_truncated", budget.truncated))
	}
}

type contentBudget struct {
	remaining, limit, nodes int
	truncated               bool
	secrets                 *strings.Replacer
}

func sensitiveField(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	for _, suffix := range []string{"password", "passwd", "secret", "secretkey", "apikey", "publickey", "privatekey", "accesstoken", "refreshtoken", "authorization", "cookie", "token"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func (b *contentBudget) text(value string) string {
	// Bound work before regex matching even for multi-megabyte tool output.
	if len(value) > b.limit*4 {
		value = utf8Prefix(value, b.limit*4)
		b.truncated = true
	}
	value = b.secrets.Replace(value)
	for _, pattern := range contentSecrets {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	if len(value) > b.remaining {
		value = utf8Prefix(value, max(0, b.remaining))
		b.truncated = true
	}
	b.remaining -= len(value) + 8
	return value
}

func (b *contentBudget) clean(value reflect.Value, depth int) any {
	if b.remaining <= 0 || b.nodes <= 0 || depth > 16 {
		b.truncated = true
		return "[truncated]"
	}
	b.nodes--
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return b.clean(value.Elem(), depth+1)
	}
	if raw, ok := value.Interface().(json.RawMessage); ok {
		if len(raw) > b.limit*4 {
			b.truncated = true
			return "[JSON exceeds capture limit]"
		}
		var parsed any
		if json.Unmarshal(raw, &parsed) != nil {
			return "[invalid JSON]"
		}
		return b.clean(reflect.ValueOf(parsed), depth+1)
	}
	switch value.Kind() {
	case reflect.String:
		return b.text(value.String())
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Float32, reflect.Float64:
		return value.Float()
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return "[binary omitted]"
		}
		result := make([]any, 0, min(value.Len(), b.nodes))
		for i := 0; i < value.Len(); i++ {
			if b.remaining <= 0 || b.nodes <= 0 {
				b.truncated = true
				break
			}
			result = append(result, b.clean(value.Index(i), depth+1))
		}
		return result
	case reflect.Map:
		result := make(map[string]any)
		iterator := value.MapRange()
		for iterator.Next() {
			if b.remaining <= 0 || b.nodes <= 0 {
				b.truncated = true
				break
			}
			key := iterator.Key().String()
			b.remaining -= len(key) + 8
			if sensitiveField(key) {
				result[key] = "[redacted]"
			} else {
				result[key] = b.clean(iterator.Value(), depth+1)
			}
		}
		return result
	case reflect.Struct:
		result := make(map[string]any)
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if key == "-" || field.Name == "ImageURL" || field.Name == "RawMetadata" || field.Name == "Raw" {
				continue
			}
			if key == "" {
				key = field.Name
			}
			if strings.Contains(field.Tag.Get("json"), "omitempty") && value.Field(i).IsZero() {
				continue
			}
			if b.remaining <= 0 || b.nodes <= 0 {
				b.truncated = true
				break
			}
			b.remaining -= len(key) + 8
			if sensitiveField(key) {
				result[key] = "[redacted]"
			} else {
				result[key] = b.clean(value.Field(i), depth+1)
			}
		}
		return result
	}
	return "[unsupported content]"
}

func utf8Prefix(value string, size int) string {
	if len(value) <= size {
		return value
	}
	for size > 0 && !utf8.RuneStart(value[size]) {
		size--
	}
	return value[:size]
}
