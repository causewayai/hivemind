package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The exported Handle* adapters below match mcp.ToolHandlerFor's signature
// so they can be passed directly to mcp.AddTool. They wrap the unexported
// handle* methods, which hold the actual business logic and are unit-tested
// directly (see write_test.go, query_test.go, scopes_test.go) against a
// simpler (ctx, In) -> (*Out, error) shape.

// HandleMemoryWrite adapts handleMemoryWrite to mcp.ToolHandlerFor for the
// memory_write tool.
func (s *Server) HandleMemoryWrite(ctx context.Context, _ *mcp.CallToolRequest, in MemoryWriteInput) (*mcp.CallToolResult, *MemoryWriteOutput, error) {
	out, err := s.handleMemoryWrite(ctx, in)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// HandleMemoryQuery adapts handleMemoryQuery to mcp.ToolHandlerFor for the
// memory_query tool.
func (s *Server) HandleMemoryQuery(ctx context.Context, _ *mcp.CallToolRequest, in MemoryQueryInput) (*mcp.CallToolResult, *MemoryQueryOutput, error) {
	out, err := s.handleMemoryQuery(ctx, in)
	if err != nil {
		return nil, nil, err
	}
	return nil, out, nil
}

// HandleListScopes adapts handleListScopes to mcp.ToolHandlerFor for the
// list_scopes tool.
func (s *Server) HandleListScopes(ctx context.Context, _ *mcp.CallToolRequest, in ListScopesInput) (*mcp.CallToolResult, *ListScopesOutput, error) {
	return nil, s.handleListScopes(ctx, in), nil
}
