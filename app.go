package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// App holds the launcher state shared with the UI. The UI talks to it through
// bound functions; long work runs in goroutines and reports back via emit().
type App struct {
	emit func(event string, data any)

	mu          sync.Mutex
	account     *Account
	settings    Settings
	busyPack    string // pack being prepared / played ("" = idle)
	updating    bool
	updMu       sync.Mutex // one update (or publish) at a time
	updMsg      string
	loginCancel context.CancelFunc
	mods        map[string][]ModInfo
}

func NewApp(emit func(string, any)) *App {
	return &App{emit: emit, account: loadAccount(), settings: loadSettings(), mods: map[string][]ModInfo{}}
}

type AccountView struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type GameView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Short      string `json:"short"`
	Type       string `json:"type"`
	ComingSoon bool   `json:"comingSoon"`
}

type PackView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Minecraft   string   `json:"minecraft"`
	Loader      string   `json:"loader"`
	ModCount    int      `json:"modCount"`
	Server      string   `json:"server"`
	Solo        bool     `json:"solo"`
	Icon        string   `json:"icon"`
	Shaders     []string `json:"shaders"`
	Textures    []string `json:"textures"` // resource packs
}

type StateView struct {
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Account  *AccountView `json:"account"`
	RamMB    int          `json:"ramMB"`
	MaxRamMB int          `json:"maxRamMB"`
	BusyPack string       `json:"busyPack"`
	Updating bool         `json:"updating"`
	Update   UpdateView   `json:"update"`
	LastPack string       `json:"lastPack"`
	Games    []GameView   `json:"games"`
	Packs    []PackView   `json:"packs"`
}

func (a *App) GetState() StateView {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := StateView{
		Name: cfg.Name, Version: launcherVersion,
		RamMB: a.settings.RamMB, MaxRamMB: totalRAMMB(),
		BusyPack: a.busyPack, LastPack: a.settings.LastPack, Updating: a.updating,
	}
	us := loadUpdateState()
	s.Update = UpdateView{Enabled: updatesEnabled(), Admin: isAdmin(), Version: us.Version,
		Published: us.Published, Checked: us.Checked, Message: a.updMsg, URL: strings.TrimSpace(cfg.UpdateURL)}
	if a.account != nil {
		s.Account = &AccountView{Name: a.account.Name, UUID: a.account.UUID}
	}
	for _, g := range cfg.Games {
		s.Games = append(s.Games, GameView{ID: g.ID, Name: g.Name, Short: g.Short, Type: g.Type, ComingSoon: g.ComingSoon})
	}
	for i := range cfg.Packs {
		p := &cfg.Packs[i]
		loader := "Fabric"
		if p.Loader == "vanilla" {
			loader = "Vanilla"
		}
		s.Packs = append(s.Packs, PackView{ID: p.ID, Name: p.Name, Description: p.Description,
			Minecraft: p.Minecraft, Loader: loader, ModCount: countMods(p), Server: p.Server, Icon: packIcon(p), Shaders: extraNames(p, "shaderpacks/"), Textures: extraNames(p, "resourcepacks/"),
			Solo: a.settings.Solo[p.ID]})
	}
	return s
}

func (a *App) modInfos(packID string) []ModInfo {
	p := findPack(packID)
	if p == nil {
		return nil
	}
	a.mu.Lock()
	m, ok := a.mods[packID]
	a.mu.Unlock()
	if ok {
		return m
	}
	m = readModInfos(p)
	a.mu.Lock()
	a.mods[packID] = m
	a.mu.Unlock()
	return m
}

type ModView struct {
	ModInfo
	Enabled    bool     `json:"enabled"`
	RequiredBy []string `json:"requiredBy"` // enabled mods that need this one (toggle locked)
}

func (a *App) disabledSet(packID string) map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]bool{}
	for _, id := range a.settings.Disabled[packID] {
		out[id] = true
	}
	return out
}

func (a *App) ListMods(packID string) []ModView {
	mods := a.modInfos(packID)
	off := a.disabledSet(packID)
	out := make([]ModView, 0, len(mods))
	for _, m := range mods {
		v := ModView{ModInfo: m, Enabled: !(m.ClientOnly && off[m.ID])}
		if m.ClientOnly {
			v.RequiredBy = requiredBy(mods, m, off)
			if len(v.RequiredBy) > 0 {
				v.Enabled = true // can't be off while something needs it
			}
		}
		out = append(out, v)
	}
	return out
}

