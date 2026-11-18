package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
)

// EnvelopeError is the adapter refusing a run before it connects: the one
// `{"ok":false,"v":2,"error":{…}}` line `stream panel` writes instead of
// frames when its arguments are wrong or its guard refuses the caller
// (rex-amq-adapter spec §3.3, codes in §3.4). Code is the stable part;
// Message is for the human.
type EnvelopeError struct {
	Code      string
	Message   string
	Hint      string
	Retryable bool
	// NextAction is the command the adapter suggests, "" when it has none.
	NextAction string
}

func (e *EnvelopeError) Error() string {
	s := fmt.Sprintf("rex-amq-adapter refused: %s: %s", e.Code, e.Message)
	if e.NextAction != "" {
		s += " (next: " + e.NextAction + ")"
	}
	return s
}

// errEnvelopeMalformed is a failure envelope this package cannot read.
var errEnvelopeMalformed = errors.New("adapter: malformed failure envelope")

// decodeEnvelope reads line as the adapter's pre-connect failure envelope.
// It reports false for any line that is not one (a frame, or something that
// is neither), which the frame decoder then judges.
func decodeEnvelope(line []byte) (*EnvelopeError, bool, error) {
	var env struct {
		OK    *bool `json:"ok"`
		Error *struct {
			Code       string  `json:"code"`
			Message    string  `json:"message"`
			Hint       string  `json:"hint"`
			Retryable  bool    `json:"retryable"`
			NextAction *string `json:"next_action"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &env) != nil || env.OK == nil || *env.OK {
		return nil, false, nil
	}
	if env.Error == nil || env.Error.Code == "" {
		return nil, true, fmt.Errorf("%w: %s", errEnvelopeMalformed, line)
	}
	e := &EnvelopeError{Code: env.Error.Code, Message: env.Error.Message, Hint: env.Error.Hint, Retryable: env.Error.Retryable}
	if env.Error.NextAction != nil {
		e.NextAction = *env.Error.NextAction
	}
	return e, true, nil
}
