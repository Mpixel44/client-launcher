package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Game is an entry of the left game bar.
//
//	type "minecraft": opens the modpack list (packs below)
//	type "steam":     launches steam://rungameid/<steamAppId>
//	type "exe":       starts the program at "path"
type Game struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Short      string `json:"short,omitempty"` // label under the icon, e.g. "CS2"
	Type       string `json:"type"`
	SteamAppID int    `json:"steamAppId,omitempty"`
	Path       string `json:"path,omitempty"`
	ComingSoon bool   `json:"comingSoon,omitempty"` // greyed out, "Bientôt"
}

// Pack is a Minecraft modpack. Its files live in <launcher folder>/<folder>/
// (mods/, and optionally config/, resourcepacks/, shaderpacks/, options.txt).
type Pack struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Minecraft    string `json:"minecraft"`
	Loader       string `json:"loader"`                 // "fabric" or "vanilla"
	FabricLoader string `json:"fabricLoader,omitempty"` // "" = latest stable
	Folder       string `json:"folder"`                 // relative to the launcher, "" = launcher folder itself
	Server       string `json:"server,omitempty"`       // optional "ip:port" to join directly
	Icon         string `json:"icon,omitempty"`         // optional .png inside the pack folder
	// Files each player downloads straight from their official host (e.g. Modrinth),
	// for content whose licence forbids re-hosting it (shaders…).
	Downloads []RemoteFile `json:"downloads,omitempty"`
	// Resource packs to enable, top = highest priority (same order as the in-game list).
	// Applied once per change, so players can still customise afterwards.
	ResourcePacks []string `json:"resourcePacks,omitempty"`
	// Default values written into mods' JSON config files, applied once per change
	// (players can change them afterwards), e.g. ReplayMod auto-recording off.
	ConfigDefaults []ConfigDefault `json:"configDefaults,omitempty"`
}

type ConfigDefault struct {
	File string         `json:"file"` // inside the game folder, e.g. "config/replaymod.json"
	Set  map[string]any `json:"set"`  // merged into the file (nested objects are merged too)
}

type RemoteFile struct {
	Name string `json:"name,omitempty"` // shown in the launcher
	URL  string `json:"url"`
	Path string `json:"path"` // inside the game folder, e.g. "shaderpacks/x.zip"
	SHA1 string `json:"sha1"`
	Size int64  `json:"size,omitempty"`
}

// Config is read from launcher.json, next to the executable.
type Config struct {
	Name         string `json:"name"`
	DefaultRamMB int    `json:"defaultRamMB"`
	DataName     string `json:"dataName,omitempty"` // name of the data folder (kept when renaming the launcher)
	ClientID     string `json:"microsoftClientId"`
	UpdateURL    string `json:"updateUrl,omitempty"` // folder on your server holding manifest.json
	UpdateKey    string `json:"updateKey,omitempty"` // public key that signs manifest.json
	Games        []Game `json:"games"`
	Packs        []Pack `json:"packs"`

	// old single-pack format (still read)
	MinecraftVersion string `json:"minecraft,omitempty"`
	FabricLoader     string `json:"fabricLoader,omitempty"`
	Server           string `json:"server,omitempty"`
}

// Settings are per-player and live in the data folder.
type Settings struct {
	RamMB    int             `json:"ramMB"`
	LastPack string          `json:"lastPack"`
	Solo     map[string]bool `json:"solo"` // packs set to "Solo" instead of joining their server
	// client-only mods the player turned off, per pack (mod ids)
	Disabled map[string][]string `json:"disabled,omitempty"`
}

var (
	baseDir string
	dataDir string
	cfg     Config
)

func defaultGames() []Game {
	return []Game{
		{ID: "minecraft", Name: "Minecraft", Type: "minecraft"},
		{ID: "cs2", Name: "Counter-Strike 2", Short: "CS2", Type: "steam", SteamAppID: 730, ComingSoon: true},
	}
}

func defaultConfig() Config {
	return Config{
		Name:         "Client",
		DefaultRamMB: 8192,
		Games:        defaultGames(),
		Packs: []Pack{{
			ID: "client", Name: "Client", Minecraft: "1.21.1", Loader: "fabric",
		}},
	}
}

