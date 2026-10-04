package accounts

import "context"

// credentialRefreshLock serializes every credential operation for one account.
// The non-cancellable methods preserve existing Lock/Unlock callers; request
// paths use LockContext so cancellation does not wait for another refresh.
type credentialRefreshLock struct {
	token chan struct{}
}

func newCredentialRefreshLock() *credentialRefreshLock {
	return &credentialRefreshLock{token: make(chan struct{}, 1)}
}

func (l *credentialRefreshLock) Lock()   { l.token <- struct{}{} }
func (l *credentialRefreshLock) Unlock() { <-l.token }

func (l *credentialRefreshLock) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.token <- struct{}{}:
		// Prefer a concurrent cancellation over starting a new refresh.
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
