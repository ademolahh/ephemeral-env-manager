package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/google/uuid"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
	"github.com/ademolahh/ephemeral-env-manager/internal/db"
	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

const mcpCallerKey = "caller"

type noInput struct{}

type envIDInput struct {
	ID string `json:"id" jsonschema:"environment id"`
}

type createEnvInput struct {
	Type           db.EnvType         `json:"type" jsonschema:"REGULAR_TESTING, PR_REVIEW or PROD_BUG"`
	Workload       string             `json:"workload" jsonschema:"workload container image, for example ghcr.io/acme/app:sha"`
	Template       string             `json:"template" jsonschema:"stored template as name@version, see list_templates"`
	Provider       provider.Type      `json:"provider,omitempty" jsonschema:"provider to run on; defaults to the template's default provider"`
	TTL            string             `json:"ttl,omitempty" jsonschema:"overrides the template ttl, for example 30m"`
	Resources      *resourceOverrides `json:"resources,omitempty" jsonschema:"overrides template resources for the workload or named services"`
	IdempotencyKey string             `json:"idempotency_key,omitempty" jsonschema:"retrying with the same key returns the same environment"`
}

type listEnvsInput struct {
	Status string `json:"status,omitempty" jsonschema:"PROVISIONING, RUNNING, DELETING, DELETED or FAILED"`
	Page   int    `json:"page,omitempty" jsonschema:"page number, starting at 1"`
	Limit  int    `json:"limit,omitempty" jsonschema:"environments per page, up to 100"`
}

type execInput struct {
	ID  string   `json:"id" jsonschema:"environment id"`
	Cmd []string `json:"cmd" jsonschema:"command and arguments to run in the workload, for example [\"sh\", \"-c\", \"go test ./...\"]"`
}

type templateInput struct {
	Name    string `json:"name" jsonschema:"template name"`
	Version string `json:"version" jsonschema:"template version"`
}

func (h *Handler) MCP() http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "eem", Version: "v1"}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "create_environment", Description: "Create an environment from a stored template. It starts PROVISIONING; poll get_environment until RUNNING."}, h.mcpCreateEnv)
	mcp.AddTool(s, &mcp.Tool{Name: "get_environment", Description: "Get an environment's status, endpoint and instance status."}, h.mcpGetEnv)
	mcp.AddTool(s, &mcp.Tool{Name: "list_environments", Description: "List your environments, newest first."}, h.mcpListEnvs)
	mcp.AddTool(s, &mcp.Tool{Name: "delete_environment", Description: "Delete one of your environments."}, h.mcpDeleteEnv)
	mcp.AddTool(s, &mcp.Tool{Name: "exec", Description: "Run a command in a RUNNING environment's workload and return its output and exit code. Times out after 5 minutes."}, h.mcpExec)
	mcp.AddTool(s, &mcp.Tool{Name: "list_templates", Description: "List stored templates that create_environment can use."}, h.mcpListTemplates)
	mcp.AddTool(s, &mcp.Tool{Name: "get_template", Description: "Get a stored template's workload, services, resources and ttl."}, h.mcpGetTemplate)
	mcp.AddTool(s, &mcp.Tool{Name: "get_budget", Description: "Get your quota, resource usage and cost budget."}, h.mcpGetBudget)
	mcp.AddTool(s, &mcp.Tool{Name: "get_capacity", Description: "Get each provider's capacity pool and free resources."}, h.mcpGetCapacity)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	return mcpauth.RequireBearerToken(h.verifyMCPToken, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(handler)
}

func (h *Handler) verifyMCPToken(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	if auth.IsAdminToken(token) {
		return nil, fmt.Errorf("admin tokens are not accepted: %w", mcpauth.ErrInvalidToken)
	}

	caller, err := db.CallerByAPIKey(ctx, h.db, auth.HashAPIKey(token))
	if errors.Is(err, db.ErrAPIKeyNotFound) {
		return nil, fmt.Errorf("invalid api key: %w", mcpauth.ErrInvalidToken)
	}

	if err != nil {
		log.Printf("mcp authenticate: %v", err)
		return nil, errors.New("internal error")
	}

	return &mcpauth.TokenInfo{UserID: caller.ID.String(), Extra: map[string]any{mcpCallerKey: caller}}, nil
}

func mcpCaller(req *mcp.CallToolRequest) (db.AuthenticatedCaller, error) {
	if req.Extra != nil && req.Extra.TokenInfo != nil {
		if caller, ok := req.Extra.TokenInfo.Extra[mcpCallerKey].(db.AuthenticatedCaller); ok {
			return caller, nil
		}
	}
	return db.AuthenticatedCaller{}, errors.New("not authenticated")
}

func toolError(op string, err error) error {
	if errorStatus(err) == http.StatusInternalServerError {
		log.Printf("mcp %s: %v", op, err)
		return errors.New("internal error")
	}
	return err
}

