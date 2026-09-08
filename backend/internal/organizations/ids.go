package organizations

import "github.com/google/uuid"

func parseOrg(s string) (uuid.UUID, error) { return uuid.Parse(s) }

func parseUser(s string) (uuid.UUID, error) { return uuid.Parse(s) }

func parseOptionalUser(s string) (uuid.UUID, bool) {
	id, err := uuid.Parse(s)
	return id, err == nil
}
