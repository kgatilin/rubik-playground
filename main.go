// jev-playground drives a Rubik's cube with Jev decisions: `serve` is the web
// page, `run` is the same loop from the terminal, `show` prints a run log.
// Every decision goes through Jev.Decide and lands in runs/<run>.jsonl.
package main

import (
	"bufio"
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

//go:embed index.html
var indexHTML []byte

// loadEnv puts KEY=VALUE lines of .env into the environment; real env vars win.
func loadEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && !strings.HasPrefix(k, "#") && os.Getenv(k) == "" {
			os.Setenv(k, strings.Trim(v, `"'`))
		}
	}
}

// newJev returns nil without a token: the other players still work.
func newJev() *Jev {
	token := os.Getenv("JEV_API_TOKEN")
	if token == "" {
		return nil
	}
	return &Jev{Token: token, Client: &http.Client{Timeout: 30 * time.Second}}
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
			mux := http.NewServeMux()
			newHub(newJev()).routes(mux)
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
	fmt.Printf("#%-3d %-3s matched %2d→%2d  %5dms", r.Step, strings.Join(r.Moves, " "), r.MatchedBefore, r.MatchedAfter, r.Millis)
	if len(parts) > 0 {
		fmt.Printf("  conf %.2f  first offered %-3s top: %s", r.Confidence, r.Offered[0], strings.Join(parts, ", "))
	}
	fmt.Println()
	if r.Thoughts != "" {
		fmt.Println("     " + strings.ReplaceAll(strings.TrimSpace(r.Thoughts), "\n", "\n     "))
	}
}

