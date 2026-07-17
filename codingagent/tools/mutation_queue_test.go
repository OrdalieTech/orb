package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestFileMutationQueueSerializesSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same.txt")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mutex sync.Mutex
	var order []string

	appendOrder := func(value string) {
		mutex.Lock()
		order = append(order, value)
		mutex.Unlock()
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := WithFileMutationQueue(path, func() (struct{}, error) {
			appendOrder("first:start")
			close(firstStarted)
			<-releaseFirst
			appendOrder("first:end")
			return struct{}{}, nil
		})
		firstDone <- err
	}()
	<-firstStarted

	secondDone := make(chan error, 1)
	go func() {
		_, err := WithFileMutationQueue(path, func() (struct{}, error) {
			appendOrder("second:start")
			appendOrder("second:end")
			return struct{}{}, nil
		})
		secondDone <- err
	}()
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if want := []string{"first:start", "first:end", "second:start", "second:end"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestFileMutationQueueUsesRealPathForSymlinkAliases(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	alias := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = WithFileMutationQueue(target, func() (struct{}, error) {
			close(firstStarted)
			<-releaseFirst
			return struct{}{}, nil
		})
	}()
	<-firstStarted
	key, err := mutationQueueKey(target)
	if err != nil {
		t.Fatal(err)
	}
	mutationQueues.Lock()
	firstEntry := mutationQueues.byPath[key]
	mutationQueues.Unlock()
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_, _ = WithFileMutationQueue(alias, func() (struct{}, error) {
			close(secondStarted)
			return struct{}{}, nil
		})
	}()
	waitForQueueSuccessor(t, key, firstEntry)
	select {
	case <-secondStarted:
		t.Fatal("symlink alias did not wait for target queue")
	default:
	}
	close(releaseFirst)
	<-firstDone
	<-secondDone
}

func TestFileMutationQueueAllowsDifferentPathsInParallel(t *testing.T) {
	dir := t.TempDir()
	aStarted := make(chan struct{})
	releaseA := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		_, _ = WithFileMutationQueue(filepath.Join(dir, "a"), func() (struct{}, error) {
			close(aStarted)
			<-releaseA
			return struct{}{}, nil
		})
	}()
	<-aStarted
	bRan := false
	_, err := WithFileMutationQueue(filepath.Join(dir, "b"), func() (struct{}, error) {
		bRan = true
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bRan {
		t.Fatal("different path did not run")
	}
	close(releaseA)
	<-aDone
}

func TestFileMutationQueueReportsSymlinkLoopsLikeNode(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.Symlink(second, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Fatal(err)
	}
	_, err := WithFileMutationQueue(first, func() (struct{}, error) { return struct{}{}, nil })
	want := "ELOOP: too many symbolic links encountered, realpath '" + first + "'"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestFileMutationQueueRejectsNullPathLikeNodeRealpath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a\x00b")
	_, err := WithFileMutationQueue(path, func() (struct{}, error) { return struct{}{}, nil })
	want := "The argument 'path' must be a string, Uint8Array, or URL without null bytes. Received " + nodeInspectString(path)
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestNodeInspectStringUsesNodeQuoteAndControlEscapes(t *testing.T) {
	for input, want := range map[string]string{
		"a\x00b":       `'a\x00b'`,
		"a'\x00b":      `"a'\x00b"`,
		"a'\"\x00b":    "`a'\"\\x00b`",
		"a'\"`\x00b":   "'a\\'\"`\\x00b'",
		"a\n\t\v\f\rb": `'a\n\t\v\f\rb'`,
	} {
		if got := nodeInspectString(input); got != want {
			t.Fatalf("nodeInspectString(%q) = %q, want %q", input, got, want)
		}
	}
}
