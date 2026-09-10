package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	runtimeIdentityFilename = "runtime-identity.json"
	runtimeIdentityVersion  = 1
	maxRuntimeIdentityBytes = 4 * 1024
)

var runtimeIdentityMu sync.Mutex

type runtimeIdentityDocument struct {
	SchemaVersion uint32 `json:"schema_version"`
	TenantID      string `json:"tenant_id"`
	AccountID     string `json:"account_id"`
}

// resolveRuntimeIdentity creates an opaque tenant/account pair once. The
// durable record is the only source of truth on later starts.
func resolveRuntimeIdentity(dataDir string) (identity.TenantID, identity.AccountID, error) {
	runtimeIdentityMu.Lock()
	defer runtimeIdentityMu.Unlock()

	path := filepath.Join(dataDir, runtimeIdentityFilename)
	storedTenant, storedAccount, found, err := readRuntimeIdentity(path)
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, fmt.Errorf("load runtime identity: %w", err)
	}
	if found {
		return storedTenant, storedAccount, nil
	}

	tenantID, err := identity.NewTenantID()
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, err
	}
	accountID, err := identity.NewAccountID()
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, err
	}
	if err := writeRuntimeIdentity(path, tenantID, accountID); err != nil {
		return identity.TenantID{}, identity.AccountID{}, fmt.Errorf("persist runtime identity: %w", err)
	}
	return tenantID, accountID, nil
}

func readRuntimeIdentity(path string) (identity.TenantID, identity.AccountID, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return identity.TenantID{}, identity.AccountID{}, false, nil
		}
		return identity.TenantID{}, identity.AccountID{}, false, err
	}
	if !info.Mode().IsRegular() {
		return identity.TenantID{}, identity.AccountID{}, false, errors.New("runtime identity path must be a regular file")
	}
	if info.Size() > maxRuntimeIdentityBytes {
		return identity.TenantID{}, identity.AccountID{}, false, fmt.Errorf("runtime identity exceeds %d bytes", maxRuntimeIdentityBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRuntimeIdentityBytes+1))
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, false, err
	}
	if len(data) > maxRuntimeIdentityBytes {
		return identity.TenantID{}, identity.AccountID{}, false, fmt.Errorf("runtime identity exceeds %d bytes", maxRuntimeIdentityBytes)
	}
	var document runtimeIdentityDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return identity.TenantID{}, identity.AccountID{}, false, errors.New("runtime identity contains invalid JSON")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return identity.TenantID{}, identity.AccountID{}, false, errors.New("runtime identity contains trailing data")
	}
	if document.SchemaVersion != runtimeIdentityVersion {
		return identity.TenantID{}, identity.AccountID{}, false, fmt.Errorf("unsupported runtime identity schema version %d", document.SchemaVersion)
	}
	tenantID, err := identity.ParseTenantID(document.TenantID)
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, false, errors.New("runtime identity contains an invalid tenant ID")
	}
	accountID, err := identity.ParseAccountID(document.AccountID)
	if err != nil {
		return identity.TenantID{}, identity.AccountID{}, false, errors.New("runtime identity contains an invalid account ID")
	}
	return tenantID, accountID, true, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected second JSON value")
	}
	return err
}

func writeRuntimeIdentity(path string, tenantID identity.TenantID, accountID identity.AccountID) (resultErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); resultErr == nil && closeErr != nil {
			resultErr = closeErr
		}
		if resultErr != nil {
			_ = os.Remove(path)
		}
	}()
	document := runtimeIdentityDocument{
		SchemaVersion: runtimeIdentityVersion,
		TenantID:      tenantID.String(),
		AccountID:     accountID.String(),
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(document); err != nil {
		return err
	}
	return file.Sync()
}
