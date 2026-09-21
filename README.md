# jev-playground

A Rubik's cube on a 2D canvas, driven by [Jev](https://docs.typesafe.ai/) — a System One
decision model. Each step sends the cube state plus one `choice` question (the offered moves)
to Jev, applies the chosen move, and repeats up to a move limit.

    echo 'JEV_API_TOKEN=...' > .env
    go build -o jev-playground .
    ./jev-playground serve                              # http://localhost:7810
    ./jev-playground run --scramble "R U" --lookahead   # same loop in the terminal
    ./jev-playground show [run] [--state] [--list]      # read a run log

    ./jev-playground run --ui [--scramble "R U"]        # play on the served cube; the page shows it

`serve` owns one cube session (scramble + history). The page is a view over its event stream
(`/api/events`, SSE); page buttons and `run --ui` change it through the same endpoints
(`/api/reset`, `/api/scramble`, `/api/move`, `/api/step`). `run` without `--ui` plays a local cube.

Every decision goes through `Jev.Decide` (`jev.go`): it rebuilds the cube from scramble + history,
builds the prompt, calls `https://api.typesafe.ai/v1/systemone` and appends one JSON line per
step to `runs/<run>.jsonl` (state text, offered order, probabilities, sticker counts, latency).
The token stays server-side.

Prompt knobs (page checkboxes and `run` flags): `--shuffle` option order, `--hide-history`,
`--no-undo`, `--sample`, `--lookahead` (each move's criteria lists the sticker count it leads to).
