package mcpserver

import (
	"context"
	"reflect"
	"testing"
)

func TestListScopes(t *testing.T) {
	srv := New(nil, nil)
	out := srv.handleListScopes(context.Background(), ListScopesInput{})
	want := []string{"session", "user"}
	if !reflect.DeepEqual(out.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", out.Scopes, want)
	}
}
