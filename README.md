# jev-playground

A Rubik's cube on a 2D canvas, driven by [Jev](https://docs.typesafe.ai/) — a System One
decision model. Each step sends the cube state plus one `choice` question (the offered moves)
to Jev, applies the chosen move, and repeats up to a move limit.

    echo 'JEV_API_TOKEN=...' > .env
    go run .            # http://localhost:7810

The Go server embeds the page and proxies `/api/decide` to `https://api.typesafe.ai/v1/systemone`,
so the token stays server-side.
