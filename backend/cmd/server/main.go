// Command server is the dianjing backend entrypoint: HTTP API +
// (later milestones) compensation ticker and embedded admin panel.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"dianjing/backend/internal/api"
	"dianjing/backend/internal/config"
	"dianjing/backend/internal/store"
)

func main() {
	cfgPath := flag.String("config", "dianjing.env", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := &api.Server{Cfg: cfg, St: st}
	addr := cfg.ListenAddr
	fmt.Printf("dianjing backend listening on %s (data: %s)\n", addr, cfg.DataDir)
	log.Fatal(http.ListenAndServe(addr, srv.Routes()))
}
