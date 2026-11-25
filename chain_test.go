package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the actual HTTP list/download path through all four nodes.
func TestFourNodeChain(t *testing.T) {
	nodes := make([]*Node, 4)
	for i := range nodes {
		n := &Node{cfg: Config{Name: string(rune('A' + i)), Dir: t.TempDir()}, cli: newHTTPClient()}
		t.Cleanup(n.cli.CloseIdleConnections)
		mux := http.NewServeMux()
		mux.HandleFunc("/list", n.handleList)
		mux.HandleFunc("/file", n.handleFile)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		if i > 0 {
			n.cfg.Upstream = nodes[i-1].cfg.Listen
		}
		n.cfg.Listen = srv.URL
		nodes[i] = n
	}
	name := "同步 test.bin"
	for version := 1; version <= 2; version++ {
		content := bytes.Repeat([]byte{byte(version), 0, 255}, 50000*version)
		source := filepath.Join(nodes[0].cfg.Dir, name)
		if err := os.WriteFile(source, content, 0644); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1700000000+int64(version), 0)
		if err := os.Chtimes(source, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		// B must also be able to resume an interrupted transfer.
		if version == 1 {
			if err := os.WriteFile(filepath.Join(nodes[1].cfg.Dir, ".incoming-"+name+".part"), content[:1234], 0644); err != nil {
				t.Fatal(err)
			}
		}
		for i := 1; i < len(nodes); i++ {
			if err := nodes[i].pollOnce(); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(nodes[i].cfg.Dir, name)
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("node %s version %d: content mismatch (read error: %v)", nodes[i].cfg.Name, version, err)
			}
			info, err := os.Stat(dest)
			if err != nil || !info.ModTime().Equal(stamp) {
				t.Fatalf("node %s: mtime mismatch (%v)", nodes[i].cfg.Name, err)
			}
		}
	}
	// A local file introduced at a relay travels only toward its consumers.
	middleContent := []byte("file introduced on the relay")
	if err := os.WriteFile(filepath.Join(nodes[1].cfg.Dir, "middle.txt"), middleContent, 0644); err != nil {
		t.Fatal(err)
	}
	for i := 2; i < len(nodes); i++ {
		if err := nodes[i].pollOnce(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(nodes[i].cfg.Dir, "middle.txt"))
		if err != nil || !bytes.Equal(got, middleContent) {
			t.Fatalf("relay file missing at %s: %v", nodes[i].cfg.Name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(nodes[0].cfg.Dir, "middle.txt")); !os.IsNotExist(err) {
		t.Fatal("relay file propagated back to source")
	}
}
