package coordinatorwait

import "time"

// Clock keeps the adapter independent of any provider or Kubernetes runtime.
// Tests adapt their fake clock to these two operations.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type RealClock struct{}

func (RealClock) Now() time.Time                 { return time.Now() }
func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }
