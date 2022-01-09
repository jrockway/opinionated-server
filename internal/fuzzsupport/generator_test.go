package fuzzsupport

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

type fakeReader string

func (fakeReader) Read([]byte) (int, error) {
	return 0, nil
}

func (fakeReader) Close() error { return nil }

// canonizalizeBody makes the buffers inside the response readable by cmp.
// bytes.Buffer/bytes.Reader/strings.Reader have unexported fields that we don't control.  Using a
// Transformer that exports a new field with the body bytes doesn't work because it can't look
// inside the unexported fields of the original reader, or if you null it out, it complains about
// the transformer being nondeterministic.
//
// We could do better than this (see what http.Response#Write does to read the first byte), but we
// don't need to, so we don't.
func canonicalizeBody(r *io.ReadCloser) {
	if *r == nil {
		*r = fakeReader("")
		return
	}
	b, err := io.ReadAll(*r)
	if err != nil {
		panic(err) // can't happen
	}
	(*r).Close()
	*r = fakeReader(b)
}

func TestGeneratedHTTPResponse(t *testing.T) {
	testData := []struct {
		name  string
		input string
		want  *http.Response
	}{
		{
			name:  "empty",
			input: "",
			want: &http.Response{
				StatusCode: http.StatusOK,
				Status:     "OK",
			},
		},
		{
			name:  "ok with no headers or body",
			input: "\x04\x02",
			want: &http.Response{
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				StatusCode: http.StatusOK,
				Status:     "OK",
			},
		},
		{
			name:  "not found with headers and body",
			input: "\x1a\x02foo-bar\x00baz\x01\x01foo\x01\x01bar\x00hello",
			want: &http.Response{
				Proto:         "HTTP/1.1",
				ProtoMajor:    1,
				ProtoMinor:    1,
				StatusCode:    http.StatusNotFound,
				Status:        "Not Found",
				ContentLength: 5,
				Body:          io.NopCloser(strings.NewReader("hello")),
				Header: http.Header{
					"Authorization": []string{"foo", "bar"},
					"Foo-Bar":       []string{"baz"},
				},
			},
		},
	}

	for _, test := range testData {
		t.Run(test.name, func(t *testing.T) {
			got := new(GeneratedHTTPResponse)
			if err := got.UnmarshalText([]byte(test.input)); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			canonicalizeBody(&test.want.Body)
			canonicalizeBody(&got.Body)
			if diff := cmp.Diff(got, (*GeneratedHTTPResponse)(test.want), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("response (-got +want):\n%s", diff)
			}
		})
	}
}

func TestGeneratedHTTPRequest(t *testing.T) {
	testData := []struct {
		name  string
		input string
		want  *http.Request
	}{
		{
			name:  "empty",
			input: "",
			want:  &http.Request{},
		},
		{
			name:  "basic post request",
			input: "\x02\x03key\x00value\x00foo",
			want: &http.Request{
				Method:     "POST",
				Proto:      "HTTP/2.0",
				Header:     http.Header{"Key": []string{"value"}},
				ProtoMajor: 2,
				ProtoMinor: 0,
				Body:       io.NopCloser(strings.NewReader("foo")),
			},
		},
	}

	for _, test := range testData {
		t.Run(test.name, func(t *testing.T) {
			got := new(GeneratedHTTPRequest)
			if err := got.UnmarshalText([]byte(test.input)); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			canonicalizeBody(&got.Body)
			canonicalizeBody(&test.want.Body)
			diff := cmp.Diff((*http.Request)(got), test.want,
				cmpopts.IgnoreUnexported(http.Request{}),
				cmpopts.EquateEmpty(),
			)
			if diff != "" {
				t.Errorf("request:\n%s", diff)
			}
		})
	}
}
