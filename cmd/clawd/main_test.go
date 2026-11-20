package main

import (
	"reflect"
	"testing"
)

func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flags after the prompt",
			in:   []string{"explain this", "--model", "gpt-4o", "--verbose"},
			want: []string{"--model", "gpt-4o", "--verbose", "explain this"},
		},
		{
			name: "equals form",
			in:   []string{"fix it", "--model=gpt-4o"},
			want: []string{"--model=gpt-4o", "fix it"},
		},
		{
			name: "no flags",
			in:   []string{"just", "a", "prompt"},
			want: []string{"just", "a", "prompt"},
		},
		{
			name: "double dash stops parsing",
			in:   []string{"--model", "x", "--", "--not-a-flag"},
			want: []string{"--model", "x", "--not-a-flag"},
		},
		{
			name: "unknown dash args stay positional",
			in:   []string{"-weird", "prompt"},
			want: []string{"-weird", "prompt"},
		},
		{
			name: "prompt that contains a flag word",
			in:   []string{"why is --verbose slow"},
			want: []string{"why is --verbose slow"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("reorderArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
