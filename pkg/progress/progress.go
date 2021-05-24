package progress

import (
	"fmt"
	"sync"
	"time"
)

const minTickerTime = time.Second / 60

type Progress struct {
	OnStart   func()
	OnUpdate  ProgressFunc
	OnDone    ProgressFunc
	funcMutex sync.Mutex

	currentStat  Stat
	currentMutex sync.Mutex
	startTime    time.Time
	ticker       *time.Ticker
	cancel       chan struct{}
	once         sync.Once
	duration     time.Duration
	lastUpdate   time.Time

	running bool
}

// Stat
type Stat struct {
	Files  uint64
	Dirs   uint64
	Bytes  uint64
	Errors uint64
}

type ProgressFunc func(s Stat, runtime time.Duration, ticker bool)

func NewProgress(d time.Duration) *Progress {
	return &Progress{duration: d}
}

// Start resets and runs the progress reporter.
func (p *Progress) Start() {
	if p == nil || p.running {
		return
	}

	p.once = sync.Once{}
	p.cancel = make(chan struct{})
	p.running = true
	p.Reset()
	p.startTime = time.Now()
	p.ticker = time.NewTicker(p.duration)

	if p.OnStart != nil {
		p.OnStart()
	}
	go p.reporter()
}

// Reset resets all statistic counters to zero.
func (p *Progress) Reset() {
	if p == nil {
		return
	}

	if !p.running {
		panic("resetting a non-running Progress")
	}
	p.currentMutex.Lock()
	p.currentStat = Stat{}
	p.currentMutex.Unlock()
}

func (p *Progress) updateProgress(current Stat, ticker bool) {
	if p.OnUpdate == nil {
		return
	}

	p.funcMutex.Lock()
	p.OnUpdate(current, time.Since(p.startTime), ticker)
	p.funcMutex.Unlock()
}

func (p *Progress) reporter() {
	if p == nil {
		return
	}
	for {
		select {
		case <-p.ticker.C:
			p.currentMutex.Lock()
			current := p.currentStat
			p.currentMutex.Unlock()
			p.updateProgress(current, true)
		case <-p.cancel:
			p.ticker.Stop()
			return
		}
	}
}

// Report adds the statistics from s to the current state and tries to report
// the accumulated statistics via the feedback channel.
func (p *Progress) Report(s Stat) {
	if p == nil {
		return
	}

	if !p.running {
		panic("reporting in a non-running Progress")
	}
	p.currentMutex.Lock()
	p.currentStat.Add(s)
	current := p.currentStat
	needUpdate := false
	if time.Since(p.lastUpdate) > minTickerTime {
		p.lastUpdate = time.Now()
		needUpdate = true
	}
	p.currentMutex.Unlock()

	if needUpdate {
		p.updateProgress(current, false)
	}
}

func (p *Progress) Done() {
	if p == nil || !p.running {
		return
	}

	p.running = false
	p.once.Do(func() {
		close(p.cancel)
	})
	cur := p.currentStat
	if p.OnDone != nil {
		p.funcMutex.Lock()
		p.OnDone(cur, time.Since(p.startTime), false)
		p.funcMutex.Unlock()
	}
}

// Add accumulates other into s.
func (s *Stat) Add(other Stat) {
	s.Bytes += other.Bytes
	s.Dirs += other.Dirs
	s.Files += other.Files
	s.Errors += other.Errors
}

func (s Stat) String() string {
	b := float64(s.Bytes)
	var str string

	switch {
	case s.Bytes > 1<<40:
		str = fmt.Sprintf("%.3f TiB", b/(1<<40))
	case s.Bytes > 1<<30:
		str = fmt.Sprintf("%.3f GiB", b/(1<<30))
	case s.Bytes > 1<<20:
		str = fmt.Sprintf("%.3f MiB", b/(1<<20))
	case s.Bytes > 1<<10:
		str = fmt.Sprintf("%.3f KiB", b/(1<<10))
	default:
		str = fmt.Sprintf("%dB", s.Bytes)
	}
	return fmt.Sprintf("Stat(%d files, %d dirs, %d error, %v)",
		s.Files, s.Dirs, s.Errors, str)
}
