package server

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readRandom(b []byte) error {
	_, err := rand.Read(b)
	return err
}
