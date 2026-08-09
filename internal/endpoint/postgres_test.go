package endpoint

import (
	"os"
	"strings"
	"testing"
)

func TestPostgresCatalogSourceIsSelectOnly(t *testing.T) {
	data, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ToUpper(string(data))
	if !strings.Contains(source, "SELECT EXISTS") {
		t.Fatal("catalog no longer performs the bounded existence lookup")
	}
	for _, write := range []string{"INSERT INTO", "UPDATE ENDPOINTS", "DELETE FROM", "CREATE TABLE", "ALTER TABLE", ".EXEC("} {
		if strings.Contains(source, write) {
			t.Fatalf("read-only catalog contains write operation %q", write)
		}
	}
}
