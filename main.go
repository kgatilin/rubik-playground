// jev-playground drives a Rubik's cube with Jev decisions: `serve` is the web
// page, `run` is the same loop from the terminal, `show` prints a run log.
// Every decision goes through Jev.Decide and lands in runs/<run>.jsonl.
package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

//go:embed index.html
var indexHTML []byte

// loadToken reads JEV_API_TOKEN from the environment, falling back to .env.
func loadToken() (string, error) {
	if t := os.Getenv("JEV_API_TOKEN"); t != "" {
		return t, nil
	}
	if f, err := os.Open(".env"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
			if ok && k == "JEV_API_TOKEN" {
				return strings.Trim(v, `"'`), nil
			}
		}
	}
	return "", fmt.Errorf("JEV_API_TOKEN is not set (env or .env)")
}

func newJev() (*Jev, error) {
	token, err := loadToken()
	if err != nil {
		return nil, err
	}
	return &Jev{Token: token, Client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func randomScramble(n int) []string {
	var seq []string
	for len(seq) < n {
		m := string("UDLRFB"[rand.IntN(6)]) + []string{"", "'", "2"}[rand.IntN(3)]
		if len(seq) == 0 || seq[len(seq)-1][0] != m[0] {
			seq = append(seq, m)
		}
	}
	return seq
}

func serveCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the cube page",
		RunE: func(cmd *cobra.Command, args []string) error {
			jev, err := newJev()
			if err != nil {
				return err
			}
			http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(indexHTML)
			})
			http.HandleFunc("/api/step", func(w http.ResponseWriter, r *http.Request) {
				var req StepRequest
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				rec, err := jev.Decide(req)
				if err != nil {
					log.Printf("step: %v", err)
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(rec)
			})
			log.Printf("cube at http://%s, run logs in %s/", addr, runsDir)
			return http.ListenAndServe(addr, nil)
		},
	}
	cmd.Flags().StringVar(&addr, "http", "localhost:7810", "listen address")
	return cmd
}

func printStep(r *StepRecord) {
	type kv struct {
		m string
		p float64
	}
	var top []kv
	for m, p := range r.Probabilities {
		top = append(top, kv{m, p})
	}
	sort.Slice(top, func(a, b int) bool { return top[a].p > top[b].p || top[a].p == top[b].p && top[a].m < top[b].m })
	var parts []string
	for _, t := range top[:min(4, len(top))] {
		parts = append(parts, fmt.Sprintf("%s %.2f", t.m, t.p))
	}
	fmt.Printf("#%-3d %-3s conf %.2f  matched %2d→%2d  %4dms  first offered %-3s top: %s\n",
		r.Step, r.Move, r.Confidence, r.MatchedBefore, r.MatchedAfter, r.Millis, r.Offered[0], strings.Join(parts, ", "))
}

func runCmd() *cobra.Command {
	var (
		req         StepRequest
		scramble    string
		scrambleLen int
		moves       string
		maxMoves    int
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Scramble a cube and let Jev play up to --max moves",
		RunE: func(cmd *cobra.Command, args []string) error {
			jev, err := newJev()
			if err != nil {
				return err
			}
			req.Scramble = strings.Fields(scramble)
			if scramble == "" {
				req.Scramble = randomScramble(scrambleLen)
			}
			req.Options = strings.Fields(moves)
			req.Run = newRunID("cli")
			fmt.Printf("run %s  scramble: %s\n", req.Run, strings.Join(req.Scramble, " "))
			for range maxMoves {
				rec, err := jev.Decide(req)
				if err != nil {
					return err
				}
				printStep(rec)
				req.History = append(req.History, rec.Move)
				if rec.Solved {
					fmt.Println("solved")
					break
				}
			}
			fmt.Printf("moves: %s\nlog: %s\n", strings.Join(req.History, " "), filepath.Join(runsDir, req.Run+".jsonl"))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&scramble, "scramble", "", "scramble moves, e.g. \"R U F'\" (default: random)")
	f.IntVar(&scrambleLen, "scramble-len", 3, "length of the random scramble")
	f.IntVar(&maxMoves, "max", 10, "move limit")
	f.StringVar(&moves, "moves", strings.Join(allMoves, " "), "moves offered per step")
	f.StringVar(&req.Instructions, "instructions", defaultInstructions, "choice question instructions")
	f.BoolVar(&req.Sample, "sample", false, "draw the move from the probabilities")
	f.BoolVar(&req.NoUndo, "no-undo", true, "do not offer the inverse of the previous move")
	f.BoolVar(&req.Shuffle, "shuffle", false, "randomise the order of offered moves")
	f.BoolVar(&req.HideHistory, "hide-history", false, "leave the move history out of the state text")
	f.BoolVar(&req.Lookahead, "lookahead", false, "describe each move by the sticker count it leads to")
	return cmd
}

func showCmd() *cobra.Command {
	var state bool
	cmd := &cobra.Command{
		Use:   "show [run]",
		Short: "Print a run log (default: the latest); without a run and with --list, list runs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, _ := filepath.Glob(filepath.Join(runsDir, "*.jsonl"))
			if len(files) == 0 {
				return fmt.Errorf("no runs in %s/", runsDir)
			}
			sort.Slice(files, func(a, b int) bool {
				ia, _ := os.Stat(files[a])
				ib, _ := os.Stat(files[b])
				return ia.ModTime().Before(ib.ModTime())
			})
			if list, _ := cmd.Flags().GetBool("list"); list {
				for _, f := range files {
					fmt.Println(strings.TrimSuffix(filepath.Base(f), ".jsonl"))
				}
				return nil
			}
			path := files[len(files)-1]
			if len(args) == 1 {
				path = filepath.Join(runsDir, filepath.Base(args[0])+".jsonl")
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(nil, 1<<20)
			for first := true; sc.Scan(); first = false {
				var r StepRecord
				if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
					return err
				}
				if first {
					fmt.Printf("run %s  scramble: %s  sample=%v no_undo=%v shuffle=%v hide_history=%v lookahead=%v\ninstructions: %s\n",
						r.Request.Run, strings.Join(r.Request.Scramble, " "), r.Request.Sample, r.Request.NoUndo, r.Request.Shuffle, r.Request.HideHistory, r.Request.Lookahead, r.Request.Instructions)
				}
				printStep(&r)
				if state {
					fmt.Println(r.State)
				}
			}
			return sc.Err()
		},
	}
	cmd.Flags().Bool("list", false, "list runs, oldest first")
	cmd.Flags().BoolVar(&state, "state", false, "also print the state text sent on each step")
	return cmd
}

func main() {
	root := &cobra.Command{Use: "jev-playground", SilenceUsage: true}
	root.AddCommand(serveCmd(), runCmd(), showCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
