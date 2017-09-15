// Copyright 2017 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

// Package tester contains utilities for executing Rego tests.
package tester

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/topdown"

	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/loader"
	"github.com/open-policy-agent/opa/rego"
)

// TestPrefix declares the prefix for all rules.
const TestPrefix = "test_"

// Run executes all test cases found under files in path.
func Run(ctx context.Context, path ...string) ([]*Result, error) {
	ch, err := NewRunner().Paths(ctx, path...)
	if err != nil {
		return nil, err
	}
	result := []*Result{}
	for r := range ch {
		result = append(result, r)
	}
	return result, nil
}

// Result represents a single test case result.
type Result struct {
	Location *ast.Location `json:"location"`
	Package  string        `json:"package"`
	Name     string        `json:"name"`
	Fail     *interface{}  `json:"fail,omitempty"`
	Error    error         `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

func newResult(loc *ast.Location, pkg, name string, duration time.Duration) *Result {
	return &Result{
		Location: loc,
		Package:  pkg,
		Name:     name,
		Duration: duration,
	}
}

// Pass returns true if the test case passed.
func (r Result) Pass() bool {
	return r.Fail == nil && r.Error == nil
}

func (r *Result) String() string {
	return fmt.Sprintf("%v.%v: %v (%v)", r.Package, r.Name, r.outcome(), r.Duration/time.Microsecond)
}

func (r *Result) outcome() string {
	if r.Pass() {
		return "PASS"
	}
	if r.Fail != nil {
		return "FAIL"
	}
	return "ERROR"
}

func (r *Result) setFail(fail interface{}) {
	r.Fail = &fail
}

// Runner implements simple test discovery and execution.
type Runner struct {
	compiler *ast.Compiler
}

// NewRunner returns a new runner.
func NewRunner() *Runner {
	return &Runner{}
}

// SetCompiler sets the compiler used by the runner.
func (r *Runner) SetCompiler(compiler *ast.Compiler) *Runner {
	r.compiler = compiler
	return r
}

// Paths executes all tests contained in policies under the specified paths.
func (r *Runner) Paths(ctx context.Context, path ...string) (ch chan *Result, err error) {

	if r.compiler == nil {
		r.compiler = ast.NewCompiler()
	}

	result, err := loader.AllRegos(path)
	if err != nil {
		return nil, err
	}

	modules := map[string]*ast.Module{}
	for _, m := range result.Modules {
		modules[m.Name] = m.Parsed
	}

	return r.Modules(ctx, modules)
}

// Modules executes all tests contained in the specified modules.
func (r *Runner) Modules(ctx context.Context, modules map[string]*ast.Module) (ch chan *Result, err error) {

	filenames := make([]string, 0, len(modules))
	for name := range modules {
		filenames = append(filenames, name)
	}

	sort.Strings(filenames)

	if r.compiler.Compile(modules); r.compiler.Failed() {
		return nil, r.compiler.Errors
	}

	ch = make(chan *Result)

	go func() {
		defer close(ch)
		for _, name := range filenames {
			module := r.compiler.Modules[name]
			for _, rule := range module.Rules {
				if !strings.HasPrefix(string(rule.Head.Name), TestPrefix) {
					continue
				}
				tr, stop := r.runTest(ctx, module, rule)
				ch <- tr
				if stop {
					return
				}
			}
		}
	}()

	return ch, nil
}

func (r *Runner) runTest(ctx context.Context, mod *ast.Module, rule *ast.Rule) (*Result, bool) {

	rego := rego.New(
		rego.Compiler(r.compiler),
		rego.Query(rule.Path().String()),
	)

	t0 := time.Now()
	rs, err := rego.Eval(ctx)
	dt := time.Since(t0)

	tr := newResult(rule.Loc(), mod.Package.Path.String(), string(rule.Head.Name), dt)
	var stop bool

	if err != nil {
		tr.Error = err
		if err, ok := err.(*topdown.Error); ok && err.Code == topdown.CancelErr {
			stop = true
		}
	} else if len(rs) == 0 {
		tr.setFail(false)
	} else if b, ok := rs[0].Expressions[0].Value.(bool); !ok || !b {
		tr.setFail(rs[0].Expressions[0].Value)
	}

	return tr, stop
}