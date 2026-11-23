package auth

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSharedTokenResolvesOnlyTheSecretToTheBoundUser(t *testing.T) {
	tok := NewSharedToken("open-sesame", " Owner@Example.COM ")

	if _, ok := tok.Resolve("open-sesame"); ok {
		t.Fatal("a token that is not bound to a user must resolve to nobody")
	}
	tok.BindUser("user-1")

	if id, ok := tok.Resolve("open-sesame"); !ok || id != "user-1" {
		t.Fatalf("Resolve(secret) = %q, %v; want user-1, true", id, ok)
	}
	if got := tok.Email(); got != "owner@example.com" {
		t.Fatalf("Email() = %q; want the email stored the way logins store it", got)
	}
	for _, wrong := range []string{
		"", "open-sesam", "open-sesame2", "OPEN-SESAME", " open-sesame", "open-sesame ", "mul_open-sesame",
	} {
		if _, ok := tok.Resolve(wrong); ok {
			t.Errorf("Resolve(%q) accepted a value that is not the secret", wrong)
		}
	}
	var missing *SharedToken
	if missing.Match("open-sesame") || missing.Match("") {
		t.Error("a missing shared token must match nothing")
	}
}

func TestResolveSharedTokenIsOffUntilConfiguredAndBound(t *testing.T) {
	t.Cleanup(func() { SetSharedToken(nil) })

	SetSharedToken(nil)
	if _, ok := ResolveSharedToken("open-sesame"); ok {
		t.Fatal("with no token configured every credential must be refused")
	}

	SetSharedToken(NewSharedToken("open-sesame", "owner@example.com"))
	if _, ok := ResolveSharedToken("open-sesame"); ok {
		t.Fatal("a token whose account startup has not prepared yet must be refused")
	}
}

func TestSharedTokenFromEnv(t *testing.T) {
	tests := []struct {
		name                  string
		token                 string
		email                 string
		want                  bool
		mustNameEmailVariable bool
	}{
		{name: "unset", want: false},
		{name: "token without email", token: "open-sesame", want: false, mustNameEmailVariable: true},
		{name: "token with blank email", token: "open-sesame", email: "   ", want: false, mustNameEmailVariable: true},
		{name: "email without token", email: "owner@example.com", want: false},
		{name: "token and email", token: "open-sesame", email: " owner@example.com ", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(SharedTokenEnv, tc.token)
			t.Setenv(SharedTokenEmailEnv, tc.email)

			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			got := SharedTokenFromEnv()
			slog.SetDefault(previous)

			if (got != nil) != tc.want {
				t.Fatalf("SharedTokenFromEnv() = %v; want configured: %v", got, tc.want)
			}
			if strings.Contains(logs.String(), "open-sesame") {
				t.Errorf("the secret reached the log:\n%s", logs.String())
			}
			if tc.mustNameEmailVariable && !strings.Contains(logs.String(), SharedTokenEmailEnv) {
				t.Errorf("the startup log must say %s is required:\n%s", SharedTokenEmailEnv, logs.String())
			}
			if tc.want {
				got.BindUser("user-1")
				if id, ok := got.Resolve("open-sesame"); !ok || id != "user-1" {
					t.Fatalf("configured token Resolve(secret) = %q, %v; want user-1, true", id, ok)
				}
			}
		})
	}
}
