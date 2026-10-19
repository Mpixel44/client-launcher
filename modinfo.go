package main

import (
	"archive/zip"
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
}

var ctrlChars = regexp.MustCompile(`[\x00-\x1f]`)

func readModInfos(p *Pack) []ModInfo {
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
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Icon        json.RawMessage `json:"icon"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return
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
