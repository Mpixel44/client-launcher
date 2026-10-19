package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	manifestURL    = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"
	javaRuntimeURL = "https://launchermeta.mojang.com/v1/products/java-runtime/2ec0cc96c44e5a76b9c8b7c39df7210883d12871/all.json"
	fabricMetaURL  = "https://meta.fabricmc.net/v2"
	assetsBaseURL  = "https://resources.download.minecraft.net/"
	librariesURL   = "https://libraries.minecraft.net/"
)

// ---------- Version JSON model ----------

type OSRule struct {
	Name    string `json:"name"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
}

type Rule struct {
	Action   string          `json:"action"`
	OS       *OSRule         `json:"os"`
	Features map[string]bool `json:"features"`
}

type Artifact struct {
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type Library struct {
	Name      string `json:"name"`
	URL       string `json:"url"` // Fabric style: maven repository base
	SHA1      string `json:"sha1"`
	Size      int64  `json:"size"`
	Downloads *struct {
		Artifact    *Artifact           `json:"artifact"`
		Classifiers map[string]Artifact `json:"classifiers"`
	} `json:"downloads"`
	Natives map[string]string `json:"natives"`
	Rules   []Rule            `json:"rules"`
}

// Argument is either a plain string or {"rules": [...], "value": string|[]string}.
type Argument struct {
	Value []string
	Rules []Rule
}

func (a *Argument) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		a.Value = []string{s}
		return nil
	}
	var obj struct {
		Rules []Rule          `json:"rules"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	a.Rules = obj.Rules
	if err := json.Unmarshal(obj.Value, &s); err == nil {
		a.Value = []string{s}
		return nil
	}
	return json.Unmarshal(obj.Value, &a.Value)
}

type VersionJSON struct {
	ID                 string `json:"id"`
	InheritsFrom       string `json:"inheritsFrom"`
	MainClass          string `json:"mainClass"`
	Type               string `json:"type"`
	Assets             string `json:"assets"`
	MinecraftArguments string `json:"minecraftArguments"`
	Arguments          *struct {
		Game []Argument `json:"game"`
		JVM  []Argument `json:"jvm"`
	} `json:"arguments"`
	AssetIndex *struct {
		ID        string `json:"id"`
		SHA1      string `json:"sha1"`
		Size      int64  `json:"size"`
		TotalSize int64  `json:"totalSize"`
		URL       string `json:"url"`
	} `json:"assetIndex"`
	Downloads   map[string]Artifact `json:"downloads"`
	Libraries   []Library           `json:"libraries"`
	JavaVersion *struct {
		Component    string `json:"component"`
		MajorVersion int    `json:"majorVersion"`
	} `json:"javaVersion"`
	Logging *struct {
		Client *struct {
			Argument string `json:"argument"`
			File     struct {
				ID   string `json:"id"`
				SHA1 string `json:"sha1"`
				Size int64  `json:"size"`
				URL  string `json:"url"`
			} `json:"file"`
		} `json:"client"`
	} `json:"logging"`
}

// ---------- Platform helpers ----------

// overridable in tests
var goos, goarch = runtime.GOOS, runtime.GOARCH

func osName() string {
	switch goos {
	case "windows":
		return "windows"
	case "darwin":
		return "osx"
	default:
		return "linux"
	}
}

func ruleMatches(r Rule) bool {
	if r.OS != nil {
		if r.OS.Name != "" && r.OS.Name != osName() {
			return false
		}
		if r.OS.Arch != "" {
			// Mojang uses "x86" for 32-bit systems
			arch := map[string]string{"386": "x86", "amd64": "x86_64", "arm64": "arm64"}[goarch]
			if r.OS.Arch != arch {
				return false
			}
		}
	}
	for _, v := range r.Features { // we enable no optional feature (demo, custom resolution, quick play…)
		if v {
			return false
		}
	}
	return true
}

func rulesAllow(rules []Rule) bool {
	if len(rules) == 0 {
		return true
	}
	allowed := false
	for _, r := range rules {
		if ruleMatches(r) {
			allowed = r.Action == "allow"
		}
	}
	return allowed
}

