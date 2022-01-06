package fuzzsupport

import (
	"bytes"
	"io"
	"net/http"
)

// GeneratedHTTPResponse is a well-formed http.Response that can be unmarshalled from random bytes.
type GeneratedHTTPResponse http.Response

// statusMap is list of all known HTTP status codes.
var statusMap []int

// interestingHeaders is a list of interesting HTTP headers
var interestingHeaders = []string{
	"",
	"Authorization",
	"Proxy-Authorization",
	"Content-Type",
}

func init() {
	for i := 100; i < 600; i++ {
		if str := http.StatusText(i); str != "" {
			statusMap = append(statusMap, i)
		}
	}
	statusMap = append(statusMap, 242, 442, 542) // some invalid ones!
}

type generatorState int

const (
	statusState generatorState = iota
	headerKeyState
	headerValueState
	bodyState
)

func (res *GeneratedHTTPResponse) UnmarshalText(in []byte) error {
	res.Header = make(http.Header)
	res.StatusCode = http.StatusOK

	buf := new(bytes.Buffer)
	res.Body = io.NopCloser(buf)

	state := statusState
	var k, v []byte
	for _, b := range in {
		switch state {
		case statusState:
			res.StatusCode = statusMap[int(b)%len(statusMap)]
			state = headerKeyState
		case headerKeyState:
			switch {
			case b == 0:
				state = headerValueState
			case len(k) == 0 && int(b) < len(interestingHeaders):
				k = []byte(interestingHeaders[int(b)])
				state = headerValueState
			default:
				k = append(k, b)
			}
		case headerValueState:
			switch {
			case b == 0:
				state = bodyState
				res.Header.Add(string(k), string(v))
				k, v = nil, nil
			case b == 1:
				state = headerKeyState
				res.Header.Add(string(k), string(v))
				k, v = nil, nil
			default:
				v = append(v, b)
			}
		case bodyState:
			buf.WriteByte(b)
		}
	}
	res.Status = http.StatusText(res.StatusCode)
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		res.Header.Set("Location", "http://example.invalid/redirect")
	}
	res.ContentLength = int64(buf.Len())
	return nil
}
