package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// countMods returns the number of .jar files shipped next to the launcher.
func countMods(p *Pack) int {
	if baseDir == "" {
		return 0
	}
	entries, err := os.ReadDir(pPackModsSrc(p))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			n++
		}
	}
	return n
}

// syncMods makes game/mods an exact copy of the mods folder shipped with the launcher.
// Optional folders (config, resourcepacks, shaderpacks…) are copied only when missing,
// so players keep their own settings.
func syncMods(p *Pack, skip map[string]bool, status func(string)) error {
	status("Synchronisation des mods…")
	src := pPackModsSrc(p)
	dst := filepath.Join(pInstance(p), "mods")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	wanted := map[string]bool{}
	if entries, err := os.ReadDir(src); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".jar") {
				continue
			}
			if skip[name] {
				continue // turned off by the player: removed from the game folder below
			}
			wanted[strings.ToLower(name)] = true
			si, err := e.Info()
			if err != nil {
				return err
			}
			dp := filepath.Join(dst, name)
			if di, err := os.Stat(dp); err == nil && di.Size() == si.Size() && di.ModTime().Equal(si.ModTime()) {
				continue
			}
			if err := copyFile(filepath.Join(src, name), dp); err != nil {
				return err
			}
			_ = os.Chtimes(dp, si.ModTime(), si.ModTime())
		}
	}
	// remove mods that are no longer in the pack
	if entries, err := os.ReadDir(dst); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".jar") && !wanted[strings.ToLower(e.Name())] {
				_ = os.Remove(filepath.Join(dst, e.Name()))
			}
		}
	}

	for _, extra := range []string{"config", "resourcepacks", "shaderpacks", "options.txt", "servers.dat"} {
		_ = copyIfMissing(filepath.Join(pPackSource(p), extra), filepath.Join(pInstance(p), extra))
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	os.Remove(dst)
	return os.Rename(tmp, dst)
}

func copyIfMissing(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return nil
	}
	if !st.IsDir() {
		if _, err := os.Stat(dst); err == nil {
			return nil
		}
		return copyFile(src, dst)
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		d := filepath.Join(dst, rel)
		if _, err := os.Stat(d); err == nil {
			return nil
		}
		return copyFile(p, d)
	})
}

// fetchPackDownloads gets the pack's external files (shaders…) from their official
// host into the game folder. Files already present and intact are skipped.
func fetchPackDownloads(p *Pack, status func(string), progress Progress) error {
	var tasks []dlTask
	for _, d := range p.Downloads {
		if !strings.HasPrefix(d.URL, "https://") || d.Path == "" || d.SHA1 == "" {
			continue
		}
		rel := filepath.FromSlash(d.Path)
		if filepath.IsAbs(rel) || strings.Contains(d.Path, "..") {
			continue
		}
		tasks = append(tasks, dlTask{URL: d.URL, Path: filepath.Join(pInstance(p), rel), SHA1: d.SHA1, Size: d.Size})
	}
	if len(tasks) == 0 {
		return nil
	}
	status("Vérification des shaders et packs de textures…")
	return downloadAll("Shaders et packs de textures", tasks, true, 4, progress)
}

// applyResourcePacks writes the pack's resource pack order into options.txt.
// It only does so when the published list changed since the last time, so a
// player who later rearranges their packs keeps their own choice.
func applyResourcePacks(p *Pack) error {
	if len(p.ResourcePacks) == 0 {
		return nil
	}
	order := make([]string, 0, len(p.ResourcePacks)) // options.txt: lowest priority first
	for i := len(p.ResourcePacks) - 1; i >= 0; i-- {
		order = append(order, p.ResourcePacks[i])
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(order); err != nil {
		return err
	}
	list := strings.TrimSpace(buf.String())
	sum := sha256hex([]byte(list))

	inst := pInstance(p)
	marker := filepath.Join(inst, ".launcher-resourcepacks")
	optPath := filepath.Join(inst, "options.txt")
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == sum {
		if _, err := os.Stat(optPath); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(inst, 0o755); err != nil {
		return err
	}
	var lines []string
	if b, err := os.ReadFile(optPath); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	}
	done := false
	for i, l := range lines {
		if strings.HasPrefix(l, "resourcePacks:") {
			lines[i] = "resourcePacks:" + list
			done = true
		}
	}
	if !done {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "resourcePacks:"+list)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.WriteFile(optPath, []byte(out), 0o644); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(sum), 0o644)
}

// applyConfigDefaults merges the pack's default settings into mods' JSON config
// files. Like resource packs, it runs again only when the published defaults change.
func applyConfigDefaults(p *Pack) error {
	if len(p.ConfigDefaults) == 0 {
		return nil
	}
	raw, _ := json.Marshal(p.ConfigDefaults)
	sum := sha256hex(raw)
	inst := pInstance(p)
	marker := filepath.Join(inst, ".launcher-config-defaults")
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == sum {
		return nil
	}
	for _, cd := range p.ConfigDefaults {
		if cd.File == "" || strings.Contains(cd.File, "..") || filepath.IsAbs(filepath.FromSlash(cd.File)) {
			continue
		}
		path := filepath.Join(inst, filepath.FromSlash(cd.File))
		cur := map[string]any{}
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			if err := json.Unmarshal(b, &cur); err != nil {
				continue // not a JSON object: leave the player's file alone
			}
		}
		mergeJSON(cur, cd.Set)
		out, err := json.MarshalIndent(cur, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(marker, []byte(sum), 0o644)
}

func mergeJSON(dst, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				mergeJSON(dv, sv)
				continue
			}
			cp := map[string]any{}
			mergeJSON(cp, sv)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}
