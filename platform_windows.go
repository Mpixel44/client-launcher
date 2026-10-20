//go:build windows

package main

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const createNoWindow = 0x08000000

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func openURL(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func openFolder(path string) {
	_ = exec.Command("explorer", path).Start()
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

// protect encrypts data for the current Windows user (DPAPI), so the saved
// Microsoft session cannot be reused from another account or PC.
func protect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func copyToClipboard(text string) {
	cmd := exec.Command("clip")
	cmd.Stdin = strings.NewReader(text)
	hideConsole(cmd)
	_ = cmd.Run()
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func totalRAMMB() int {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 8192
	}
	return int(m.TotalPhys / (1024 * 1024))
}

// systemProxy reads the proxy set in Windows settings (Paramètres > Réseau > Proxy).
func systemProxy() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return ""
	}
	s, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return ""
	}
	// "host:port" or "http=host:port;https=host:port"
	if !strings.Contains(s, "=") {
		return strings.TrimSpace(s)
	}
	var fallback string
	for _, part := range strings.Split(s, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "https":
			return strings.TrimSpace(kv[1])
		case "http":
			fallback = strings.TrimSpace(kv[1])
		}
	}
	return fallback
}

// pickFolder shows the standard Windows folder picker (through PowerShell, which
// ships with Windows) and returns the chosen folder, or "" if cancelled.
func pickFolder(initial string) string {
	script := `[Console]::OutputEncoding=[Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$owner = New-Object System.Windows.Forms.Form -Property @{TopMost=$true}
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Où installer le jeu ?'
$d.ShowNewFolderButton = $true
$d.SelectedPath = '` + strings.ReplaceAll(initial, "'", "''") + `'
if ($d.ShowDialog($owner) -eq 'OK') { $d.SelectedPath }`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script)
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(string(out), "\ufeff"))
}
