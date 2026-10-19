package main

// Admin side: builds publication/en-ligne/, the folder to upload to the server.
// It only exists on the owner's PC: the secret key lives in
// publication/cle-secrete.txt and is never published.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func pPublication() string { return filepath.Join(exeDir, "publication") }
func pSecretKey() string   { return filepath.Join(pPublication(), "cle-secrete.txt") }
func pOnline() string      { return filepath.Join(pPublication(), "en-ligne") }

// isAdmin: this copy of the launcher is the source of the updates.
func isAdmin() bool {
	_, err := os.Stat(pSecretKey())
	return err == nil
}

func loadSecretKey() (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(pSecretKey())
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, err := base64.StdEncoding.DecodeString(line)
		if err == nil && len(k) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(k), nil
		}
	}
	return nil, errors.New("clé secrète illisible (publication/cle-secrete.txt)")
}

type PublishResult struct {
	Version int    `json:"version"`
	Files   int    `json:"files"`
	SizeMB  int    `json:"sizeMB"`
	Folder  string `json:"folder"`
	URL     string `json:"url"`
}

// publish snapshots the launcher folder into publication/en-ligne.
func publish(status func(string), progress Progress) (*PublishResult, error) {
	key, err := loadSecretKey()
	if err != nil {
		return nil, err
	}
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	if cfg.UpdateKey != pub {
		return nil, errors.New("updateKey de launcher.json ne correspond pas à la clé secrète : ne modifie pas ces deux valeurs")
	}
	if !updatesEnabled() {
		return nil, errors.New("updateUrl est vide dans launcher.json")
	}

	out := pOnline()
	status("Préparation du dossier de publication…")
	if err := os.RemoveAll(out); err != nil {
		return nil, fmt.Errorf("impossible de vider %s (fichier ouvert ?) : %w", out, err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}

	version := 1
	if b, err := os.ReadFile(filepath.Join(pPublication(), "version.txt")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			version = n + 1
		}
	}

	m := Manifest{Version: version, Published: time.Now().Format(time.RFC3339)}
	var total int64
	add := func(abs string) (*UpdFile, error) {
		rel, err := filepath.Rel(exeDir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%s n'est pas dans le dossier du launcher", abs)
		}
		sum, err := fileSHA256(abs)
		if err != nil {
			return nil, err
		}
		st, _ := os.Stat(abs)
		dst := filepath.Join(out, sum) // flat: works for any web folder and for GitHub release assets
		if _, err := os.Stat(dst); err != nil {
			if err := copyFile(abs, dst); err != nil {
				return nil, err
			}
			total += st.Size()
		}
		return &UpdFile{Path: filepath.ToSlash(rel), SHA256: sum, Size: st.Size()}, nil
	}

	// launcher + config
	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		if e := os.Getenv("LAUNCHER_EXE"); e != "" { // publishing from another machine
			exe = e
		}
		if strings.EqualFold(filepath.Ext(exe), ".exe") {
			f, err := add(exe)
			if err != nil {
				return nil, err
			}
			f.Path = filepath.Base(exe)
			m.Launcher = f
		}
	}
	cf, err := add(filepath.Join(exeDir, "launcher.json"))
	if err != nil {
		return nil, err
	}
	m.Config = cf

	// pack files
	var paths []string
	seen := map[string]bool{}
	collect := func(p string) {
		if !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			paths = append(paths, p)
		}
	}
	for i := range cfg.Packs {
		p := &cfg.Packs[i]
		modsDir := pPackModsSrc(p)
		rel, _ := filepath.Rel(exeDir, modsDir)
		m.Mirror = append(m.Mirror, filepath.ToSlash(rel))
		if entries, err := os.ReadDir(modsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
					collect(filepath.Join(modsDir, e.Name()))
				}
			}
		}
		for _, extra := range []string{"config", "resourcepacks", "shaderpacks", "options.txt", "servers.dat"} {
			src := filepath.Join(pPackSource(p), extra)
			st, err := os.Stat(src)
			if err != nil {
				continue
			}
			if !st.IsDir() {
				collect(src)
				continue
			}
			_ = filepath.Walk(src, func(fp string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					collect(fp)
				}
				return nil
			})
		}
		if p.Icon != "" {
			if ip := filepath.Join(pPackSource(p), filepath.FromSlash(p.Icon)); fileExists(ip) {
				collect(ip)
			}
		}
	}
	sort.Strings(paths)
	for i, fp := range paths {
		progress("Publication", i, len(paths))
		f, err := add(fp)
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, *f)
	}
	progress("Publication", len(paths), len(paths))
	saveHashCache()

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(key, body)
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), body, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.sig"), []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
		return nil, err
	}
	_ = os.WriteFile(filepath.Join(pPublication(), "version.txt"), []byte(strconv.Itoa(version)), 0o644)

	return &PublishResult{Version: version, Files: len(m.Files) + 2, SizeMB: int(total >> 20), Folder: out, URL: updateBase()}, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// generateKeys creates a new key pair (used once, when setting up updates).
func generateKeys() (pub, secret string, err error) {
	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pk), base64.StdEncoding.EncodeToString(sk), nil
}
