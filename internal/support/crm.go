package support

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type User struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Plan    string `json:"plan"`
	Status  string `json:"status"`
	QuotaMB int    `json:"quotaMB"`
	UsedMB  int    `json:"usedMB"`
}
type Ticket struct {
	ID       string   `json:"id"`
	UserID   string   `json:"userId"`
	Subject  string   `json:"subject"`
	Status   string   `json:"status"`
	Code     string   `json:"code"`
	Facts    []string `json:"facts"`
	Messages []string `json:"messages"`
}
type CRMData struct {
	Users   []User
	Tickets []Ticket
}

func LoadCRM(dir string) (*CRMData, error) {
	d := &CRMData{}
	for name, target := range map[string]any{"users.json": &d.Users, "tickets.json": &d.Tickets} {
		b, e := os.ReadFile(dir + "/" + name)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, target); e != nil {
			return nil, e
		}
	}
	return d, nil
}
func (d *CRMData) user(id string) (User, error) {
	for _, u := range d.Users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, errors.New("user not found")
}
func (d *CRMData) ticket(actor, id string) (Ticket, error) {
	for _, t := range d.Tickets {
		if t.ID == id && t.UserID == actor {
			return t, nil
		}
	}
	return Ticket{}, ErrDenied
}

var ErrDenied = errors.New("Обращение не найдено или недоступно")
var ErrUnavailable = errors.New("MCP недоступен")

func Token(user, secret string, expiry int64) string {
	value := user + "." + strconv.FormatInt(expiry, 10)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(value))
	return value + "." + hex.EncodeToString(h.Sum(nil))
}
func Actor(token, secret string) (string, error) {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return "", ErrDenied
	}
	n, e := strconv.ParseInt(p[1], 10, 64)
	if e != nil || n < time.Now().Unix() || n > time.Now().Add(10*time.Minute).Unix() {
		return "", ErrDenied
	}
	want := Token(p[0], secret, n)
	if !hmac.Equal([]byte(want), []byte(token)) {
		return "", ErrDenied
	}
	return p[0], nil
}

type actorKey struct{}

func CRMHandler(data *CRMData, secret string) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "support-crm", Version: "1.0.0"}, nil)
	closed := false
	ann := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed}
	mcp.AddTool(s, &mcp.Tool{Name: "get_current_user", Description: "Read the authenticated user's public product profile. Identity is fixed by server authentication.", Annotations: ann}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, User, error) {
		id, _ := ctx.Value(actorKey{}).(string)
		u, e := data.user(id)
		return nil, u, e
	})
	type ticketList struct {
		Tickets []Ticket `json:"tickets"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "list_my_tickets", Description: "List only the authenticated user's tickets.", Annotations: ann}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ticketList, error) {
		id, _ := ctx.Value(actorKey{}).(string)
		out := ticketList{[]Ticket{}}
		for _, t := range data.Tickets {
			if t.UserID == id {
				out.Tickets = append(out.Tickets, t)
			}
		}
		return nil, out, nil
	})
	type args struct {
		TicketID string `json:"ticketId"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "get_ticket", Description: "Read a ticket if it belongs to the authenticated user. Foreign and nonexistent tickets return the same denial.", Annotations: ann}, func(ctx context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, Ticket, error) {
		id, _ := ctx.Value(actorKey{}).(string)
		t, e := data.ticket(id, a.TicketID)
		return nil, t, e
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "Origin forbidden", 403)
			return
		}
		actor, e := Actor(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), secret)
		if e != nil {
			http.Error(w, "Unauthorized", 401)
			return
		}
		if _, e = data.user(actor); e != nil {
			http.Error(w, "Unauthorized", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
	return mux
}

type CRMClient struct{ URL, Secret string }
type authTransport struct{ token string }

func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

type ToolRun struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"durationMs"`
	OK         bool   `json:"ok"`
}

func (c CRMClient) Call(ctx context.Context, user, name string, args any, target any) (ToolRun, error) {
	started := time.Now()
	run := ToolRun{Name: name}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "support-assistant", Version: "1.0.0"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.URL, HTTPClient: &http.Client{Transport: authTransport{Token(user, c.Secret, time.Now().Add(5*time.Minute).Unix())}, Timeout: 8 * time.Second}}, nil)
	if e != nil {
		run.DurationMS = time.Since(started).Milliseconds()
		return run, ErrUnavailable
	}
	defer session.Close()
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	run.DurationMS = time.Since(started).Milliseconds()
	if e != nil {
		return run, ErrUnavailable
	}
	if result.IsError {
		return run, ErrDenied
	}
	var b []byte
	if result.StructuredContent != nil {
		b, e = json.Marshal(result.StructuredContent)
	} else {
		for _, v := range result.Content {
			if t, ok := v.(*mcp.TextContent); ok {
				b = []byte(t.Text)
				break
			}
		}
	}
	if e != nil || len(b) > 32<<10 || json.Unmarshal(b, target) != nil {
		return run, fmt.Errorf("invalid MCP result")
	}
	run.OK = true
	return run, nil
}
