# rubik-playground

`AGENTS.md` is a symlink to this file: one set of instructions for every coding agent and
for agents that come here to play.

A Rubik's cube game for comparing players: decision models called by the server
(Jev, Gemini) and agents that play from a terminal through the CLI.

Status: sections marked **(proposed)** describe the agreed direction and are not
implemented yet. Everything else matches the code.

## The game

- A cube is solved + scramble + the moves made since. One `serve` holds several (`Hub`):
  the sandbox cube, and one session per registration, so games run side by side. A session
  has a short number (1, 2, 3… per `serve` run), handed out by `play`; `?game=<n>` on the
  API and `--game <n>` on the CLI pick it; without them it is the sandbox.
- Goal: solve it in the fewest face turns. Limit: 100 face turns per game
  (`defaultLimit`); the server refuses moves past it and a decision's move list is cut
  at it. Reaching the limit unsolved is a DNF. `run --max` and the page's "decisions
  max" only cap the number of built-in decisions.
- Score depends on the game's `goal`, fixed at registration (`play --goal`, the page's
  goal selector, `run --game --goal`) and told to the player in the prompt:
  - `turns` (default): face turns in the half-turn metric, each of the 18 single moves
    costs 1. A bundle costs the number of single moves it expands to. Decisions and
    latency are logged as secondary numbers and do not affect the score.
  - `time`: seconds from registration to the last move. Face turns do not count and there
    is no turn limit; the game is a DNF 30 minutes after registration (`timeLimit`), closed
    by a timer even if nobody moves. The observation's last line is the time left instead
    of the turns used.
- The scramble is hidden from every player. A player that read the scramble, the
  server session (`/api/state` JSON, `runs/*.jsonl` of the current game) or the
  event stream has forfeited the game.

## Games and the leaderboard

A game is one registered attempt (`game.go`). Registration (`play --as <model>`, the page's
Play button, `run --ui --game`) drops the open game, gives the cube a fresh random 20-move
scramble and opens a game under that name in a new session; other games are not touched,
and one name can have several games going at once. `play` prints the player prompt (game
number, the two commands, the rules) and the first observation. It closes by
itself: `solved`, `dnf` at the turn limit, or `abandoned` when its cube is scrambled or reset. A closed game is one line of `runs/games.jsonl`: player, category,
scramble, every move in order, decisions (built-in players), times, outcome.

The leaderboard (`leaderboard` command, `/api/leaderboard`, the page panel) is computed
from that file on every read: per player and category, games, solved, DNF, abandoned, best
and mean face turns of solved games, with the duration of the best game and the mean
duration (registration to last move). Category is the observation mode, plus `+lookahead`
for Jev, plus `+time` for the time goal. Rows are grouped by category; a `+time` row's best
game is the fastest one and the row sorts by seconds, every other row's best game has the
fewest turns. A built-in player's `observation`, `goal` and `lookahead` are fixed at
registration, and a step by another built-in player is refused while a game is open.

Not covered: the name is whatever the player declares; a forfeit (reading the scramble) is
not detected; every move made while a game is open counts towards it, including moves from
the page; scrambles are random per attempt, so compare players by the mean over many games;
a game open when `serve` stops is not recorded, so a game a player walked away from stays
open and unrecorded unless its tab is reset; sessions live in memory until `serve` stops,
finished ones included.

## Players

A player is anything that turns an observation into an action. There are two ways
to be one, and both use the same observation, the same action catalog, the same
turn accounting and the same log:

- **built-in**: the server drives the model (`/api/step`, `run`).
  - Jev: one stateless call per decision, the action catalog as `choice` criteria.
  - Gemini on Vertex AI (`gemini.go`, player id `gemini:<model>[@minimal|low|medium|high]`,
    the suffix is the thinking level, none = the model's default): one conversation per
    game with a single tool, `move(actions)`. The tool response is the next observation,
    rendered when the next decision starts, so moves made by others in between are seen.
    The model's turns go back into the conversation untouched, so thought signatures
    survive and the model carries its own plan between decisions. Thought summaries are
    logged (`thoughts`), and so is the model's turn as returned (`response`: text parts and
    function calls with their arguments, without thought signatures). Several `move` calls in
    one turn are applied in order and each gets a tool response; the observation is in the last. Scramble and reset start a new conversation.
  - The page's player selector and `run --player` pick one; the list comes from
    `GEMINI_MODELS` (comma-separated, default in `players()`).
- **external**: an agent in a terminal calls `state` and `move` against the served cube.

### Rules for external (CLI) players

- Allowed commands: `rubik-playground play --as <name>` (register: fresh scramble, the
  result goes to the leaderboard under that name), `rubik-playground state --game <n>`,
  `rubik-playground move <actions> --game <n>`, with the game number `play` printed.
  `play --view <mode>` picks the observation of the game and `play --goal turns|time` what
  it is ranked by; `state` and `move` then print
  that mode by themselves (`--view` on them overrides it, and picks the mode on the sandbox).
  `rubik-playground actions` **(proposed)**. Nothing else touches the cube.