// SetModEnabled is the toggle on a client-only mod.
func (a *App) SetModEnabled(packID, modID string, enabled bool) error {
	mods := a.modInfos(packID)
	var target *ModInfo
	for i := range mods {
		if mods[i].ID == modID {
			target = &mods[i]
		}
	}
	if target == nil || !target.ClientOnly {
		return errors.New("ce mod est nécessaire pour jouer sur le serveur, il ne peut pas être désactivé")
	}
	if !enabled {
		if by := requiredBy(mods, *target, a.disabledSet(packID)); len(by) > 0 {
			return fmt.Errorf("%s est utilisé par %s : désactive d'abord ce mod", target.Name, strings.Join(by, ", "))
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settings.Disabled == nil {
		a.settings.Disabled = map[string][]string{}
	}
	var list []string
	for _, id := range a.settings.Disabled[packID] {
		if id != modID {
			list = append(list, id)
		}
	}
	if !enabled {
		list = append(list, modID)
	}
	if len(list) == 0 {
		delete(a.settings.Disabled, packID)
	} else {
		a.settings.Disabled[packID] = list
	}
	return saveSettings(a.settings)
}

// skippedFiles lists the jars of the player's disabled client-only mods
// (never one that another enabled mod still needs).
func (a *App) skippedFiles(p *Pack) map[string]bool {
	mods := readModInfos(p)
	off := a.disabledSet(p.ID)
	skip := map[string]bool{}
	for _, m := range mods {
		if m.ClientOnly && off[m.ID] && len(requiredBy(mods, m, off)) == 0 {
			skip[m.File] = true
		}
	}
	return skip
}

func (a *App) SelectPack(packID string) {
	if findPack(packID) == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.LastPack = packID
	_ = saveSettings(a.settings)
}

// SetSolo switches a pack between joining its server and solo (title screen).
func (a *App) SetSolo(packID string, solo bool) {
	if findPack(packID) == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settings.Solo == nil {
		a.settings.Solo = map[string]bool{}
	}
	if solo {
		a.settings.Solo[packID] = true
	} else {
		delete(a.settings.Solo, packID)
	}
	_ = saveSettings(a.settings)
}

func (a *App) SetRam(mb int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if mb < 1024 {
		mb = 1024
	}
	a.settings.RamMB = mb
	_ = saveSettings(a.settings)
}

// LaunchGame starts a non-Minecraft game of the game bar.
func (a *App) LaunchGame(gameID string) error {
	g := findGame(gameID)
	if g == nil {
		return errors.New("jeu inconnu")
	}
	if g.ComingSoon {
		return fmt.Errorf("%s arrive bientôt", g.Name)
	}
	switch g.Type {
	case "steam":
		if g.SteamAppID <= 0 {
			return errors.New("steamAppId manquant dans launcher.json")
		}
		openURL(fmt.Sprintf("steam://rungameid/%d", g.SteamAppID))
		return nil
	case "exe":
		if g.Path == "" {
			return errors.New("chemin du jeu manquant dans launcher.json")
		}
		cmd := exec.Command(g.Path)
		cmd.Dir = filepath.Dir(g.Path)
		return cmd.Start()
	}
	return errors.New("ce jeu se lance depuis sa liste de packs")
}

// ---------- account ----------

type LoginStart struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

func (a *App) StartLogin() (LoginStart, error) {
	dc, err := requestDeviceCode()
	if err != nil {
		return LoginStart{}, humanize(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.loginCancel != nil {
		a.loginCancel()
	}
	a.loginCancel = cancel
	a.mu.Unlock()

	copyToClipboard(dc.UserCode)
	go func() {
		acc, err := waitDeviceLogin(ctx, dc)
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			a.emit("loginError", humanize(err).Error())
			return
		}
		a.mu.Lock()
		a.account = acc
		a.mu.Unlock()
		a.emit("loginDone", AccountView{Name: acc.Name, UUID: acc.UUID})
	}()
	return LoginStart{Code: dc.UserCode, URL: dc.VerificationURI}, nil
}

func (a *App) CancelLogin() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.loginCancel != nil {
		a.loginCancel()
		a.loginCancel = nil
	}
}

func (a *App) Logout() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.account = nil
	deleteAccount()
}

// ---------- play ----------

func (a *App) Play(packID string) error {
	p := findPack(packID)
	if p == nil {
		return errors.New("pack introuvable")
	}
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return errors.New("mise à jour en cours, patiente quelques secondes")
	}
	if a.busyPack != "" {
		a.mu.Unlock()
		return errors.New("un pack est déjà en cours de lancement")
	}
	if a.account == nil {
		a.mu.Unlock()
		return errors.New("connecte-toi d'abord")
	}
	a.busyPack = p.ID
	acc := *a.account
	ram := a.settings.RamMB
	server := p.Server
	if a.settings.Solo[p.ID] {
		server = ""
	}
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.busyPack = ""
			a.mu.Unlock()
			a.emit("idle", p.ID)
		}()
		if err := a.run(p, &acc, ram, server); err != nil {
			a.emit("error", humanize(err).Error())
		}
	}()
	return nil
}

func (a *App) run(p *Pack, acc *Account, ram int, server string) error {
	status := func(s string) { a.emit("status", s) }
	var lastEmit time.Time
	var pmu sync.Mutex
	progress := func(label string, done, total int) {
		pmu.Lock()
		defer pmu.Unlock()
		if done != total && time.Since(lastEmit) < 80*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		a.emit("progress", map[string]any{"label": label, "done": done, "total": total})
	}

	status("Vérification du compte…")
	if err := refreshAccount(acc); err != nil {
		if errors.Is(err, errNeedLogin) {
			a.mu.Lock()
			a.account = nil
			a.mu.Unlock()
			deleteAccount()
			a.emit("loggedOut", nil)
			return errors.New("ta session Microsoft a expiré, reconnecte-toi")
		}
		if acc.MCToken == "" {
			return err
		}
		status("Hors ligne : le multijoueur peut ne pas fonctionner")
	} else {
		a.mu.Lock()
		a.account = acc
		a.mu.Unlock()
	}

	// make sure the pack matches what was published before playing
	if updatesEnabled() && !isAdmin() {
		a.updMu.Lock()
		status("Recherche de mises à jour…")
		if m, err := fetchManifest(); err == nil {
			res, err := applyUpdate(m, false, status, progress)
			if err != nil {
				a.updMu.Unlock()
				return err
			}
			if res.ConfigChanged {
				a.reloadConfig()
			}
		} else {
			status("Mise à jour impossible (" + humanize(err).Error() + "), on continue avec les fichiers actuels")
		}
		a.updMu.Unlock()
		if np := findPack(p.ID); np != nil {
			p = np
		} else {
			return errors.New("ce pack a été retiré par la mise à jour")
		}
	}

	inst, err := install(p, status, progress)
	if err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.mods, p.ID) // the update may have changed the mods
	a.mu.Unlock()
	if err := syncMods(p, a.skippedFiles(p), status); err != nil {
		return fmt.Errorf("copie des mods impossible : %w", err)
	}
	if err := fetchPackDownloads(p, status, progress); err != nil {
		return fmt.Errorf("téléchargement des shaders / packs de textures impossible : %w", err)
	}
	if err := applyResourcePacks(p); err != nil {
		return fmt.Errorf("activation des packs de textures impossible : %w", err)
	}
	if err := applyConfigDefaults(p); err != nil {
		return fmt.Errorf("réglages par défaut des mods impossibles : %w", err)
	}

	if server != "" {
		status("Lancement de " + p.Name + " et connexion à " + server + "…")
	} else {
		status("Lancement de " + p.Name + " en solo…")
	}
	cmd, err := buildCommand(p, inst, acc, ram, server)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("impossible de démarrer Java : %w", err)
	}
	a.emit("playing", p.ID)
	start := time.Now()
	err = cmd.Wait()
	if f, ok := cmd.Stdout.(*os.File); ok {
		f.Close()
	}
	if err != nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() != 0 {
		tail := lastLines(p, 25)
		msg := "Minecraft s'est fermé avec une erreur."
		if time.Since(start) < 2*time.Minute {
			msg = "Minecraft a planté au démarrage. Souvent un mod incompatible : regarde le dossier crash-reports."
		}
		a.emit("crash", map[string]string{"message": msg, "log": tail, "pack": p.ID})
	}
	return nil
}

func (a *App) OpenGameFolder(packID string) {
	if p := findPack(packID); p != nil {
		_ = os.MkdirAll(pInstance(p), 0o755)
		openFolder(pInstance(p))
	}
}

func (a *App) OpenPackFolder(packID string) {
	if p := findPack(packID); p != nil {
		_ = os.MkdirAll(pPackModsSrc(p), 0o755)
		openFolder(pPackSource(p))
	}
}

func (a *App) OpenDataFolder() { openFolder(dataDir) }

func (a *App) OpenURL(u string) {
	if strings.HasPrefix(u, "https://") {
		openURL(u)
	}
}

func (a *App) CopyText(s string) { copyToClipboard(s) }

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------- updates ----------

type UpdateView struct {
	Enabled   bool   `json:"enabled"`
	Admin     bool   `json:"admin"`
	Version   int    `json:"version"`
	Published string `json:"published"`
	Checked   string `json:"checked"`
	Message   string `json:"message"`
	URL       string `json:"url"`
}

