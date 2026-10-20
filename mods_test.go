package main

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackDownloads(t *testing.T) {
	data := []byte("fake shader zip")
	h := sha1.Sum(data)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer srv.Close()
	old := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = old }()
	dataDir = t.TempDir()
	p := &Pack{ID: "client", Downloads: []RemoteFile{
		{Name: "Complementary", URL: srv.URL + "/c.zip", Path: "shaderpacks/c.zip", SHA1: hex.EncodeToString(h[:]), Size: int64(len(data))},
		{URL: srv.URL + "/evil", Path: "../../evil.zip", SHA1: "x"},
		{URL: "http://insecure/x", Path: "shaderpacks/x.zip", SHA1: "x"},
	}}
	if err := fetchPackDownloads(p, func(string) {}, func(string, int, int) {}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pInstance(p), "shaderpacks", "c.zip"))
	if err != nil || string(b) != string(data) {
		t.Fatal("shader not downloaded")
	}
	entries, _ := os.ReadDir(filepath.Join(pInstance(p), "shaderpacks"))
	if len(entries) != 1 {
		t.Fatal("unsafe entries should be skipped")
	}
	_ = strings.TrimSpace
}
