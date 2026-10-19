package main

// Self-update: the launcher downloads manifest.json (+ manifest.sig) from
// cfg.UpdateURL, checks the ed25519 signature with cfg.UpdateKey, then:
//   - replaces Launcher.exe and restarts if it changed,
//   - replaces launcher.json if it changed,
//   - downloads changed pack files next to the launcher and removes .jar files
//     that are no longer published (in the "mirror" folders).
// Player customisation is never touched: the game folders under
// %APPDATA% only receive *new* config files (see syncMods / copyIfMissing).

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type UpdFile struct {
	Path   string `json:"path"` // relative to the launcher folder, with "/"
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version   int       `json:"version"`
	Published string    `json:"published"`
	Launcher  *UpdFile  `json:"launcher,omitempty"`
	Config    *UpdFile  `json:"config,omitempty"`
	Files     []UpdFile `json:"files"`
	Mirror    []string  `json:"mirror"` // folders whose .jar files must match exactly
}

type UpdateResult struct {
	Version       int
	Restart       bool // a new Launcher.exe is in place
	ConfigChanged bool
	Downloaded    int
	Removed       int
}

var errUpdatesOff = errors.New("mises à jour non configurées")

func updatesEnabled() bool { return strings.TrimSpace(cfg.UpdateURL) != "" && cfg.UpdateKey != "" }

