package main

import (
	_ "embed"
	"strings"
)

//go:embed ui.html
var uiHTML string

// bindings lists the functions the page can call (window.<name>(...) → Promise).
func bindings(a *App) map[string]any {
	return map[string]any{
		"getState":       a.GetState,
		"listMods":       a.ListMods,
		"selectPack":     a.SelectPack,
		"setRam":         a.SetRam,
		"setSolo":        a.SetSolo,
		"checkUpdates":   a.CheckUpdates,
		"publish":        a.Publish,
		"openPublishDir": a.OpenPublishFolder,
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
	return strings.ReplaceAll(uiHTML, "{{TITLE}}", cfg.Name)
}
