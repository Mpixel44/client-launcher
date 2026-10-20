package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

func TestPublishAndUpdateEndToEnd(t *testing.T) {
	pub, sec, _ := generateKeys()
	admin, friend := t.TempDir(), t.TempDir()

	// --- server serving admin's publication/en-ligne ---
	srv := httptest.NewServer(http.StripPrefix("/launcher/", http.FileServer(http.Dir(filepath.Join(admin, "publication", "en-ligne")))))
	defer srv.Close()

	cfgJSON := `{"name":"Client","updateUrl":"` + srv.URL + `/launcher","updateKey":"` + pub + `",
	  "packs":[{"id":"client","name":"Client","folder":""},{"id":"survie","name":"Survie","folder":"packs/survie"}]}`

	// --- admin folder ---
	write(t, filepath.Join(admin, "launcher.json"), cfgJSON)
	write(t, filepath.Join(admin, "publication", "cle-secrete.txt"), "# secret\n"+sec+"\n")
	write(t, filepath.Join(admin, "mods", "a.jar"), "AAA-v2")
	write(t, filepath.Join(admin, "mods", "new [Fabric].jar"), "NEW")
	write(t, filepath.Join(admin, "packs", "survie", "mods", "s.jar"), "SSS")
	write(t, filepath.Join(admin, "config", "mod.toml"), "default=1")
	write(t, filepath.Join(admin, "config", "added.toml"), "brand-new")

	baseDir, dataDir = admin, t.TempDir()
	t.Setenv("LAUNCHER_DIR", admin)
	t.Setenv("LAUNCHER_DATA", dataDir)
	hashCache = nil
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if !isAdmin() {
		t.Fatal("admin not detected")
	}
	res, err := publish(func(string) {}, func(string, int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != 1 || res.Files < 6 {
		t.Fatalf("publish: %+v", res)
	}

	// --- friend folder: old launcher.json without survie, outdated + obsolete mods, customised config ---
	write(t, filepath.Join(friend, "launcher.json"), `{"name":"Client","updateUrl":"`+srv.URL+`/launcher/","updateKey":"`+pub+`","packs":[{"id":"client","name":"Client"}]}`)
	write(t, filepath.Join(friend, "mods", "a.jar"), "AAA-v1")
	write(t, filepath.Join(friend, "mods", "old.jar"), "OLD")
	write(t, filepath.Join(friend, "mods", "readme.txt"), "keep me")
	fdata := t.TempDir()
	t.Setenv("LAUNCHER_DIR", friend)
	t.Setenv("LAUNCHER_DATA", fdata)
	hashCache = nil
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	// player's own game settings in the instance
	write(t, filepath.Join(pInstance(&cfg.Packs[0]), "config", "mod.toml"), "default=1\nmy_setting=42")

	m, err := fetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	r, err := applyUpdate(m, true, func(string) {}, func(string, int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result: %+v", r)
	if !r.ConfigChanged || r.Removed != 1 {
		t.Fatalf("unexpected result %+v", r)
	}
	if read(t, filepath.Join(friend, "mods", "a.jar")) != "AAA-v2" ||
		read(t, filepath.Join(friend, "mods", "new [Fabric].jar")) != "NEW" ||
		read(t, filepath.Join(friend, "packs", "survie", "mods", "s.jar")) != "SSS" {
		t.Fatal("mods not updated")
	}
	if read(t, filepath.Join(friend, "mods", "old.jar")) != "<absent>" {
		t.Fatal("obsolete mod not removed")
	}
	if read(t, filepath.Join(friend, "mods", "readme.txt")) != "keep me" {
		t.Fatal("non-jar file should be kept")
	}
	if !strings.Contains(read(t, filepath.Join(friend, "launcher.json")), "survie") {
		t.Fatal("launcher.json not updated")
	}

	// reload config + sync into the game folder: new config added, customised one kept
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Packs) != 2 {
		t.Fatal("new pack not visible after reload")
	}
	if err := syncMods(&cfg.Packs[0], nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	inst := pInstance(&cfg.Packs[0])
	if got := read(t, filepath.Join(inst, "config", "mod.toml")); got != "default=1\nmy_setting=42" {
		t.Fatalf("player setting overwritten: %q", got)
	}
	if read(t, filepath.Join(inst, "config", "added.toml")) != "brand-new" {
		t.Fatal("new config file not added")
	}
	if read(t, filepath.Join(inst, "mods", "new [Fabric].jar")) != "NEW" {
		t.Fatal("mod not synced to instance")
	}

	// second run: nothing to do
	r2, err := applyUpdate(m, true, func(string) {}, func(string, int, int) {})
	if err != nil || r2.Downloaded != 0 || r2.Removed != 0 || r2.ConfigChanged {
		t.Fatalf("second run should be a no-op: %+v %v", r2, err)
	}

	// tampered manifest is refused
	mp := filepath.Join(admin, "publication", "en-ligne", "manifest.json")
	b, _ := os.ReadFile(mp)
	os.WriteFile(mp, []byte(strings.Replace(string(b), `"version": 1`, `"version": 9`, 1)), 0o644)
	if _, err := fetchManifest(); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered manifest accepted: %v", err)
	}
}

func TestSafeLocal(t *testing.T) {
	baseDir = t.TempDir()
	for _, bad := range []string{"../evil.jar", "mods/../../x", "C:/Windows/x", "/"} {
		if _, err := safeLocal(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if p, err := safeLocal("mods/a b [Fabric].jar"); err != nil || !strings.HasPrefix(p, baseDir) {
		t.Errorf("good path refused: %v", err)
	}
}

func TestSwapExecutable(t *testing.T) {
	pub, sec, _ := generateKeys()
	_ = sec
	dir := t.TempDir()
	srvDir := t.TempDir()
	newExe := []byte("NEW LAUNCHER v2")
	sum := sha256hex(newExe)
	write(t, filepath.Join(srvDir, sum), string(newExe))
	srv := httptest.NewServer(http.FileServer(http.Dir(srvDir)))
	defer srv.Close()
	cfg = Config{UpdateURL: srv.URL, UpdateKey: pub}
	exe := filepath.Join(dir, "Launcher.exe")
	write(t, exe, "OLD LAUNCHER v1")
	if err := swapExecutable(exe, &UpdFile{Path: "Launcher.exe", SHA256: sum, Size: int64(len(newExe))}); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "NEW LAUNCHER v2" || read(t, exe+".old") != "OLD LAUNCHER v1" || read(t, exe+".new") != "<absent>" {
		t.Fatal("swap failed")
	}
	// corrupted download must leave the current launcher untouched
	write(t, filepath.Join(srvDir, "deadbeef"), "garbage")
	if err := swapExecutable(exe, &UpdFile{SHA256: "deadbeef", Size: 7}); err == nil {
		t.Fatal("corrupted launcher accepted")
	}
	if read(t, exe) != "NEW LAUNCHER v2" {
		t.Fatal("launcher damaged by a failed update")
	}
}

func TestStandaloneExeBootstrap(t *testing.T) {
	data := t.TempDir()
	t.Setenv("LAUNCHER_DIR", "")
	os.Unsetenv("LAUNCHER_DIR")
	t.Setenv("LAUNCHER_DATA", data)
	hashCache = nil
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if !standalone || baseDir != filepath.Join(data, "fichiers") {
		t.Fatalf("standalone=%v baseDir=%s", standalone, baseDir)
	}
	if !updatesEnabled() || len(cfg.Packs) == 0 || !strings.Contains(cfg.UpdateURL, "mises-a-jour") {
		t.Fatalf("embedded config not used: %+v", cfg)
	}
	if read(t, filepath.Join(baseDir, "launcher.json")) == "<absent>" {
		t.Fatal("launcher.json not written")
	}
	if isAdmin() {
		t.Fatal("a player must not be admin")
	}

	// simulate the first update from a (test) server: mods land in <data>/fichiers/mods
	pub, sec, _ := generateKeys()
	admin := t.TempDir()
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(admin, "publication", "en-ligne"))))
	defer srv.Close()
	write(t, filepath.Join(admin, "launcher.json"), `{"name":"Client","updateUrl":"`+srv.URL+`","updateKey":"`+pub+`","packs":[{"id":"client","name":"Client"}]}`)
	write(t, filepath.Join(admin, "publication", "cle-secrete.txt"), sec)
	write(t, filepath.Join(admin, "mods", "x.jar"), "XXX")
	playerBase, playerCfg := baseDir, cfg
	baseDir = admin
	cfg = Config{Name: "Client", UpdateURL: srv.URL, UpdateKey: pub, Packs: []Pack{{ID: "client", Name: "Client", Folder: ""}}}
	_ = cfg.normalize()
	if _, err := publish(func(string) {}, func(string, int, int) {}); err != nil {
		t.Fatal(err)
	}
	baseDir, cfg = playerBase, playerCfg
	cfg.UpdateURL, cfg.UpdateKey = srv.URL, pub
	m, err := fetchManifest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyUpdate(m, false, func(string) {}, func(string, int, int) {}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(data, "fichiers", "mods", "x.jar")) != "XXX" {
		t.Fatal("mod not downloaded into the data folder")
	}
}
