//go:build windows

package main

import (
	"os"
	"runtime"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// dpiScale returns the display scaling (1.0 = 100 %). The manifest declares the
// app DPI-aware so text stays sharp; window sizes are therefore in real pixels.
func dpiScale() float64 {
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("GetDpiForSystem")
	if proc.Find() != nil {
		return 1
	}
	dpi, _, _ := proc.Call()
	if dpi < 96 {
		return 1
	}
	return float64(dpi) / 96
}

func main() {
	runtime.LockOSThread()
	for _, a := range os.Args[1:] {
		if a == "--after-update" { // let the previous launcher close first
			time.Sleep(1500 * time.Millisecond)
		}
	}
	if err := loadConfig(); err != nil {
		messageBox("Launcher", "launcher.json est invalide :\n"+err.Error())
		return
	}

	scale := dpiScale()
	width, height := uint(1040*scale), uint(660*scale)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  pWebview(),
		WindowOptions: webview2.WindowOptions{
			Title:  cfg.Name,
			Width:  width,
			Height: height,
			IconId: 2, // group icon embedded by rsrc (manifest is 1)
			Center: true,
		},
	})
	if w == nil {
		messageBox(cfg.Name, "Le composant Microsoft Edge WebView2 est introuvable.\n\n"+
			"Installe-le gratuitement depuis :\nhttps://go.microsoft.com/fwlink/p/?LinkId=2124703\npuis relance le launcher.")
		openURL("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
		return
	}
	defer w.Destroy()
	w.SetSize(int(860*scale), int(560*scale), webview2.HintMin)

	app := NewApp(func(event string, data any) {
		js := "window.__onEvent && window.__onEvent(" + jsonString(event) + "," + jsonString(data) + ")"
		w.Dispatch(func() { w.Eval(js) })
	})
	for name, fn := range bindings(app) {
		_ = w.Bind(name, fn)
	}
	w.SetHtml(pageHTML())
	go app.StartupUpdate()
	w.Run()
}
