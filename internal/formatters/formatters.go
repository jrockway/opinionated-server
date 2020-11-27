package formatters

import (
	"errors"
	"fmt"
	"sort"

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
			enc.AppendString(fmt.Sprintf("%s=%s", k, v))
		}
	}
	return nil
}
