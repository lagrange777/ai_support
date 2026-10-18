package support

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var assets embed.FS

type Response struct {
	Answer  Answer    `json:"answer"`
	Text    string    `json:"text"`
	Sources []Source  `json:"sources"`
	Runs    []ToolRun `json:"runs"`
	Warning string    `json:"warning,omitempty"`
	Metrics Metrics   `json:"metrics"`
	Goal    string    `json:"goal"`
}
type App struct {
	CRM         CRMClient
	Index       *Index
	Store       *Store
	Model       Generator
	ReportsPath string
	mu          sync.Mutex
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	for _, p := range []string{"app.js", "app.css"} {
		p := p
		mux.HandleFunc("GET /"+p, func(w http.ResponseWriter, r *http.Request) {
			b, _ := assets.ReadFile("static/" + p)
			if strings.HasSuffix(p, "js") {
				w.Header().Set("Content-Type", "application/javascript")
			} else {
				w.Header().Set("Content-Type", "text/css")
			}
			w.Write(b)
		})
	}
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			User string `json:"user"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.User != "alice" && in.User != "bob" {
			writeJSON(w, 400, map[string]string{"error": "Демонстрационный пользователь не найден"})
			return
		}
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			writeJSON(w, 500, map[string]string{"error": "Сбой создания сессии"})
			return
		}
		id := hex.EncodeToString(b)
		if e := a.Store.Session(id, in.User); e != nil {
			writeJSON(w, 500, map[string]string{"error": "Хранилище недоступно"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "support_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 86400})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.actor(w, r)
		if !ok {
			return
		}
		var u User
		var list struct {
			Tickets []Ticket `json:"tickets"`
		}
		run, e := a.CRM.Call(r.Context(), user, "get_current_user", struct{}{}, &u)
		if e != nil {
			writeJSON(w, 503, map[string]string{"error": "CRM через MCP недоступна. Повторите позже."})
			return
		}
		run2, e := a.CRM.Call(r.Context(), user, "list_my_tickets", struct{}{}, &list)
		if e != nil {
			writeJSON(w, 503, map[string]string{"error": "Список обращений MCP недоступен"})
			return
		}
		writeJSON(w, 200, map[string]any{"user": u, "tickets": list.Tickets, "runs": []ToolRun{run, run2}, "knowledge": map[string]any{"documents": a.Index.Documents, "chunks": len(a.Index.Chunks), "hash": a.Index.Hash}})
	})
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.actor(w, r)
		if !ok {
			return
		}
		history, e := a.Store.History(user, r.URL.Query().Get("ticket"))
		if e != nil {
			writeJSON(w, 500, map[string]string{"error": "История недоступна"})
			return
		}
		writeJSON(w, 200, map[string]any{"messages": history})
	})
	mux.HandleFunc("POST /api/reset", func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Ticket string `json:"ticket"`
		}
		if !decode(w, r, &in) {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if e := a.Store.Reset(user, in.Ticket); e != nil {
			writeJSON(w, 500, map[string]string{"error": "Сбой хранилища"})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Ticket  string `json:"ticket"`
			Message string `json:"message"`
		}
		if !decode(w, r, &in) {
			return
		}
		in.Message = strings.TrimSpace(in.Message)
		if len([]rune(in.Message)) < 2 || len([]rune(in.Message)) > 2000 || len(in.Ticket) > 64 || in.Ticket == "" {
			writeJSON(w, 400, map[string]string{"error": "Нужны тикет и вопрос от 2 до 2000 символов"})
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		response, e := a.Chat(r.Context(), user, in.Ticket, in.Message)
		if errors.Is(e, ErrDenied) {
			writeJSON(w, 403, map[string]string{"error": ErrDenied.Error()})
			return
		}
		if e != nil {
			writeJSON(w, 502, map[string]string{"error": e.Error()})
			return
		}
		writeJSON(w, 200, response)
	})
	// Contains only bundled synthetic evaluation data; this entire service is a localhost demo.
	mux.HandleFunc("GET /api/evaluation", func(w http.ResponseWriter, r *http.Request) {
		b, e := os.ReadFile(a.ReportsPath)
		if e != nil {
			writeJSON(w, 200, map[string]any{"cases": []any{}, "status": "not_run"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "POST" {
			if r.Header.Get("X-Support-Client") != "1" {
				http.Error(w, "client header required", 403)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					http.Error(w, "Origin forbidden", 403)
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) actor(w http.ResponseWriter, r *http.Request) (string, bool) {
	c, e := r.Cookie("support_session")
	if e != nil {
		writeJSON(w, 401, map[string]string{"error": "Выберите демонстрационный профиль"})
		return "", false
	}
	user, e := a.Store.Actor(c.Value)
	if e != nil {
		writeJSON(w, 401, map[string]string{"error": "Сессия истекла"})
		return "", false
	}
	return user, true
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, 415, map[string]string{"error": "Нужен JSON"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		writeJSON(w, 400, map[string]string{"error": "Некорректный JSON"})
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "Лишние данные"})
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func source(ref, tool string, value any) Source {
	b, _ := json.MarshalIndent(value, "", "  ")
	return Source{Ref: ref, Kind: "mcp", Source: "mcp://support-crm/" + tool, Section: tool, ChunkID: tool + "@" + time.Now().UTC().Format(time.RFC3339Nano), Text: string(b)}
}
func (a *App) Chat(ctx context.Context, user, ticketID, question string) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r := Response{Sources: []Source{}, Runs: []ToolRun{}, Goal: "Помочь по обращению " + ticketID}
	var u User
	var ticket Ticket
	for _, step := range []struct {
		tool   string
		args   any
		target any
		ref    string
	}{{"get_current_user", struct{}{}, &u, "M1"}, {"get_ticket", map[string]string{"ticketId": ticketID}, &ticket, "M2"}} {
		run, e := a.CRM.Call(ctx, user, step.tool, step.args, step.target)
		r.Runs = append(r.Runs, run)
		if errors.Is(e, ErrDenied) {
			return r, ErrDenied
		}
		if e != nil {
			r.Warning = "MCP недоступен: текущие данные пользователя или тикета не подтверждены. Доступны только общие инструкции."
			continue
		}
		r.Sources = append(r.Sources, source(step.ref, step.tool, step.target))
	}
	if ticket.ID != "" {
		r.Goal = "Решить " + ticket.ID + ": " + ticket.Subject
	}
	history, e := a.Store.History(user, ticketID)
	if e != nil {
		return r, errors.New("хранилище истории недоступно")
	}
	if len(history) > 10 {
		history = history[len(history)-10:]
	}
	clarifications := []string{}
	for _, m := range history {
		if m.Role == "user" {
			clarifications = append(clarifications, "Пользователь сообщил: "+m.Text)
		}
	}
	clarifications = append(clarifications, "Текущий вопрос пользователя: "+question)
	r.Sources = append(r.Sources, Source{Ref: "H1", Kind: "history", Source: "history://current-user/" + ticketID, Section: "Уточнения пользователя (не диагностика)", ChunkID: "clarifications-" + ticketID, Text: strings.Join(clarifications, "\n")})
	query := question + " " + ticket.Subject + " " + ticket.Code
	for _, m := range history {
		if m.Role == "user" {
			query += " " + m.Text
		}
	}
	docs := a.Index.Search(query)
	r.Sources = append(r.Sources, docs...)
	if len(docs) == 0 {
		r.Answer = Answer{Status: "unknown", Claims: []Claim{}, Question: "Не знаю: в базе нет подходящей инструкции. Уточните, какое действие в Nimbus Drive вы выполняете и какой текст ошибки видите."}
	} else {
		type hist struct{ Role, Text string }
		hs := []hist{}
		for _, m := range history {
			hs = append(hs, hist{m.Role, m.Text})
		}
		type numbered struct{ Ref, Source, Section, Text string }
		ns := []numbered{}
		for _, s := range r.Sources {
			var b strings.Builder
			for i, l := range strings.Split(s.Text, "\n") {
				fmt.Fprintf(&b, "%d | %s\n", i+1, l)
			}
			ns = append(ns, numbered{s.Ref, s.Source, s.Section, b.String()})
		}
		input := map[string]any{"question": question, "goal": r.Goal, "history": hs, "warning": r.Warning, "sources": ns}
		for attempt := 0; attempt < 2; attempt++ {
			raw, metrics, e := a.Model.Generate(ctx, input)
			r.Metrics.Model = metrics.Model
			r.Metrics.InputTokens += metrics.InputTokens
			r.Metrics.OutputTokens += metrics.OutputTokens
			r.Metrics.DurationMS += metrics.DurationMS
			r.Metrics.Calls += metrics.Calls
			if e != nil {
				return r, e
			}
			answer, e := Validate(raw, r.Sources)
			if e == nil {
				r.Answer = answer
				break
			}
			if attempt == 1 {
				return r, fmt.Errorf("ответ не прошёл проверку источников: %s", e)
			}
			input["validationFeedback"] = e.Error()
			input["previousOutput"] = raw
		}
	}
	for _, c := range r.Answer.Claims {
		r.Text += c.Text + "\n\n"
	}
	r.Text += r.Answer.Question
	r.Text = strings.TrimSpace(r.Text)
	if e := a.Store.SaveTurn(user, ticketID, question, r); e != nil {
		return r, errors.New("ответ получен, но сохранить историю не удалось")
	}
	return r, nil
}
