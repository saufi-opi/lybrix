// strategy.go is the public entry point for adaptive chunking. Callers use
// Split / SplitParentChild rather than SplitText directly; the strategy resolver
// picks a tier chain from the document profile and the configured Strategy hint.
//
// SplitText remains the guaranteed net: the chain always ends in TierLegacy, so
// a caller always receives a usable chunk set.
package chunker

// Split chunks text using the strategy in cfg. When cfg.Strategy is empty or
// "auto", the document profiler picks the tier chain.
//
// Tiers are attempted in order and the first one the validator accepts wins. If
// every tier is rejected the legacy tier's output is returned rather than
// nothing — a rejected-but-present result beats an empty index.
func Split(text string, cfg SplitterConfig) []Chunk {
	out, _ := SplitWithDiagnostics(text, cfg)
	return out
}

// SplitWithDiagnostics is Split plus the diagnostic trace: which tier won, the
// chain attempted, and why each rejected tier was rejected. Intended for debug
// surfaces rather than the ingestion hot path.
func SplitWithDiagnostics(text string, cfg SplitterConfig) ([]Chunk, *Diagnostics) {
	diag := &Diagnostics{SelectedTier: TierLegacy}
	if text == "" {
		return nil, diag
	}
	cfg = ensureDefaults(cfg)
	chain, profile := resolveChainWithProfile(text, cfg)
	diag.TierChain = chain
	diag.Profile = profile
	totalChars := runeLen(text)

	var lastOut []Chunk
	var lastTier StrategyTier
	for i, tier := range chain {
		out := runTier(tier, text, cfg, profile)
		v := ValidateChunks(out, totalChars, cfg.ChunkSize)
		if v.OK {
			diag.SelectedTier = tier
			return out, diag
		}
		diag.Rejected = append(diag.Rejected, TierRejection{Tier: tier, Reason: v.Reason})
		if tier == TierLegacy && i == len(chain)-1 {
			lastOut, lastTier = out, tier
		}
	}
	if lastOut != nil {
		diag.SelectedTier = lastTier
		return lastOut, diag
	}
	return SplitText(text, cfg), diag
}

// resolveChainWithProfile returns the tier chain to attempt and, when the chain
// was chosen by the profiler, the DocProfile that drove it. Profile is nil for
// an explicit non-auto strategy, so callers do not pay for an unused pass.
func resolveChainWithProfile(text string, cfg SplitterConfig) ([]StrategyTier, *DocProfile) {
	switch cfg.Strategy {
	case StrategyHeading:
		return []StrategyTier{TierHeading, TierLegacy}, nil
	case StrategyHeuristic:
		return []StrategyTier{TierHeuristic, TierLegacy}, nil
	case StrategyRecursive, StrategyLegacy:
		// "recursive" is a public alias for the legacy splitter; both invoke
		// SplitText. Kept for compatibility with stored configs.
		return []StrategyTier{TierLegacy}, nil
	case StrategyAuto, "":
		// Empty means auto, deliberately: treating it as legacy would silently
		// disable heading breadcrumbs for every caller that forgot the field.
		profile := ProfileDocument(text)
		return SelectStrategy(profile), profile
	default:
		profile := ProfileDocument(text)
		return SelectStrategy(profile), profile
	}
}

// runTier dispatches to the tier's implementation.
func runTier(tier StrategyTier, text string, cfg SplitterConfig, profile *DocProfile) []Chunk {
	switch tier {
	case TierHeading:
		return splitByHeadingsImpl(text, cfg, profile)
	case TierHeuristic:
		return splitByHeuristicsImpl(text, cfg, profile)
	default:
		return SplitText(text, cfg)
	}
}

// SplitParentChild performs two-level chunking:
//
//  1. split the document into large parent chunks (parentCfg),
//  2. split each parent into small child chunks (childCfg) for embedding.
//
// pages is the per-line page map from Stitch (len(pages) == number of lines in
// markdown), or nil when no page information exists. When present, every parent
// and child gets its page range computed from its own rune offsets. The offsets
// are exact because Content is a verbatim slice, so no text matching is needed —
// this is what fixes the NULL child page ranges of BACKLOG R-27.
//
// Seq is one monotone sequence shared by parents and children, so neighbour
// queries and prev/next links never mix the two kinds.
func SplitParentChild(markdown string, pages []int, parentCfg, childCfg SplitterConfig) ParentChildResult {
	result, _ := splitParentChild(markdown, pages, parentCfg, childCfg)
	return result
}

