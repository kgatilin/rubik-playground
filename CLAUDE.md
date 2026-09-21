# jev-playground

A Rubik's cube game for comparing players: decision models called by the server
(Jev, Gemini) and agents that play from a terminal through the CLI.

Status: sections marked **(proposed)** describe the agreed direction and are not
implemented yet. Everything else matches the code.

## The game

- One cube per `serve` process: solved + scramble + the moves made since.
- Goal: solve it in the fewest face turns. Limit: 100 face turns per game
  (`defaultLimit`); the server refuses moves past it and a decision's move list is cut
  at it. Reaching the limit unsolved is a DNF. `run --max` and the page's "decisions
  max" only cap the number of built-in decisions.
- Score = face turns in the half-turn metric: each of the 18 single moves costs 1.
  A bundle costs the number of single moves it expands to. Decisions and latency are
  logged as secondary numbers and do not affect the score.
- The scramble is hidden from every player. A player that read the scramble, the
  server session (`/api/state` JSON, `runs/*.jsonl` of the current game) or the
  event stream has forfeited the game.

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
    logged (`thoughts`). Scramble and reset start a new conversation.
  - The page's player selector and `run --player` pick one; the list comes from
    `GEMINI_MODELS` (comma-separated, default in `players()`).
- **external**: an agent in a terminal calls `state` and `move` against the served cube.

### Rules for external (CLI) players

- Allowed commands: `jev-playground state`, `jev-playground move <actions> --as <name>`,
  `jev-playground actions` **(proposed)**. Nothing else touches the cube.
- No scripts, loops, solvers or simulation of the cube in code. The cube is simulated
  only in the player's head. `state` takes no arguments for this reason.
- Looking is free: `state` can be called any number of times.
- Any number of actions per `move` call; they are applied in order and each one costs
  its face turns.

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
- **Piece identity.** The observation is facelets only: which three stickers form one
  corner has to be derived from the stated face orientation. Same for everyone.
- **Prior knowledge.** Bundles hand every player the standard algorithms; knowing when
  to use them is the test.

## Findings so far

- Gemini (`gemini-3.8-flash`) solved 2-move scrambles in 2–3 face turns, 8–40 s per
  decision; it reasons the scramble back from the facelets. Longer scrambles not run yet.
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
- A CLI agent (Claude) solved a 20-move scramble in 99 face turns layer by layer; the
  first 30 turns used simulation, which the rules now forbid.

## Architecture

- `cube.go` — cube model, facelets, `StateText`. `cube_test.go` pins move notation.
- `jev.go` — `Decider` interface, shared `prepareStep`/`finishStep`, `Jev.Decide`, one
  JSON line per decision in `runs/<run>.jsonl`.
- `gemini.go` — `Gemini.Decide`: the tool-calling conversation.
- `server.go` — `Session` (the served cube), SSE event stream `/api/events`, commands
  `/api/reset`, `/api/scramble`, `/api/move`, `/api/step`. The page only renders events.
- `main.go` — cobra commands: `serve`, `run [--ui]`, `show`, `state`, `move`.
- `index.html` — canvas cube and control panel, embedded into the binary.

Build and check: `go vet ./... && go test ./... && go build -o jev-playground .`
After changing `index.html` or Go code, restart `serve` (the page is embedded).
Secrets and config come from env or `.env` (gitignored, loaded by `loadEnv`):
`JEV_API_TOKEN`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, `GEMINI_MODELS`.
Vertex AI uses application default credentials (`gcloud auth application-default login`);
no key files and no project ids in the repo.

Not recorded today: moves made through `move` or the page are in the event stream and
the page log, not in `runs/*.jsonl` (only Jev decisions are). A game log that covers
every player is part of the proposed work.
