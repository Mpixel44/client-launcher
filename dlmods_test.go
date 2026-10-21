package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadedModsKept(t *testing.T) {
	baseDir = t.TempDir()
	gameRoot = t.TempDir()
	p := &Pack{ID: "x", Folder: "pk", Downloads: []RemoteFile{
		{Name: "Voice", URL: "https://cdn.modrinth.com/v.jar", Path: "mods/v.jar", SHA1: "abc"},
		{URL: "https://cdn.modrinth.com/s.zip", Path: "shaderpacks/s.zip", SHA1: "def"},
	}}
	os.MkdirAll(filepath.Join(pPackSource(p), "mods"), 0o755)
	os.WriteFile(filepath.Join(pPackSource(p), "mods", "a.jar"), []byte("a"), 0o644)
	dst := filepath.Join(pInstance(p), "mods")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "v.jar"), []byte("v"), 0o644)
	os.WriteFile(filepath.Join(dst, "old.jar"), []byte("o"), 0o644)

	if err := syncMods(p, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dst, "v.jar")) || !fileExists(filepath.Join(dst, "a.jar")) || fileExists(filepath.Join(dst, "old.jar")) {
		t.Fatal("wrong mods kept")
	}
	if n := countMods(p); n != 2 {
		t.Fatalf("count %d", n)
	}
	if len(readModInfos(p)) != 2 {
		t.Fatal("downloaded mod not listed")
	}
	if err := syncMods(p, map[string]bool{"v.jar": true}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(dst, "v.jar")) {
		t.Fatal("skipped download mod kept")
	}
}

func TestOptionalModsOverride(t *testing.T) {
	baseDir = t.TempDir()
	gameRoot = t.TempDir()
	p := &Pack{ID: "x", Folder: "pk", OptionalMods: []string{"jade"}}
	dir := filepath.Join(pPackSource(p), "mods")
	os.MkdirAll(dir, 0o755)
	writeJar(t, filepath.Join(dir, "jade.jar"), `{"schemaVersion":1,"id":"jade","version":"1","environment":"*"}`)
	writeJar(t, filepath.Join(dir, "other.jar"), `{"schemaVersion":1,"id":"other","version":"1","environment":"*"}`)
	got := map[string]bool{}
	for _, m := range readModInfos(p) {
		got[m.ID] = m.ClientOnly
	}
	if !got["jade"] || got["other"] {
		t.Fatalf("%v", got)
	}
}

func writeJar(t *testing.T, path, modJSON string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	w, _ := z.Create("fabric.mod.json")
	w.Write([]byte(modJSON))
	z.Close()
}
