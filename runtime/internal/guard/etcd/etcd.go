// Package etcd is the coordination backend a deployment configures, and the only package
// that imports the etcd client.
//
// It drives its own renewals rather than the library's keep-alive helpers: those signal a
// lost claim at the expiry itself, measured at 5.001 s for a 5 s claim
// (specs/002-guard/research.md §2), which leaves a holder nothing to stop its run in.
package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// Options are what the adapter needs beyond its client.
type Options struct {
	// Prefix is under what every key this deployment writes lives, so that two
	// deployments can share one etcd without sharing claims.
	Prefix string
	// Clock is read only to stamp when a claim was taken, which nothing compares.
	Clock guard.Clock
	// Seam, when set, is called between reading a name's last tick and sending the
	// transaction that takes the claim (C13). Only the contract suite sets one.
	Seam guard.Seam
	// Watching, when set, is called once Released has begun to watch a held claim. Only the
	// contract suite sets one, to know Released is waiting without sleeping (C10).
	Watching func(name string)
}

// Coordinator holds claims in etcd.
type Coordinator struct {
	client   *clientv3.Client
	prefix   string
	clock    guard.Clock
	seam     guard.Seam
	watching func(name string)
}

var _ guard.Coordinator = (*Coordinator)(nil)

// New returns a coordinator over client.
func New(client *clientv3.Client, opts Options) *Coordinator {
	clock := opts.Clock
	if clock == nil {
		clock = guard.SystemClock()
	}
	return &Coordinator{
		client:   client,
		prefix:   strings.TrimSuffix(opts.Prefix, "/") + "/",
		clock:    clock,
		seam:     opts.Seam,
		watching: opts.Watching,
	}
}

// Reach implements guard.Coordinator.
func (c *Coordinator) Reach() string { return guard.ReachCrossHost }

func (c *Coordinator) claimKey(name string) string { return c.prefix + "claims/" + name }
func (c *Coordinator) tickKey(name string) string  { return c.prefix + "ticks/" + name }

// rateKey is one of a playbook's rate slots, 0 <= i < the declared limit's runs.
func (c *Coordinator) rateKey(name string, i int) string {
	return fmt.Sprintf("%srate/%s/%d", c.prefix, name, i)
}

// holderValue is the claim key's value: read back only to name the holder in a refusal.
type holderValue struct {
	Host     string    `json:"host"`
	Instance string    `json:"instance"`
	RunID    string    `json:"run"`
	Taken    time.Time `json:"taken"`
}

// tickValue is the tick key's value. Only DueAt is ever compared (FR-129).
type tickValue struct {
	DueAt    time.Time `json:"due_at"`
	Host     string    `json:"host"`
	Instance string    `json:"instance"`
	RunID    string    `json:"run"`
}

func (t tickValue) holder() guard.Holder {
	return guard.Holder{Host: t.Host, Instance: t.Instance, RunID: t.RunID}
}

// unavailable is every failure that is not a refusal: a backend that cannot be reached,
// one that refused, one that did not answer in time. Never held — a trigger told a claim
// is held waits on one nobody holds (C11, FR-107).
func unavailable(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", guard.ErrUnavailable, what, err)
}

// revokeBound is what a revocation gets when the call it belongs to has already used its
// own deadline. A revocation that fails too costs the playbook its claim expiry, no more.
const revokeBound = 2 * time.Second

