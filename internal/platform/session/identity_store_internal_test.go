package session

import "testing"

func TestNewIdentityStore_NilLoggerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("newIdentityStore(pool, nil) did not panic")
		}
	}()
	newIdentityStore(nil, nil)
}
