package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

type bearer struct {
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (s server) mcpSession(t *testing.T, key string) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: key}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func call(t *testing.T, s server, session *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	s.settle(t)

	if out != nil && !res.IsError {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal %s result: %v", name, err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %s result %s: %v", name, b, err)
		}
	}
	return res
}

func toolText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, " ")
}

func TestMCPTools(t *testing.T) {
	s := newServer(t)
	session := s.mcpSession(t, s.keyWithRole(t, db.RoleUser))

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	want := []string{"create_environment", "delete_environment", "exec", "get_budget", "get_capacity", "get_environment", "get_template", "list_environments", "list_templates"}
	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

func TestMCPRejectsBadTokens(t *testing.T) {
	s := newServer(t)
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)

	for name, token := range map[string]string{
		"none":    "",
		"unknown": "eem_unknown",
		"admin":   s.adminToken(t),
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

func TestMCPEnvironmentLifecycle(t *testing.T) {
	s := newServer(t)
	s.storeTemplate(t)
	session := s.mcpSession(t, s.keyWithRole(t, db.RoleUser))

	var created db.Environment
	res := call(t, s, session, "create_environment", map[string]any{
		"type":      "REGULAR_TESTING",
		"workload":  "img:tag",
		"template":  "clear-dashboard@v1",
		"resources": map[string]any{"workload": map[string]any{"ram_gb": 8}},
	}, &created)
	if res.IsError {
		t.Fatalf("create_environment error: %s", toolText(res))
	}
	if created.TemplateName != "clear-dashboard" || created.Resources.RAMGB != 13 {
		t.Errorf("created = %s@%s %+v, want clear-dashboard with 13 ram", created.TemplateName, created.TemplateVersion, created.Resources)
	}

	var got db.Environment
	if res := call(t, s, session, "get_environment", map[string]any{"id": created.ID.String()}, &got); res.IsError {
		t.Fatalf("get_environment error: %s", toolText(res))
	}
	if got.Status != db.EnvRunning || got.InstanceStatus != provider.StatusRunning {
		t.Errorf("env = %s/%s, want RUNNING/running", got.Status, got.InstanceStatus)
	}

	var list listEnvsResponse
	if res := call(t, s, session, "list_environments", map[string]any{"status": "running"}, &list); res.IsError {
		t.Fatalf("list_environments error: %s", toolText(res))
	}
	if len(list.Environments) != 1 || list.Environments[0].ID != created.ID {
		t.Errorf("list = %+v, want the created env", list.Environments)
	}

	s.fake.execOut = "ok\n"
	var execOut execResponse
	if res := call(t, s, session, "exec", map[string]any{"id": created.ID.String(), "cmd": []string{"go", "test"}}, &execOut); res.IsError {
		t.Fatalf("exec error: %s", toolText(res))
	}
	if execOut != (execResponse{Output: "ok\n"}) {
		t.Errorf("exec = %+v, want ok", execOut)
	}

	var deleted db.Environment
	if res := call(t, s, session, "delete_environment", map[string]any{"id": created.ID.String()}, &deleted); res.IsError {
		t.Fatalf("delete_environment error: %s", toolText(res))
	}
	if deleted.Status != db.EnvDeleting {
		t.Errorf("deleted status = %s, want DELETING", deleted.Status)
	}
}

func TestMCPReadTools(t *testing.T) {
	s := newServer(t)
	s.storeTemplate(t)
	callerID, key := s.callerWithRole(t, db.RoleUser)
	session := s.mcpSession(t, key)

	var templates struct {
		Templates []templateRequest `json:"templates"`
	}
	if res := call(t, s, session, "list_templates", map[string]any{}, &templates); res.IsError || len(templates.Templates) != 1 {
		t.Errorf("list_templates = %+v %s, want one template", templates, toolText(res))
	}

	var tpl templateRequest
	if res := call(t, s, session, "get_template", map[string]any{"name": "clear-dashboard", "version": "v1"}, &tpl); res.IsError || tpl.TTL != "4h" {
		t.Errorf("get_template = %+v %s, want clear-dashboard", tpl, toolText(res))
	}

	var budget budgetResponse
	if res := call(t, s, session, "get_budget", map[string]any{}, &budget); res.IsError || budget.CallerID != callerID {
		t.Errorf("get_budget = %+v %s, want caller %s", budget, toolText(res), callerID)
	}

	var capacity capacityResponse
	if res := call(t, s, session, "get_capacity", map[string]any{}, &capacity); res.IsError || len(capacity.Pools) != 1 {
		t.Errorf("get_capacity = %+v %s, want docker pool", capacity, toolText(res))
	}
}

func TestMCPToolErrors(t *testing.T) {
	s := newServer(t)
	s.storeTemplate(t)
	session := s.mcpSession(t, s.keyWithRole(t, db.RoleUser))

	other := s.createEnv(t, s.keyWithRole(t, db.RoleUser))
	s.fake.execErr = nil

	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{name: "unknown template", tool: "create_environment", args: map[string]any{"type": "REGULAR_TESTING", "workload": "img", "template": "nope@v1"}, want: "template nope@v1 not found"},
		{name: "unknown service override", tool: "create_environment", args: map[string]any{"type": "REGULAR_TESTING", "workload": "img", "template": "clear-dashboard@v1", "resources": map[string]any{"services": map[string]any{"mysql": map[string]any{"cpu_core": 1}}}}, want: "not in the template"},
		{name: "invalid id", tool: "get_environment", args: map[string]any{"id": "nope"}, want: "invalid environment id"},
		{name: "other caller's env", tool: "get_environment", args: map[string]any{"id": other.ID.String()}, want: "not found"},
		{name: "delete other caller's env", tool: "delete_environment", args: map[string]any{"id": other.ID.String()}, want: "not found"},
		{name: "bad status", tool: "list_environments", args: map[string]any{"status": "gone"}, want: "must be one of"},
		{name: "empty cmd", tool: "exec", args: map[string]any{"id": other.ID.String(), "cmd": []string{}}, want: "cmd is required"},
		{name: "unknown template version", tool: "get_template", args: map[string]any{"name": "clear-dashboard", "version": "v9"}, want: "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := call(t, s, session, tt.tool, tt.args, nil)
			if !res.IsError || !strings.Contains(toolText(res), tt.want) {
				t.Errorf("%s = error %v %q, want error containing %q", tt.tool, res.IsError, toolText(res), tt.want)
			}
		})
	}
}
