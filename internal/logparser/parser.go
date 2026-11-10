package logparser

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type LogEntry struct {
	Timestamp time.Time
	Content   string
	Level     Level
}

type LogCounter struct {
	Level    Level
	Hash     string
	Sample   string
	Messages int
}

// OverflowPatternHash is the pattern hash under which messages are counted once a parser
// has reached Options.MaxPatterns distinct patterns.
const OverflowPatternHash = "overflow"

// Options bound the memory a single parser may retain.
type Options struct {
	// MaxPatterns caps distinct warning/error patterns tracked per parser. Messages that
	// would create a new pattern beyond the cap are counted under OverflowPatternHash.
	// 0 means unlimited (the upstream behavior).
	MaxPatterns int
	// MaxSampleLen truncates the stored sample message of each pattern (UTF-8 safe).
	// 0 means the full message (up to the multiline limit, 64KiB) is kept.
	MaxSampleLen int
	// DisablePatterns skips pattern extraction and counting entirely; messages are still
	// assembled (multiline) and passed to the callback with an empty pattern hash.
	DisablePatterns bool
}

// DefaultOptions are used by NewParser. They are set once at startup by the agent.
var DefaultOptions = Options{}

type Parser struct {
	decoder Decoder
	opts    Options

	patterns   map[patternKey]*patternStat
	overflowed int
	lock       sync.RWMutex

	multilineCollector *MultilineCollector

	stop func()

	onMsgCb OnMsgCallbackF
}

type OnMsgCallbackF func(ts time.Time, level Level, patternHash string, msg string)

func NewParser(ch <-chan LogEntry, decoder Decoder, onMsgCallback OnMsgCallbackF, multilineCollectorTimeout time.Duration) *Parser {
	return NewParserWithOptions(ch, decoder, onMsgCallback, multilineCollectorTimeout, DefaultOptions)
}

func NewParserWithOptions(ch <-chan LogEntry, decoder Decoder, onMsgCallback OnMsgCallbackF, multilineCollectorTimeout time.Duration, opts Options) *Parser {
	p := &Parser{
		decoder:  decoder,
		opts:     opts,
		patterns: map[patternKey]*patternStat{},
		onMsgCb:  onMsgCallback,
	}
	ctx, stop := context.WithCancel(context.Background())
	p.stop = stop
	p.multilineCollector = NewMultilineCollector(ctx, multilineCollectorTimeout, multilineCollectorLimit)

	go func() {
		var err error
		for {
			select {
			case <-ctx.Done():
				return
			case entry := <-ch:
				if p.decoder != nil {
					if entry.Content, err = p.decoder.Decode(entry.Content); err != nil {
						continue
					}
				}
				p.multilineCollector.Add(entry)
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-p.multilineCollector.Messages:
				p.inc(msg)
			}
		}
	}()

	return p
}

func (p *Parser) Stop() {
	p.stop()
}

func (p *Parser) inc(msg Message) {
	if p.opts.DisablePatterns {
		if p.onMsgCb != nil {
			p.onMsgCb(msg.Timestamp, msg.Level, "", msg.Content)
		}
		return
	}
	p.lock.Lock()
	defer p.lock.Unlock()

	if msg.Level == LevelUnknown || msg.Level == LevelDebug || msg.Level == LevelInfo {
		key := patternKey{level: msg.Level, hash: ""}
		if stat := p.patterns[key]; stat == nil {
			p.patterns[key] = &patternStat{}
		}
		p.patterns[key].messages++
		if p.onMsgCb != nil {
			p.onMsgCb(msg.Timestamp, msg.Level, "", msg.Content)
		}
		return
	}

	pattern := NewPattern(msg.Content)
	key := patternKey{level: msg.Level, hash: pattern.Hash()}
	stat := p.patterns[key]
	if stat == nil {
		for k, ps := range p.patterns {
			if k.level == msg.Level && ps.pattern != nil && ps.pattern.WeakEqual(pattern) {
				stat = ps
				break
			}
		}
		if stat == nil {
			if p.opts.MaxPatterns > 0 && len(p.patterns) >= p.opts.MaxPatterns {
				p.overflowed++
				key = patternKey{level: msg.Level, hash: OverflowPatternHash}
				stat = p.patterns[key]
				if stat == nil {
					// The overflow buckets (one per level) may exceed MaxPatterns by at most
					// the number of levels; they never hold a pattern, only a counter.
					stat = &patternStat{sample: "pattern limit reached"}
					p.patterns[key] = stat
				}
			} else {
				stat = &patternStat{pattern: pattern, sample: truncateUtf8(msg.Content, p.opts.MaxSampleLen)}
				p.patterns[key] = stat
			}
		}
	}
	if p.onMsgCb != nil {
		p.onMsgCb(msg.Timestamp, msg.Level, key.hash, msg.Content)
	}
	stat.messages++
}

// Overflowed returns how many messages were counted under OverflowPatternHash because the
// pattern table was full.
func (p *Parser) Overflowed() int {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.overflowed
}

// PatternCount returns the number of tracked patterns.
func (p *Parser) PatternCount() int {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return len(p.patterns)
}

func truncateUtf8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return strings.Clone(s[:max])
}

func (p *Parser) GetCounters() []LogCounter {
	p.lock.RLock()
	defer p.lock.RUnlock()
	res := make([]LogCounter, 0, len(p.patterns))
	for k, ps := range p.patterns {
		res = append(res, LogCounter{Level: k.level, Hash: k.hash, Sample: ps.sample, Messages: ps.messages})
	}
	return res
}

type patternKey struct {
	level Level
	hash  string
}

type patternStat struct {
	pattern  *Pattern
	sample   string
	messages int
}
