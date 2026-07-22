package tty

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/bkum/weftly/internal/events"
	"github.com/bkum/weftly/internal/secrets"
)

// JSONRenderer emits one JSON object per event on Out, for --json mode.
type JSONRenderer struct {
	Out     io.Writer
	Secrets *secrets.Registry
}

func NewJSON(w io.Writer, sec *secrets.Registry) *JSONRenderer {
	return &JSONRenderer{Out: w, Secrets: sec}
}

func (r *JSONRenderer) Handle(e events.Event) {
	typeName := fmt.Sprintf("%T", e)
	// strip package qualifier so consumers see e.g. "StepLog"
	for i := len(typeName) - 1; i >= 0; i-- {
		if typeName[i] == '.' {
			typeName = typeName[i+1:]
			break
		}
	}
	if log, ok := e.(events.StepLog); ok && r.Secrets != nil {
		log.Line = r.Secrets.Mask(log.Line)
		e = log
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintf(r.Out, "{\"type\":%q,\"event\":%s}\n", typeName, payload)
}
