package agent

import "context"

// stubScriptRunner is a runner whose verdicts a test decides.
//
// A function per call, so a test can say "it fails, then it builds" — which is
// the whole behaviour under test and cannot be expressed by a fixed answer.
type stubScriptRunner func(source string) error

func (f stubScriptRunner) RunScript(_ context.Context, source string) error {
	if f == nil {
		return nil
	}
	return f(source)
}
