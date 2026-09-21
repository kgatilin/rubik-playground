// jev-playground serves a Rubik's cube page and proxies its decision requests
// to the Jev API, so the API token never reaches the browser.
package main

import (
	"bufio"
	_ "embed"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const jevURL = "https://api.typesafe.ai/v1/systemone"

//go:embed index.html
var indexHTML []byte

// loadToken reads JEV_API_TOKEN from the environment, falling back to .env.
func loadToken() string {
	if t := os.Getenv("JEV_API_TOKEN"); t != "" {
		return t
	}
	f, err := os.Open(".env")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && k == "JEV_API_TOKEN" {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func main() {
	addr := flag.String("http", "localhost:7810", "listen address")
	flag.Parse()

	token := loadToken()
	if token == "" {
		log.Fatal("JEV_API_TOKEN is not set (env or .env)")
	}
	client := &http.Client{Timeout: 30 * time.Second}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	http.HandleFunc("/api/decide", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, jevURL, http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	log.Printf("cube at http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