func updateBase() string {
	u := strings.TrimSpace(cfg.UpdateURL)
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

func fetchBytes(url string, limit int64) ([]byte, error) {
	resp, err := httpGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// fetchManifest downloads and verifies the signed manifest.
func fetchManifest() (*Manifest, error) {
	if !updatesEnabled() {
		return nil, errUpdatesOff
	}
	pub, err := base64.StdEncoding.DecodeString(cfg.UpdateKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("updateKey invalide dans launcher.json")
	}
	bust := fmt.Sprintf("?t=%d", time.Now().Unix())
	body, err := fetchBytes(updateBase()+"manifest.json"+bust, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("serveur de mise à jour injoignable : %w", err)
	}
	sigTxt, err := fetchBytes(updateBase()+"manifest.sig"+bust, 4096)
	if err != nil {
		return nil, fmt.Errorf("signature de mise à jour introuvable : %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigTxt)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), body, sig) {
		return nil, errors.New("mise à jour refusée : signature invalide")
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest illisible : %w", err)
	}
	return &m, nil
}

// safeLocal maps a manifest path to a file next to the launcher, refusing anything
// that would escape the launcher folder.
func safeLocal(p string) (string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if clean == "/" || strings.Contains(p, "..") || strings.Contains(p, ":") {
		return "", fmt.Errorf("chemin refusé : %s", p)
	}
	return filepath.Join(baseDir, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}

// ---------- hash cache (avoid re-hashing 150 Mo of mods at each start) ----------

type hashEntry struct {
	Size int64  `json:"size"`
	Mod  int64  `json:"mod"`
	Sum  string `json:"sum"`
}

var (
	hashMu    sync.Mutex
	hashCache map[string]hashEntry
)

func hashCachePath() string { return filepath.Join(dataDir, "hashcache.json") }

func fileSHA256(p string) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	hashMu.Lock()
	if hashCache == nil {
		hashCache = map[string]hashEntry{}
		if b, err := os.ReadFile(hashCachePath()); err == nil {
			_ = json.Unmarshal(b, &hashCache)
		}
	}
	if e, ok := hashCache[p]; ok && e.Size == st.Size() && e.Mod == st.ModTime().UnixNano() {
		hashMu.Unlock()
		return e.Sum, nil
	}
	hashMu.Unlock()

	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	hashMu.Lock()
	hashCache[p] = hashEntry{Size: st.Size(), Mod: st.ModTime().UnixNano(), Sum: sum}
	hashMu.Unlock()
	return sum, nil
}

func saveHashCache() {
	hashMu.Lock()
	defer hashMu.Unlock()
	if hashCache == nil {
		return
	}
	b, _ := json.Marshal(hashCache)
	_ = os.WriteFile(hashCachePath(), b, 0o644)
}

func sameFile(local string, f *UpdFile) bool {
	sum, err := fileSHA256(local)
	return err == nil && sum == f.SHA256
}

func updTask(f *UpdFile, dest string) dlTask {
	return dlTask{URL: updateBase() + f.SHA256, Path: dest, SHA256: f.SHA256, Size: f.Size}
}

// ---------- apply ----------

// applyUpdate brings the launcher folder in line with the manifest.
// allowSelf=false skips replacing Launcher.exe (used right before playing).
func applyUpdate(m *Manifest, allowSelf bool, status func(string), progress Progress) (*UpdateResult, error) {
	res := &UpdateResult{Version: m.Version}
	defer saveHashCache()

	// 1. the launcher itself (Windows only: that's what we publish)
	if allowSelf && m.Launcher != nil && runtime.GOOS == "windows" {
		exe, err := os.Executable()
		if err == nil {
			exe, _ = filepath.EvalSymlinks(exe)
			if !sameFile(exe, m.Launcher) {
				status("Téléchargement de la nouvelle version du launcher…")
				if err := swapExecutable(exe, m.Launcher); err != nil {
					return nil, err
				}
				res.Restart = true
				return res, nil // the new launcher finishes the job after restarting
			}
		}
	}

	// 2. launcher.json
	if m.Config != nil {
		local := filepath.Join(baseDir, "launcher.json")
		if !sameFile(local, m.Config) {
			status("Mise à jour de la liste des packs…")
			if err := downloadFile(updTask(m.Config, local+".new")); err != nil {
				return nil, err
			}
			var probe Config
			b, _ := os.ReadFile(local + ".new")
			if err := json.Unmarshal(b, &probe); err != nil {
				os.Remove(local + ".new")
				return nil, fmt.Errorf("launcher.json publié invalide : %w", err)
			}
			_ = os.Remove(local)
			if err := os.Rename(local+".new", local); err != nil {
				return nil, err
			}
			res.ConfigChanged = true
		}
	}

	// 3. pack files
	status("Vérification des fichiers des packs…")
	wanted := map[string]bool{}
	var todo []dlTask
	for i := range m.Files {
		f := &m.Files[i]
		local, err := safeLocal(f.Path)
		if err != nil {
			return nil, err
		}
		wanted[strings.ToLower(local)] = true
		if !sameFile(local, f) {
			todo = append(todo, updTask(f, local))
		}
	}
	if len(todo) > 0 {
		status(fmt.Sprintf("Mise à jour : %d fichier(s) à télécharger…", len(todo)))
		if err := downloadMany("Mise à jour", todo, 6, progress); err != nil {
			return nil, err
		}
		res.Downloaded = len(todo)
	}

	// 4. remove mods that are no longer published
	for _, dir := range m.Mirror {
		localDir, err := safeLocal(dir)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(localDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
				continue
			}
			p := filepath.Join(localDir, e.Name())
			if !wanted[strings.ToLower(p)] {
				if os.Remove(p) == nil {
					res.Removed++
				}
			}
		}
	}

	st := loadUpdateState()
	st.Version = m.Version
	st.Published = m.Published
	st.Checked = time.Now().Format(time.RFC3339)
	saveUpdateState(st)
	return res, nil
}

// swapExecutable downloads the new launcher next to the running one and swaps
// them (Windows lets a running .exe be renamed, not overwritten).
func swapExecutable(exe string, f *UpdFile) error {
	tmp := exe + ".new"
	if err := downloadFile(updTask(f, tmp)); err != nil {
		return err
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("impossible de remplacer le launcher : %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("impossible de remplacer le launcher : %w", err)
	}
	return nil
}

// ---------- small persisted state ----------

type UpdateState struct {
	Version   int    `json:"version"`
	Published string `json:"published"`
	Checked   string `json:"checked"`
}

func loadUpdateState() UpdateState {
	var s UpdateState
	if b, err := os.ReadFile(filepath.Join(dataDir, "update.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveUpdateState(s UpdateState) {
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(filepath.Join(dataDir, "update.json"), b, 0o644)
}

// ---------- restart ----------

// cleanupOldExe removes the previous launcher left by a self-update.
func cleanupOldExe() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	for i := 0; i < 10; i++ {
		if err := os.Remove(exe + ".old"); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func restartSelf() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	cmd := exec.Command(exe, "--after-update")
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err == nil {
		os.Exit(0)
	}
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
