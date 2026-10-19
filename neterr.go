package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"syscall"
)

// proxyFunc uses HTTPS_PROXY/HTTP_PROXY when set, otherwise the Windows system proxy.
func proxyFunc(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
		return u, err
	}
	if p := systemProxy(); p != "" {
		if !strings.Contains(p, "://") {
			p = "http://" + p
		}
		return url.Parse(p)
	}
	return nil, nil
}

func init() {
	httpClient.Transport = &http.Transport{
		Proxy:               proxyFunc,
		MaxIdleConnsPerHost: 16,
		ForceAttemptHTTP2:   true,
	}
}

// humanize turns low-level network errors into something a player can act on.
func humanize(err error) error {
	if err == nil {
		return nil
	}
	var errno syscall.Errno
	msg := err.Error()
	if (errors.As(err, &errno) && errno == 10013) || strings.Contains(msg, "forbidden by its access permissions") {
		return errors.New("Windows bloque l'accès à Internet du launcher (erreur 10013). C'est un antivirus, un pare-feu ou un VPN qui bloque Launcher.exe : ajoute-le aux programmes autorisés de ton antivirus / pare-feu, puis réessaie.")
	}
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "dial tcp") && strings.Contains(msg, "timeout") {
		return errors.New("Impossible de joindre les serveurs (pas d'internet ?). Vérifie ta connexion puis réessaie.")
	}
	return err
}
