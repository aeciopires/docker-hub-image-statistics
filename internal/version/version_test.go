package version

import (
	"runtime"
	"testing"
)

func TestGet(t *testing.T) {
	info := Get()
	if info.Version != Version || info.Commit != Commit || info.Date != Date {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.GoVersion != runtime.Version() {
		t.Fatalf("go version = %q, want %q", info.GoVersion, runtime.Version())
	}
}
