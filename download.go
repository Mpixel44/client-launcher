package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

const userAgent = "NexusLauncher/2.0"

type dlTask struct {
	URL    string
	Path   string
	SHA1   string
	SHA256 string // used by launcher updates
	Size   int64
}

func httpGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d pour %s", resp.StatusCode, url)
	}
	return resp, nil
}

func getJSON(url string, v any) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		resp, err := httpGet(url)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(v)
			resp.Body.Close()
			if err == nil {
				return nil
			}
		}
		lastErr = err
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	return lastErr
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// needsDownload reports whether the file is missing or does not match.
// When verify is false only the size is checked (fast path for thousands of assets).
func needsDownload(t dlTask, verify bool) bool {
	st, err := os.Stat(t.Path)
	if err != nil || st.IsDir() {
		return true
	}
	if t.Size > 0 && st.Size() != t.Size {
		return true
	}
	if verify && t.SHA1 != "" {
		sum, err := fileSHA1(t.Path)
		if err != nil || sum != t.SHA1 {
			return true
		}
	}
	return false
}

func downloadFile(t dlTask) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		lastErr = downloadOnce(t)
		if lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("téléchargement impossible (%s) : %w", filepath.Base(t.Path), lastErr)
}

func downloadOnce(t dlTask) error {
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
		return err
	}
	resp, err := httpGet(t.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp := t.Path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var h hash.Hash = sha1.New()
	if t.SHA256 != "" {
		h = sha256.New()
	}
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if t.Size > 0 && n != t.Size {
		os.Remove(tmp)
		return fmt.Errorf("taille incorrecte (%d au lieu de %d)", n, t.Size)
	}
	if want := t.SHA1 + t.SHA256; want != "" && hex.EncodeToString(h.Sum(nil)) != want {
		os.Remove(tmp)
		return fmt.Errorf("fichier corrompu (sha1)")
	}
	os.Remove(t.Path)
	return os.Rename(tmp, t.Path)
}

// downloadAll fetches every task that is missing, with a pool of workers,
// and reports progress as (done, total).
func downloadAll(label string, tasks []dlTask, verify bool, workers int, progress func(label string, done, total int)) error {
	var todo []dlTask
	for _, t := range tasks {
		if needsDownload(t, verify) {
			todo = append(todo, t)
		}
	}
	return downloadMany(label, todo, workers, progress)
}

// downloadMany downloads every task (no "already there" check).
func downloadMany(label string, todo []dlTask, workers int, progress func(label string, done, total int)) error {
	total := len(todo)
	if total == 0 {
		return nil
	}
	progress(label, 0, total)

	var done int64
	var firstErr error
	var errMu sync.Mutex
	ch := make(chan dlTask)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				if err := downloadFile(t); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
				}
				d := atomic.AddInt64(&done, 1)
				progress(label, int(d), total)
			}
		}()
	}
	for _, t := range todo {
		errMu.Lock()
		stop := firstErr != nil
		errMu.Unlock()
		if stop {
			break
		}
		ch <- t
	}
	close(ch)
	wg.Wait()
	return firstErr
}
