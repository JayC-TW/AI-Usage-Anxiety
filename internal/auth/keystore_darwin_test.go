//go:build darwin && cgo

package auth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestNativeKeychainRoundTrip(t *testing.T) {
	if os.Getenv("AIUSAGE_TEST_KEYCHAIN") != "1" {
		t.Skip("requires isolated macOS Keychain test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := &KeychainStore{service: fmt.Sprintf("aiusage.test.%d.%d", os.Getpid(), time.Now().UnixNano()), account: "fixture"}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := exec.CommandContext(cleanup, securityPath, "delete-generic-password", "-s", store.service, "-a", store.account).Run(); err != nil {
			t.Error("test Keychain cleanup failed")
		}
	})
	for _, value := range []string{"fixture-key-one", "fixture-key-two"} {
		if err := store.Save(ctx, value); err != nil {
			t.Fatal("native save failed:", err)
		}
		got, err := store.Load(ctx)
		if err != nil || got != value {
			t.Fatal("native saved key did not round trip")
		}
	}
}
