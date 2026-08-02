package terminal

import (
	"context"
	"runtime"
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

	received := make([]OutputChunk, 0, 3)
	seenStreams := map[string]bool{}
	deadline := time.After(time.Second)
	for len(seenStreams) < 2 {
		select {
		case chunk := <-chunks:
			received = append(received, chunk)
			seenStreams[chunk.Stream] = true
		case <-deadline:
			t.Fatalf("received streams=%+v chunks=%+v", seenStreams, received)
		}
	}
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
	chunks := make(chan OutputChunk, 1)
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
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !task.Wait(waitCtx) {
		t.Fatal("task did not exit")
	}

	select {
	case chunk := <-chunks:
		if chunk.RequestID != "req_1" || chunk.Tool != "command_execute" {
			t.Fatalf("observation identity=%+v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observed output")
	}
}
