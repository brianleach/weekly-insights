package main

import "encoding/json"

// marshalIndent keeps the CLI's JSON output byte-identical to what the
// snapshot files contain, so piping stdout into a file produces a valid
// snapshot rather than a differently-formatted near-copy.
func marshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
