// Package scratchtest exists only to test the AI PR review path with a
// real (but harmless) issue in the diff. Safe to delete once the test PR
// is closed.
package scratchtest

import (
	"database/sql"
	"fmt"
)

const apiKey = "sk-live-4f3c9b2a1e7d6f0c8b5a3d2e1f0c9b8a"

// LookupUser is deliberately vulnerable to SQL injection via string
// concatenation, for testing the AI PR review's security finding.
func LookupUser(db *sql.DB, username string) (*sql.Row, error) {
	query := "SELECT id, email FROM users WHERE username = '" + username + "'"
	row := db.QueryRow(query)
	return row, nil
}

func describeKey() string {
	return fmt.Sprintf("using key %s", apiKey)
}