// SplitParentChildWithDiagnostics is SplitParentChild plus the tier trace.
func SplitParentChildWithDiagnostics(markdown string, pages []int, parentCfg, childCfg SplitterConfig) (ParentChildResult, *Diagnostics) {
	return splitParentChild(markdown, pages, parentCfg, childCfg)
}

func splitParentChild(markdown string, pages []int, parentCfg, childCfg SplitterConfig) (ParentChildResult, *Diagnostics) {
	parentCfg = ensureDefaults(parentCfg)
	childCfg = ensureDefaults(childCfg)
	if markdown == "" {
		return ParentChildResult{}, &Diagnostics{}
	}

	parents, diag := SplitWithDiagnostics(markdown, parentCfg)
	if len(parents) == 0 {
		return ParentChildResult{}, diag
	}

	pg := newPageMapper(markdown, pages)
	var newParents []Chunk
	var children []ChildChunk
	seq := 0

	for _, parent := range parents {
		subs := Split(parent.Content, childCfg)

		// A parent is materialised only when it actually has children: a parent
		// that splits into exactly one identical child would be a duplicate row.
		parentIndex := -1
		if len(subs) > 1 || (len(subs) == 1 && subs[0].Content != parent.Content) {
			parentIndex = len(newParents)
			p := parent
			p.Seq = seq
			p.PageStart, p.PageEnd = pg.rangeFor(p.Start, p.End)
			newParents = append(newParents, p)
			seq++
		}

		for _, sub := range subs {
			// Map the child from parent-content coordinates to document
			// coordinates, accounting for any header the parent had injected.
			sub = remapChild(sub, parent)
			sub.Seq = seq
			sub.ContextHeader = mergeBreadcrumbs(parent.ContextHeader, sub.ContextHeader)
			sub.PageStart, sub.PageEnd = pg.rangeFor(sub.Start, sub.End)
			seq++
			children = append(children, ChildChunk{
				Chunk:       sub,
				ParentIndex: parentIndex,
				ParentHash:  HashText(parent.Content),
			})
		}
	}

	return ParentChildResult{Parents: newParents, Children: children}, diag
}

// remapChild maps a child chunk from parent-content coordinates into document
// coordinates.
//
// The complication is that parent.Content is NOT a pure source slice when the
// parent carries a re-injected table header: it is
//
//	[parent header: SyntheticPrefixRunes generated runes][source: parent.Start..parent.End]
//
// and SplitText was run over that whole string. So a child's local offsets index
// a buffer that begins with generated text, and its Content can therefore begin
// with (part of) the parent's header before reaching source runes.
//
// The mapping is:
//
//	inherited   = clamp(parentSynthetic - localStart, 0, localEnd-localStart)
//	sourceStart = parent.Start + max(0, localStart - parentSynthetic)
//	sourceEnd   = parent.Start + max(0, localEnd   - parentSynthetic)
//
// and the child's synthetic prefix is its OWN injected header (already set by
// buildChunk) plus whatever part of the parent's header it inherited.
//
// In practice `inherited` is either 0 or the parent's full prefix, because the
// parent's header is a protected span and so is never split across children.
func remapChild(sub Chunk, parent Chunk) Chunk {
	synthetic := parent.SyntheticPrefixRunes
	if synthetic == 0 {
		// Fast path: the parent is a pure source slice, so the shift is additive.
		sub.Start += parent.Start
		sub.End += parent.Start
		return sub
	}

	localStart, localEnd := sub.Start, sub.End
	bodyLen := localEnd - localStart
	if bodyLen < 0 {
		bodyLen = 0
	}

	inherited := synthetic - localStart
	if inherited < 0 {
		inherited = 0
	}
	if inherited > bodyLen {
		inherited = bodyLen
	}

	sourceStart := localStart - synthetic
	if sourceStart < 0 {
		sourceStart = 0
	}
	sourceEnd := localEnd - synthetic
	if sourceEnd < 0 {
		sourceEnd = 0
	}

	sub.Start = parent.Start + sourceStart
	sub.End = parent.Start + sourceEnd
	sub.SyntheticPrefixRunes += inherited
	return sub
}

// Diagnostics captures which tier produced the returned chunks, the chain that
// was attempted, and any rejected tiers.
type Diagnostics struct {
	SelectedTier StrategyTier    `json:"selected_tier"`
	TierChain    []StrategyTier  `json:"tier_chain"`
	Rejected     []TierRejection `json:"rejected"`
	Profile      *DocProfile     `json:"profile,omitempty"`
}

// TierRejection records why a tier was rejected and the chain advanced.
type TierRejection struct {
	Tier   StrategyTier `json:"tier"`
	Reason string       `json:"reason"`
}
