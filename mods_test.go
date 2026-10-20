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

func TestApplyResourcePacks(t *testing.T) {
	dataDir = t.TempDir()
	p := &Pack{ID: "client", ResourcePacks: []string{"betterbeds:fancyconnectedbeds", "file/Nature X - 11.1.zip", "file/Better+Lanterns+v1.2(mc-1.21).zip", "fabric", "vanilla"}}
	opt := filepath.Join(pInstance(p), "options.txt")
	write(t, opt, "version:3955\nresourcePacks:[\"fabric\"]\nlang:fr_fr\n")
	if err := applyResourcePacks(p); err != nil {
		t.Fatal(err)
	}
	got := read(t, opt)
	want := `resourcePacks:["vanilla","fabric","file/Better+Lanterns+v1.2(mc-1.21).zip","file/Nature X - 11.1.zip","betterbeds:fancyconnectedbeds"]`
	if !strings.Contains(got, want) || !strings.Contains(got, "lang:fr_fr") {
		t.Fatalf("options.txt:\n%s", got)
	}
	// player reorders → launcher keeps it on next launch
	write(t, opt, "version:3955\nresourcePacks:[\"vanilla\"]\nlang:fr_fr\n")
	if err := applyResourcePacks(p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, opt), `resourcePacks:["vanilla"]`) {
		t.Fatal("player choice overwritten")
	}
	// new published list → applied again
	p.ResourcePacks = append([]string{"file/x.zip"}, p.ResourcePacks...)
	if err := applyResourcePacks(p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, opt), `"betterbeds:fancyconnectedbeds","file/x.zip"]`) {
		t.Fatal("new list not applied")
	}
	// fresh game folder: options.txt created
	p2 := &Pack{ID: "neuf", ResourcePacks: []string{"fabric", "vanilla"}}
	if err := applyResourcePacks(p2); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(pInstance(p2), "options.txt")) != "resourcePacks:[\"vanilla\",\"fabric\"]\n" {
		t.Fatal("new options.txt wrong")
	}
}

func TestConfigDefaults(t *testing.T) {
	dataDir = t.TempDir()
	p := &Pack{ID: "client", ConfigDefaults: []ConfigDefault{{File: "config/replaymod.json",
		Set: map[string]any{"recording": map[string]any{"autoStartRecording": false}}}}}
	cfgFile := filepath.Join(pInstance(p), "config", "replaymod.json")
	// existing player file with other settings
	write(t, cfgFile, `{"recording":{"autoStartRecording":true,"indicator":false},"core":{"notifications":false}}`)
	if err := applyConfigDefaults(p); err != nil {
		t.Fatal(err)
	}
	got := read(t, cfgFile)
	for _, want := range []string{`"autoStartRecording": false`, `"indicator": false`, `"notifications": false`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	// player turns it back on → kept
	write(t, cfgFile, `{"recording":{"autoStartRecording":true}}`)
	if err := applyConfigDefaults(p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, cfgFile), "true") {
		t.Fatal("player choice overwritten")
	}
	// fresh player: file created
	p2 := &Pack{ID: "neuf", ConfigDefaults: p.ConfigDefaults}
	if err := applyConfigDefaults(p2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(pInstance(p2), "config", "replaymod.json")), `"autoStartRecording": false`) {
		t.Fatal("not created")
	}
}
