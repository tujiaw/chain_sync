package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadStalePartial(t *testing.T) {
	for _, tc := range []struct {
		name             string
		content, partial []byte
	}{
		{"complete partial", []byte("new content"), []byte("new content")},
		{"same size wrong content", []byte("new content"), []byte("old content")},
		{"oversized partial", []byte("new"), []byte("old oversized content")},
		{"empty source", nil, []byte("old")},
		{"valid resume", []byte("new content"), []byte("new")},
		{"range ignored", []byte("new content"), []byte("old")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &Node{cfg: Config{Dir: t.TempDir()}}
			path := filepath.Join(source.cfg.Dir, "template_serving.tar")
			if err := os.WriteFile(path, tc.content, 0644); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "range ignored" {
					r.Header.Del("Range")
				}
				source.handleFile(w, r)
			}))
			defer srv.Close()
			client := &Node{cfg: Config{Dir: t.TempDir(), Upstream: srv.URL}, cli: newHTTPClient()}
			defer client.cli.CloseIdleConnections()
			partial := filepath.Join(client.cfg.Dir, ".incoming-template_serving.tar.part")
			if err := os.WriteFile(partial, tc.partial, 0644); err != nil {
				t.Fatal(err)
			}
			if err := client.download(FileInfo{Name: info.Name(), Size: info.Size(), Mtime: info.ModTime().UnixNano()}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(client.cfg.Dir, info.Name()))
			if err != nil || !bytes.Equal(got, tc.content) {
				t.Fatalf("content mismatch: %v", err)
			}
			if _, err := os.Stat(partial); !os.IsNotExist(err) {
				t.Fatal("partial file remains")
			}
		})
	}
}

func TestDownload416ResetsPartial(t *testing.T) {
	source := &Node{cfg: Config{Dir: t.TempDir()}}
	path := filepath.Join(source.cfg.Dir, "template_serving.tar")
	if err := os.WriteFile(path, []byte("new content"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ranges := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges <- r.Header.Get("Range")
		// Emulate the upstream changing between /list and /file.
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		source.handleFile(w, r)
	}))
	defer srv.Close()
	client := &Node{cfg: Config{Dir: t.TempDir(), Upstream: srv.URL}, cli: newHTTPClient()}
	defer client.cli.CloseIdleConnections()
	partial := filepath.Join(client.cfg.Dir, ".incoming-template_serving.tar.part")
	if err := os.WriteFile(partial, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	f := FileInfo{Name: info.Name(), Size: info.Size(), Mtime: info.ModTime().UnixNano()}
	if err := client.download(f); err == nil {
		t.Fatal("expected first attempt to report 416")
	}
	if err := client.download(f); err != nil {
		t.Fatalf("retry did not recover: %v", err)
	}
	if first, second := <-ranges, <-ranges; first != "bytes=3-" || second != "" {
		t.Fatalf("unexpected request ranges: %q, %q", first, second)
	}
	got, err := os.ReadFile(filepath.Join(client.cfg.Dir, info.Name()))
	if err != nil || string(got) != "new content" {
		t.Fatalf("content mismatch: %v", err)
	}
}
