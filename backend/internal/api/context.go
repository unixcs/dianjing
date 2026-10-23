package api

import (
	"context"
	"net/http"

	"dianjing/backend/internal/store"
)

type ctxKey int

const userKey ctxKey = 1

func withUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}
