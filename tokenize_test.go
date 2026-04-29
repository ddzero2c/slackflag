package slackflag

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"empty", "", nil, false},
		{"single", "foo", []string{"foo"}, false},
		{"basic", "a b c", []string{"a", "b", "c"}, false},
		{"extra-spaces", "  -id  u123  -force  ", []string{"-id", "u123", "-force"}, false},
		{"double-quote", `-msg "hello world"`, []string{"-msg", "hello world"}, false},
		{"single-quote", `-msg 'hello world'`, []string{"-msg", "hello world"}, false},
		{"escaped-quote", `-msg "she said \"hi\""`, []string{"-msg", `she said "hi"`}, false},
		{"escaped-space", `a\ b`, []string{"a b"}, false},
		{"empty-string", `""`, []string{""}, false},
		{"unmatched-double", `"unterminated`, nil, true},
		{"unmatched-single", `'unterminated`, nil, true},
		{"trailing-backslash", `foo\`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenize(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("want err, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