// normalize fills defaults and converts the old single-pack format.
func (c *Config) normalize() error {
	if c.Name == "" {
		c.Name = "Client"
	}
	if c.DefaultRamMB <= 0 {
		c.DefaultRamMB = 8192
	}
	if len(c.Packs) == 0 {
		mc := c.MinecraftVersion
		if mc == "" {
			mc = "1.21.1"
		}
		c.Packs = []Pack{{ID: slug(c.Name), Name: c.Name, Minecraft: mc, Loader: "fabric",
			FabricLoader: c.FabricLoader, Server: c.Server}}
	}
	seen := map[string]bool{}
	for i := range c.Packs {
		p := &c.Packs[i]
		if p.Name == "" {
			p.Name = fmt.Sprintf("Pack %d", i+1)
		}
		if p.ID == "" {
			p.ID = slug(p.Name)
		}
		p.ID = slug(p.ID)
		if seen[p.ID] {
			return fmt.Errorf("deux packs ont le même id « %s »", p.ID)
		}
		seen[p.ID] = true
		if p.Minecraft == "" {
			p.Minecraft = "1.21.1"
		}
		p.Server = cleanServer(p.Server)
		p.Loader = strings.ToLower(p.Loader)
		if p.Loader == "" {
			p.Loader = "fabric"
		}
		if p.Loader != "fabric" && p.Loader != "vanilla" {
			return fmt.Errorf("pack « %s » : loader « %s » non géré (fabric ou vanilla)", p.Name, p.Loader)
		}
	}
	if len(c.Games) == 0 {
		c.Games = defaultGames()
	}
	hasMC := false
	for i := range c.Games {
		g := &c.Games[i]
		if g.ID == "" {
			g.ID = slug(g.Name)
		}
		if g.Type == "" {
			g.Type = "exe"
		}
		if g.Short == "" {
			g.Short = g.Name
		}
		if g.Type == "minecraft" {
			hasMC = true
		}
	}
	if !hasMC {
		c.Games = append([]Game{{ID: "minecraft", Name: "Minecraft", Type: "minecraft"}}, c.Games...)
	}
	return nil
}

// defaultLauncherJSON is the configuration built into the exe, so a player can
// receive the .exe alone: everything else is downloaded by the updater.
//
//go:embed res/default-launcher.json
var defaultLauncherJSON []byte

// standalone is true when the exe runs without a launcher.json next to it.
// Pack files then live in <game folder>/fichiers instead of next to the exe.
var standalone bool

// gameRoot is where Minecraft, Java, the packs and the game folders are
// installed. The player picks it on first launch ("" = not chosen yet).
// dataDir only keeps the launcher's own small files (settings, account…).
var gameRoot string

// storageName keeps the data folder stable even if the launcher is renamed.
func storageName(c Config) string {
	if c.DataName != "" {
		return c.DataName
	}
	if c.Name != "" {
		return c.Name
	}
	return "Client"
}

func dataDirFor(name string) string {
	if d := os.Getenv("LAUNCHER_DATA"); d != "" {
		return d
	}
	base, err := os.UserConfigDir() // %APPDATA% on Windows
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "."+slug(name)+"-launcher")
}

func locationFile() string { return filepath.Join(dataDir, "location.json") }

// resolveGameRoot reads the chosen install folder. Installs made before this
// choice existed keep using the data folder, without asking.
func resolveGameRoot() {
	gameRoot = ""
	var loc struct {
		Path string `json:"path"`
	}
	if b, err := os.ReadFile(locationFile()); err == nil && json.Unmarshal(b, &loc) == nil && loc.Path != "" {
		gameRoot = loc.Path
		return
	}
	if os.Getenv("LAUNCHER_DATA") != "" && os.Getenv("LAUNCHER_ASK") == "" { // tests
		gameRoot = dataDir
		return
	}
	for _, d := range []string{"versions", "instances", "fichiers", "game"} {
		if _, err := os.Stat(filepath.Join(dataDir, d)); err == nil {
			gameRoot = dataDir
			saveGameRoot()
			return
		}
	}
}

func saveGameRoot() {
	b, _ := json.MarshalIndent(map[string]string{"path": gameRoot}, "", "  ")
	_ = os.WriteFile(locationFile(), b, 0o644)
}

func installed() bool { return gameRoot != "" }

