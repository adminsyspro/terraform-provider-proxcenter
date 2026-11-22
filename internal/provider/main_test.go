package provider

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Acceptance runs (TF_ACC) talk to a real ProxCenter: real waits.
	if os.Getenv("TF_ACC") == "" {
		pollFirst, pollMax = 5*time.Millisecond, 20*time.Millisecond
		vmListLag = 200 * time.Millisecond
	}
	os.Exit(m.Run())
}
