package workerowner

import (
	"context"
	"errors"
)

// WaitSource consumes requests from ONE already-authenticated registration.
// Host lifetime and source lifetime are separate: host cancellation seals this
// Owner, while source cancellation/EOF ends only this source's last owned work.
// It issues no grant and proves no registration/authentication. Its producer
// must retain the authenticated source mapping and supply each original finite
// Request.SourceContext; Approve must perform real protected policy admission.
// A source stop is not an atomic native authorization/revocation barrier.
//
// On ErrUnknown the caller retains Owner and its original native resources.
// Never create a replacement Owner or registration as cleanup recovery. This
// method does not reconnect, resume a Session or retry a failed Close.
func (o *Owner) WaitSource(host, source context.Context, requests <-chan *Request) error {
	if host == nil || source == nil || !o.ValidBudget() {
		return ErrRejected
	}
	life, cancel := context.WithCancel(host)
	stopSource := context.AfterFunc(source, cancel)
	defer stopSource()
	defer cancel()
	var last *Request
	finish := func(seal bool) error {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), o.budget)
		defer stop()
		if seal || host.Err() != nil {
			return o.Stop(cleanup)
		}
		if last == nil {
			return nil
		}
		err := o.StopRequest(cleanup, last)
		// Admission cannot replace an attempt until all of its original cleanup and
		// joins are confirmed. A stale request therefore already finished; it must
		// not stop or mark a newer source's request unknown.
		if errors.Is(err, ErrRejected) {
			err = nil
		}
		// Host cancellation may arrive during the source-only cleanup wait.
		// Seal the same Owner using the original cleanup context: no renewed
		// budget, replacement, or retry of the original Close.
		if host.Err() != nil {
			return errors.Join(err, o.Stop(cleanup))
		}
		return err
	}
	for {
		if host.Err() != nil {
			return finish(true)
		}
		if source.Err() != nil {
			return finish(false)
		}
		select {
		case <-host.Done():
			return finish(true)
		case <-source.Done():
			return finish(false)
		case r, ok := <-requests:
			if !ok {
				return finish(host.Err() != nil)
			}
			err := ErrRejected
			if host.Err() == nil && source.Err() == nil && r != nil && r.SourceContext != nil {
				if _, admitted := o.admit(life, r); admitted == nil {
					last, err = r, nil
				} else {
					err = admitted
				}
			}
			if r != nil && r.Reply != nil {
				select {
				case r.Reply <- err:
				default:
				}
			}
		}
	}
}
