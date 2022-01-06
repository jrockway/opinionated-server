package fuzzsupport

import (
	"bytes"
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

func TestGeneratedHTTPResponse(t *testing.T) {
	testData := []struct {
		name string
		text string
		want *http.Response
	}{
		{
			name: "empty",
			text: "",
			want: &http.Response{
				StatusCode: http.StatusOK,
				Status:     "OK",
			},
		},
		{
			name: "ok with no headers or body",
			text: "\x04",
			want: &http.Response{
				StatusCode: http.StatusOK,
				Status:     "OK",
			},
		},
		{
			name: "not found with headers and body",
			text: "\x1afoo-bar\x00baz\x01\x01foo\x01\x01bar\x00hello",
			want: &http.Response{
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
			if err := got.UnmarshalText([]byte(test.text)); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// Make the buffers inside the response readable by cmp.  (Using a
			// Transformer that exports a new field with the body bytes doesn't work
			// because it can't look inside the unexported fields of the original
			// reader, or if you null it out, it complains about the transformer being
			// nondeterministic.)
			if test.want.Body == nil {
				test.want.Body = fakeReader("")
			} else {
				b, _ := io.ReadAll(test.want.Body)
				test.want.Body.Close()
				test.want.Body = fakeReader(b)
			}
			if got.Body == nil {
				got.Body = fakeReader("")
			} else {
				b, _ := io.ReadAll(got.Body)
				got.Body.Close()
				got.Body = fakeReader(b)
			}
			diff := cmp.Diff(got, (*GeneratedHTTPResponse)(test.want),
				cmpopts.EquateEmpty(),
				cmpopts.IgnoreUnexported(bytes.Buffer{}))
			if diff != "" {
				t.Errorf("response (-got +want):\n%s", diff)
			}
		})
	}
}
