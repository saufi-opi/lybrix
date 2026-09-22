package pipeline

// OCRVerdict is the gate result (ocr_gate.py).
type OCRVerdict struct {
	NeedsOCR         bool
	MeanCharsPerPage float64
}

// NeedsOCR decides whether a shard needs OCR: mean chars/page below
// OCRMinCharsPerPage ⇒ needs_ocr. Port of ocr_gate.py needs_ocr.
func NeedsOCR(path string, pageStart, pageEnd, minCharsPerPage int) (OCRVerdict, error) {
	counts, err := PageTextChars(path, pageStart, pageEnd)
	if err != nil {
		return OCRVerdict{}, err
	}
	if len(counts) == 0 {
		return OCRVerdict{NeedsOCR: true, MeanCharsPerPage: 0.0}, nil
	}
	sum := 0
	for _, c := range counts {
		sum += c
	}
	mean := float64(sum) / float64(len(counts))
	return OCRVerdict{NeedsOCR: mean < float64(minCharsPerPage), MeanCharsPerPage: mean}, nil
}

// YieldOK is the blueprint §4.2 fast-path yield check: accept anydoc output
// when it produced at least minYield chars per page. Shard-level units
// only — never per-page requests.
func YieldOK(markdown string, pages int, minYield int) bool {
	if pages <= 0 {
		pages = 1
	}
	return len(markdown) > 0 && len(markdown)/pages >= minYield
}
