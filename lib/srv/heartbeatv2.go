/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package srv

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gravitational/trace"
	log "github.com/sirupsen/logrus"

	"github.com/gravitational/teleport/api/client/proto"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/api/utils/retryutils"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/inventory"
	"github.com/gravitational/teleport/lib/inventory/metadata"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/lib/utils/interval"
)

// SSHServerHeartbeatConfig configures the HeartbeatV2 for an ssh server.
type SSHServerHeartbeatConfig struct {
	// InventoryHandle is used to send heartbeats.
	InventoryHandle inventory.DownstreamHandle
	// GetServer gets the latest server spec.
	GetServer func() *types.ServerV2

	// -- below values are all optional

	// Announcer is a fallback used to perform basic upsert-style heartbeats
	// if the control stream is unavailable.
	//
	// DELETE IN: 11.0 (only exists for back-compat with v9 auth servers)
	Announcer auth.Announcer
	// OnHeartbeat is a per-attempt callback (optional).
	OnHeartbeat func(error)
	// AnnounceInterval is the interval at which heartbeats are attempted (optional).
	AnnounceInterval time.Duration
	// PollInterval is the interval at which checks for change are performed (optional).
	PollInterval time.Duration
}

func (c *SSHServerHeartbeatConfig) Check() error {
	if c.InventoryHandle == nil {
		return trace.BadParameter("missing required parameter InventoryHandle for ssh heartbeat")
	}
	if c.GetServer == nil {
		return trace.BadParameter("missing required parameter GetServer for ssh heartbeat")
	}
	return nil
}

func NewSSHServerHeartbeat(cfg SSHServerHeartbeatConfig) (*HeartbeatV2, error) {
	if err := cfg.Check(); err != nil {
		return nil, trace.Wrap(err)
	}

	var metadataPtr atomic.Pointer[metadata.Metadata]
	inner := &sshServerHeartbeatV2{
		getMetadata: metadata.Get,
		announcer:   cfg.Announcer,
	}
	inner.getServer = func(ctx context.Context) *types.ServerV2 {
		server := cfg.GetServer()

		if meta := metadataPtr.Load(); meta == nil {
			go func() {
				meta, err := inner.getMetadata(ctx)
				if err != nil {
					if ctx.Err() == nil {
						log.Warnf("Failed to get metadata: %v", err)
					}
				} else if meta != nil && meta.CloudMetadata != nil {
					// Set the metadata immediately to give the heartbeat
					// a chance to use it.
					server.SetCloudMetadata(meta.CloudMetadata)
					metadataPtr.CompareAndSwap(nil, meta)
				}
			}()
		} else if meta.CloudMetadata != nil {
			// Server isn't cached between heartbeats, so set the metadata again.
			server.SetCloudMetadata(meta.CloudMetadata)
		}

		return server
	}

	return newHeartbeatV2(cfg.InventoryHandle, inner, heartbeatV2Config{
		onHeartbeatInner: cfg.OnHeartbeat,
		announceInterval: cfg.AnnounceInterval,
		pollInterval:     cfg.PollInterval,
	}), nil
}

// hbv2TestEvent is a basic event type used to monitor/await
// specific events within the HeartbeatV2 type's operations
// during tests.
type hbv2TestEvent string

const (
	hbv2AnnounceOk  hbv2TestEvent = "announce-ok"
	hbv2AnnounceErr hbv2TestEvent = "announce-err"

	hbv2FallbackOk  hbv2TestEvent = "fallback-ok"
	hbv2FallbackErr hbv2TestEvent = "fallback-err"

	hbv2PollSame hbv2TestEvent = "poll-same"
	hbv2PollDiff hbv2TestEvent = "poll-diff"

	hbv2Start hbv2TestEvent = "hb-start"
	hbv2Close hbv2TestEvent = "hb-close"

	hbv2AnnounceInterval hbv2TestEvent = "hb-announce-interval"

	hbv2FallbackBackoff hbv2TestEvent = "hb-fallback-backoff"

	hbv2NoFallback hbv2TestEvent = "no-fallback"

	hbv2OnHeartbeatOk  = "on-heartbeat-ok"
	hbv2OnHeartbeatErr = "on-heartbeat-err"
)

