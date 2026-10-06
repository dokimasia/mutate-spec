package probefail

import (
	"os"
	"testing"
	"time"
)

func TestPass(t *testing.T) {}

func TestError(t *testing.T) { t.Error("plain failure") }

func TestPanic(t *testing.T) { panic("panic in the test goroutine") }

func TestGoroutinePanic(t *testing.T) {
	go func() { panic("panic in another goroutine") }()
	time.Sleep(time.Second)
}

func TestExit1(t *testing.T) { os.Exit(1) }

func TestExit0(t *testing.T) { os.Exit(0) }

func TestSub(t *testing.T) {
	t.Run("inner", func(t *testing.T) { t.Error("subtest failure") })
}

func TestHang(t *testing.T) { select {} }
