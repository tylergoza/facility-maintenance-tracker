package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergoza/facility-maintenance-tracker/internal/secrets"
)

func TestSecretSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "maintenance.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecretSetting("calendar_token", "cal_abc"); err != nil {
		t.Fatal(err)
	}
	if raw := st.Setting("calendar_token", ""); !strings.HasPrefix(raw, "enc:v1:") {
		t.Fatalf("stored %q, want sealed", raw)
	}
	if v, err := st.SecretSetting("calendar_token"); err != nil || v != "cal_abc" {
		t.Fatalf("got %q, %v", v, err)
	}
	// A plain token from before sealing gets sealed on the next start.
	st.SetSetting("sso_client_secret", "plain-secret")
	st.Close()
	if st, err = Open(path); err != nil {
		t.Fatal(err)
	}
	if raw := st.Setting("sso_client_secret", ""); !strings.HasPrefix(raw, "enc:v1:") {
		t.Fatalf("plain token left as %q", raw)
	}
	if v, _ := st.SecretSetting("sso_client_secret"); v != "plain-secret" {
		t.Fatalf("sso_client_secret = %q", v)
	}
	st.Close()

	// Restored without its key: the secret can't be read, nothing else breaks.
	os.Remove(filepath.Join(dir, "secrets.key"))
	if st, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.SecretSetting("calendar_token"); !errors.Is(err, secrets.ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
}
