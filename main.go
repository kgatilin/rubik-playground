// jev-playground drives a Rubik's cube with Jev decisions: `serve` is the web
// page, `run` is the same loop from the terminal, `show` prints a run log.
// Every decision goes through Jev.Decide and lands in runs/<run>.jsonl.
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
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
			mux := http.NewServeMux()
			newSession(jev).routes(mux)
			log.Printf("cube at http://%s, run logs in %s/", addr, runsDir)
			return http.ListenAndServe(addr, mux)
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
		ui          string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Scramble a cube and let Jev play up to --max moves",
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Options = strings.Fields(moves)
			if ui != "" {
				return runOnServer(ui, req, strings.Fields(scramble), maxMoves)
			}
			jev, err := newJev()
			if err != nil {
				return err
			}
			req.Scramble = strings.Fields(scramble)
			if scramble == "" {
				req.Scramble = randomScramble(scrambleLen)
			}
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
	f.StringVar(&ui, "ui", "", "play on the cube of a running `serve` so the page shows it, e.g. localhost:7810; without --scramble the current cube is kept")
	f.Lookup("ui").NoOptDefVal = "localhost:7810"
	f.StringVar(&scramble, "scramble", "", "scramble moves, e.g. \"R U F'\" (default: random)")
	f.IntVar(&scrambleLen, "scramble-len", 3, "length of the random scramble")
	f.IntVar(&maxMoves, "max", 10, "move limit")
	f.StringVar(&moves, "moves", strings.Join(allMoves, " "), "moves offered per step")
	f.StringVar(&req.Instructions, "instructions", defaultInstructions, "choice question instructions")
	f.BoolVar(&req.Sample, "sample", false, "draw the move from the probabilities")
	f.BoolVar(&req.NoUndo, "no-undo", true, "do not offer the inverse of the previous move")
	f.BoolVar(&req.Shuffle, "shuffle", false, "randomise the order of offered moves")
	f.BoolVar(&req.HideHistory, "hide-history", false, "leave the move history out of the state text")
	f.BoolVar(&req.Lookahead, "lookahead", true, "describe each move by the sticker count it leads to")
	return cmd
}

// runOnServer plays on the served cube: the page animates every move.
func runOnServer(addr string, req StepRequest, scramble []string, maxMoves int) error {
	call := func(path string, in, out any) error {
		body, _ := json.Marshal(in)
		resp, err := http.Post("http://"+addr+path, "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("%s: %s", path, strings.TrimSpace(string(msg)))
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	if len(scramble) > 0 {
		var applied []string
		if err := call("/api/reset", struct{}{}, &struct{}{}); err != nil {
			return err
		}
		if err := call("/api/scramble", map[string]any{"moves": scramble}, &applied); err != nil {
			return err
		}
		fmt.Printf("scramble: %s\n", strings.Join(applied, " "))
	}
	var rec StepRecord
	for range maxMoves {
		rec = StepRecord{}
		if err := call("/api/step", req, &rec); err != nil {
			return err
		}
		printStep(&rec)
		if rec.Solved {
			fmt.Println("solved")
			break
		}
	}
	fmt.Printf("log: %s\n", filepath.Join(runsDir, rec.Request.Run+".jsonl"))
	return nil
}

// servedCube fetches the session of a running `serve` and rebuilds its cube.
func servedCube(addr string) (*Cube, []string, error) {
	resp, err := http.Get("http://" + addr + "/api/state")
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var st event
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, nil, err
	}
	cube := NewCube()
	cube.ApplyAll(st.Scramble)
	return cube, st.History, cube.ApplyAll(st.History)
}

// playCmds are for a player other than Jev: read the served cube, think, move.
func playCmds() []*cobra.Command {
	var addr, by string
	state := &cobra.Command{
		Use:   "state",
		Short: "Print the served cube: the same observation every player gets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cube, history, err := servedCube(addr)
			if err != nil {
				return err
			}
			fmt.Println(cube.StateText(history))
			return nil
		},
	}
	move := &cobra.Command{
		Use:   "move <moves...>",
		Short: "Turn faces of the served cube, e.g. move R U R'",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, m := range strings.Fields(strings.Join(args, " ")) {
				body, _ := json.Marshal(map[string]string{"move": m, "by": by})
				resp, err := http.Post("http://"+addr+"/api/move", "application/json", bytes.NewReader(body))
				if err != nil {
					return err
				}
				msg, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return fmt.Errorf("%s: %s", m, strings.TrimSpace(string(msg)))
				}
				time.Sleep(280 * time.Millisecond) // let the page finish the turn
			}
			cube, history, err := servedCube(addr)
			if err != nil {
				return err
			}
			fmt.Println(cube.StateText(history))
			return nil
		},
	}
	for _, c := range []*cobra.Command{state, move} {
		c.Flags().StringVar(&addr, "ui", "localhost:7810", "address of the running serve")
	}
	move.Flags().StringVar(&by, "as", "cli", "player name shown in the page log")
	return []*cobra.Command{state, move}
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
	root.AddCommand(playCmds()...)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
