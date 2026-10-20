package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// countMods returns the number of .jar files shipped next to the launcher.
func countMods(p *Pack) int {
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
func syncMods(p *Pack, status func(string)) error {
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
	status("Vérification des shaders et fichiers externes…")
	return downloadAll("Shaders", tasks, true, 4, progress)
}
