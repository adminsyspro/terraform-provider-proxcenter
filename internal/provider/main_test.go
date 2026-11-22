package provider

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	pollFirst, pollMax = 5*time.Millisecond, 20*time.Millisecond
	os.Exit(m.Run())
}