// Acquire implements guard.Coordinator.
func (c *Coordinator) Acquire(ctx context.Context, req guard.AcquireRequest) (guard.Claim, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	if req.Expiry <= 0 || req.Expiry%time.Second != 0 {
		return nil, fmt.Errorf("a claim expiry of %s is not a whole number of seconds", req.Expiry)
	}

	lease, err := c.client.Grant(ctx, int64(req.Expiry/time.Second))
	if err != nil {
		return nil, unavailable("granting a lease", err)
	}
	granted := time.Duration(lease.TTL) * time.Second
	if err := CheckGrant(req.Expiry, granted); err != nil {
		c.revoke(ctx, lease.ID)
		return nil, err
	}

	// A rate slot's own lease, granted alongside the claim's so both can be written in one
	// transaction (C8). Its window is minutes or hours, far past the server's minimum grant,
	// so nothing here compares what came back with what was asked, unlike the claim's.
	var rateLease clientv3.LeaseID
	if req.Rate != nil {
		rateGrant, err := c.client.Grant(ctx, int64(req.Rate.Per/time.Second))
		if err != nil {
			c.revoke(ctx, lease.ID)
			return nil, unavailable("granting a rate slot's lease", err)
		}
		rateLease = rateGrant.ID
	}

	claim, err := c.take(ctx, req, lease.ID, granted, rateLease)
	if err != nil {
		// Including a decision that ran out of time: the transaction can commit after
		// the client has stopped listening, so what was granted is given back rather
		// than left to block the playbook for its whole expiry.
		c.revoke(ctx, lease.ID)
		if req.Rate != nil {
			c.revoke(ctx, rateLease)
		}
		return nil, err
	}
	return claim, nil
}

// CheckGrant refuses an expiry shorter than the one asked for (C12). A server grants at
// least one and a half election timeouts whatever it is asked, and a client cannot know a
// remote server's, so what came back is compared with what was requested.
func CheckGrant(asked, granted time.Duration) error {
	if granted < asked {
		return fmt.Errorf("%w: the backend granted an expiry of %s, less than the %s asked for",
			guard.ErrUnavailable, granted, asked)
	}
	return nil
}

// state is the claim, the last tick and the rate slots of one name, read together.
type state struct {
	holder  *holderValue
	tick    *tickValue
	tickRev int64
	// slotFree is which of a declared limit's slots are free, len(slotFree) == req.Rate.Runs.
	// Nil when req.Rate is nil.
	slotFree []bool
}

// ops is what one read of req.Name needs to decide it: the claim, the last tick for a
// scheduled trigger, and every one of a declared limit's rate slots. Both the read below
// and the failed transaction's Else branch use it, so a fresh read and a refused write
// parse identically (stateOf).
func (c *Coordinator) ops(req guard.AcquireRequest) []clientv3.Op {
	ops := []clientv3.Op{clientv3.OpGet(c.claimKey(req.Name))}
	if req.Trigger.Kind == guard.KindSchedule {
		ops = append(ops, clientv3.OpGet(c.tickKey(req.Name)))
	}
	if req.Rate != nil {
		for i := range req.Rate.Runs {
			ops = append(ops, clientv3.OpGet(c.rateKey(req.Name, i)))
		}
	}
	return ops
}

func (c *Coordinator) read(ctx context.Context, req guard.AcquireRequest) (state, error) {
	// One transaction with no condition: every key is read at one revision, so the tick
	// revision compared below belongs to the same snapshot as the claim.
	resp, err := c.client.Txn(ctx).Then(c.ops(req)...).Commit()
	if err != nil {
		return state{}, unavailable("reading the claim", err)
	}
	return stateOf(resp.Responses, req)
}

func stateOf(responses []*etcdserverpb.ResponseOp, req guard.AcquireRequest) (state, error) {
	var read state
	at := 0
	if kvs := responses[at].GetResponseRange().Kvs; len(kvs) > 0 {
		var holder holderValue
		if err := json.Unmarshal(kvs[0].Value, &holder); err != nil {
			return state{}, fmt.Errorf("reading the holder of a claim: %w", err)
		}
		read.holder = &holder
	}
	at++
	if req.Trigger.Kind == guard.KindSchedule {
		if kvs := responses[at].GetResponseRange().Kvs; len(kvs) > 0 {
			var tick tickValue
			if err := json.Unmarshal(kvs[0].Value, &tick); err != nil {
				return state{}, fmt.Errorf("reading a recorded tick: %w", err)
			}
			read.tick, read.tickRev = &tick, kvs[0].ModRevision
		}
		at++
	}
	if req.Rate != nil {
		read.slotFree = make([]bool, req.Rate.Runs)
		for i := range req.Rate.Runs {
			read.slotFree[i] = len(responses[at].GetResponseRange().Kvs) == 0
			at++
		}
	}
	return read, nil
}

