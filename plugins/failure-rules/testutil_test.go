package failurerules

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"loadout/core/db"
)

var memCounter int64

// openMemory 打开带完整迁移的内存库（唯一名防串库）。
func openMemory(t *testing.T) (*sql.DB, error) {
	t.Helper()
	n := atomic.AddInt64(&memCounter, 1)
	d, err := db.Open(fmt.Sprintf("file:failure-rules-test-%d?mode=memory&cache=shared", n))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { d.Close() })
	return d, nil
}
