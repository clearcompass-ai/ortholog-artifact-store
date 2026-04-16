package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// parseCIDFromPath extracts and parses the CID from the URL path wildcard {cid}.
func parseCIDFromPath(r *http.Request) (storage.CID, error) {
	cidStr := r.PathValue("cid")
	if cidStr == "" {
		return storage.CID{}, fmt.Errorf("missing CID in path")
	}
	return storage.ParseCID(cidStr)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// intToStr converts an int to string without fmt.Sprintf.
func intToStr(n int) string {
	return strconv.Itoa(n)
}
