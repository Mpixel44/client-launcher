package main

import (
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
}

// Config is read from launcher.json, next to the executable.
type Config struct {
	Name         string `json:"name"`
	DefaultRamMB int    `json:"defaultRamMB"`
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
}

var (
	exeDir  string
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

func loadConfig() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	exeDir = filepath.Dir(exe)
	if d := os.Getenv("LAUNCHER_DIR"); d != "" { // handy for testing
		exeDir = d
	}

	path := filepath.Join(exeDir, "launcher.json")
	if b, err := os.ReadFile(path); err == nil {
		cfg = Config{}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return err
		}
	} else {
		cfg = defaultConfig()
		b, _ := json.MarshalIndent(cfg, "", "  ")
		_ = os.WriteFile(path, b, 0o644)
	}
	if err := cfg.normalize(); err != nil {
		return err
	}

	base, err := os.UserConfigDir() // %APPDATA% on Windows
	if err != nil {
		base = exeDir
	}
	dataDir = filepath.Join(base, "."+slug(cfg.Name)+"-launcher")
	if d := os.Getenv("LAUNCHER_DATA"); d != "" {
		dataDir = d
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	migrateLegacyGameDir()
	return nil
}

// Before multi-pack support the single game folder was <data>/game:
// hand it to the first pack so worlds and settings are kept.
func migrateLegacyGameDir() {
	old := filepath.Join(dataDir, "game")
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

// Folder layout inside dataDir (shared by every pack, except instances/).
func pVersions() string           { return filepath.Join(dataDir, "versions") }
func pLibraries() string          { return filepath.Join(dataDir, "libraries") }
func pAssets() string             { return filepath.Join(dataDir, "assets") }
func pRuntime() string            { return filepath.Join(dataDir, "runtime") }
func pWebview() string            { return filepath.Join(dataDir, "webview") }
func pInstance(p *Pack) string    { return filepath.Join(dataDir, "instances", p.ID) }
func pPackSource(p *Pack) string  { return filepath.Join(exeDir, filepath.FromSlash(p.Folder)) }
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
