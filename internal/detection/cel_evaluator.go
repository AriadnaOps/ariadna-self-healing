package detection

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// CELEvaluator compiles and evaluates CEL expressions against detection input data.
//
// CEL expressions are compiled once at scenario load time and reused for every evaluation.
// The compiled program (cel.Program) is safe for concurrent use.
//
// Variables available in expressions:
//   - data:     map<string, dyn>  — full resource data (status, spec, metadata, etc.)
//   - resource: map<string, dyn>  — resource reference (kind, namespace, name, etc.)
//   - labels:   map<string, dyn>  — metadata labels from the detection input
type CELEvaluator struct {
	env     *cel.Env
	cache   map[string]cel.Program
	cacheMu sync.RWMutex
}

// NewCELEvaluator creates a new CEL evaluator with the standard environment.
// The environment defines which variables and functions are available in expressions.
func NewCELEvaluator() (*CELEvaluator, error) {
	env, err := cel.NewEnv(
		cel.Variable("data", cel.DynType),
		cel.Variable("resource", cel.DynType),
		cel.Variable("labels", cel.DynType),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	return &CELEvaluator{
		env:   env,
		cache: make(map[string]cel.Program),
	}, nil
}

// Compile compiles a CEL expression and caches the resulting program.
// Returns an error if the expression is invalid or does not evaluate to a boolean.
// Safe for concurrent use.
func (e *CELEvaluator) Compile(expression string) (cel.Program, error) {
	e.cacheMu.RLock()
	if prog, ok := e.cache[expression]; ok {
		e.cacheMu.RUnlock()
		return prog, nil
	}
	e.cacheMu.RUnlock()

	ast, issues := e.env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compilation error: %w", issues.Err())
	}

	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("CEL expression must return bool, got %s", ast.OutputType())
	}

	prog, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program creation error: %w", err)
	}

	e.cacheMu.Lock()
	e.cache[expression] = prog
	e.cacheMu.Unlock()

	return prog, nil
}

// Evaluate evaluates a previously compiled CEL expression against the provided data.
// Returns the boolean result or an error if evaluation fails.
func (e *CELEvaluator) Evaluate(expression string, data, resource, labels map[string]interface{}) (bool, error) {
	prog, err := e.Compile(expression)
	if err != nil {
		return false, err
	}

	return EvalProgram(prog, data, resource, labels)
}

// EvalProgram evaluates a compiled CEL program against the provided variables.
// Useful when callers cache the compiled program themselves.
func EvalProgram(prog cel.Program, data, resource, labels map[string]interface{}) (bool, error) {
	activation := map[string]interface{}{
		"data":     data,
		"resource": resource,
		"labels":   labels,
	}

	out, _, err := prog.Eval(activation)
	if err != nil {
		return false, fmt.Errorf("CEL evaluation error: %w", err)
	}

	return boolFromRef(out)
}

// boolFromRef extracts a bool from a CEL ref.Val.
func boolFromRef(val ref.Val) (bool, error) {
	if val.Type() != types.BoolType {
		return false, fmt.Errorf("CEL expression returned %s, expected bool", val.Type())
	}
	b, ok := val.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression returned non-bool value: %v", val.Value())
	}
	return b, nil
}
