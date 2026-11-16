// Package apc reads the panel's private control channel out of its input.
// Rex's Lua plugin types `ESC _ amqui;<verb> ESC \` into the panel's
// terminal (block.method write) to pause it before minimizing, resume it,
// zoom it, or switch its tab. The grammar:
//
//	ESC _ amqui; ( pause | resume | zoom | tab:<overview|members|mail|graph|epics> ) ESC \
//
// Parser takes these sequences out of a byte stream however the stream is
// cut into reads, so none of their bytes ever reaches the key handling.
// Unknown verbs and tabs are consumed and ignored. Any other APC (a Kitty
// graphics reply, `ESC _ G ...`) and every other byte pass through
// unchanged.
package apc

import "bytes"

// Verb is what a command asks for.
type Verb int

const (
	Pause Verb = iota + 1
	Resume
	Zoom
	Tab
)

func (v Verb) String() string {
	switch v {
	case Pause:
		return "pause"
	case Resume:
		return "resume"
	case Zoom:
		return "zoom"
	case Tab:
		return "tab"
	}
	return "verb?"
}

// Command is one control sequence. Tab is set for the Tab verb only.
type Command struct {
	Verb Verb
	Tab  string
}

// Tabs are the tab names `tab:<name>` accepts.
var Tabs = []string{"overview", "members", "mail", "graph", "epics"}

// Prefix opens every sequence of this channel.
const Prefix = "\x1b_amqui;"

// Encode is the sequence for c, as the Lua plugin writes it.
func Encode(c Command) string {
	if c.Verb == Tab {
		return Prefix + "tab:" + c.Tab + "\x1b\\"
	}
	return Prefix + c.Verb.String() + "\x1b\\"
}

// maxPayload bounds a sequence's payload. The longest verb is 12 bytes; a
// sequence that runs past this was cut off, and holding on to it would eat
// the keys typed after it.
const maxPayload = 32

type state int

const (
	ground  state = iota
	esc           // a lone ESC, held: it may open a sequence
	prefix        // inside Prefix, held
	payload       // after Prefix: the verb, held
	payEsc        // an ESC inside the payload: ST, or a broken sequence
)

// Parser filters a byte stream. Its zero value is ready.
type Parser struct {
	st  state
	buf []byte // the held bytes of the sequence in progress
}

// Feed reads the next bytes of the stream and returns those to pass on and
// the commands completed in them. Bytes that may open or belong to a
// sequence of this channel are held until it completes or breaks.
func (p *Parser) Feed(in []byte) (out []byte, cmds []Command) {
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch p.st {
		case ground:
			if c == 0x1b {
				p.st, p.buf = esc, append(p.buf[:0], c)
				continue
			}
			out = append(out, c)
		case esc:
			if c == '_' {
				p.st, p.buf = prefix, append(p.buf, c)
				continue
			}
			out = append(out, p.buf...)
			p.reset()
			i-- // c starts afresh: it may be another ESC
		case prefix:
			p.buf = append(p.buf, c)
			if !bytes.HasPrefix([]byte(Prefix), p.buf) {
				// Another program's APC: give it back and let it pass.
				out = append(out, p.buf[:len(p.buf)-1]...)
				p.reset()
				i--
				continue
			}
			if len(p.buf) == len(Prefix) {
				p.st = payload
			}
		case payload:
			switch {
			case c == 0x1b:
				p.st = payEsc
			case len(p.buf)-len(Prefix) >= maxPayload || !verbByte(c):
				// Broken: drop it, and read c as ordinary input.
				p.reset()
				i--
			default:
				p.buf = append(p.buf, c)
			}
		case payEsc:
			if c == '\\' {
				if cmd, ok := parse(string(p.buf[len(Prefix):])); ok {
					cmds = append(cmds, cmd)
				}
				p.reset()
				continue
			}
			// Broken by another sequence: drop it, and that ESC opens c's.
			p.st, p.buf = esc, append(p.buf[:0], 0x1b)
			i--
		}
	}
	return out, cmds
}

// HoldsEsc reports whether the stream so far ends in a lone ESC: the Esc key,
// or the first byte of a sequence whose rest has not arrived.
func (p *Parser) HoldsEsc() bool { return p.st == esc }

// Flush gives back a held lone ESC, deciding that it was the Esc key. A
// partial sequence of this channel is dropped.
func (p *Parser) Flush() []byte {
	var out []byte
	if p.st == esc {
		out = []byte{0x1b}
	}
	p.reset()
	return out
}

func (p *Parser) reset() { p.st, p.buf = ground, p.buf[:0] }

func verbByte(c byte) bool { return c >= 'a' && c <= 'z' || c == ':' }

func parse(s string) (Command, bool) {
	switch s {
	case "pause":
		return Command{Verb: Pause}, true
	case "resume":
		return Command{Verb: Resume}, true
	case "zoom":
		return Command{Verb: Zoom}, true
	}
	if name, ok := bytes.CutPrefix([]byte(s), []byte("tab:")); ok {
		for _, t := range Tabs {
			if string(name) == t {
				return Command{Verb: Tab, Tab: t}, true
			}
		}
	}
	return Command{}, false
}
