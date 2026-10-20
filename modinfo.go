package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type ModInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Icon        string `json:"icon"` // data: URI or ""
	File        string `json:"file"`

	ID         string   `json:"id"`
	ClientOnly bool     `json:"clientOnly"` // "environment": "client" → can be turned off
	Requires   []string `json:"requires"`   // mod ids it depends on
	Provides   []string `json:"provides"`   // its id, "provides" and bundled (jar-in-jar) mods
}

var ctrlChars = regexp.MustCompile(`[\x00-\x1f]`)

func readModInfos(p *Pack) []ModInfo {
	if baseDir == "" {
		return nil
	}
	dir := pPackModsSrc(p)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []ModInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			continue
		}
		mi := ModInfo{File: e.Name(), Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))}
		readFabricMeta(filepath.Join(dir, e.Name()), &mi)
		out = append(out, mi)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func readFabricMeta(jar string, mi *ModInfo) {
	z, err := zip.OpenReader(jar)
	if err != nil {
		return
	}
	defer z.Close()
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	f, ok := files["fabric.mod.json"]
	if !ok {
		return
	}
	b, err := readZip(f, 1<<20)
	if err != nil {
		return
	}
	b = ctrlChars.ReplaceAll(b, []byte(" "))
	var meta struct {
		Name        string                     `json:"name"`
		Version     string                     `json:"version"`
		Description string                     `json:"description"`
		Icon        json.RawMessage            `json:"icon"`
		ID          string                     `json:"id"`
		Environment string                     `json:"environment"`
		Depends     map[string]json.RawMessage `json:"depends"`
		Provides    []string                   `json:"provides"`
		Jars        []struct {
			File string `json:"file"`
		} `json:"jars"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return
	}
	mi.ID = meta.ID
	mi.ClientOnly = meta.Environment == "client"
	builtin := map[string]bool{"minecraft": true, "java": true, "fabricloader": true, "fabric": true}
	for dep := range meta.Depends {
		if !builtin[dep] {
			mi.Requires = append(mi.Requires, dep)
		}
	}
	sort.Strings(mi.Requires)
	mi.Provides = append([]string{meta.ID}, meta.Provides...)
	for _, j := range meta.Jars { // bundled libraries count as provided by this jar
		if jf, ok := files[j.File]; ok {
			mi.Provides = append(mi.Provides, nestedModIDs(jf)...)
		}
	}
	if meta.Name != "" {
		mi.Name = meta.Name
	}
	mi.Version = meta.Version
	mi.Description = strings.TrimSpace(meta.Description)

	var icon string
	if json.Unmarshal(meta.Icon, &icon) != nil {
		var sizes map[string]string
		if json.Unmarshal(meta.Icon, &sizes) == nil {
			for _, v := range sizes { // any size will do, CSS scales it
				icon = v
				break
			}
		}
	}
	icon = strings.TrimPrefix(icon, "/")
	if icf, ok := files[icon]; ok && icf.UncompressedSize64 < 200<<10 {
		if data, err := readZip(icf, 200<<10); err == nil {
			mime := "image/png"
			if strings.EqualFold(path.Ext(icon), ".jpg") || strings.EqualFold(path.Ext(icon), ".jpeg") {
				mime = "image/jpeg"
			}
			mi.Icon = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
		}
	}
}

func readZip(f *zip.File, limit int64) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, limit))
}

// packIcon returns the pack's optional icon as a data: URI.
func packIcon(p *Pack) string {
	if p.Icon == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(pPackSource(p), filepath.FromSlash(p.Icon)))
	if err != nil || len(b) > 512<<10 {
		return ""
	}
	mime := "image/png"
	if e := strings.ToLower(filepath.Ext(p.Icon)); e == ".jpg" || e == ".jpeg" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func nestedModIDs(f *zip.File) []string {
	data, err := readZip(f, 64<<20)
	if err != nil {
		return nil
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	for _, e := range z.File {
		if e.Name != "fabric.mod.json" {
			continue
		}
		b, err := readZip(e, 1<<20)
		if err != nil {
			return nil
		}
		var m struct {
			ID       string   `json:"id"`
			Provides []string `json:"provides"`
		}
		if json.Unmarshal(ctrlChars.ReplaceAll(b, []byte(" ")), &m) != nil {
			return nil
		}
		return append([]string{m.ID}, m.Provides...)
	}
	return nil
}

// requiredBy returns the names of enabled mods that need this one, i.e. that depend
// on something only this jar provides (libraries bundled by several mods don't count).
func requiredBy(mods []ModInfo, target ModInfo, disabled map[string]bool) []string {
	provided := map[string]bool{}
	for _, id := range target.Provides {
		provided[id] = true
	}
	others := map[string]bool{} // ids provided by other enabled jars
	for _, m := range mods {
		if m.File == target.File || (m.ClientOnly && disabled[m.ID]) {
			continue
		}
		for _, id := range m.Provides {
			others[id] = true
		}
	}
	var out []string
	for _, m := range mods {
		if m.File == target.File || (m.ClientOnly && disabled[m.ID]) {
			continue
		}
		for _, r := range m.Requires {
			if provided[r] && !others[r] {
				out = append(out, m.Name)
				break
			}
		}
	}
	return out
}

// zipPackIcon returns the pack.png of a downloaded resource pack, if any.
func zipPackIcon(zipPath string) string {
	z, err := zip.OpenReader(zipPath)
	if err != nil {
		return ""
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == "pack.png" && f.UncompressedSize64 < 300<<10 {
			if data, err := readZip(f, 300<<10); err == nil {
				return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
			}
		}
	}
	return ""
}
