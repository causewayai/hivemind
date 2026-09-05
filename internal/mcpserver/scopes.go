package mcpserver

import "context"

type ListScopesInput struct{}

type ListScopesOutput struct {
	Scopes []string `json:"scopes"`
}

// Team and sub-team scopes are N/A for the Local Edition per the PRD's
// Memory Scopes table.
func (s *Server) handleListScopes(ctx context.Context, in ListScopesInput) (*ListScopesOutput, error) {
	return &ListScopesOutput{Scopes: []string{"session", "user"}}, nil
}
