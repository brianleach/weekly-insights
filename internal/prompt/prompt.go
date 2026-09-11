// Package prompt embeds the facet-extraction prompt in the binary.
//
// The prompt is the contract between this tool and whatever model extracts
// facets. Shipping it inside the binary means a single distributed artifact
// carries the vocabulary it expects, so a stale copy on disk cannot silently
// produce labels the aggregator will not recognize.
package prompt

import _ "embed"

//go:embed extraction.md
var extraction string

// Extraction returns the facet-extraction prompt.
func Extraction() string { return extraction }
