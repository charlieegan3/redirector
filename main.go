package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	port := "8080"

	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	http.HandleFunc("/api/redirect", redirectHandler)

	staticFS := http.FS(staticFiles)
	fileServer := http.FileServer(staticFS)

	// Catch-all handler: serve index.html for SPA routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if _, err := staticFS.Open("static" + r.URL.Path); err != nil {
			// Fallback to index.html for unknown paths
			f, err := staticFS.Open("static/index.html")
			if err != nil {
				http.Error(w, "index.html not found", http.StatusInternalServerError)
				return
			}
			defer f.Close()
			w.Header().Set("Content-Type", "text/html")
			io.Copy(w, f)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	log.Println("Listening on", port)

	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%s", port), nil))
}

func redirectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}

	doh := r.URL.Query().Get("doh")
	rawURL := r.URL.Query().Get("url")
	if doh == "" || rawURL == "" {
		http.Error(w, "Missing 'doh' or 'url' parameters", http.StatusBadRequest)
		return
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		http.Error(w, "Invalid 'url' parameter", http.StatusBadRequest)
		return
	}

	// Resolve hostname to IP using DoH
	ip, err := resolveDoH(doh, parsedURL.Hostname())
	if err != nil {
		http.Error(w, fmt.Sprintf("DoH resolution failed: %v", err), http.StatusBadGateway)
		return
	}

	// Follow redirects
	redirectChain, err := followRedirects(parsedURL, ip)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to follow redirects: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(redirectChain)
}

func resolveDoH(dohURL string, hostname string) (string, error) {
	reqURL := fmt.Sprintf("%s?name=%s&type=A", dohURL, hostname)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/dns-json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Answer []struct {
			Data string `json:"data"`
		} `json:"Answer"`
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return "", err
	}

	for _, answer := range result.Answer {
		if ip := net.ParseIP(answer.Data); ip != nil {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no valid A record found")
}

type redirectStep struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

func followRedirects(originalURL *url.URL, ip string) ([]redirectStep, error) {
	const maxRedirects = 10
	var chain []redirectStep
	currentURL := *originalURL
	host := originalURL.Host

	for i := 0; i < maxRedirects; i++ {
		req, err := http.NewRequest("GET", currentURL.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/113.0.0.0 Safari/537.36")

		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				ServerName:         host,
				InsecureSkipVerify: false,
			},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip, portForScheme(currentURL.Scheme)))
			},
		}

		client := &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		chain = append(chain, redirectStep{
			URL:    currentURL.String(),
			Status: resp.StatusCode,
		})

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			break
		}

		loc := resp.Header.Get("Location")
		if loc == "" {
			break
		}

		newURL, err := url.Parse(loc)
		if err != nil {
			return nil, err
		}
		if !newURL.IsAbs() {
			newURL = currentURL.ResolveReference(newURL)
		}

		if newHost := newURL.Hostname(); newHost != host {
			ip, err = resolveDoH("https://dns.google/resolve", newHost)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve %s: %v", newHost, err)
			}
			host = newHost
		}

		currentURL = *newURL
	}

	return chain, nil
}

func portForScheme(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}
