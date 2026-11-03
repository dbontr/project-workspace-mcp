package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dbontr/project-workspace-mcp/internal/controller"
)

func main() {
	home, _ := os.UserHomeDir()
	listen := flag.String("listen", "127.0.0.1:8080", "local Go gateway listen address")
	upstream := flag.String("upstream", "http://127.0.0.1:8000", "local Python MCP compute backend")
	database := flag.String("db", filepath.Join(home, ".local", "share", "project-workspace", "state.db"), "existing SQLite database")
	flag.Parse()
	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	db, err := controller.OpenDatabase(*database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	service, err := controller.NewServer(db, os.Getenv("PWM_ADMIN_TOKEN"), target)
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{Addr: *listen, Handler: service, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 16}
	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer done()
	go func() {
		<-ctx.Done()
		wait, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = httpServer.Shutdown(wait)
	}()
	fmt.Printf("Project Workspace Go controller listening at %s\n", *listen)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
