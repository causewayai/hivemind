package mcpserver

import "context"

// ListScopesInput is the list_scopes tool's (empty) input.
type ListScopesInput struct{}

// ListScopesOutput is the list_scopes tool's output.
type ListScopesOutput struct {
	Scopes []string `json:"scopes"`
}

// handleListScopes returns the fixed set of scopes available in this
// edition. Team and sub-team scopes are N/A for the Local Edition per the
// PRD's Memory Scopes table; there is no failure mode, so unlike the other
// handlers this returns no error.
func (s *Server) handleListScopes(_ context.Context, _ ListScopesInput) *ListScopesOutput {
	return &ListScopesOutput{Scopes: []string{"session", "user"}}
}
