package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/gateway"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

var (
	addr            = flag.String("addr", "", "DEPRECATED: use --listen")
	listen          = flag.String("listen", ":8080", "HTTP listen address (e.g. :8080)")
	registryPath    = flag.String("registry", "", "Path to registry.yaml (enables server allowlist + safe proxy routing)")
	backendTemplate = flag.String("backend-template", "", "Backend websocket URL template (default: ws://{server}:8080/ws)")
)

func main() {
	flag.Parse()

	tp, err := gateway.InitTracer()
	if err != nil {
		log.Fatalf("InitTracer: %v", err)
	}
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("tp.Shutdown: %v", err)
		}
	}()

	defaultAddr := *listen
	if *addr != "" {
		defaultAddr = *addr
	}
	if envPort := os.Getenv("PORT"); envPort != "" {
		if envPort[0] != ':' {
			defaultAddr = ":" + envPort
		} else {
			defaultAddr = envPort
		}
	}

	hub := gateway.NewHub()
	hub.Redactor = gateway.NewRedactor()
	hub.BackendURLTemplate = *backendTemplate

	if p := *registryPath; p != "" {
		reg, err := registry.Load(p)
		if err != nil {
			log.Fatalf("Load registry: %v", err)
		}
		hub.Registry = reg
	}

	// Configure authentication if token is provided via env
	token := os.Getenv("HUB_TOKEN")
	if token == "" {
		token = os.Getenv("FI_MCP_HUB_TOKEN")
	}
	if token != "" {
		log.Println("Enabling Token Authentication")
		hub.Authenticator = &gateway.TokenAuthenticator{Token: token}
	}

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		gateway.Handler(hub, w, r)
	})

	http.HandleFunc("/hosts", func(w http.ResponseWriter, r *http.Request) {
		gateway.HostsHandler(hub, w, r)
	})

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{Addr: defaultAddr}

	go func() {
		log.Printf("Gateway listening on %s", defaultAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe: %v", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down gateway...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %v", err)
	}
}
