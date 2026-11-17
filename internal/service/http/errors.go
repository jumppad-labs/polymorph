package http

import (
	"encoding/json"
	"net/http"
)

// writeJSONError writes a JSON error body of the form {"error": msg} with the
// given status code. The Content-Type header is set before WriteHeader so it is
// actually sent, and the message is JSON-encoded so quotes and other special
// characters in error strings produce valid JSON.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
