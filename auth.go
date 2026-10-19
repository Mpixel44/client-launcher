package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Default: the public Microsoft client used by many community Minecraft tools for
// the "device code" sign-in. A launcher owner can put their own Azure app id
// in launcher.json ("microsoftClientId") once Mojang has approved it.
const defaultClientID = "00000000441cc96b"

type Account struct {
	Name         string    `json:"name"`
	UUID         string    `json:"uuid"`
	XUID         string    `json:"xuid"`
	MCToken      string    `json:"mcToken"`
	MCExpires    time.Time `json:"mcExpires"`
	RefreshToken string    `json:"refreshToken"`
	ClientID     string    `json:"clientId"`
}

var errNeedLogin = errors.New("session expirée, reconnecte-toi")

func clientID() string {
	if cfg.ClientID != "" {
		return cfg.ClientID
	}
	return defaultClientID
}

// Legacy "Live" ids look like 00000000xxxxxxxx; Azure ids are GUIDs.
func isLiveClient(id string) bool { return len(id) == 16 && strings.HasPrefix(id, "00000000") }

type msEndpoints struct{ device, token, scope, ticket string }

func endpoints(id string) msEndpoints {
	if isLiveClient(id) {
		return msEndpoints{
			device: "https://login.live.com/oauth20_connect.srf",
			token:  "https://login.live.com/oauth20_token.srf",
			scope:  "service::user.auth.xboxlive.com::MBI_SSL",
			ticket: "t=",
		}
	}
	return msEndpoints{
		device: "https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode",
		token:  "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
		scope:  "XboxLive.signin offline_access",
		ticket: "d=",
	}
}

// ---------- saved account ----------

func accountPath() string { return filepath.Join(dataDir, "account.dat") }

func loadAccount() *Account {
	b, err := os.ReadFile(accountPath())
	if err != nil {
		return nil
	}
	b, err = unprotect(b)
	if err != nil {
		return nil
	}
	var a Account
	if json.Unmarshal(b, &a) != nil || a.RefreshToken == "" {
		return nil
	}
	return &a
}

func saveAccount(a *Account) error {
	b, _ := json.Marshal(a)
	b, err := protect(b)
	if err != nil {
		return err
	}
	return os.WriteFile(accountPath(), b, 0o600)
}

func deleteAccount() { _ = os.Remove(accountPath()) }

// ---------- HTTP helpers ----------

