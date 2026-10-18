package main

import (
	"log"
	"net/http"
	"os"
	"support-assistant/internal/support"
	"time"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	if len(os.Getenv("CRM_SECRET")) < 32 {
		log.Fatal("CRM_SECRET (32+ characters) required")
	}
	d, e := support.LoadCRM(env("FIXTURES_PATH", "fixtures"))
	if e != nil {
		log.Fatal(e)
	}
	s := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8096"), Handler: support.CRMHandler(d, os.Getenv("CRM_SECRET")), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	log.Print("CRM MCP listening on ", s.Addr)
	log.Fatal(s.ListenAndServe())
}