// tickRan reports whether a scheduled trigger's occurrence is at or before the one
// recorded. The comparison is on the instants a schedule computed, never on a clock.
func tickRan(req guard.AcquireRequest, recorded *tickValue) bool {
	if req.Trigger.Kind != guard.KindSchedule || recorded == nil {
		return false
	}
	return !req.Trigger.DueAt.After(recorded.DueAt)
}

// freeSlot is the lowest-numbered rate slot not taken, or -1 when every one of them is
// (FR-116).
func freeSlot(slotFree []bool) int {
	for i, free := range slotFree {
		if free {
			return i
		}
	}
	return -1
}

// take sends the transaction that makes the claim, the tick, a rate slot and their
// refusals one step (C8, C13).
func (c *Coordinator) take(
	ctx context.Context, req guard.AcquireRequest, lease clientv3.LeaseID, granted time.Duration,
	rateLease clientv3.LeaseID,
) (guard.Claim, error) {
	scheduled := req.Trigger.Kind == guard.KindSchedule
	claimKey, tickKey := c.claimKey(req.Name), c.tickKey(req.Name)

	holder, err := json.Marshal(holderValue{
		Host: req.Holder.Host, Instance: req.Holder.Instance, RunID: req.Holder.RunID,
		Taken: c.clock.Wall(),
	})
	if err != nil {
		return nil, err
	}
	tick, err := json.Marshal(tickValue{
		DueAt: req.Trigger.DueAt.UTC(),
		Host:  req.Holder.Host, Instance: req.Holder.Instance, RunID: req.Holder.RunID,
	})
	if err != nil {
		return nil, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, unavailable("deciding", err)
		}
		read, err := c.read(ctx, req)
		if err != nil {
			return nil, err
		}
		// A tick that has already run is refused before anything is sent, because the
		// transaction below would otherwise take the claim for it. The holder is what is
		// named when there is one: it is usually the run of that very tick. When the rate
		// window is full as well, this is what is returned rather than the limit (C13):
		// naming the limit would suggest raising it had let the tick run.
		if tickRan(req, read.tick) {
			if read.holder != nil {
				return nil, guard.HeldBy(holderOf(read.holder))
			}
			return nil, guard.TickRanAs(read.tick.DueAt, read.tick.holder())
		}
		slot := -1
		if req.Rate != nil {
			if slot = freeSlot(read.slotFree); slot < 0 {
				return nil, guard.RateLimitedBy(*req.Rate)
			}
		}

		if scheduled && c.seam != nil {
			c.seam(ctx, req.Name)
		}

		conditions := []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(claimKey), "=", 0)}
		writes := []clientv3.Op{clientv3.OpPut(claimKey, string(holder), clientv3.WithLease(lease))}
		if scheduled {
			conditions = append(conditions, clientv3.Compare(clientv3.ModRevision(tickKey), "=", read.tickRev))
			writes = append(writes, clientv3.OpPut(tickKey, string(tick)))
		}
		if req.Rate != nil {
			slotKey := c.rateKey(req.Name, slot)
			conditions = append(conditions, clientv3.Compare(clientv3.CreateRevision(slotKey), "=", 0))
			writes = append(writes, clientv3.OpPut(slotKey, req.Holder.RunID, clientv3.WithLease(rateLease)))
		}
		committed, err := c.client.Txn(ctx).
			If(conditions...).
			Then(writes...).
			Else(c.ops(req)...).
			Commit()
		if err != nil {
			return nil, unavailable("taking the claim", err)
		}
		if committed.Succeeded {
			// The claim key did not exist, so the revision that created it is the one
			// this transaction wrote at: the fencing token (C6).
			return &claim{
				coordinator: c, name: req.Name, lease: lease,
				token: committed.Header.Revision, granted: granted,
			}, nil
		}

		// A failed transaction does not say which comparison lost, so the refusal is
		// decided from what it read back rather than from the failure.
		fresh, err := stateOf(committed.Responses, req)
		if err != nil {
			return nil, err
		}
		if fresh.holder != nil {
			return nil, guard.HeldBy(holderOf(fresh.holder))
		}
		if tickRan(req, fresh.tick) {
			return nil, guard.TickRanAs(fresh.tick.DueAt, fresh.tick.holder())
		}
		// Overtaken by a host that has since let go, or that took the rate slot this one
		// was about to: read and decide again, inside the same deadline.
	}
}

