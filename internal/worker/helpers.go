package worker

func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

// redisPendingExtArgsPtr builds the single-entry XPENDING lookup shape.
func redisPendingExtArgsPtr(stream, entryID string) *pendingExtArgs {
	return &pendingExtArgs{Stream: stream, Group: "rag-workers", Start: entryID, End: entryID, Count: 1}
}
