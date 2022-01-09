package fuzzsupport

import (
	"bytes"
	"io"
	"net/http"
)

// GeneratedHTTPResponse is a well-formed http.Response that can be unmarshalled from random bytes.
type GeneratedHTTPResponse http.Response

// GeneratedHTTPRequest is a well-formed http.Request that can be unmarshalled from random bytes.
type GeneratedHTTPRequest http.Request

// statusMap is list of all known HTTP status codes.
var statusMap []int

// interestingHeaders is a list of interesting HTTP headers
var interestingHeaders = []string{
	"", // Unused.
	"Authorization",
	"Proxy-Authorization",
	"Content-Type",
	"Cookie",
	"Set-Cookie",
	"Host",
	"User-Agent",
	"Date",
	"Server",
	"Referer",
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
	protoVersionState
	headerKeyState
	headerValueState
	bodyState
	methodState = statusState
)

func proto(b byte) (string, int, int) {
	switch b % 4 {
	case 0:
		return "HTTP/0.9", 0, 9
	case 1:
		return "HTTP/1.0", 1, 0
	case 2:
		return "HTTP/1.1", 1, 1
	case 3:
		return "HTTP/2.0", 2, 0
	}
	panic("unreachable")
}

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
			state = protoVersionState
		case protoVersionState:
			res.Proto, res.ProtoMajor, res.ProtoMinor = proto(b)
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

var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PRI", "INVALID"}

func (req *GeneratedHTTPRequest) UnmarshalText(in []byte) error {
	req.Header = make(http.Header)
	req.Host = "example.invalid"
	buf := new(bytes.Buffer)
	req.Body = io.NopCloser(buf)
	state := methodState
	var k, v []byte
	for _, b := range in {
		switch state {
		case methodState:
			req.Method = httpMethods[int(b)%len(httpMethods)]
			state = protoVersionState
		case protoVersionState:
			req.Proto, req.ProtoMajor, req.ProtoMinor = proto(b)
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
				req.Header.Add(string(k), string(v))
				k, v = nil, nil
			case b == 1:
				state = headerKeyState
				req.Header.Add(string(k), string(v))
				k, v = nil, nil
			default:
				v = append(v, b)
			}
		case bodyState:
			buf.WriteByte(b)
		}
	}
	if req.Method == "GET" || req.Method == "HEAD" {
		req.Body = http.NoBody
	}
	req.Header.Del("")
	return nil
}