- A leaderboard attempt starts with `play` and is played in one observation mode, the one
  given to `play`. Calling `play` again starts another game with its own number; the first
  one stays open.
- No scripts, loops, solvers or simulation of the cube in code. The cube is simulated
  only in the player's head. `state` takes no cube arguments for this reason; `--image` only
  names the file the picture is written to.
- Looking is free: `state` can be called any number of times.
- Any number of actions per `move` call; they are applied in order and each one costs
  its face turns.
- Quote the action list: `rubik-playground move "R U R'" --game <n>`. Unquoted, the shell
  pairs the prime marks as quotes and drops them (`move R' U R'` arrives as `R U R`, three
  clockwise turns), with no error when the number of primes is even. The server cannot
  tell this from an intended `R U R`, so the turns are applied and count.

## Observation

One function renders it (`Cube.StateText`); `state` prints it verbatim, built-in
players receive it verbatim:

- the six faces as 3 rows each, with the reading orientation stated once;
- stickers matching their face centre, n/54;
- progress by layer, counted in pieces (`Cube.Progress`): D edges and corners solved,
  middle edges solved, U edges white-up / solved, U corners in place / solved. A piece is
  solved when it is in its own place with its own orientation. This fixes D as the first
  layer and U as the last for every player, the same choice the bundles make;
- the player's own actions in this game, and the previous move with its inverse;
- face turns used, of the limit.

The observation has no per-player switches. `--no-undo` (the inverse of the previous
move is not offered) and `--shuffle` (option order) act on the action list of built-in
players only.

`observation` is a game setting, recorded in the log: `text` (default), `net`, `json`,
`pieces` or `image`. `net` and `json` carry the same facelets as `text` in another shape.
`net` (`Cube.netText`) is the unfolded cross of letters separated by spaces, U on top, L F R B
in a row, D below, so which row touches which is visible instead of described. `json`
(`Cube.jsonText`) is one 3x3 array per face with colour words (`"white"`), which tests
whether glued letters like `WWO` are what a model misreads. With `pieces` the six face rows are replaced by the 8 corner and 12 edge places
(`Cube.piecesText`), one line each: the sticker on every face of the place and the place
the piece belongs to, e.g. `UFR: U=G F=Y R=R (belongs at DFR)`; a piece at home reads
`solved`, `flipped` or `twisted`. This hands the player piece identity, which `text` and
the facelet modes leave to be derived. With
`image` the six face rows are replaced by one PNG (`Cube.StateImage`, `image.go`): the
unfolded net (U / L F R B / D) and two corner views (from U-F-R, from D-B-L), face letters
on the centre stickers. Everything else (sticker count, layer progress, turns, history)
stays text. Gemini gets the picture as an inline part of the first message and inside each
tool response; a CLI agent gets the PNG as a file (`--image <file>`, default a file in the
temp dir); Jev takes text only and refuses
the mode. The page picks it with the "faces" selector next to the player, and a decision
card shows the picture the player saw. Each observation mode is its own leaderboard category.

`lookahead` (each action's criteria lists the sticker count it leads to) is a game
setting, recorded in the log. It is search done by the harness, so games played with it
are a separate category from games played without it.

## Actions

One catalog, shown identically to every player: `criteria` for Jev, the enum of the
`move` tool's `actions` argument for Gemini, `actions` output for CLI agents.

- 18 single moves: `U U' U2 D D' D2 L L' L2 R R' R2 F F' F2 B B' B2`
  (`X` is clockwise seen from outside face X).
- Bundles **(proposed)**: named fixed sequences. There are no whole-cube rotations, so a
  bundle is written for the front face and generated for all four side faces by
  conjugation: `<bundle>@F|R|B|L`. First layer is D, last layer is U.

| bundle | sequence @F | use |
|---|---|---|
| `corner-in-right` | `R U R' U'` | first-layer corner, slot right of the face |
| `corner-in-left` | `L' U' L U` | mirror |
| `edge-in-right` | `U R U' R' U' F' U F` | middle-layer edge from the U slot of the face |
| `edge-in-left` | `U' L' U L U F U' F'` | mirror |
| `ll-cross` | `F U R U' R' F'` | orient last-layer edges |
| `ll-edge-swap` | `R U R' U R U2 R' U` | swap the front and left last-layer edges |
| `ll-corner-cycle` | `U R U' L' U R' U' L` | cycle three last-layer corners, front-right stays |
| `ll-corner-twist` | `R' D' R D R' D' R D` | twist the front-right corner, breaks D until repeated to 6 |

## What is not equal between players

Stated so results are read correctly; none of it is compensated unless listed.

- **Memory.** A CLI agent and Gemini (tool-calling conversation) keep a plan across
  turns. Jev is stateless and returns no text, so it has no equivalent; what it knows
  about the past is the action history in the observation.
- **Decisions per look.** A built-in player acts once per observation. A CLI agent may
  send a long action list after one look. The score counts face turns, so this changes
  the number of calls and nothing else.
- **Piece identity.** Outside the `pieces` observation the player sees facelets only: which
  three stickers form one corner has to be derived from the stated face orientation. Same
  for everyone.
- **Prior knowledge.** Bundles hand every player the standard algorithms; knowing when
  to use them is the test.

## Findings so far

- Gemini (`gemini-3.8-flash`) solved 2-move scrambles in 2–3 face turns, 8–40 s per
  decision; it reasons the scramble back from the facelets. Longer scrambles not run yet.
- First `image` game (`gemini-3.8-flash@low`, scramble `R U F'`): Vertex accepts the PNG
  inside tool responses; the model misread face rows from the picture (F top row read as
  red red yellow, it is red yellow yellow) and did not solve in three decisions.
- `gemini-2.5-flash` returns an empty STOP response when a tool call is forced
  (`FunctionCallingConfigModeAny`), so it cannot play.
- Without `lookahead` Jev's distribution over the 18 moves is near flat (top option
  0.10–0.20, confidence < 0.2) with a small prior for `U`; argmax then repeats `U`.
  The prior survives reordering faces, removing `U` from the legend and word option keys.
