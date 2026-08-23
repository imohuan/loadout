package requestlog

import "testing"

func TestOpenRequestLogDB(t *testing.T) {
	database, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var tables int
	if err := database.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('request_logs','request_log_config')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 2 {
		t.Fatalf("tables = %d, want 2", tables)
	}

	// 脱敏开关默认开（redact=1），且配置行已初始化
	var redact int
	if err := database.QueryRow("SELECT redact FROM request_log_config WHERE id = 1").Scan(&redact); err != nil {
		t.Fatal(err)
	}
	if redact != 1 {
		t.Fatalf("redact = %d, want 1", redact)
	}
}

func TestOpenRequestLogDBEmptyPath(t *testing.T) {
	if _, err := openRequestLogDB(""); err == nil {
		t.Fatal("empty path should error")
	}
}
