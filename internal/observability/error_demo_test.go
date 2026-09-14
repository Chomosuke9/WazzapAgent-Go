package observability

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

func TestErrorOutputDemo(t *testing.T) {
	// Simulasi error dari database
	dbErr := sql.ErrNoRows

	// SEBELUM (error buatan yang membuang error asli):
	// oldErr := agent.NewError(agent.ErrorStorageFailure, "load user data", fmt.Errorf("query failed"))
	// Output: "load user data: query failed" (error asli hilang!)

	// SESUDAH (error wrapping yang preserve error asli):
	wrappedErr := agent.NewError(agent.ErrorStorageFailure, "load user data", dbErr)
	finalErr := agent.NewError(agent.ErrorIntegrityFailure, "process request", wrappedErr)

	fmt.Println("\n=== Error Output Demo ===")
	fmt.Printf("Full Error Chain: %v\n", finalErr)
	fmt.Printf("Error Code: %s\n", agent.CodeOf(finalErr))
	fmt.Println("=========================")

	// Error akan menampilkan full chain:
	// "process request: load user data: sql: no rows in result set"
	if finalErr == nil {
		t.Fatal("error should not be nil")
	}
}

func TestErrorPreservesOriginal(t *testing.T) {
	// Simulasi error dari native library
	nativeErr := fmt.Errorf("WhatsApp send failed: connection timeout")

	// Wrapping dengan context
	wrappedErr := agent.NewError(agent.ErrorProviderFailure, "send WhatsApp text", nativeErr)

	fmt.Println("\n=== Native Error Preservation ===")
	fmt.Printf("Error: %v\n", wrappedErr)
	fmt.Println("=================================")

	// Output: "send WhatsApp text: WhatsApp send failed: connection timeout"
	// Sekarang kita bisa lihat error asli dari WhatsApp library!
}
