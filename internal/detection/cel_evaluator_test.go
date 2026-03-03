package detection

import (
	"fmt"
	"testing"
)

func TestNewCELEvaluator(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}
	if eval == nil {
		t.Fatal("NewCELEvaluator() returned nil")
	}
	if eval.env == nil {
		t.Fatal("CELEvaluator.env is nil")
	}
}

func TestCompile_ValidExpression(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	prog, err := eval.Compile(`data.reason == "OOMKilled"`)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if prog == nil {
		t.Fatal("Compile() returned nil program")
	}
}

func TestCompile_InvalidExpression(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	_, err = eval.Compile(`this is not valid CEL !!!`)
	if err == nil {
		t.Fatal("Compile() expected error for invalid expression, got nil")
	}
}

func TestCompile_NonBoolReturn(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	_, err = eval.Compile(`data.reason`)
	if err == nil {
		t.Fatal("Compile() expected error for non-bool return type, got nil")
	}
}

func TestCompile_CachesProgram(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	expr := `data.reason == "OOMKilled"`
	prog1, err := eval.Compile(expr)
	if err != nil {
		t.Fatalf("Compile() first call error = %v", err)
	}

	prog2, err := eval.Compile(expr)
	if err != nil {
		t.Fatalf("Compile() second call error = %v", err)
	}

	// Both calls should return the same cached program
	if fmt.Sprintf("%p", prog1) != fmt.Sprintf("%p", prog2) {
		t.Error("Compile() did not return cached program on second call")
	}
}

