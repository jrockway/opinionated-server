package formatters

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap/zapcore"
)

// MetadataWrapper marshals grpc metadata and http headers.
type MetadataWrapper struct {
	MD map[string][]string
}

// MarshalLogArray implements zapcore.ArrayMarshaler.
func (m *MetadataWrapper) MarshalLogArray(enc zapcore.ArrayEncoder) error {
	if m == nil {
		return errors.New("nil MetadataWrapper")
	}
	if m.MD == nil {
		return errors.New("nil metadata.MD in MetadataWrapper")
	}

	var keys []string
	for k := range m.MD {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range m.MD[k] {
			// Remove key material from "authorization" headers.
			if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "proxy-authorization") {
				v = sanitizeAuthorization(v)
			}

			enc.AppendString(fmt.Sprintf("%s=%s", k, v))
		}
	}
	return nil
}

var knownAuthSchemes = map[string]struct{}{
	// From https://developer.mozilla.org/en-US/docs/Web/HTTP/Authentication#authentication_schemes
	"Basic":            {},
	"Bearer":           {},
	"Digest":           {},
	"HOBA":             {},
	"Mutual":           {},
	"Negotiate":        {},
	"vapid":            {}, // The RFC shows it in lowercase, so that's what we accept.
	"SCRAM-SHA-256":    {}, // The RFC reserves 'other-scram-name', but that complicates parsing.
	"SCRAM-SHA-1":      {},
	"AWS4-HMAC-SHA256": {},
}

func sanitizeAuthorization(v string) string {
	if v == "" {
		return "(0 bytes)"
	}
	space := strings.IndexByte(v, ' ')
	if space < 1 {
		return fmt.Sprintf("...(%d bytes)", len(v))
	}
	part, rest := v[:space], v[space:]
	if _, ok := knownAuthSchemes[part]; ok {
		return fmt.Sprintf("%v ...(%d bytes)", part, len(rest)-1)
	}
	return fmt.Sprintf("...(%d bytes)", len(v))
}