func loadConfig() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	baseDir = filepath.Dir(exe)
	if d := os.Getenv("LAUNCHER_DIR"); d != "" { // handy for testing
		baseDir = d
	}

	path := filepath.Join(baseDir, "launcher.json")
	_, statErr := os.Stat(path)
	standalone = statErr != nil
	if standalone {
		var emb Config
		if err := json.Unmarshal(defaultLauncherJSON, &emb); err != nil {
			return err
		}
		dataDir = dataDirFor(storageName(emb))
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return err
		}
		resolveGameRoot()
		if !installed() {
			// first launch: run on the built-in config until the player picks a folder
			cfg = emb
			baseDir = ""
			return cfg.normalize()
		}
		baseDir = filepath.Join(gameRoot, "fichiers")
		path = filepath.Join(baseDir, "launcher.json")
		if _, err := os.Stat(path); err != nil {
			if err := os.MkdirAll(baseDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, defaultLauncherJSON, 0o644); err != nil {
				return err
			}
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg = Config{}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if err := cfg.normalize(); err != nil {
		return err
	}
	if !standalone {
		dataDir = dataDirFor(storageName(cfg))
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return err
		}
		resolveGameRoot()
		if !installed() { // the owner's copy: no question, use the data folder
			gameRoot = dataDir
			saveGameRoot()
		}
	}
	migrateLegacyGameDir()
	return nil
}

// suggestedInstallDir is what the first-launch window proposes.
func suggestedInstallDir() string { return dataDir }

// finalInstallDir: picking a folder that already holds other things installs
// into a "Nexus" sub-folder instead of spreading files into it.
func finalInstallDir(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == filepath.Clean(dataDir) {
		return p
	}
	entries, err := os.ReadDir(p)
	if err != nil || len(entries) == 0 {
		return p
	}
	for _, d := range []string{"versions", "instances", "fichiers"} {
		if _, err := os.Stat(filepath.Join(p, d)); err == nil {
			return p // already an install
		}
	}
	name := cfg.Name
	if name == "" {
		name = "Nexus"
	}
	return filepath.Join(p, name)
}

// setInstallDir is called once, from the first-launch window.
func setInstallDir(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("choisis un dossier complet (par exemple D:\\Jeux\\Nexus)")
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return fmt.Errorf("impossible de créer %s : %w", p, err)
	}
	probe := filepath.Join(p, ".test-ecriture")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("impossible d'écrire dans %s (dossier protégé ?) : %w", p, err)
	}
	_ = os.Remove(probe)
	gameRoot = p
	saveGameRoot()
	return loadConfig()
}

// Before multi-pack support the single game folder was <data>/game:
// hand it to the first pack so worlds and settings are kept.
func migrateLegacyGameDir() {
	if !installed() {
		return
	}
	old := filepath.Join(gameRoot, "game")
	if _, err := os.Stat(old); err != nil || len(cfg.Packs) == 0 {
		return
	}
	dst := pInstance(&cfg.Packs[0])
	if _, err := os.Stat(dst); err == nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.Rename(old, dst)
}

func findPack(id string) *Pack {
	for i := range cfg.Packs {
		if cfg.Packs[i].ID == id {
			return &cfg.Packs[i]
		}
	}
	return nil
}

func findGame(id string) *Game {
	for i := range cfg.Games {
		if cfg.Games[i].ID == id {
			return &cfg.Games[i]
		}
	}
	return nil
}

func slug(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "client"
	}
	return s
}

func loadSettings() Settings {
	s := Settings{RamMB: cfg.DefaultRamMB}
	if b, err := os.ReadFile(filepath.Join(dataDir, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.RamMB < 1024 {
		s.RamMB = cfg.DefaultRamMB
	}
	if findPack(s.LastPack) == nil && len(cfg.Packs) > 0 {
		s.LastPack = cfg.Packs[0].ID
	}
	return s
}

func saveSettings(s Settings) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(dataDir, "settings.json"), b, 0o644)
}

// Folder layout inside the install folder (shared by every pack, except instances/).
func pVersions() string           { return filepath.Join(gameRoot, "versions") }
func pLibraries() string          { return filepath.Join(gameRoot, "libraries") }
func pAssets() string             { return filepath.Join(gameRoot, "assets") }
func pRuntime() string            { return filepath.Join(gameRoot, "runtime") }
func pWebview() string            { return filepath.Join(dataDir, "webview") }
func pInstance(p *Pack) string    { return filepath.Join(gameRoot, "instances", p.ID) }
func pPackSource(p *Pack) string  { return filepath.Join(baseDir, filepath.FromSlash(p.Folder)) }
func pPackModsSrc(p *Pack) string { return filepath.Join(pPackSource(p), "mods") }

// cleanServer turns what people paste ("http://play.example.com/", " play.example.com:25565 ")
// into the "host[:port]" form Minecraft expects.
func cleanServer(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