- With the move history in the state Jev copies the previous move (probability of the
  repeated move grew 0.12 → 0.30 over three steps). Layer progress in the observation
  does not change this: `R U F'`, no lookahead, six decisions, `U` every time.
- With `lookahead` Jev is a confident argmax (0.9+) and plays greedy on the sticker
  count: solves 2-move scrambles, stalls near 38/54 on longer ones.
- First leaderboard game by a CLI agent (`claude-fable-5-1`, `text`, no simulation): solved
  in 67 face turns, 6m45s, 8 `move` calls: cross 9, three paired slot inserts 6 + 7 + 7,
  last middle edge 8, last layer 6 + 9 + 15.
- A CLI agent (Claude) solved a 20-move scramble in 99 face turns layer by layer; the
  first 30 turns used simulation, which the rules now forbid.

## Architecture

- `cube.go` — cube model, facelets, `StateText`. `cube_test.go` pins move notation.
- `image.go` — `StateImage`: the faces as a PNG, drawn with the standard library and the
  `x/image` bitmap font.
- `jev.go` — `Decider` interface, shared `prepareStep`/`finishStep`, `Jev.Decide`, one
  JSON line per decision in `runs/<run>.jsonl`.
- `gemini.go` — `Gemini.Decide`: the tool-calling conversation.
- `game.go` — `Game`, `Session.Play`, game closing, `leaderboard` over `runs/games.jsonl`.
- `server.go` — `Hub` (the sessions of one serve, `/api/games`), `Session` (one served cube), SSE event stream `/api/events`, commands
  `/api/reset`, `/api/scramble`, `/api/move`, `/api/step`. The page only renders events.
- `main.go` — cobra commands: `serve`, `run [--ui [--game]]`, `show`, `state`, `move`,
  `play`, `leaderboard`.
- `index.html` — canvas cube and the game log, embedded into the binary. The log is the
  main surface: one card per decision (moves, sticker delta, probabilities or thought
  summary, the observation the player saw, the raw record), one line per `move` call of a
  CLI agent or click by hand (turn numbers, the moves, the pause before the call, the time
  since the game started; events carry a server `time`, and the moves of one call share it), separators for game start and end. Above the log: the games panel
  (collapsed: the shown game and how many games are open; expanded: one tab per session, sandbox
  and `#n player`, polled from `/api/games`; a tab switches the event
  stream, so the cube and the log are that game's), the player,
  thinking level, faces (observation) and goal selectors, Play (register a leaderboard game and
  run), Run (keep playing the current cube), Step, Stop, and the Leaderboard panel. The
  server replays the current game's events on connect
  (`sync.log`), so the log survives a reload. `#open` in the URL expands every card.

The page's Stop aborts the `/api/step` request; the request context reaches the model call
(`Decider.Decide(ctx, …)`), so the decision in progress is cancelled and nothing is applied.
The run loop of a built-in player lives in the page, one per game, and keeps going while
another tab is shown; closing the page stops it (`run --ui --game` in a terminal does not
depend on the page).

Build and check: `go vet ./... && go test ./... && go build -o rubik-playground .`
After changing `index.html` or Go code, restart `serve` (the page is embedded).
Secrets and config come from env or `.env` (gitignored, loaded by `loadEnv`):
`JEV_API_TOKEN`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, `GEMINI_MODELS`.
Vertex AI uses application default credentials (`gcloud auth application-default login`);
no key files and no project ids in the repo.

Moves made through `move` or the page are recorded only inside a registered game
(`runs/games.jsonl`); outside one they are in the event stream and the page log only.
`runs/<run>.jsonl` holds built-in decisions.