// newHeartbeatV2 configures a new HeartbeatV2 instance to wrap a given implementation.
func newHeartbeatV2(handle inventory.DownstreamHandle, inner heartbeatV2Driver, cfg heartbeatV2Config) *HeartbeatV2 {
	cfg.SetDefaults()
	ctx, cancel := context.WithCancel(handle.CloseContext())
	return &HeartbeatV2{
		heartbeatV2Config: cfg,
		handle:            handle,
		inner:             inner,
		testAnnounce:      make(chan chan struct{}),
		closeContext:      ctx,
		cancel:            cancel,
	}
}

// HeartbeatV2 heartbeats presence via the inventory control stream.
type HeartbeatV2 struct {
	heartbeatV2Config

	handle inventory.DownstreamHandle
	inner  heartbeatV2Driver

	testAnnounce chan chan struct{}

	closeContext context.Context
	cancel       context.CancelFunc

	// ----------------------------------------------------------
	// all fields below this point are local variables for the
	// background goroutine and not safe for access from anywhere
	// else.

	announceFailed error
	fallbackFailed error
	icsUnavailable error

	announce      *interval.Interval
	poll          *interval.Interval
	degradedCheck *interval.Interval

	// fallbackBackoffTime approximately replicate the backoff used by heartbeat V1 when an announce
	// fails. It can be removed once we remove the fallback announce operation, since control-stream
	// based heartbeats inherit backoff from the stream handle and don't need special backoff.
	fallbackBackoffTime time.Time

	// shouldAnnounce is set to true if announce interval elapses, or if polling informs us of a change.
	// it stays true until a *successful* announce. the value of this variable is preserved when going
	// between the inner control stream based announce loop and the outer upsert based announce loop.
	// the initial value is false to give the control stream a chance to become available.  the first
	// call to poll always returns true, so we still heartbeat within a few seconds of startup regardless.
	shouldAnnounce bool

	// announceWaiters are used in tests to wait for an announce operation to occur
	announceWaiters []chan struct{}
}

type heartbeatV2Config struct {
	announceInterval time.Duration
	pollInterval     time.Duration
	onHeartbeatInner func(error)

	// -- below values only used in tests

	fallbackBackoff       time.Duration
	testEvents            chan hbv2TestEvent
	degradedCheckInterval time.Duration
}

func (c *heartbeatV2Config) SetDefaults() {
	if c.announceInterval == 0 {
		// default to 2/3rds of the default server expiry.  since we use the "seventh jitter"
		// for our periodics, that translates to an average interval of ~6m, a slight increase
		// from the average of ~5m30s that was used for V1 ssh server heartbeats.
		c.announceInterval = 2 * (apidefaults.ServerAnnounceTTL / 3)
	}
	if c.pollInterval == 0 {
		c.pollInterval = defaults.HeartbeatCheckPeriod
	}
	if c.fallbackBackoff == 0 {
		// only set externally during tests
		c.fallbackBackoff = time.Minute
	}

	if c.degradedCheckInterval == 0 {
		// a lot of integration tests rely on overriding ServerKeepAliveTTL to modify how
		// quickly teleport detects that it is in a degraded state.
		c.degradedCheckInterval = apidefaults.ServerKeepAliveTTL()
	}
}

// noSenderErr is used to periodically trigger "degraded state" events when the control
// stream has no sender available.
var noSenderErr = trace.Errorf("no control stream sender available")