func TestEvaluate_SimpleFieldEquals(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{
		"reason": "OOMKilled",
	}

	result, err := eval.Evaluate(`data.reason == "OOMKilled"`, data, nil, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result {
		t.Error("Evaluate() = false, want true")
	}
}

func TestEvaluate_SimpleFieldNotEquals(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{
		"reason": "CrashLoopBackOff",
	}

	result, err := eval.Evaluate(`data.reason == "OOMKilled"`, data, nil, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result {
		t.Error("Evaluate() = true, want false")
	}
}

func TestEvaluate_NestedFieldAccess(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{
		"status": map[string]interface{}{
			"phase": "Failed",
		},
	}

	result, err := eval.Evaluate(`data.status.phase == "Failed"`, data, nil, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result {
		t.Error("Evaluate() = false, want true")
	}
}

func TestEvaluate_HasField(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	tests := []struct {
		name       string
		data       map[string]interface{}
		expression string
		want       bool
	}{
		{
			name:       "field exists",
			data:       map[string]interface{}{"status": map[string]interface{}{"phase": "Running"}},
			expression: `has(data.status)`,
			want:       true,
		},
		{
			name:       "field does not exist",
			data:       map[string]interface{}{"other": "value"},
			expression: `has(data.status)`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eval.Evaluate(tt.expression, tt.data, nil, nil)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result != tt.want {
				t.Errorf("Evaluate() = %v, want %v", result, tt.want)
			}
		})
	}
}

func TestEvaluate_ListExists(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{
		"status": map[string]interface{}{
			"containerStatuses": []interface{}{
				map[string]interface{}{
					"name": "app",
					"lastState": map[string]interface{}{
						"terminated": map[string]interface{}{
							"reason":   "OOMKilled",
							"exitCode": 137,
						},
					},
				},
				map[string]interface{}{
					"name":      "sidecar",
					"lastState": map[string]interface{}{},
				},
			},
		},
	}

	expr := `
		has(data.status) &&
		has(data.status.containerStatuses) &&
		data.status.containerStatuses.exists(cs,
			has(cs.lastState) &&
			has(cs.lastState.terminated) &&
			cs.lastState.terminated.reason == "OOMKilled"
		)
	`

	result, err := eval.Evaluate(expr, data, nil, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result {
		t.Error("Evaluate() = false, want true (OOMKilled container exists)")
	}
}

func TestEvaluate_ListExists_NoMatch(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{
		"status": map[string]interface{}{
			"containerStatuses": []interface{}{
				map[string]interface{}{
					"name": "app",
					"lastState": map[string]interface{}{
						"terminated": map[string]interface{}{
							"reason":   "Error",
							"exitCode": 1,
						},
					},
				},
			},
		},
	}

	expr := `
		has(data.status) &&
		has(data.status.containerStatuses) &&
		data.status.containerStatuses.exists(cs,
			has(cs.lastState) &&
			has(cs.lastState.terminated) &&
			cs.lastState.terminated.reason == "OOMKilled"
		)
	`

	result, err := eval.Evaluate(expr, data, nil, nil)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result {
		t.Error("Evaluate() = true, want false (no OOMKilled container)")
	}
}

func TestEvaluate_ResourceVariable(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{"reason": "OOMKilled"}
	resource := map[string]interface{}{
		"kind":      "Pod",
		"namespace": "ariadna",
		"name":      "oom-simulator-abc123",
	}

	result, err := eval.Evaluate(
		`data.reason == "OOMKilled" && resource.kind == "Pod"`,
		data, resource, nil,
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result {
		t.Error("Evaluate() = false, want true")
	}
}

func TestEvaluate_LabelsVariable(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{"reason": "OOMKilled"}
	labels := map[string]interface{}{
		"eventType": "update",
	}

	result, err := eval.Evaluate(
		`data.reason == "OOMKilled" && labels.eventType == "update"`,
		data, nil, labels,
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result {
		t.Error("Evaluate() = false, want true")
	}
}

func TestEvaluate_NumericComparison(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	tests := []struct {
		name       string
		data       map[string]interface{}
		expression string
		want       bool
	}{
		{
			name:       "int greater than",
			data:       map[string]interface{}{"restartCount": 5},
			expression: `int(data.restartCount) > 3`,
			want:       true,
		},
		{
			name:       "int not greater than",
			data:       map[string]interface{}{"restartCount": 2},
			expression: `int(data.restartCount) > 3`,
			want:       false,
		},
		{
			name:       "double threshold",
			data:       map[string]interface{}{"errorRate": 0.08},
			expression: `double(data.errorRate) > 0.05`,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eval.Evaluate(tt.expression, tt.data, nil, nil)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result != tt.want {
				t.Errorf("Evaluate() = %v, want %v", result, tt.want)
			}
		})
	}
}

func TestEvaluate_LogicalOperators(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	data := map[string]interface{}{
		"reason": "OOMKilled",
		"phase":  "Failed",
	}

	tests := []struct {
		name       string
		expression string
		want       bool
	}{
		{
			name:       "AND both true",
			expression: `data.reason == "OOMKilled" && data.phase == "Failed"`,
			want:       true,
		},
		{
			name:       "AND one false",
			expression: `data.reason == "OOMKilled" && data.phase == "Running"`,
			want:       false,
		},
		{
			name:       "OR one true",
			expression: `data.reason == "OOMKilled" || data.phase == "Running"`,
			want:       true,
		},
		{
			name:       "NOT",
			expression: `!(data.reason == "Error")`,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := eval.Evaluate(tt.expression, data, nil, nil)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result != tt.want {
				t.Errorf("Evaluate() = %v, want %v", result, tt.want)
			}
		})
	}
}

func TestEvaluate_NilData(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	// Accessing a field on nil data should produce an evaluation error
	_, err = eval.Evaluate(`data.reason == "OOMKilled"`, nil, nil, nil)
	if err == nil {
		t.Error("Evaluate() expected error for nil data, got nil")
	}
}

func TestEvaluate_EmptyData(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	// Accessing a missing field should produce an evaluation error
	_, err = eval.Evaluate(`data.reason == "OOMKilled"`, map[string]interface{}{}, nil, nil)
	if err == nil {
		t.Error("Evaluate() expected error for missing field, got nil")
	}
}

func TestEvaluate_SafeAccessWithHas(t *testing.T) {
	eval, err := NewCELEvaluator()
	if err != nil {
		t.Fatalf("NewCELEvaluator() error = %v", err)
	}

	// Safe access using has() should not error even on empty data
	result, err := eval.Evaluate(
		`has(data.reason) && data.reason == "OOMKilled"`,
		map[string]interface{}{},
		nil, nil,
	)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result {
		t.Error("Evaluate() = true, want false (field doesn't exist)")
	}
}

// Ensure fmt is used (referenced in TestCompile_CachesProgram)
var _ = fmt.Sprintf
