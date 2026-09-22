// cmd/lybrix-eval — Go eval harness (replaces scripts/eval).
//
// Loads the golden-set JSONL (unchanged datasets), calls the live MCP
// endpoint (streamable HTTP, bearer key), computes hit@top-k / MRR /
// hit_at_top_k identical to the Python harness, writes results JSON +
// human summary MD to scripts/eval/results/.
//
//	lybrix-eval run --dataset scripts/eval/datasets/seed.jsonl \
//	                --top-k 8 --label baseline --junk-filter on
//	lybrix-eval compare scripts/eval/results/baseline-*.json \
//	                    scripts/eval/results/phase1-*.json
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "compare":
		err = compareCmd(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: lybrix-eval <command>

commands:
  run      run the golden-set dataset against the live MCP endpoint
  compare  compare two eval results files (A vs B)

env:
  LYBRIX_MCP_URL    e.g. http://100.109.176.118:8430/mcp
  LYBRIX_MCP_TOKEN  bearer key
`)
}