func (h *HeartbeatV2) run() {
	// note: these errors are never actually displayed, but onHeartbeat expects an error,
	// so we just allocate something reasonably descriptive once.
	h.announceFailed = trace.Errorf("control stream heartbeat failed (variant=%T)", h.inner)
	h.fallbackFailed = trace.Errorf("upsert fallback heartbeat failed (variant=%T)", h.inner)
	h.icsUnavailable = trace.Errorf("ics unavailable for heartbeat (variant=%T)", h.inner)

	// set up interval for forced announcement (i.e. heartbeat even if state is unchanged).
	h.announce = interval.New(interval.Config{
		FirstDuration: utils.HalfJitter(h.announceInterval),
		Duration:      h.announceInterval,
		Jitter:        retryutils.NewSeventhJitter(),
	})
	defer h.announce.Stop()

	// set up interval for polling the inner heartbeat impl for changes.
	h.poll = interval.New(interval.Config{
		FirstDuration: utils.HalfJitter(h.pollInterval),
		Duration:      h.pollInterval,
		Jitter:        retryutils.NewSeventhJitter(),
	})
	defer h.poll.Stop()

	// set a "degraded state check". this is a bit hacky, but since the old-style heartbeat would
	// cause a DegradedState event to be emitted once every ServerKeepAliveTTL, we now rely on
	// that (at least in tests, possibly elsewhere), as an indicator that auth connectivity is
	// down.  Since we no longer perform keepalives, we instead simply emit an error on this
	// interval when we don't have a healthy control stream.
	// TODO(fspmarshall): find a more elegant solution to this problem.
	h.degradedCheck = interval.New(interval.Config{
		Duration: h.degradedCheckInterval,
	})
	defer h.degradedCheck.Stop()

	h.testEvent(hbv2Start)
	defer h.testEvent(hbv2Close)

	for {
		// outer loop performs announcement via the fallback method (used for backwards compatibility
		// with older auth servers). Not all drivers support fallback.

		if h.shouldAnnounce {
			if h.inner.SupportsFallback() {
				if time.Now().After(h.fallbackBackoffTime) {
					if ok := h.inner.FallbackAnnounce(h.closeContext); ok {
						h.testEvent(hbv2FallbackOk)
						// reset announce interval and state on successful announce
						h.announce.Reset()
						h.degradedCheck.Reset()
						h.shouldAnnounce = false
						h.onHeartbeat(nil)

						// unblock tests waiting on an announce operation
						for _, waiter := range h.announceWaiters {
							close(waiter)
						}
						h.announceWaiters = nil
					} else {
						h.testEvent(hbv2FallbackErr)
						// announce failed, enter a backoff state.
						h.fallbackBackoffTime = time.Now().Add(utils.SeventhJitter(h.fallbackBackoff))
						h.onHeartbeat(h.fallbackFailed)
					}
				} else {
					h.testEvent(hbv2FallbackBackoff)
				}
			} else {
				h.testEvent(hbv2NoFallback)
			}
		}

		// wait for a sender to become available. until one does, announce/poll
		// events are handled via the FallbackAnnounce method which doesn't rely on having a
		// healthy sender stream.
		select {
		case sender := <-h.handle.Sender():
			// sender is available, hand off to the primary run loop
			h.runWithSender(sender)
			h.degradedCheck.Reset()
		case <-h.announce.Next():
			h.testEvent(hbv2AnnounceInterval)
			h.shouldAnnounce = true
		case <-h.poll.Next():
			if h.inner.Poll(h.closeContext) {
				h.testEvent(hbv2PollDiff)
				h.shouldAnnounce = true
			} else {
				h.testEvent(hbv2PollSame)
			}
		case <-h.degradedCheck.Next():
			if !h.inner.SupportsFallback() || (!h.inner.Poll(h.closeContext) && !h.shouldAnnounce) {
				// if we don't have fallback and/or aren't planning to hit the fallback
				// soon, then we need to emit a heartbeat error in order to inform the
				// rest of teleport that we are in a degraded state.
				h.onHeartbeat(noSenderErr)
			}
		case ch := <-h.testAnnounce:
			h.shouldAnnounce = true
			h.announceWaiters = append(h.announceWaiters, ch)
		case <-h.closeContext.Done():
			return
		}
	}
}

func (h *HeartbeatV2) runWithSender(sender inventory.DownstreamSender) {
	// poll immediately when sender becomes available.
	if h.inner.Poll(h.closeContext) {
		h.shouldAnnounce = true
	}

	for {
		if h.shouldAnnounce {
			if ok := h.inner.Announce(h.closeContext, sender); ok {
				h.testEvent(hbv2AnnounceOk)
				// reset announce interval and state on successful announce
				h.announce.Reset()
				h.degradedCheck.Reset()
				h.shouldAnnounce = false
				h.onHeartbeat(nil)

				// unblock tests waiting on an announce operation
				for _, waiter := range h.announceWaiters {
					close(waiter)
				}
				h.announceWaiters = nil
			} else {
				h.testEvent(hbv2AnnounceErr)
				h.onHeartbeat(h.announceFailed)
			}
		}

		select {
		case <-sender.Done():
			// sender closed, yield to the outer loop which will wait for
			// a new sender to be available.
			return
		case <-h.announce.Next():
			h.testEvent(hbv2AnnounceInterval)
			h.shouldAnnounce = true
		case <-h.poll.Next():
			if h.inner.Poll(h.closeContext) {
				h.testEvent(hbv2PollDiff)
				h.shouldAnnounce = true
			} else {
				h.testEvent(hbv2PollSame)
			}
		case <-h.degradedCheck.Next():
			if !h.inner.Poll(h.closeContext) && !h.shouldAnnounce {
				// its been a while since we announced and we are not in a retry/announce
				// state now, so clear up any degraded state.
				h.onHeartbeat(nil)
			}
		case waiter := <-h.testAnnounce:
			h.shouldAnnounce = true
			h.announceWaiters = append(h.announceWaiters, waiter)
		case <-h.closeContext.Done():
			return
		}
	}
}

