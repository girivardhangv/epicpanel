package agent

import (
	"os"
	"context"
	"testing"
	"time"
)

// TestPPAE2E_FieldPoisoned runs the real PPA flow against a deliberately
// poisoned apt state (deb822 resolute entry, like the field report).
// Skipped when SKIP_PPA_E2E is unset (needs network + apt).
func TestPPAE2E_FieldPoisoned(t *testing.T) {
	if os.Getenv("SKIP_PPA_E2E") == "" {
		t.Skip("set SKIP_PPA_E2E=1 to run the live PPA end-to-end test")
	}
	e := NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := e.addOndrejPPA(ctx); err != nil {
		t.Fatalf("addOndrejPPA: %v", err)
	}
	if err := e.aptPackagesVisible(ctx, []string{"php8.3-fpm", "php8.3-cli", "php8.3-common"}); err != nil {
		t.Fatalf("packages not visible after PPA flow: %v", err)
	}
}
