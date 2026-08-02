package terminal

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTaskOutputSinkReportsIndependentStreamOffsets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	chunks := make(chan OutputChunk, 8)
	manager := NewTaskManager()
	manager.SetOutputSink(func(chunk OutputChunk) {
		chunks <- chunk
	})
	task, err := manager.StartRemote(context.Background(), "rs_observer", "demo", t.TempDir(), "sleep 0.05; printf 'out-1'; printf 'err-1' >&2; printf 'out-2'")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("task did not exit")
	}

	received := collectExpectedOutputChunks(t, chunks, map[string]string{
		"stdout": "out-1out-2",
		"stderr": "err-1",
	})
	offsets := map[string]int64{}
	seen := map[string]string{}
	for _, chunk := range received {
		if chunk.TaskID != task.ID || chunk.RemoteSessionID != "rs_observer" || chunk.WorkspaceName != "demo" {
			t.Fatalf("chunk identity mismatch: %+v", chunk)
		}
		if chunk.Stream != "stdout" && chunk.Stream != "stderr" {
			t.Fatalf("unexpected stream: %+v", chunk)
		}
		if previous, ok := offsets[chunk.Stream]; ok && chunk.Offset < previous {
			t.Fatalf("offset regressed for %s: previous=%d current=%d", chunk.Stream, previous, chunk.Offset)
		}
		offsets[chunk.Stream] = chunk.Offset + int64(len(chunk.Data))
		seen[chunk.Stream] += string(chunk.Data)
	}
	if seen["stdout"] != "out-1out-2" || seen["stderr"] != "err-1" {
		t.Fatalf("output chunks=%+v", seen)
	}
}

func TestTaskOutputSinkReportsObservationIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell assertion is Unix-specific")
	}
	chunks := make(chan OutputChunk, 8)
	manager := NewTaskManager()
	manager.SetOutputSink(func(chunk OutputChunk) {
		chunks <- chunk
	})
	task, err := manager.StartRemoteWithObservation(
		context.Background(), "req_1", "command_execute",
		"rs_observer", "demo", t.TempDir(), "printf 'out'",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if task.RequestID != "req_1" || task.Tool != "command_execute" {
		t.Fatalf("task observation identity=%+v", task)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("task did not exit")
	}

	received := collectExpectedOutputChunks(t, chunks, map[string]string{
		"stdout": "out",
	})
	for _, chunk := range received {
		if chunk.RequestID != "req_1" || chunk.Tool != "command_execute" {
			t.Fatalf("observation identity=%+v", chunk)
		}
	}
	if len(received) == 0 {
		t.Fatal("received no observed output chunks")
	}
}

func collectExpectedOutputChunks(t *testing.T, chunks <-chan OutputChunk, expected map[string]string) []OutputChunk {
	t.Helper()
	received := make([]OutputChunk, 0, len(expected))
	seen := make(map[string]string, len(expected))
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()

	for {
		complete := len(seen) == len(expected)
		if complete {
			for stream, want := range expected {
				if seen[stream] != want {
					complete = false
					break
				}
			}
		}
		if complete {
			return received
		}

		select {
		case chunk := <-chunks:
			want, ok := expected[chunk.Stream]
			if !ok {
				t.Fatalf("unexpected output stream: %+v", chunk)
			}
			seen[chunk.Stream] += string(chunk.Data)
			if !strings.HasPrefix(want, seen[chunk.Stream]) {
				t.Fatalf("unexpected output for %s: got %q, want prefix of %q", chunk.Stream, seen[chunk.Stream], want)
			}
			received = append(received, chunk)
		case <-deadline.C:
			t.Fatalf("timed out collecting output: got=%+v want=%+v chunks=%+v", seen, expected, received)
		}
	}
}