func (h *HeartbeatV2) testEvent(event hbv2TestEvent) {
	if h.testEvents == nil {
		return
	}
	h.testEvents <- event
}

func (h *HeartbeatV2) Run() error {
	h.run()
	return nil
}

func (h *HeartbeatV2) Close() error {
	h.cancel()
	return nil
}

// ForceSend is used in tests to trigger an announce and block
// until it one successfully completes or the provided timeout is reached.
func (h *HeartbeatV2) ForceSend(timeout time.Duration) error {
	timeoutC := time.After(timeout)
	waiter := make(chan struct{})
	select {
	case <-timeoutC:
		return trace.Errorf("timeout waiting to trigger announce")
	case h.testAnnounce <- waiter:
	}

	select {
	case <-timeoutC:
		return trace.Errorf("timeout waiting for announce success")
	case <-waiter:
		return nil
	}
}

func (h *HeartbeatV2) onHeartbeat(err error) {
	if err != nil {
		h.testEvent(hbv2OnHeartbeatErr)
	} else {
		h.testEvent(hbv2OnHeartbeatOk)
	}
	if h.onHeartbeatInner == nil {
		return
	}
	h.onHeartbeatInner(err)
}

// heartbeatV2Driver is the pluggable core of the HeartbeatV2 type. A service needing to use HeartbeatV2 should
// have a corresponding implementation of heartbeatV2Driver.
type heartbeatV2Driver interface {
	// Poll is used to check for changes since last *successful* heartbeat (note: Poll should also
	// return true if no heartbeat has been successfully executed yet).
	Poll(ctx context.Context) (changed bool)
	// FallbackAnnounce is called if a heartbeat is needed but the inventory control stream is
	// unavailable. In theory this is probably only relevant for cases where the auth has been
	// downgraded to an earlier version than it should have been, but its still preferable to
	// make an effort to heartbeat in that case, so we're including it for now.
	FallbackAnnounce(ctx context.Context) (ok bool)
	// Announce attempts to heartbeat via the inventory control stream.
	Announce(ctx context.Context, sender inventory.DownstreamSender) (ok bool)
	// SupportsFallback checks if the driver supports fallback.
	SupportsFallback() bool
}

type metadataGetter func(ctx context.Context) (*metadata.Metadata, error)

// sshServerHeartbeatV2 is the heartbeatV2 implementation for ssh servers.
type sshServerHeartbeatV2 struct {
	getServer   func(ctx context.Context) *types.ServerV2
	getMetadata metadataGetter
	announcer   auth.Announcer
	prev        *types.ServerV2
}

func (h *sshServerHeartbeatV2) Poll(ctx context.Context) (changed bool) {
	if h.prev == nil {
		return true
	}
	return services.CompareServers(h.getServer(ctx), h.prev) == services.Different
}

func (h *sshServerHeartbeatV2) SupportsFallback() bool {
	return h.announcer != nil
}

func (h *sshServerHeartbeatV2) FallbackAnnounce(ctx context.Context) (ok bool) {
	if h.announcer == nil {
		return false
	}
	server := h.getServer(ctx)
	_, err := h.announcer.UpsertNode(ctx, server)
	if err != nil {
		log.Warnf("Failed to perform fallback heartbeat for ssh server: %v", err)
		return false
	}
	h.prev = server
	return true
}

func (h *sshServerHeartbeatV2) Announce(ctx context.Context, sender inventory.DownstreamSender) (ok bool) {
	server := h.getServer(ctx)
	err := sender.Send(ctx, proto.InventoryHeartbeat{
		SSHServer: h.getServer(ctx),
	})
	if err != nil {
		log.Warnf("Failed to perform inventory heartbeat for ssh server: %v", err)
		return false
	}
	h.prev = server
	return true
}
