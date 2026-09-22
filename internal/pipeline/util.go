package pipeline

import "os"

// readFile loads a file's bytes (anydoc path when PDFBytes was empty).
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }
