package main

import (
	"reflect"
	"testing"
)

// `cairn export graph.html --dir ~/repo` exported the current directory
// instead of ~/repo, because Go's flag package stops parsing at the first
// non-flag argument. It failed silently - nine repositories produced the same
// 40,547 bytes - which is the worst way for a flag to fail.
func TestFlagsAreAcceptedAfterPositionalArguments(t *testing.T) {
	takesValue := map[string]bool{"dir": true, "addr": true, "base": true}

	for _, c := range []struct {
		name           string
		in             []string
		flags, posArgs []string
	}{
		{"flag after positional",
			[]string{"graph.html", "--dir", "/repo"},
			[]string{"--dir", "/repo"}, []string{"graph.html"}},
		{"flag before positional still works",
			[]string{"--dir", "/repo", "graph.html"},
			[]string{"--dir", "/repo"}, []string{"graph.html"}},
		{"equals form needs no second token",
			[]string{"graph.html", "--dir=/repo", "--json"},
			[]string{"--dir=/repo", "--json"}, []string{"graph.html"}},
		{"a bool flag must not swallow the argument after it",
			[]string{"--json", "lib/utils.ts"},
			[]string{"--json"}, []string{"lib/utils.ts"}},
		{"single dash",
			[]string{"lib/utils.ts", "-dir", "/repo"},
			[]string{"-dir", "/repo"}, []string{"lib/utils.ts"}},
		{"a value-taking flag at the end has no value to take",
			[]string{"graph.html", "--dir"},
			[]string{"--dir"}, []string{"graph.html"}},
		{"-- ends flag parsing",
			[]string{"--json", "--", "-weird-file-name"},
			[]string{"--json"}, []string{"-weird-file-name"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			flags, pos := splitArgs(c.in, takesValue)
			if !reflect.DeepEqual(flags, c.flags) {
				t.Errorf("flags = %q, want %q", flags, c.flags)
			}
			if !reflect.DeepEqual(pos, c.posArgs) {
				t.Errorf("positional = %q, want %q", pos, c.posArgs)
			}
		})
	}
}
