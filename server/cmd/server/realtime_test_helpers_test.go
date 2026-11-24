package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type membershipFailCommitPool struct{ pool *pgxpool.Pool }
type membershipFailCommitTx struct{ pgx.Tx }

func (pool membershipFailCommitPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := pool.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return membershipFailCommitTx{tx}, nil
}

func (membershipFailCommitTx) Commit(context.Context) error {
	return errors.New("injected membership commit failure")
}

func realtimeMembershipConn(t *testing.T, server *httptest.Server, _, workspaceID string) (*websocket.Conn, string) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/ws?workspace_id="+workspaceID,
		http.Header{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, ""
}
