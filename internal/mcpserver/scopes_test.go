package mcpserver

import (
	"context"
	"reflect"
	"testing"
)

func TestListScopes(t *testing.T) {
	srv := New(nil, nil)
	out, err := srv.handleListScopes(context.Background(), ListScopesInput{})
	if err != nil {
		t.Fatalf("handleListScopes() error = %v", err)
	}
	want := []string{"session", "user"}
	if !reflect.DeepEqual(out.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", out.Scopes, want)
	}
}
