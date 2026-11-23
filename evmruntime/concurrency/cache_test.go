package concurrency_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/andreyvit/evm/evmruntime/concurrency"
)

func TestExpiryAndInvalidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c concurrency.Cache[string, string]
		first := c.Set("k", "same", time.Now().Add(time.Minute))
		current := c.Set("k", "same", time.Now().Add(time.Minute))
		c.InvalidateIfCurrent("k", first)
		if e, ok := c.Get("k"); !ok || e != current {
			t.Fatal(e, ok)
		}
		time.Sleep(time.Minute)
		if _, ok := c.Get("k"); ok {
			t.Fatal("entry survived its expiry")
		}
		next := c.Set("k", "same", time.Time{})
		c.InvalidateIfCurrent("k", current)
		c.InvalidateIfCurrent("k", next)
		if _, ok := c.Get("k"); ok {
			t.Fatal("current entry survived invalidation")
		}
	})
}

func TestOverlappingLoads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := concurrency.NewCache[int, int]()
		done := make(chan struct{})
		var calls atomic.Int64
		var wg sync.WaitGroup
		load := func() (int, time.Time, error) { calls.Add(1); <-done; return 42, time.Time{}, nil }
		for range 8 {
			wg.Go(func() {
				e, err := c.GetOrLoad(t.Context(), 0, load)
				if err != nil || e.Value != 42 {
					t.Error(e, err)
				}
			})
		}
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		wg.Go(func() {
			_, err := c.GetOrLoad(ctx, 0, load)
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		})
		synctest.Wait()
		cancel()
		synctest.Wait()
		c.Set(0, 7, time.Time{})
		close(done)
		wg.Wait()
		e, _ := c.Get(0)
		if calls.Load() != 1 || e.Value != 7 {
			t.Fatal(calls.Load(), e)
		}
	})
}

func TestFailedLoaderReleasesWaiter(t *testing.T) {
	for _, panics := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c := concurrency.NewCache[int, int]()
			done := make(chan struct{})
			go func() {
				defer func() {
					if (recover() != nil) != panics {
						t.Error("unexpected panic result")
					}
				}()
				_, err := c.GetOrLoad(t.Context(), 1, func() (int, time.Time, error) {
					<-done
					if panics {
						panic("failed")
					}
					return 0, time.Time{}, errors.New("failed")
				})
				if err == nil {
					t.Error("missing error")
				}
			}()
			synctest.Wait()
			go func() {
				e, err := c.GetOrLoad(t.Context(), 1, func() (int, time.Time, error) { return 7, time.Time{}, nil })
				if err != nil || e.Value != 7 {
					t.Error(e, err)
				}
			}()
			synctest.Wait()
			close(done)
			synctest.Wait()
		})
	}
}
