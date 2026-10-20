package main

import (
	_ "embed"
	"encoding/base64"
	"strings"
)

//go:embed ui.html
var uiHTML string

//go:embed res/logo.png
var logoPNG []byte

// bindings lists the functions the page can call (window.<name>(...) → Promise).
func bindings(a *App) map[string]any {
	return map[string]any{
		"getState":       a.GetState,
		"listMods":       a.ListMods,
		"selectPack":     a.SelectPack,
		"setRam":         a.SetRam,
		"setSolo":        a.SetSolo,
		"setModEnabled":  a.SetModEnabled,
		"listExtras":     a.ListExtras,
		"setExtraOn":     a.SetExtraEnabled,
		"chooseFolder":   a.ChooseFolder,
		"setInstallDir":  a.SetInstallDir,
		"checkUpdates":   a.CheckUpdates,
		"launchGame":     a.LaunchGame,
		"openPackFolder": a.OpenPackFolder,
		"openDataFolder": a.OpenDataFolder,
		"startLogin":     a.StartLogin,
		"cancelLogin":    a.CancelLogin,
		"logout":         a.Logout,
		"play":           a.Play,
		"openGameFolder": a.OpenGameFolder,
		"openURL":        a.OpenURL,
		"copyText":       a.CopyText,
	}
}

func pageHTML() string {
	logo := "data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG)
	return strings.NewReplacer("{{TITLE}}", cfg.Name, "{{LOGO}}", logo).Replace(uiHTML)
}
