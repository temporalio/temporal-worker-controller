package main

import (
	"log"
	"net/http"

	"github.com/DataDog/temporal-worker-controller/internal/demo/tamagotchi/api"
	"github.com/DataDog/temporal-worker-controller/internal/demo/tamagotchi/frontend"
	"github.com/DataDog/temporal-worker-controller/internal/demo/util"
)

func main() {
	// Create Temporal client
	c, stopFunc := util.NewClient("")
	defer stopFunc()

	// Create API handler
	a := api.NewHandler(c)

	// Create a new mux for handling both API and static files
	mux := http.NewServeMux()

	// Handle API routes
	mux.Handle("/api/", a)

	// Serve static files
	fs := http.FileServer(http.FS(frontend.FS))
	mux.Handle("/", fs)

	// Start the server
	if err := http.ListenAndServe(":9000", mux); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
