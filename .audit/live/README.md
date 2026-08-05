# Live benchmarks

These spend money. They exist because the tier assignment in
`internal/config/config.go` is a claim about three real models, and a claim like
that should be measured rather than assumed.

    set -a; . ./.env; set +a
    python .audit/live/bench.py    # typed extraction
    python .audit/live/bench2.py   # a proration with a distractor figure

Each runs four samples across `gpt-5.6-luna`, `-sol`, and `-terra`, and reports
median latency and how often the answer was correct.

The result on 2026-08-05 is recorded in the comment above the tier constants in
`config.go`. The short version: all three models were correct every time, so the
only thing the benchmark could measure was latency. That justified moving `Quick`
to terra and nothing else — see TODOS.md P-014 for what a benchmark that
discriminates on quality would need to contain.

Raw responses are written next to these scripts and are gitignored: they carry
account identifiers and are reproducible by rerunning.
