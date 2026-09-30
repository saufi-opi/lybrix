package pipeline

// allFormats enumerates every pipeline.Format value — the shared driver for
// the capability tests in both lanes (untagged helpers are visible to the
// tagged test files in the same package).
func allFormats() []Format {
	return []Format{
		FmtPDF,
		FmtEPUB,
		FmtDOCX,
		FmtPPTX,
		FmtXLSX,
		FmtTXT,
		FmtMD,
		FmtHTML,
	}
}
