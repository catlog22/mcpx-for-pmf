package auth

import (
	"context"
	"testing"
)

func TestCheckBearer(t *testing.T) {
	if !CheckBearer("", "") {
		t.Fatal("no auth required")
	}
	if CheckBearer("", "secret") {
		t.Fatal("missing token")
	}
	if !CheckBearer("Bearer secret", "secret") {
		t.Fatal("ok")
	}
	if CheckBearer("Bearer wrong", "secret") {
		t.Fatal("wrong")
	}
}

func TestAuthorizationContext(t *testing.T) {
	ctx := ContextWithAuthorization(context.Background(), "Bearer abc")
	if AuthorizationFromContext(ctx) != "Bearer abc" {
		t.Fatal(AuthorizationFromContext(ctx))
	}
	if AuthorizationFromContext(context.Background()) != "" {
		t.Fatal("empty")
	}
}
