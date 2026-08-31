package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenSharedElectsOneOwnerAndFollowerCanUseIt(t *testing.T) {
	socketPath := shortSocketPath(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const instances = 4
	transports := make([]*SharedTransport, instances)
	errs := make([]error, instances)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range instances {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			transports[index], errs[index] = OpenShared(ctx, socketPath)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("OpenShared[%d]: %v", i, err)
		}
		defer transports[i].Close()
	}

	ownerCount := 0
	followerIndex := -1
	for i, transport := range transports {
		if transport.currentOwner() != nil {
			ownerCount++
		} else {
			followerIndex = i
		}
	}
	if ownerCount != 1 || followerIndex == -1 {
		t.Fatalf("owners = %d, follower = %d", ownerCount, followerIndex)
	}

	done := make(chan struct {
		result json.RawMessage
		err    error
	}, 1)
	go func() {
		result, err := transports[followerIndex].Do(ctx, OperationSessionIdentity, nil)
		done <- struct {
			result json.RawMessage
			err    error
		}{result, err}
	}()
	operation := pollOperation(t, socketPath)
	postOperationResult(t, socketPath, operation.RequestID, true, "", json.RawMessage(`{"authenticated":true}`))

	select {
	case outcome := <-done:
		if outcome.err != nil || string(outcome.result) != `{"authenticated":true}` {
			t.Fatalf("result = %s, err = %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower call did not return")
	}
}

func TestFollowerTakesOwnershipAfterOwnerStops(t *testing.T) {
	socketPath := shortSocketPath(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	owner, follower := first, second
	if second.currentOwner() != nil {
		owner, follower = second, first
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := follower.Do(ctx, OperationBasketGet, nil)
		done <- err
	}()
	waitForSocket(t, socketPath)
	operation := pollOperation(t, socketPath)
	postOperationResult(t, socketPath, operation.RequestID, true, "", json.RawMessage(`{"items":[]}`))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if follower.currentOwner() == nil {
		t.Fatal("follower did not take ownership")
	}
}

func TestSharedStateIsVisibleAcrossProcessesAndSurvivesFailover(t *testing.T) {
	socketPath := shortSocketPath(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	owner, follower := first, second
	if second.currentOwner() != nil {
		owner, follower = second, first
	}
	value := json.RawMessage(`{"store_id":"660500","basket_id":"basket-9"}`)
	if err := owner.StoreState(ctx, "shopping:account-1", value); err != nil {
		t.Fatal(err)
	}
	got, found, err := follower.LoadState(ctx, "shopping:account-1")
	if err != nil || !found || string(got) != string(value) {
		t.Fatalf("LoadState() = %s, %v, %v", got, found, err)
	}

	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	got, found, err = follower.LoadState(ctx, "shopping:account-1")
	if err != nil || !found || string(got) != string(value) {
		t.Fatalf("LoadState() after failover = %s, %v, %v", got, found, err)
	}
	if follower.currentOwner() == nil {
		t.Fatal("follower did not take ownership")
	}
}

func TestSharedStateLockSerializesAccountMutations(t *testing.T) {
	directory := t.TempDir()
	first, err := lockSharedState(t.Context(), directory, "shopping:account")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		unlock, lockErr := lockSharedState(t.Context(), directory, "shopping:account")
		if lockErr == nil {
			acquired <- unlock
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second mutation acquired account lock early")
	case <-time.After(30 * time.Millisecond):
	}
	first()
	select {
	case unlock := <-acquired:
		unlock()
	case <-time.After(time.Second):
		t.Fatal("second mutation did not acquire released account lock")
	}
}

func TestElectionLoserWaitsForTheNewOwner(t *testing.T) {
	socketPath := shortSocketPath(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	owner, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	firstFollower, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstFollower.Close()
	secondFollower, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFollower.Close()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	for _, follower := range []*SharedTransport{firstFollower, secondFollower} {
		go func(follower *SharedTransport) {
			_, err := follower.Do(ctx, OperationBasketGet, nil)
			done <- err
		}(follower)
	}
	waitForSocket(t, socketPath)
	for range 2 {
		operation := pollOperation(t, socketPath)
		postOperationResult(t, socketPath, operation.RequestID, true, "", json.RawMessage(`{"items":[]}`))
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseCancelsQueuedOwnerAndFollowerCalls(t *testing.T) {
	t.Run("owner", func(t *testing.T) {
		socketPath := shortSocketPath(t)
		transport, err := OpenShared(t.Context(), socketPath)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := transport.Do(context.Background(), OperationBasketGet, nil)
			done <- err
		}()
		if err := transport.Close(); err != nil {
			t.Fatal(err)
		}
		assertCanceledCall(t, done)
	})

	t.Run("follower", func(t *testing.T) {
		socketPath := shortSocketPath(t)
		owner, err := OpenShared(t.Context(), socketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		follower, err := OpenShared(t.Context(), socketPath)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := follower.Do(context.Background(), OperationBasketGet, nil)
			done <- err
		}()
		if err := follower.Close(); err != nil {
			t.Fatal(err)
		}
		assertCanceledCall(t, done)
	})
}

func assertCanceledCall(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		var coded CodedError
		if !errors.As(err, &coded) || coded.Code() != "canceled" {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call remained blocked after Close")
	}
}

func waitForSocket(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Lstat(socketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bridge socket was not created")
}

func TestSharedTransportDoesNotRetryAnInFlightCallAfterOwnerStops(t *testing.T) {
	socketPath := shortSocketPath(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	owner, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := OpenShared(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()

	done := make(chan error, 1)
	go func() {
		_, err := follower.Do(ctx, OperationBasketApply, json.RawMessage(`{"items":[]}`))
		done <- err
	}()
	_ = pollOperation(t, socketPath)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		var coded CodedError
		if !errors.As(err, &coded) {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call did not return after owner stopped")
	}
	if follower.currentOwner() != nil {
		t.Fatal("ambiguous in-flight call triggered automatic failover")
	}
}

func TestOpenSharedRejectsUnsafeOwnerLock(t *testing.T) {
	socketPath := shortSocketPath(t)
	lockPath := filepath.Join(filepath.Dir(socketPath), lockFilename)
	if err := os.WriteFile(lockPath, []byte("unsafe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenShared(t.Context(), socketPath); err == nil {
		t.Fatal("expected unsafe owner lock rejection")
	}
}

func TestServeOwnerCleanupCancelsLifecycleBeforeReleasingOwnership(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	ownerContext, cancelOwner := context.WithCancel(t.Context())
	owner := &sharedOwner{server: server, ctx: ownerContext, cancel: cancelOwner, done: make(chan struct{})}
	transport := &SharedTransport{owner: owner}

	transport.serveOwner(ownerContext, owner)
	if ownerContext.Err() == nil {
		t.Fatal("owner lifecycle remained active after Serve ended")
	}
	if transport.currentOwner() != nil {
		t.Fatal("ownership remained visible after Serve ended")
	}
}

func TestElectionCancellationReturnsCanceled(t *testing.T) {
	socketPath := shortSocketPath(t)
	if _, err := prepareRuntimeDirectory(socketPath); err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := acquireOwnerLock(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("owner lock was not acquired")
	}
	defer releaseOwnerLock(lock)
	follower, err := OpenShared(t.Context(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Close()
	callContext, cancelCall := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := follower.Do(callContext, OperationBasketGet, nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelCall()
	assertCanceledCall(t, done)
}
