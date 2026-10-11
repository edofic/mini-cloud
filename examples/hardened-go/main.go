package main

import (
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	listener, err := net.Listen(os.Getenv("LISTEN_NETWORK"), os.Getenv("LISTEN_ADDRESS"))
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hardened process example\n")) })
	log.Fatal(http.Serve(listener, mux))
}
