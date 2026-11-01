package main

import (
	"path/filepath"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"foo.txt", true},
		{"archive.tar.gz", true},
		{"", false},
		{".", false},
		{"..", false},
		{".hidden", false},
		{filepath.Join("..", "..", "etc", "passwd"), false}, // ../..\etc\passwd
		{filepath.Join("a", "b"), false},                    // a\b
	}
	for _, c := range cases {
		if got := sanitizeName(c.name); got != c.want {
			t.Errorf("sanitizeName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