func postForm(u string, form url.Values, out any) (int, error) {
	req, _ := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if len(b) > 0 {
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode, nil
}

func postJSON(u string, body any, bearer string, out any) (int, []byte, error) {
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", u, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode, b, nil
}

// ---------- Device code sign-in ----------

type DeviceCode struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type msToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func requestDeviceCode() (*DeviceCode, error) {
	id := clientID()
	ep := endpoints(id)
	form := url.Values{"client_id": {id}, "scope": {ep.scope}}
	if isLiveClient(id) {
		form.Set("response_type", "device_code")
	}
	var dc DeviceCode
	code, err := postForm(ep.device, form, &dc)
	if err != nil {
		return nil, humanize(err)
	}
	if code != 200 || dc.DeviceCode == "" {
		return nil, fmt.Errorf("Microsoft a refusé la demande de connexion (HTTP %d)", code)
	}
	if dc.VerificationURI == "" {
		dc.VerificationURI = "https://www.microsoft.com/link"
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	return &dc, nil
}

// waitDeviceLogin polls until the player has entered the code, then finishes the
// Xbox → Minecraft chain and returns a ready account.
func waitDeviceLogin(ctx context.Context, dc *DeviceCode) (*Account, error) {
	id := clientID()
	ep := endpoints(id)
	interval := time.Duration(dc.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if dc.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, errors.New("le code a expiré, recommence")
		}
		var tok msToken
		_, err := postForm(ep.token, url.Values{
			"client_id":   {id},
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &tok)
		if err != nil {
			continue // network hiccup, keep polling
		}
		switch tok.Error {
		case "":
			if tok.AccessToken == "" {
				continue
			}
			acc := &Account{RefreshToken: tok.RefreshToken, ClientID: id}
			if err := minecraftLogin(acc, tok.AccessToken); err != nil {
				return nil, err
			}
			return acc, saveAccount(acc)
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "expired_token":
			return nil, errors.New("le code a expiré, recommence")
		default:
			return nil, fmt.Errorf("connexion refusée (%s)", tok.Error)
		}
	}
}

// refresh gets a new Minecraft token when the current one is about to expire.
func refreshAccount(acc *Account) error {
	if acc.MCToken != "" && time.Until(acc.MCExpires) > 10*time.Minute {
		return nil
	}
	id := acc.ClientID
	if id == "" {
		id = clientID()
	}
	ep := endpoints(id)
	var tok msToken
	code, err := postForm(ep.token, url.Values{
		"client_id":     {id},
		"grant_type":    {"refresh_token"},
		"refresh_token": {acc.RefreshToken},
		"scope":         {ep.scope},
	}, &tok)
	if err != nil {
		return err // offline
	}
	if tok.Error == "invalid_grant" || (code >= 400 && code < 500) {
		return errNeedLogin
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("Microsoft ne répond pas correctement (HTTP %d)", code)
	}
	if tok.RefreshToken != "" {
		acc.RefreshToken = tok.RefreshToken
	}
	if err := minecraftLogin(acc, tok.AccessToken); err != nil {
		return err
	}
	return saveAccount(acc)
}

var xstsErrors = map[int64]string{
	2148916233: "Ce compte Microsoft n'a pas encore de profil Xbox. Connecte-toi une fois sur minecraft.net pour le créer, puis réessaie.",
	2148916235: "Xbox Live n'est pas disponible dans le pays de ce compte.",
	2148916236: "Ce compte doit passer la vérification d'âge sur xbox.com.",
	2148916237: "Ce compte doit passer la vérification d'âge sur xbox.com.",
	2148916238: "C'est un compte enfant : un adulte doit l'ajouter à une famille Microsoft pour autoriser le jeu.",
}

// minecraftLogin runs Microsoft token → Xbox Live → XSTS → Minecraft → profile.
func minecraftLogin(acc *Account, msAccess string) error {
	ep := endpoints(acc.ClientID)

	var xbl struct {
		Token         string `json:"Token"`
		DisplayClaims struct {
			Xui []struct {
				Uhs string `json:"uhs"`
			} `json:"xui"`
		} `json:"DisplayClaims"`
	}
	code, _, err := postJSON("https://user.auth.xboxlive.com/user/authenticate", map[string]any{
		"Properties": map[string]any{
			"AuthMethod": "RPS",
			"SiteName":   "user.auth.xboxlive.com",
			"RpsTicket":  ep.ticket + msAccess,
		},
		"RelyingParty": "http://auth.xboxlive.com",
		"TokenType":    "JWT",
	}, "", &xbl)
	if err != nil {
		return err
	}
	if code != 200 || xbl.Token == "" || len(xbl.DisplayClaims.Xui) == 0 {
		return fmt.Errorf("connexion Xbox Live refusée (HTTP %d)", code)
	}
	uhs := xbl.DisplayClaims.Xui[0].Uhs

	var xsts struct {
		Token string `json:"Token"`
		XErr  int64  `json:"XErr"`
	}
	code, _, err = postJSON("https://xsts.auth.xboxlive.com/xsts/authorize", map[string]any{
		"Properties": map[string]any{
			"SandboxId":  "RETAIL",
			"UserTokens": []string{xbl.Token},
		},
		"RelyingParty": "rp://api.minecraftservices.com/",
		"TokenType":    "JWT",
	}, "", &xsts)
	if err != nil {
		return err
	}
	if code != 200 || xsts.Token == "" {
		if msg, ok := xstsErrors[xsts.XErr]; ok {
			return errors.New(msg)
		}
		return fmt.Errorf("autorisation Xbox refusée (HTTP %d, code %d)", code, xsts.XErr)
	}

	var mc struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	code, body, err := postJSON("https://api.minecraftservices.com/authentication/login_with_xbox", map[string]any{
		"identityToken": "XBL3.0 x=" + uhs + ";" + xsts.Token,
	}, "", &mc)
	if err != nil {
		return err
	}
	if code != 200 || mc.AccessToken == "" {
		if code == 403 || strings.Contains(string(body), "Invalid app registration") {
			return errors.New("Mojang refuse cet identifiant d'application Microsoft (microsoftClientId). Laisse le champ vide dans launcher.json ou fais valider ton appli Azure.")
		}
		return fmt.Errorf("connexion Minecraft refusée (HTTP %d)", code)
	}

	req, _ := http.NewRequest("GET", "https://api.minecraftservices.com/minecraft/profile", nil)
	req.Header.Set("Authorization", "Bearer "+mc.AccessToken)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return errors.New("Ce compte Microsoft ne possède pas Minecraft Java Edition.")
	}
	var prof struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&prof); err != nil || prof.ID == "" {
		return fmt.Errorf("profil Minecraft illisible (HTTP %d)", resp.StatusCode)
	}

	acc.Name = prof.Name
	acc.UUID = prof.ID
	acc.MCToken = mc.AccessToken
	if mc.ExpiresIn <= 0 {
		mc.ExpiresIn = 86400
	}
	acc.MCExpires = time.Now().Add(time.Duration(mc.ExpiresIn) * time.Second)
	acc.XUID = jwtClaim(mc.AccessToken, "xuid")
	return nil
}

func jwtClaim(token, key string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}
