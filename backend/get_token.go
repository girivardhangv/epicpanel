//go:build ignore
// +build ignore

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func main() {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_dev?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	var serverID uuid.UUID
	err = conn.QueryRow(ctx, "SELECT id FROM servers LIMIT 1").Scan(&serverID)
	if err != nil {
		log.Fatal("Could not find a server:", err)
	}

	token := uuid.New().String()
	hashBytes := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hashBytes[:])

	_, err = conn.Exec(ctx, "INSERT INTO server_registration_tokens (server_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')", serverID, tokenHash)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(token)
}
