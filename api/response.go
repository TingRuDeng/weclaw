package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// writeJSONResponse 统一写入 JSON，并显式记录客户端断开等响应错误。
func writeJSONResponse(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[api] failed to encode JSON response: %v", err)
	}
}

func writeJSONStatus(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[api] failed to encode JSON response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, code string, message string) {
	writeJSONStatus(w, status, map[string]string{"status": "error", "code": code, "message": message})
}
