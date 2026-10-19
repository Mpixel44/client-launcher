package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, p string) *VersionJSON {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skip("testdata absent : ", err)
	}
	var v VersionJSON
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func TestMavenPath(t *testing.T) {
	cases := map[string]string{
		"net.fabricmc:fabric-loader:0.17.2":               "net/fabricmc/fabric-loader/0.17.2/fabric-loader-0.17.2.jar",
		"org.lwjgl:lwjgl:3.3.3:natives-windows":           "org/lwjgl/lwjgl/3.3.3/lwjgl-3.3.3-natives-windows.jar",
		"de.oceanlabs.mcp:mcp_config:1.16.5-20210115@zip": "de/oceanlabs/mcp/mcp_config/1.16.5-20210115/mcp_config-1.16.5-20210115.zip",
	}
	for in, want := range cases {
		if got := mavenPath(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
}

func TestWindowsCommand(t *testing.T) {
	goos, goarch = "windows", "amd64"
	dataDir = t.TempDir()
	cfg = defaultConfig()
	_ = cfg.normalize()
	pack := &cfg.Packs[0]
	pack.Server = "play.example.net"
	vanilla := load(t, "testdata/1.21.1.json")
	fabric := load(t, "testdata/fabric.json")
	v := merge(vanilla, fabric)

	if v.MainClass != "net.fabricmc.loader.impl.launch.knot.KnotClient" {
		t.Fatal("main class", v.MainClass)
	}
	var cp []string
	for _, l := range v.Libraries {
		if rulesAllow(l.Rules) {
			cp = append(cp, l.Name)
		}
	}
	joined := strings.Join(cp, "\n")
	for _, must := range []string{"org.lwjgl:lwjgl:3.3.3:natives-windows", "net.fabricmc:fabric-loader:0.17.2", "org.ow2.asm:asm:9.8", "com.mojang:authlib"} {
		if !strings.Contains(joined, must) {
			t.Errorf("missing %s", must)
		}
	}
	for _, mustNot := range []string{"natives-linux", "natives-macos", "java-objc-bridge"} {
		if strings.Contains(joined, mustNot) {
			t.Errorf("should not include %s", mustNot)
		}
	}
	t.Logf("%d libraries on Windows classpath", len(cp))

	inst := &Installed{Version: v, Classpath: []string{`C:\a.jar`, `C:\b.jar`}, Natives: `C:\natives`, Java: "javaw.exe", LogConfig: `C:\log.xml`}
	acc := &Account{Name: "Steve", UUID: "abc", MCToken: "TOKEN", XUID: ""}
	cmd, err := buildCommand(pack, inst, acc, 6144, pack.Server)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	t.Log(args)
	for _, must := range []string{"-Xmx6144M", "-DFabricMcEmu= net.minecraft.client.main.Main ", "--username Steve", "--accessToken TOKEN",
		"--assetIndex 17", "-Djava.library.path=C:\\natives", "--quickPlayMultiplayer play.example.net", "-XX:HeapDumpPath=", "-Dlog4j.configurationFile=C:\\log.xml",
		"KnotClient --username"} {
		if !strings.Contains(args, must) {
			t.Errorf("args missing %q", must)
		}
	}
	for _, mustNot := range []string{"${", "--demo", "--width", "-XstartOnFirstThread", "--xuid", "--clientId", "-Xss1M"} {
		if strings.Contains(args, mustNot) {
			t.Errorf("args should not contain %q", mustNot)
		}
	}
	_ = filepath.Join

	solo, _ := buildCommand(pack, inst, acc, 6144, "")
	if strings.Contains(strings.Join(solo.Args, " "), "quickPlay") {
		t.Error("solo mode must not join the server")
	}
}

func TestModInfos(t *testing.T) {
	dir := os.Getenv("MODS_DIR")
	if dir == "" {
		t.Skip("MODS_DIR not set")
	}
	exeDir = dir
	cfg = defaultConfig()
	_ = cfg.normalize()
	m := readModInfos(&cfg.Packs[0])
	icons := 0
	for _, x := range m {
		if x.Icon != "" {
			icons++
		}
	}
	t.Logf("%d mods, %d with icons; first: %+v", len(m), icons, m[0].Name)
	if len(m) == 0 {
		t.Fatal("no mods")
	}
}

func TestLegacyConfigAndMigration(t *testing.T) {
	exeDir = t.TempDir()
	dataDir = t.TempDir()
	os.WriteFile(filepath.Join(exeDir, "launcher.json"), []byte(`{"name":"Client","minecraft":"1.21.1","server":"a.b:25565","defaultRamMB":8192}`), 0o644)
	os.MkdirAll(filepath.Join(dataDir, "game", "saves", "MonMonde"), 0o755)
	t.Setenv("LAUNCHER_DIR", exeDir)
	t.Setenv("LAUNCHER_DATA", dataDir)
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Packs) != 1 || cfg.Packs[0].ID != "client" || cfg.Packs[0].Server != "a.b:25565" || cfg.Packs[0].Loader != "fabric" {
		t.Fatalf("legacy pack: %+v", cfg.Packs)
	}
	if len(cfg.Games) != 2 || cfg.Games[0].Type != "minecraft" || !cfg.Games[1].ComingSoon {
		t.Fatalf("games: %+v", cfg.Games)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "instances", "client", "saves", "MonMonde")); err != nil {
		t.Fatal("world not migrated")
	}
	if pPackModsSrc(&cfg.Packs[0]) != filepath.Join(exeDir, "mods") {
		t.Fatal("mods path", pPackModsSrc(&cfg.Packs[0]))
	}
}

func TestMultiPackConfig(t *testing.T) {
	c := Config{Packs: []Pack{{Name: "Survie Modée", Folder: "packs/survie"}, {Name: "Vanilla", Loader: "vanilla", Minecraft: "1.21.4"}}}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Packs[0].ID != "survie-mod-e" && c.Packs[0].ID != "survie-modee" {
		t.Log("id:", c.Packs[0].ID)
	}
	if c.Packs[1].Loader != "vanilla" || c.Packs[0].Minecraft != "1.21.1" {
		t.Fatalf("%+v", c.Packs)
	}
	bad := Config{Packs: []Pack{{Name: "A"}, {Name: "a"}}}
	if bad.normalize() == nil {
		t.Fatal("duplicate ids not detected")
	}
}

func TestCleanServer(t *testing.T) {
	for in, want := range map[string]string{
		"http://minecraft.nexusdescartes.com/": "minecraft.nexusdescartes.com",
		" https://a.b:25566/x ":                "a.b:25566",
		"play.example.fr":                      "play.example.fr",
		"":                                     "",
	} {
		if got := cleanServer(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
