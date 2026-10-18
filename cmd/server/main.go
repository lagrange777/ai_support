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
	if os.Getenv("OPENAI_API_KEY") == "" || len(os.Getenv("CRM_SECRET")) < 32 {
		log.Fatal("OPENAI_API_KEY and CRM_SECRET (32+ characters) required")
	}
	index, e := support.BuildIndex(env("KNOWLEDGE_PATH", "knowledge"))
	if e != nil {
		log.Fatal(e)
	}
	store, e := support.OpenStore(env("DATABASE_PATH", "data/support.sqlite"))
	if e != nil {
		log.Fatal(e)
	}
	defer store.DB.Close()
	a := &support.App{CRM: support.CRMClient{URL: env("CRM_URL", "http://127.0.0.1:8096/mcp"), Secret: os.Getenv("CRM_SECRET")}, Index: index, Store: store, Model: support.OpenAI{Key: os.Getenv("OPENAI_API_KEY"), BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"), Model: env("OPENAI_MODEL", "gpt-5.3-codex"), Client: &http.Client{Timeout: 90 * time.Second}}, ReportsPath: env("EVALUATION_PATH", "artifacts/evaluation.json")}
	s := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8086"), Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 150 * time.Second, IdleTimeout: 60 * time.Second}
	log.Print("Support Assistant demo listening on ", s.Addr)
	log.Fatal(s.ListenAndServe())
}
