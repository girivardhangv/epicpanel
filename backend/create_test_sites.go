//go:build ignore
// +build ignore

package main

import (
	"context"
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
		log.Fatal(err)
	}

	softwares := []map[string]string{
		{"type": "php", "version": "8.1"},
		{"type": "php", "version": "8.2"},
		{"type": "php", "version": "8.3"},
		{"type": "php", "version": "8.4"},
		{"type": "php", "version": "8.5"},
		{"type": "node", "version": "20"},
		{"type": "node", "version": "21"},
		{"type": "node", "version": "22"},
		{"type": "node", "version": "23"},
		{"type": "node", "version": "24"},
		{"type": "go", "version": "1.24"},
		{"type": "go", "version": "1.26"},
		{"type": "java", "version": "21"},
		{"type": "java", "version": "25"},
		{"type": "python", "version": "latest"},
		{"type": "openlitespeed", "version": "1.8"},
	}

	for _, sw := range softwares {
		payload := fmt.Sprintf(`{"type": "%s", "version": "%s"}`, sw["type"], sw["version"])
		_, err = conn.Exec(ctx, "INSERT INTO jobs (server_id, type, payload) VALUES ($1, 'install_runtime', $2)", serverID, payload)
		if err != nil {
			log.Printf("Failed to insert job %v: %v", sw, err)
		} else {
			fmt.Printf("Queued install_runtime for %s %s\n", sw["type"], sw["version"])
		}
	}

	// Insert dbtools
	_, err = conn.Exec(ctx, "INSERT INTO jobs (server_id, type, payload) VALUES ($1, 'install_database_tools', '{}')", serverID)
	if err != nil {
		log.Printf("Failed to insert dbtools: %v", err)
	} else {
		fmt.Println("Queued install_database_tools")
	}
}