func parseEnvID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, badRequest("invalid environment id")
	}
	return id, nil
}

func (h *Handler) mcpCreateEnv(ctx context.Context, req *mcp.CallToolRequest, in createEnvInput) (*mcp.CallToolResult, any, error) {
	caller, err := mcpCaller(req)
	if err != nil {
		return nil, nil, err
	}

	cr, err := h.createRequest(ctx, caller.ID, createEnvRequest{
		Type:      in.Type,
		Provider:  in.Provider,
		Workload:  in.Workload,
		Template:  templateField{Ref: in.Template},
		Resources: in.Resources,
		TTL:       in.TTL,
	}, in.IdempotencyKey)
	if err != nil {
		return nil, nil, toolError("create environment", err)
	}

	e, err := h.envs.Create(ctx, cr)
	if err != nil {
		return nil, nil, toolError("create environment", err)
	}

	return nil, e, nil
}

func (h *Handler) mcpGetEnv(ctx context.Context, req *mcp.CallToolRequest, in envIDInput) (*mcp.CallToolResult, any, error) {
	caller, err := mcpCaller(req)
	if err != nil {
		return nil, nil, err
	}

	id, err := parseEnvID(in.ID)
	if err != nil {
		return nil, nil, err
	}

	e, status, err := h.envs.Get(ctx, caller, id)
	if err != nil {
		return nil, nil, toolError("get environment", err)
	}

	e.InstanceStatus = status
	return nil, e, nil
}

func (h *Handler) mcpListEnvs(ctx context.Context, req *mcp.CallToolRequest, in listEnvsInput) (*mcp.CallToolResult, any, error) {
	caller, err := mcpCaller(req)
	if err != nil {
		return nil, nil, err
	}

	status, err := envStatus(in.Status)
	if err != nil {
		return nil, nil, err
	}

	page, limit := max(in.Page, 1), in.Limit
	if limit == 0 {
		limit = defaultListLimit
	}

	if limit < 1 || limit > maxListLimit {
		return nil, nil, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
	}

	resp, err := h.listEnvs(ctx, caller.ID, status, page, limit)
	if err != nil {
		return nil, nil, toolError("list environments", err)
	}

	return nil, resp, nil
}

func (h *Handler) mcpDeleteEnv(ctx context.Context, req *mcp.CallToolRequest, in envIDInput) (*mcp.CallToolResult, any, error) {
	caller, err := mcpCaller(req)
	if err != nil {
		return nil, nil, err
	}

	id, err := parseEnvID(in.ID)
	if err != nil {
		return nil, nil, err
	}

	e, err := h.envs.Delete(ctx, caller, id)
	if err != nil {
		return nil, nil, toolError("delete environment", err)
	}

	return nil, e, nil
}

func (h *Handler) mcpExec(ctx context.Context, req *mcp.CallToolRequest, in execInput) (*mcp.CallToolResult, any, error) {
	caller, err := mcpCaller(req)
	if err != nil {
		return nil, nil, err
	}

	id, err := parseEnvID(in.ID)
	if err != nil {
		return nil, nil, err
	}

	if len(in.Cmd) == 0 || in.Cmd[0] == "" {
		return nil, nil, errors.New("cmd is required")
	}

	out, err := h.envs.Exec(ctx, caller, id, in.Cmd)
	if exit, ok := errors.AsType[*provider.ExitError](err); ok {
		return nil, execResponse{Output: out, ExitCode: exit.Code}, nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, errors.New("command timed out")
	}

	if err != nil {
		return nil, nil, toolError("exec environment", err)
	}

	return nil, execResponse{Output: out}, nil
}

func (h *Handler) mcpListTemplates(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	specs, err := db.ListTemplates(ctx, h.db)
	if err != nil {
		return nil, nil, toolError("list templates", err)
	}

	if specs == nil {
		specs = []json.RawMessage{}
	}
	return nil, map[string][]json.RawMessage{"templates": specs}, nil
}

func (h *Handler) mcpGetTemplate(ctx context.Context, _ *mcp.CallToolRequest, in templateInput) (*mcp.CallToolResult, any, error) {
	spec, err := db.GetTemplate(ctx, h.db, in.Name, in.Version)
	if err != nil {
		return nil, nil, toolError("get template", err)
	}

	return nil, spec, nil
}

func (h *Handler) mcpGetBudget(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	caller, err := mcpCaller(req)
	if err != nil {
		return nil, nil, err
	}

	resp, err := h.callerBudget(ctx, caller.ID)
	if err != nil {
		return nil, nil, toolError("get budget", err)
	}

	return nil, resp, nil
}

func (h *Handler) mcpGetCapacity(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	resp, err := h.capacity(ctx)
	if err != nil {
		return nil, nil, toolError("get capacity", err)
	}

	return nil, resp, nil
}
