package formatters

import "testing"

func TestSanitizeAuthorization(t *testing.T) {
	testData := []struct {
		name, input, want string
	}{
		{
			name:  "empty",
			input: "",
			want:  "(0 bytes)",
		},
		{
			name:  "bare header",
			input: "0a0b0c0d0e0f1a1b1c1d1e1f",
			want:  "...(24 bytes)",
		},
		{
			name:  "custom scheme",
			input: "Foo-Bar credentials-are-here",
			want:  "...(28 bytes)",
		},
		{
			name:  "unknown scheme with trailing space",
			input: "credentials-are-here ",
			want:  "...(21 bytes)",
		},
		{
			name:  "basic",
			input: "Basic Zm9vOmJhcg==",
			want:  "Basic ...(12 bytes)",
		},
		{
			name:  "basic with trailing space",
			input: "Basic ",
			want:  "Basic ...(0 bytes)",
		},
		{
			name:  "bearer",
			input: "Bearer abcdef1234.abcedf1234.abcedf1234",
			want:  "Bearer ...(32 bytes)",
		},
	}

	for _, test := range testData {
		t.Run(test.name, func(t *testing.T) {
			if got, want := sanitizeAuthorization(test.input), test.want; got != want {
				t.Errorf("sanitize:\n  got: %v\n want: %v", got, want)
			}
		})
	}
}