// mavenPath turns "group:artifact:version[:classifier][@ext]" into a relative path.
func mavenPath(name string) string {
	ext := "jar"
	if i := strings.Index(name, "@"); i >= 0 {
		ext = name[i+1:]
		name = name[:i]
	}
	p := strings.Split(name, ":")
	if len(p) < 3 {
		return name
	}
	file := p[1] + "-" + p[2]
	if len(p) >= 4 {
		file += "-" + p[3]
	}
	return strings.ReplaceAll(p[0], ".", "/") + "/" + p[1] + "/" + p[2] + "/" + file + "." + ext
}

// libKey identifies a library regardless of version, to let Fabric override vanilla ones.
func libKey(name string) string {
	name = strings.SplitN(name, "@", 2)[0]
	p := strings.Split(name, ":")
	if len(p) >= 4 {
		return p[0] + ":" + p[1] + ":" + p[3]
	}
	if len(p) >= 2 {
		return p[0] + ":" + p[1]
	}
	return name
}

// ---------- Install ----------

type Progress func(label string, done, total int)

type Installed struct {
	Version   *VersionJSON // merged (Fabric on top of vanilla)
	Classpath []string
	Natives   string
	Java      string
	LogConfig string
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// fetchVanilla downloads (or reads from cache) the vanilla version JSON.
func fetchVanilla(version string) (*VersionJSON, error) {
	path := filepath.Join(pVersions(), version, version+".json")
	var manifest struct {
		Versions []struct {
			ID   string `json:"id"`
			URL  string `json:"url"`
			SHA1 string `json:"sha1"`
		} `json:"versions"`
	}
	if err := getJSON(manifestURL, &manifest); err == nil {
		for _, v := range manifest.Versions {
			if v.ID == version {
				t := dlTask{URL: v.URL, Path: path, SHA1: v.SHA1}
				if needsDownload(t, true) {
					if err := downloadFile(t); err != nil {
						return nil, err
					}
				}
				break
			}
		}
	}
	var vj VersionJSON
	if err := readJSONFile(path, &vj); err != nil {
		return nil, fmt.Errorf("impossible de récupérer Minecraft %s (pas d'internet ?)", version)
	}
	return &vj, nil
}

// fetchFabric downloads (or reads from cache) the Fabric loader profile for this Minecraft version.
// key identifies the pack, so packs pinned to different loaders don't mix.
func fetchFabric(mc, loader, key string) (*VersionJSON, error) {
	current := filepath.Join(pVersions(), "fabric-current-"+key+".txt")
	if loader == "" {
		var list []struct {
			Loader struct {
				Version string `json:"version"`
				Stable  bool   `json:"stable"`
			} `json:"loader"`
		}
		if err := getJSON(fabricMetaURL+"/versions/loader/"+mc, &list); err == nil {
			for _, l := range list {
				if l.Loader.Stable {
					loader = l.Loader.Version
					break
				}
			}
			if loader == "" && len(list) > 0 {
				loader = list[0].Loader.Version
			}
		}
	}
	if loader != "" {
		id := "fabric-loader-" + loader + "-" + mc
		path := filepath.Join(pVersions(), id, id+".json")
		if _, err := os.Stat(path); err != nil {
			var raw json.RawMessage
			if err := getJSON(fabricMetaURL+"/versions/loader/"+mc+"/"+loader+"/profile/json", &raw); err != nil {
				return nil, fmt.Errorf("impossible de récupérer Fabric %s : %w", loader, err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				return nil, err
			}
		}
		_ = os.WriteFile(current, []byte(id), 0o644)
	}
	b, err := os.ReadFile(current)
	if err != nil {
		return nil, errors.New("impossible de récupérer Fabric (pas d'internet ?)")
	}
	id := strings.TrimSpace(string(b))
	var vj VersionJSON
	if err := readJSONFile(filepath.Join(pVersions(), id, id+".json"), &vj); err != nil {
		return nil, err
	}
	return &vj, nil
}

func merge(parent, child *VersionJSON) *VersionJSON {
	m := *parent
	m.ID = child.ID
	if child.MainClass != "" {
		m.MainClass = child.MainClass
	}
	seen := map[string]bool{}
	m.Libraries = nil
	for _, l := range append(append([]Library{}, child.Libraries...), parent.Libraries...) {
		k := libKey(l.Name)
		if seen[k] {
			continue
		}
		seen[k] = true
		m.Libraries = append(m.Libraries, l)
	}
	if child.Arguments != nil {
		if m.Arguments == nil {
			m.Arguments = child.Arguments
		} else {
			args := *m.Arguments
			args.Game = append(append([]Argument{}, args.Game...), child.Arguments.Game...)
			args.JVM = append(append([]Argument{}, args.JVM...), child.Arguments.JVM...)
			m.Arguments = &args
		}
	}
	return &m
}

// install makes sure everything needed to start the game is on disk.
func install(p *Pack, status func(string), progress Progress) (*Installed, error) {
	status("Récupération de Minecraft " + p.Minecraft + "…")
	vanilla, err := fetchVanilla(p.Minecraft)
	if err != nil {
		return nil, err
	}
	v := vanilla
	if p.Loader == "fabric" {
		status("Récupération de Fabric…")
		fabric, err := fetchFabric(p.Minecraft, p.FabricLoader, p.ID)
		if err != nil {
			return nil, err
		}
		v = merge(vanilla, fabric)
	}
	inst := &Installed{Version: v, Natives: filepath.Join(pVersions(), v.ID, "natives")}

	// --- client jar + libraries ---
	var tasks []dlTask
	client, ok := vanilla.Downloads["client"]
	if !ok {
		return nil, errors.New("jar client introuvable dans la version")
	}
	clientJar := filepath.Join(pVersions(), vanilla.ID, vanilla.ID+".jar")
	tasks = append(tasks, dlTask{URL: client.URL, Path: clientJar, SHA1: client.SHA1, Size: client.Size})

	type nativeJar struct{ path string }
	var natives []nativeJar
	for _, l := range v.Libraries {
		if !rulesAllow(l.Rules) {
			continue
		}
		var a Artifact
		switch {
		case l.Downloads != nil && l.Downloads.Artifact != nil:
			a = *l.Downloads.Artifact
		case l.Downloads != nil && len(l.Natives) > 0:
			// old-style native-only library: no main artifact
		default:
			base := l.URL
			if base == "" {
				base = librariesURL
			}
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			a = Artifact{Path: mavenPath(l.Name), URL: base + mavenPath(l.Name), SHA1: l.SHA1, Size: l.Size}
		}
		if a.Path == "" && a.URL != "" {
			a.Path = mavenPath(l.Name)
		}
		if a.Path != "" {
			p := filepath.Join(pLibraries(), filepath.FromSlash(a.Path))
			if a.URL != "" {
				tasks = append(tasks, dlTask{URL: a.URL, Path: p, SHA1: a.SHA1, Size: a.Size})
			}
			inst.Classpath = append(inst.Classpath, p)
		}
		// legacy natives (pre-1.19 style)
		if cls, ok := l.Natives[osName()]; ok && l.Downloads != nil {
			arch := "64"
			if runtime.GOARCH == "386" {
				arch = "32"
			}
			cls = strings.ReplaceAll(cls, "${arch}", arch)
			if na, ok := l.Downloads.Classifiers[cls]; ok {
				p := filepath.Join(pLibraries(), filepath.FromSlash(na.Path))
				tasks = append(tasks, dlTask{URL: na.URL, Path: p, SHA1: na.SHA1, Size: na.Size})
				natives = append(natives, nativeJar{p})
			}
		}
	}
	inst.Classpath = append(inst.Classpath, clientJar)

	if v.Logging != nil && v.Logging.Client != nil && v.Logging.Client.File.URL != "" {
		f := v.Logging.Client.File
		inst.LogConfig = filepath.Join(pAssets(), "log_configs", f.ID)
		tasks = append(tasks, dlTask{URL: f.URL, Path: inst.LogConfig, SHA1: f.SHA1, Size: f.Size})
	}

	status("Téléchargement des bibliothèques…")
	if err := downloadAll("Bibliothèques", tasks, true, 8, progress); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(inst.Natives, 0o755); err != nil {
		return nil, err
	}
	for _, n := range natives {
		if err := extractNatives(n.path, inst.Natives); err != nil {
			return nil, err
		}
	}

	// --- assets ---
	if v.AssetIndex != nil {
		status("Téléchargement des ressources du jeu…")
		idxPath := filepath.Join(pAssets(), "indexes", v.AssetIndex.ID+".json")
		it := dlTask{URL: v.AssetIndex.URL, Path: idxPath, SHA1: v.AssetIndex.SHA1, Size: v.AssetIndex.Size}
		if needsDownload(it, true) {
			if err := downloadFile(it); err != nil {
				return nil, err
			}
		}
		var idx struct {
			Objects map[string]struct {
				Hash string `json:"hash"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		if err := readJSONFile(idxPath, &idx); err != nil {
			return nil, err
		}
		var at []dlTask
		seen := map[string]bool{}
		for _, o := range idx.Objects {
			if seen[o.Hash] || len(o.Hash) < 2 {
				continue
			}
			seen[o.Hash] = true
			rel := o.Hash[:2] + "/" + o.Hash
			at = append(at, dlTask{URL: assetsBaseURL + rel, Path: filepath.Join(pAssets(), "objects", o.Hash[:2], o.Hash), SHA1: o.Hash, Size: o.Size})
		}
		if err := downloadAll("Ressources", at, false, 16, progress); err != nil {
			return nil, err
		}
	}

	// --- Java ---
	component, major := "java-runtime-delta", 21
	if v.JavaVersion != nil {
		component, major = v.JavaVersion.Component, v.JavaVersion.MajorVersion
	}
	status(fmt.Sprintf("Préparation de Java %d…", major))
	java, err := installJava(component, progress)
	if err != nil {
		return nil, err
	}
	inst.Java = java
	return inst, nil
}

func extractNatives(jar, dest string) error {
	z, err := zip.OpenReader(jar)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "META-INF/") {
			continue
		}
		out := filepath.Join(dest, filepath.Base(f.Name))
		if _, err := os.Stat(out); err == nil {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(w, r)
		r.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------- Java runtime (Mojang's official builds) ----------

func javaPlatform() string {
	switch runtime.GOOS {
	case "windows":
		switch runtime.GOARCH {
		case "arm64":
			return "windows-arm64"
		case "386":
			return "windows-x86"
		}
		return "windows-x64"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "mac-os-arm64"
		}
		return "mac-os"
	default:
		if runtime.GOARCH == "386" {
			return "linux-i386"
		}
		return "linux"
	}
}

func javaExecutable(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "bin", "javaw.exe")
	case "darwin":
		return filepath.Join(home, "jre.bundle", "Contents", "Home", "bin", "java")
	}
	return filepath.Join(home, "bin", "java")
}

func installJava(component string, progress Progress) (string, error) {
	home := filepath.Join(pRuntime(), component)
	exe := javaExecutable(home)
	marker := filepath.Join(home, ".installed")

	var all map[string]map[string][]struct {
		Manifest struct {
			SHA1 string `json:"sha1"`
			URL  string `json:"url"`
		} `json:"manifest"`
	}
	if err := getJSON(javaRuntimeURL, &all); err != nil {
		if _, e := os.Stat(exe); e == nil { // offline: keep the one we have
			return exe, nil
		}
		return "", fmt.Errorf("impossible de télécharger Java : %w", err)
	}
	entries := all[javaPlatform()][component]
	if len(entries) == 0 {
		return "", fmt.Errorf("Java %s indisponible pour %s", component, javaPlatform())
	}
	man := entries[0].Manifest
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == man.SHA1 {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}

	var files struct {
		Files map[string]struct {
			Type       string `json:"type"`
			Executable bool   `json:"executable"`
			Target     string `json:"target"`
			Downloads  struct {
				Raw *Artifact `json:"raw"`
			} `json:"downloads"`
		} `json:"files"`
	}
	if err := getJSON(man.URL, &files); err != nil {
		return "", fmt.Errorf("impossible de télécharger Java : %w", err)
	}
	var tasks []dlTask
	var execs []string
	for name, f := range files.Files {
		p := filepath.Join(home, filepath.FromSlash(name))
		switch f.Type {
		case "directory":
			_ = os.MkdirAll(p, 0o755)
		case "file":
			if f.Downloads.Raw != nil {
				tasks = append(tasks, dlTask{URL: f.Downloads.Raw.URL, Path: p, SHA1: f.Downloads.Raw.SHA1, Size: f.Downloads.Raw.Size})
				if f.Executable {
					execs = append(execs, p)
				}
			}
		case "link":
			if runtime.GOOS != "windows" {
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				_ = os.Remove(p)
				_ = os.Symlink(f.Target, p)
			}
		}
	}
	if err := downloadAll("Java", tasks, true, 8, progress); err != nil {
		return "", err
	}
	for _, p := range execs {
		_ = os.Chmod(p, 0o755)
	}
	if _, err := os.Stat(exe); err != nil {
		return "", errors.New("Java installé mais javaw introuvable")
	}
	_ = os.WriteFile(marker, []byte(man.SHA1), 0o644)
	return exe, nil
}