func runCmd() *cobra.Command {
	var (
		req         StepRequest
		scramble    string
		scrambleLen int
		moves       string
		maxMoves    int
		ui          string
		game        bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Scramble a cube and let Jev play up to --max moves",
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Options = strings.Fields(moves)
			if game && ui == "" {
				return fmt.Errorf("--game needs --ui: leaderboard games are played on a running serve")
			}
			if ui != "" {
				return runOnServer(ui, req, strings.Fields(scramble), maxMoves, game)
			}
			var player Decider
			if model, ok := strings.CutPrefix(req.Player, "gemini:"); ok {
				g, err := newGemini(model)
				if err != nil {
					return err
				}
				player = g
			} else if jev := newJev(); jev != nil {
				player = jev
			} else {
				return fmt.Errorf("JEV_API_TOKEN is not set (env or .env)")
			}
			req.Limit = defaultLimit
			req.Scramble = strings.Fields(scramble)
			if scramble == "" {
				req.Scramble = randomScramble(scrambleLen)
			}
			req.Run = newRunID("cli")
			fmt.Printf("run %s  scramble: %s\n", req.Run, strings.Join(req.Scramble, " "))
			for range maxMoves {
				rec, err := player.Decide(cmd.Context(), req)
				if err != nil {
					return err
				}
				printStep(rec)
				req.History = append(req.History, rec.Moves...)
				if rec.Solved {
					fmt.Printf("solved in %d face turns\n", len(req.History))
					break
				}
				if len(req.History) >= req.Limit {
					fmt.Printf("DNF: turn limit of %d reached\n", req.Limit)
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
	f.BoolVar(&game, "game", false, "with --ui: register a leaderboard game (fresh 20-move scramble) before playing")
	f.StringVar(&scramble, "scramble", "", "scramble moves, e.g. \"R U F'\" (default: random)")
	f.IntVar(&scrambleLen, "scramble-len", 3, "length of the random scramble")
	f.IntVar(&maxMoves, "max", 100, "decision limit (the game itself ends at 100 face turns)")
	f.StringVar(&req.Player, "player", "jev", "built-in player: jev or gemini:<model>[@minimal|low|medium|high]")
	f.StringVar(&moves, "moves", strings.Join(allMoves, " "), "moves offered per step")
	f.StringVar(&req.Instructions, "instructions", defaultInstructions, "choice question instructions")
	f.BoolVar(&req.Sample, "sample", false, "draw the move from the probabilities")
	f.BoolVar(&req.NoUndo, "no-undo", true, "do not offer the inverse of the previous move")
	f.BoolVar(&req.Shuffle, "shuffle", false, "randomise the order of offered moves")
	f.BoolVar(&req.Lookahead, "lookahead", true, "describe each move by the sticker count it leads to")
	f.StringVar(&req.Observation, "observation", obsText, "how the faces are shown to the player: text, pieces or image (image: not for jev)")
	return cmd
}

// call posts a JSON command to a running serve.
func call(addr, path string, in, out any) error {
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

// runOnServer plays on the served cube: the page animates every move.
func runOnServer(addr string, req StepRequest, scramble []string, maxMoves int, game bool) error {
	session := ""
	if game {
		session = gameQuery(req.Player)
	}
	call := func(path string, in, out any) error { return call(addr, path, in, out) }
	if game {
		in := PlayRequest{Player: req.Player, Observation: req.Observation, Lookahead: req.Lookahead}
		if err := call("/api/play", in, &Game{}); err != nil {
			return err
		}
		fmt.Printf("leaderboard game for %s\n", req.Player)
	} else if len(scramble) > 0 {
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
		if err := call("/api/step"+session, req, &rec); err != nil {
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

// gameQuery picks a player's session on the server; no name is the sandbox cube.
func gameQuery(player string) string {
	if player == "" {
		return ""
	}
	return "?game=" + url.QueryEscape(player)
}

// servedCube fetches a session of a running `serve` and rebuilds its cube.
func servedCube(addr, player string) (*Cube, []string, error) {
	resp, err := http.Get("http://" + addr + "/api/state" + gameQuery(player))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, nil, errors.New(strings.TrimSpace(string(msg)))
	}
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
	var addr, player, imagePath string
	var pieces bool
	// observe prints the observation; with --image the faces go to a PNG instead of the text rows.
	observe := func() error {
		cube, history, err := servedCube(addr, player)
		if err != nil {
			return err
		}
		if pieces && imagePath != "" {
			return fmt.Errorf("--pieces and --image are different observations: pick one")
		}
		if pieces {
			fmt.Println(cube.StateText(history, defaultLimit, obsPieces))
			return nil
		}
		if imagePath == "" {
			fmt.Println(cube.StateText(history, defaultLimit, obsText))
			return nil
		}
		if err := os.WriteFile(imagePath, cube.StateImage(), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s\nImage: %s\n", cube.StateText(history, defaultLimit, obsImage), imagePath)
		return nil
	}
	state := &cobra.Command{
		Use:   "state",
		Short: "Print the served cube: the same observation every player gets",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return observe() },
	}
	move := &cobra.Command{
		Use:   "move <moves...>",
		Short: "Turn faces of the served cube, e.g. move R U R'",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			moves := strings.Fields(strings.Join(args, " "))
			if err := call(addr, "/api/move"+gameQuery(player), map[string]any{"moves": moves, "by": cmp.Or(player, "cli")}, &struct{}{}); err != nil {
				return err
			}
			return observe()
		},
	}
	play := &cobra.Command{
		Use:   "play --as <model>",
		Short: "Register for a leaderboard game: fresh 20-move scramble, the result is recorded under the name",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			in := PlayRequest{Player: player, Observation: obsText}
			if imagePath != "" {
				in.Observation = obsImage
			} else if pieces {
				in.Observation = obsPieces
			}
			if err := call(addr, "/api/play", in, &Game{}); err != nil {
				return err
			}
			fmt.Printf("Game started for %s. Make moves with: move <actions> --as %s\n\n", player, player)
			return observe()
		},
	}
	for _, c := range []*cobra.Command{state, move, play} {
		c.Flags().StringVar(&player, "as", "", "player name: picks the player's own game; without it, the sandbox cube")
		c.Flags().StringVar(&addr, "ui", "localhost:7810", "address of the running serve")
		c.Flags().BoolVar(&pieces, "pieces", false, "list corners and edges by place instead of the face rows")
		c.Flags().StringVar(&imagePath, "image", "", "write the faces as a PNG to this file instead of printing them as text")
	}
	play.MarkFlagRequired("as")
	board := &cobra.Command{
		Use:   "leaderboard",
		Short: "Print the leaderboard computed from " + runsDir + "/" + gamesFile + ".jsonl",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := leaderboard()
			if err == nil {
				printBoard(rows)
			}
			return err
		},
	}
	return []*cobra.Command{state, move, play, board}
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
					fmt.Printf("run %s  player: %s  scramble: %s  sample=%v no_undo=%v shuffle=%v lookahead=%v observation=%s\ninstructions: %s\n",
						r.Request.Run, r.Request.Player, strings.Join(r.Request.Scramble, " "), r.Request.Sample, r.Request.NoUndo, r.Request.Shuffle, r.Request.Lookahead, cmp.Or(r.Request.Observation, obsText), r.Request.Instructions)
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
	loadEnv()
	root := &cobra.Command{Use: "jev-playground", SilenceUsage: true}
	root.AddCommand(serveCmd(), runCmd(), showCmd())
	root.AddCommand(playCmds()...)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
