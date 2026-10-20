package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launcherVersion = "2.3"

// server is the address to join on start ("" = open the title screen, i.e. solo).
func buildCommand(p *Pack, inst *Installed, acc *Account, ramMB int, server string) (*exec.Cmd, error) {
	v := inst.Version
	game := pInstance(p)
	if err := os.MkdirAll(game, 0o755); err != nil {
		return nil, err
	}
	assetIndex := v.Assets
	if v.AssetIndex != nil {
		assetIndex = v.AssetIndex.ID
	}
	vars := map[string]string{
		"auth_player_name":    acc.Name,
		"version_name":        v.ID,
		"game_directory":      game,
		"assets_root":         pAssets(),
		"game_assets":         pAssets(),
		"assets_index_name":   assetIndex,
		"auth_uuid":           acc.UUID,
		"auth_access_token":   acc.MCToken,
		"auth_session":        acc.MCToken,
		"clientid":            "",
		"auth_xuid":           acc.XUID,
		"user_type":           "msa",
		"user_properties":     "{}",
		"version_type":        p.Name,
		"natives_directory":   inst.Natives,
		"launcher_name":       slug(cfg.Name) + "-launcher",
		"launcher_version":    launcherVersion,
		"classpath":           strings.Join(inst.Classpath, string(os.PathListSeparator)),
		"classpath_separator": string(os.PathListSeparator),
		"library_directory":   pLibraries(),
	}
	expand := func(s string) string {
		for {
			i := strings.Index(s, "${")
			if i < 0 {
				return s
			}
			j := strings.Index(s[i:], "}")
			if j < 0 {
				return s
			}
			key := s[i+2 : i+j]
			s = s[:i] + vars[key] + s[i+j+1:]
		}
	}
	collect := func(list []Argument) []string {
		var out []string
		for _, a := range list {
			if !rulesAllow(a.Rules) {
				continue
			}
			for _, val := range a.Value {
				out = append(out, expand(val))
			}
		}
		return out
	}

	minRam := 1024
	if ramMB < minRam {
		minRam = ramMB
	}
	args := []string{
		fmt.Sprintf("-Xmx%dM", ramMB),
		fmt.Sprintf("-Xms%dM", minRam),
		"-XX:+UnlockExperimentalVMOptions", "-XX:+UseG1GC", "-XX:G1NewSizePercent=20",
		"-XX:G1ReservePercent=20", "-XX:MaxGCPauseMillis=50", "-XX:G1HeapRegionSize=32M",
	}
	if v.Arguments != nil {
		args = append(args, collect(v.Arguments.JVM)...)
	} else { // very old versions
		args = append(args, "-Djava.library.path="+inst.Natives, "-cp", vars["classpath"])
	}
	if inst.LogConfig != "" && v.Logging != nil && v.Logging.Client != nil {
		args = append(args, strings.ReplaceAll(v.Logging.Client.Argument, "${path}", inst.LogConfig))
	}
	args = append(args, v.MainClass)

	var gameArgs []string
	if v.Arguments != nil {
		gameArgs = collect(v.Arguments.Game)
	} else {
		for _, s := range strings.Fields(v.MinecraftArguments) {
			gameArgs = append(gameArgs, expand(s))
		}
	}
	// drop "--flag" "" pairs (e.g. empty xuid/clientId)
	for i := 0; i < len(gameArgs); i++ {
		if strings.HasPrefix(gameArgs[i], "--") && i+1 < len(gameArgs) && gameArgs[i+1] == "" {
			i++
			continue
		}
		args = append(args, gameArgs[i])
	}
	if s := strings.TrimSpace(server); s != "" {
		args = append(args, "--quickPlayMultiplayer", s)
	}

	cmd := exec.Command(inst.Java, args...)
	cmd.Dir = game
	logDir := filepath.Join(game, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	if f, err := os.Create(filepath.Join(logDir, "launcher-output.log")); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	hideConsole(cmd)
	return cmd, nil
}

// lastLines returns the end of the game output, to explain a crash.
func lastLines(p *Pack, n int) string {
	b, err := os.ReadFile(filepath.Join(pInstance(p), "logs", "launcher-output.log"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
