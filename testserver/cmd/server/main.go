package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

const version = "1.0.0"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/time", handleTime)

	addr := ":" + port
	log.Printf("testserver v%s starting on %s", version, addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	fmt.Fprintf(w, "Hello from testserver v%s!\n", version)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, version)
}

func handleTime(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)
	fmt.Fprintf(w, "Server time: %s\n", time.Now().Format(time.RFC3339))
}
