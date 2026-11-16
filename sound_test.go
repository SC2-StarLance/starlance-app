package main

import (
	"os"
	"testing"
)

func TestSoundCuesResolve(t *testing.T) {
	for _, name := range []string{"match-found"} {
		path := soundFile(name)
		if path == "" {
			t.Fatalf("%s: no path (embed or cache dir failed)", name)
		}
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: stat: %v", name, err)
		}
		if st.Size() == 0 {
			t.Fatalf("%s: wrote an empty file", name)
		}
	}
}