func holderOf(value *holderValue) guard.Holder {
	return guard.Holder{Host: value.Host, Instance: value.Instance, RunID: value.RunID}
}

// revoke gives back a lease this call is not going to use. When the call's own deadline
// is gone the revocation is left to a context of its own, so that the call still returns
// by its deadline (C4).
func (c *Coordinator) revoke(ctx context.Context, lease clientv3.LeaseID) {
	if ctx.Err() == nil {
		if _, err := c.client.Revoke(ctx, lease); err == nil {
			return
		}
	}
	go func() {
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeBound)
		defer cancel()
		_, _ = c.client.Revoke(detached, lease)
	}()
}

// Released implements guard.Coordinator: it watches the claim key rather than polling it.
func (c *Coordinator) Released(ctx context.Context, name string) error {
	key := c.claimKey(name)
	current, err := c.client.Get(ctx, key)
	if err != nil {
		return unavailable("reading the claim", err)
	}
	if len(current.Kvs) == 0 {
		return nil
	}
	watching := c.client.Watch(ctx, key, clientv3.WithRev(current.Header.Revision+1))
	if c.watching != nil {
		c.watching(name)
	}
	for change := range watching {
		if err := change.Err(); err != nil {
			return unavailable("watching the claim", err)
		}
		for _, event := range change.Events {
			if event.Type == clientv3.EventTypeDelete {
				return nil
			}
		}
	}
	return unavailable("watching the claim", ctx.Err())
}

type claim struct {
	coordinator *Coordinator
	name        string
	lease       clientv3.LeaseID
	token       int64
	granted     time.Duration
}

func (c *claim) Token() int64          { return c.token }
func (c *claim) Expiry() time.Duration { return c.granted }

// Renew implements guard.Claim: one attempt, under the caller's context. A lease the
// server no longer has is a loss and not an attempt worth repeating (C5).
func (c *claim) Renew(ctx context.Context) error {
	if _, err := c.coordinator.client.KeepAliveOnce(ctx, c.lease); err != nil {
		if lost(err) {
			return fmt.Errorf("%w: the lease of %s is gone: %w", guard.ErrLost, c.name, err)
		}
		return unavailable("renewing the claim on "+c.name, err)
	}
	return nil
}

func lost(err error) bool {
	return errors.Is(err, rpctypes.ErrLeaseNotFound) ||
		strings.Contains(err.Error(), "requested lease not found")
}

// Fence implements guard.Claim: is the claim holding this token still the current one?
func (c *claim) Fence(ctx context.Context) error {
	key := c.coordinator.claimKey(c.name)
	committed, err := c.coordinator.client.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", c.token)).
		Then(clientv3.OpGet(key)).
		Commit()
	if err != nil {
		return unavailable("fencing on the claim of "+c.name, err)
	}
	if !committed.Succeeded {
		return fmt.Errorf("%w: %s is no longer held under token %d", guard.ErrLost, c.name, c.token)
	}
	return nil
}

// Release implements guard.Claim. Releasing twice is not an error, and safety never
// depends on a release happening: the lease is what makes a claim end.
func (c *claim) Release(ctx context.Context) error {
	if _, err := c.coordinator.client.Revoke(ctx, c.lease); err != nil {
		if lost(err) {
			return nil
		}
		return unavailable("releasing the claim on "+c.name, err)
	}
	return nil
}
