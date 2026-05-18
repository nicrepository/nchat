package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/nicrepository/nchat/services/media-service/internal/server"
)

const (
	serviceName = "media-service"
	defaultPort = "8087"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	addr := ":" + port
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.NewHandler(serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("%s listening on %s", serviceName, addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", serviceName, err)
	}
}
