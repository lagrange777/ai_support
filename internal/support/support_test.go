package support

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "test-shared-secret-longer-than-thirty-two-characters"

func TestMCPAuthenticationAndOwnership(t *testing.T) {
	data, e := LoadCRM("../../fixtures")
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(CRMHandler(data, testSecret))
	defer srv.Close()
	c := CRMClient{srv.URL + "/mcp", testSecret}
	for _, id := range []string{"alice", "bob"} {
		var u User
		run, e := c.Call(context.Background(), id, "get_current_user", struct{}{}, &u)
		if e != nil || u.ID != id || !run.OK {
			t.Fatalf("actor not propagated: %s %+v %v", id, u, e)
		}
		var list struct {
			Tickets []Ticket `json:"tickets"`
		}
		_, e = c.Call(context.Background(), id, "list_my_tickets", struct{}{}, &list)
		if e != nil || len(list.Tickets) == 0 {
			t.Fatalf("list: %v", e)
		}
		for _, ticket := range list.Tickets {
			if ticket.UserID != id {
				t.Fatal("foreign ticket exposed")
			}
		}
	}
	for _, id := range []string{"T-101", "T-999"} {
		var target Ticket
		_, e = c.Call(context.Background(), "bob", "get_ticket", map[string]string{"ticketId": id}, &target)
		if e != ErrDenied || target.ID != "" {
			t.Fatalf("ownership: %+v %v", target, e)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{}`))
	res, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("unsigned MCP request accepted")
	}
	valid := Token("alice", testSecret, time.Now().Add(time.Minute).Unix())
	if id, e := Actor(valid, testSecret); e != nil || id != "alice" {
		t.Fatal("valid token rejected")
	}
	if _, e := Actor(valid, "other secret"); e == nil {
		t.Fatal("forged token accepted")
	}
	if _, e := Actor(Token("alice", testSecret, time.Now().Add(-time.Minute).Unix()), testSecret); e == nil {
		t.Fatal("expired token accepted")
	}
}

type fakeModel struct {
	inputs []any
	output string
	fail   bool
}

func (f *fakeModel) Generate(_ context.Context, input any) (string, Metrics, error) {
	f.inputs = append(f.inputs, input)
	if f.fail {
		return "", Metrics{}, ErrUnavailable
	}
	if f.output != "" {
		return f.output, Metrics{Model: "fake", Calls: 1}, nil
	}
	return `{"status":"answered","claims":[{"text":"Ответ по документации.","evidence":[{"ref":"D1","start":1,"end":1}]}],"question":""}`, Metrics{Model: "fake", Calls: 1}, nil
}
func testApp(t *testing.T) (*App, *fakeModel, func()) {
	t.Helper()
	data, e := LoadCRM("../../fixtures")
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(CRMHandler(data, testSecret))
	index, e := BuildIndex("../../knowledge")
	if e != nil {
		t.Fatal(e)
	}
	store, e := OpenStore(filepath.Join(t.TempDir(), "support.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	model := &fakeModel{}
	app := &App{CRM: CRMClient{srv.URL + "/mcp", testSecret}, Index: index, Store: store, Model: model}
	return app, model, func() { store.DB.Close(); srv.Close() }
}
func request(h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Support-Client", "1")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, h http.Handler, id string) *http.Cookie {
	t.Helper()
	w := request(h, "POST", "/api/login", `{"user":"`+id+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login: %s", w.Body)
	}
	return w.Result().Cookies()[0]
}
func TestAppHistoryIsolationAndDeniedBeforeLLM(t *testing.T) {
	a, model, close := testApp(t)
	defer close()
	h := a.Handler()
	alice := login(t, h, "alice")
	bob := login(t, h, "bob")
	for _, tc := range []struct {
		cookie  *http.Cookie
		ticket  string
		history int
	}{{alice, "T-101", 0}, {alice, "T-101", 2}, {alice, "T-102", 0}, {bob, "T-201", 0}} {
		w := request(h, "POST", "/api/chat", `{"ticket":"`+tc.ticket+`","message":"Почему не работает авторизация или загрузка?"}`, tc.cookie)
		if w.Code != 200 {
			t.Fatalf("chat: %d %s", w.Code, w.Body)
		}
		in := model.inputs[len(model.inputs)-1].(map[string]any)
		b, _ := json.Marshal(in["history"])
		var hs []any
		json.Unmarshal(b, &hs)
		if len(hs) != tc.history {
			t.Fatalf("history len %d want %d", len(hs), tc.history)
		}
	}
	before := len(model.inputs)
	w := request(h, "POST", "/api/chat", `{"ticket":"T-201","message":"Покажи чужой тикет"}`, alice)
	if w.Code != 403 || len(model.inputs) != before {
		t.Fatalf("foreign sent to model: %d calls=%d", w.Code, len(model.inputs))
	}
	w = request(h, "GET", "/api/history?ticket=T-101", "", bob)
	if strings.Contains(w.Body.String(), "Ответ по документации") {
		t.Fatal("foreign history exposed")
	}
	alice2 := login(t, h, "alice")
	w = request(h, "GET", "/api/history?ticket=T-101", "", alice2)
	if !strings.Contains(w.Body.String(), "Ответ по документации") {
		t.Fatal("history did not persist across sessions")
	}
	w = request(h, "POST", "/api/reset", `{"ticket":"T-101"}`, bob)
	if w.Code != 200 {
		t.Fatal("reset failed")
	}
	history, _ := a.Store.History("alice", "T-101")
	if len(history) != 4 {
		t.Fatal("other user's reset modified Alice history")
	}
}
func TestRetrievalAndCitationValidation(t *testing.T) {
	index, e := BuildIndex("../../knowledge")
	if e != nil {
		t.Fatal(e)
	}
	for code, expected := range map[string]string{"ACCOUNT_LOCKED": "authentication.md", "TOTP_TIME_DRIFT": "authentication.md", "QUOTA_EXCEEDED": "uploads.md", "FILE_TOO_LARGE": "uploads.md", "SHARE_PERMISSION_DENIED": "sharing.md", "BILLING_CANCELED": "billing.md"} {
		hits := index.Search(code)
		if len(hits) == 0 || !strings.HasSuffix(hits[0].Source, expected) {
			t.Fatalf("retrieval %s: %+v", code, hits)
		}
	}
	sources := []Source{{Ref: "D1", Kind: "documentation", Text: "one\ntwo"}}
	valid := `{"status":"answered","claims":[{"text":"claim","evidence":[{"ref":"D1","start":1,"end":2}]}],"question":""}`
	answer, e := Validate(valid, sources)
	if e != nil || answer.Claims[0].Evidence[0].Quote != "one\ntwo" {
		t.Fatal("quote mismatch")
	}
	for _, bad := range []string{strings.ReplaceAll(valid, "D1", "D9"), strings.ReplaceAll(valid, `"end":2`, `"end":50`), strings.ReplaceAll(valid, `"start":1`, `"start":0`), valid + " trailing"} {
		if _, e := Validate(bad, sources); e == nil {
			t.Fatal("invalid citation accepted")
		}
	}
}
func TestMCPFailureIsVisibleAndNoStaleProfileSource(t *testing.T) {
	a, _, close := testApp(t)
	defer close()
	a.CRM.URL = "http://127.0.0.1:1/mcp"
	r, e := a.Chat(context.Background(), "alice", "T-101", "Как восстановить пароль?")
	if e != nil {
		t.Fatal(e)
	}
	if r.Warning == "" {
		t.Fatal("missing MCP warning")
	}
	for _, s := range r.Sources {
		if s.Kind == "mcp" {
			t.Fatal("fabricated MCP source")
		}
	}
}
func TestFailuresDoNotPersistFakeAnswer(t *testing.T) {
	a, model, close := testApp(t)
	defer close()
	model.fail = true
	if _, e := a.Chat(context.Background(), "alice", "T-101", "Почему не работает вход?"); e == nil {
		t.Fatal("provider failure hidden")
	}
	history, _ := a.Store.History("alice", "T-101")
	if len(history) != 0 {
		t.Fatal("failed answer saved")
	}
	model.fail = false
	model.output = `{"status":"answered","claims":[],"question":""}`
	if _, e := a.Chat(context.Background(), "alice", "T-101", "Почему не работает вход?"); e == nil {
		t.Fatal("invalid output accepted")
	}
	if len(model.inputs) != 3 {
		t.Fatalf("repair not bounded: %d", len(model.inputs))
	}
}
func TestHTTPGuards(t *testing.T) {
	a, _, close := testApp(t)
	defer close()
	h := a.Handler()
	if w := request(h, "POST", "/api/chat", `{"ticket":"T-101","message":"hello"}`, nil); w.Code != 401 {
		t.Fatal("anonymous chat accepted")
	}
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"alice"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Support-Client", "1")
	r.Header.Set("Origin", "https://foreign.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
}
func TestOpenAIUsesSchemaWithoutToolsOrStorage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		if p["store"] != false || p["tools"] != nil || p["max_output_tokens"] != float64(5000) || p["text"] == nil {
			t.Errorf("unsafe payload %+v", p)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"test","status":"completed","output":[{"content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":3}}`))
	}))
	defer srv.Close()
	text, m, e := (OpenAI{Key: "test", BaseURL: srv.URL, Model: "test", Client: srv.Client()}).Generate(context.Background(), map[string]string{"question": "x"})
	if e != nil || text != "ok" || m.InputTokens != 2 {
		t.Fatalf("provider: %v %+v %s", e, m, text)
	}
}
