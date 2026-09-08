package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/polymorfa/hypermeow/store/sqlstore"
	waLog "github.com/polymorfa/hypermeow/util/log"
	_ "modernc.org/sqlite"
)

const defaultBusyTimeoutMS = 5000

func OpenDeviceStore(ctx context.Context, path string, logger waLog.Logger) (*sqlstore.Container, error) {
	if strings.ContainsRune(path, '\x00') {
		return nil, errors.New("device store path contains a null byte")
	}
	if strings.ContainsAny(path, "?#%") {
		return nil, errors.New("device store path contains URI-reserved characters")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve device store path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("create device store directory: %w", err)
	}
	container, err := sqlstore.New(ctx, "sqlite", deviceStoreDSN(absolute, defaultBusyTimeoutMS), logger)
	if err != nil {
		return nil, fmt.Errorf("open Hypermeow device store: %w", err)
	}
	return container, nil
}

func deviceStoreDSN(path string, busyTimeoutMS int) string {
	query := make(url.Values)
	query.Set("_foreign_keys", "on")
	query.Set("_busy_timeout", strconv.Itoa(busyTimeoutMS))
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	return "file:" + filepath.ToSlash(path) + "?" + query.Encode()
}