// CheckUpdates is the "Vérifier" button.
func (a *App) CheckUpdates() error {
	if !updatesEnabled() {
		return errUpdatesOff
	}
	if isAdmin() {
		return errors.New("ce PC publie les mises à jour, il ne les télécharge pas")
	}
	go a.doUpdate(true)
	return nil
}

// StartupUpdate runs once, shortly after the window opens.
func (a *App) StartupUpdate() {
	go cleanupOldExe()
	if updatesEnabled() && !isAdmin() {
		time.Sleep(600 * time.Millisecond)
		a.doUpdate(true)
	}
}

func (a *App) doUpdate(allowSelf bool) {
	a.mu.Lock()
	if a.busyPack != "" || a.updating {
		a.mu.Unlock()
		return
	}
	a.updating = true
	a.mu.Unlock()
	a.updMu.Lock()
	a.emit("updating", true)

	status := func(s string) { a.emit("status", s) }
	var lastEmit time.Time
	progress := func(label string, done, total int) {
		if done != total && time.Since(lastEmit) < 80*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		a.emit("progress", map[string]any{"label": label, "done": done, "total": total})
	}

	msg := ""
	reload := false
	status("Recherche de mises à jour…")
	m, err := fetchManifest()
	if err == nil {
		var res *UpdateResult
		res, err = applyUpdate(m, allowSelf, status, progress)
		if err == nil {
			if res.Restart {
				a.updMu.Unlock()
				a.emit("restarting", nil)
				time.Sleep(900 * time.Millisecond)
				restartSelf() // only returns if the new launcher could not be started
				a.mu.Lock()
				a.updating = false
				a.mu.Unlock()
				a.emit("updated", map[string]any{"message": "Nouvelle version installée : relance le launcher.", "ok": true})
				return
			}
			if res.ConfigChanged {
				a.reloadConfig()
			}
			reload = res.ConfigChanged || res.Downloaded+res.Removed > 0
			switch {
			case res.Downloaded+res.Removed > 0 || res.ConfigChanged:
				msg = fmt.Sprintf("Mis à jour (version %d) : %d fichier(s) téléchargé(s), %d retiré(s).", m.Version, res.Downloaded, res.Removed)
			default:
				msg = fmt.Sprintf("À jour (version %d).", m.Version)
			}
		}
	}
	if err != nil {
		msg = "Mise à jour impossible : " + humanize(err).Error()
	}
	a.updMu.Unlock()
	a.mu.Lock()
	a.updating = false
	a.updMsg = msg
	if reload {
		a.mods = map[string][]ModInfo{}
	}
	a.mu.Unlock()
	a.emit("updated", map[string]any{"message": msg, "ok": err == nil, "reload": reload})
}

// reloadConfig re-reads launcher.json after an update.
func (a *App) reloadConfig() {
	old := cfg
	if err := loadConfig(); err != nil {
		cfg = old
		return
	}
	a.mu.Lock()
	if findPack(a.settings.LastPack) == nil && len(cfg.Packs) > 0 {
		a.settings.LastPack = cfg.Packs[0].ID
	}
	a.mu.Unlock()
}

// Publish is the admin "Publier une mise à jour" button.
func (a *App) Publish() error {
	if !isAdmin() {
		return errors.New("clé de publication absente")
	}
	a.mu.Lock()
	if a.updating || a.busyPack != "" {
		a.mu.Unlock()
		return errors.New("attends la fin du lancement en cours")
	}
	a.updating = true
	a.mu.Unlock()
	a.emit("updating", true)
	go func() {
		a.updMu.Lock()
		res, err := publish(func(s string) { a.emit("status", s) }, func(l string, d, t int) {
			if d%5 == 0 || d == t {
				a.emit("progress", map[string]any{"label": l, "done": d, "total": t})
			}
		})
		a.updMu.Unlock()
		a.mu.Lock()
		a.updating = false
		a.mu.Unlock()
		if err != nil {
			a.emit("updated", map[string]any{"message": "Publication impossible : " + err.Error(), "ok": false})
			return
		}
		a.emit("published", res)
	}()
	return nil
}

func (a *App) OpenPublishFolder() {
	_ = os.MkdirAll(pOnline(), 0o755)
	openFolder(pOnline())
}

func extraNames(p *Pack, prefix string) []string {
	var out []string
	for _, d := range p.Downloads {
		if !strings.HasPrefix(d.Path, prefix) {
			continue
		}
		n := d.Name
		if n == "" {
			n = filepath.Base(d.Path)
		}
		out = append(out, n)
	}
	return out
}
