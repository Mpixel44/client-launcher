//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func hideConsole(cmd *exec.Cmd) {}

func openURL(url string) {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

func openFolder(path string) { openURL(path) }

func messageBox(title, text string) { fmt.Fprintln(os.Stderr, title+": "+text) }

func protect(data []byte) ([]byte, error)   { return data, nil }
func unprotect(data []byte) ([]byte, error) { return data, nil }

func copyToClipboard(text string) {}

func totalRAMMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		var kb int
		if _, err := fmt.Sscanf(string(b), "MemTotal: %d kB", &kb); err == nil {
			return kb / 1024
		}
	}
	return 8192
}

func systemProxy() string { return "" }
