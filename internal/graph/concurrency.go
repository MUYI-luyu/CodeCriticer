package graph

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"
)

const (
	maxConcurrencyDepth     = 7
	maxConcurrencyFunctions = 64
	maxConcurrencyFacts     = 240
)

// ConcurrencyFact is a compiler-derived program fact. It intentionally does
// not contain a bug kind or verdict: the Agent remains responsible for the
// causal conclusion.
type ConcurrencyFact struct {
	File      string
	Line      int
	Kind      string
	Detail    string
	Symbol    string
	Precision string
}

type resourceRef struct {
	ID        string
	Precision string
}

type channelInfo struct {
	Capacity string
	Basis    string
}

type heldLock struct {
	Resource resourceRef
	Mode     string
}

type heldState struct {
	Must map[string]heldLock
	May  map[string]heldLock
}

type lockOperation struct {
	Action   string
	Mode     string
	Resource resourceRef
	Method   string
	Try      bool
}

type concurrencyInspector struct {
	idx         *Index
	cg          *callgraph.Graph
	facts       []ConcurrencyFact
	seenFacts   map[string]bool
	visited     map[string]bool
	channels    map[string]channelInfo
	functions   int
	truncated   bool
	dynamicSeen bool
}

// InspectConcurrency returns a bounded concurrency slice rooted at ref. The
// slice contains operations, call/spawn edges, path-held locksets, resource
// provenance, and explicit uncertainty. It never reports that a bug exists.
func (idx *Index) InspectConcurrency(ref SymbolRef) ([]ConcurrencyFact, error) {
	fn := idx.find(ref)
	if fn == nil {
		return nil, fmt.Errorf("找不到唯一函数 %s；请提供声明行号", ref.Name)
	}
	inspector := &concurrencyInspector{
		idx:       idx,
		cg:        idx.refinedCallGraph(),
		seenFacts: make(map[string]bool),
		visited:   make(map[string]bool),
		channels:  make(map[string]channelInfo),
	}
	inspector.indexChannelOrigins()
	inspector.walk(fn, nil, newHeldState(), []string{shortFunction(fn)}, 0)
	if inspector.truncated {
		inspector.addAtFunction(fn, "analysis_uncertainty", "complete=false; reason=analysis_budget_exceeded", "incomplete")
	}
	if inspector.dynamicSeen {
		inspector.addAtFunction(fn, "analysis_uncertainty", "dynamic calls are conservative possible targets; absence of a target is not proof of absence", "vta_overapproximation")
	}
	sort.SliceStable(inspector.facts, func(i, j int) bool {
		if inspector.facts[i].File != inspector.facts[j].File {
			return inspector.facts[i].File < inspector.facts[j].File
		}
		if inspector.facts[i].Line != inspector.facts[j].Line {
			return inspector.facts[i].Line < inspector.facts[j].Line
		}
		return inspector.facts[i].Kind < inspector.facts[j].Kind
	})
	return inspector.facts, nil
}

func newHeldState() heldState {
	return heldState{Must: make(map[string]heldLock), May: make(map[string]heldLock)}
}

func cloneHeldState(in heldState) heldState {
	out := newHeldState()
	for key, value := range in.Must {
		out.Must[key] = value
	}
	for key, value := range in.May {
		out.May[key] = value
	}
	return out
}

func heldKey(lock heldLock) string {
	return lock.Mode + ":" + lock.Resource.ID
}

func addHeld(state *heldState, lock heldLock, must bool) {
	key := heldKey(lock)
	state.May[key] = lock
	if must {
		state.Must[key] = lock
	}
}

func removeHeld(state *heldState, resource resourceRef, mode string) {
	for key, lock := range state.May {
		if lock.Resource.ID == resource.ID && (mode == "" || lock.Mode == mode) {
			delete(state.May, key)
			delete(state.Must, key)
		}
	}
}

func mergeHeld(current, incoming heldState) heldState {
	out := newHeldState()
	for key, lock := range current.May {
		out.May[key] = lock
	}
	for key, lock := range incoming.May {
		out.May[key] = lock
	}
	for key, lock := range current.Must {
		if _, ok := incoming.Must[key]; ok {
			out.Must[key] = lock
		}
	}
	return out
}

func equalHeld(left, right heldState) bool {
	if len(left.Must) != len(right.Must) || len(left.May) != len(right.May) {
		return false
	}
	for key := range left.Must {
		if _, ok := right.Must[key]; !ok {
			return false
		}
	}
	for key := range left.May {
		if _, ok := right.May[key]; !ok {
			return false
		}
	}
	return true
}

func (c *concurrencyInspector) walk(fn *ssa.Function, env map[ssa.Value]resourceRef, initial heldState, path []string, depth int) {
	if fn == nil || fn.Blocks == nil || !c.repoFunction(fn) {
		return
	}
	if depth > maxConcurrencyDepth || c.functions >= maxConcurrencyFunctions || len(c.facts) >= maxConcurrencyFacts {
		c.truncated = true
		return
	}
	visitKey := fn.String() + "|" + heldSignature(initial) + "|" + environmentSignature(env)
	if c.visited[visitKey] {
		return
	}
	c.visited[visitKey] = true
	c.functions++

	entries := c.blockEntries(fn, env, initial)
	for _, block := range fn.Blocks {
		state, reachable := entries[block]
		if !reachable {
			continue
		}
		state = cloneHeldState(state)
		for _, instruction := range block.Instrs {
			if len(c.facts) >= maxConcurrencyFacts {
				c.truncated = true
				return
			}
			call, isCall := instruction.(ssa.CallInstruction)
			if isCall {
				if operation, ok := c.lockOperation(call, env); ok {
					c.emitLock(fn, instruction, operation, state, path)
					c.applyLock(&state, call, operation)
					continue
				}
				if c.emitBuiltinChannel(fn, call, env, state, path) {
					continue
				}
				c.emitInvocation(fn, call, env, state, path, depth)
				continue
			}
			switch value := instruction.(type) {
			case *ssa.MakeChan:
				resource := c.resource(value, env, 0, nil)
				info := c.channels[resource.ID]
				c.addInstruction(fn, value, "channel_make", fmt.Sprintf("channel=%s; capacity=%s; path=%s", resource.ID, info.Capacity, formatPath(path)), "exact_ssa")
			case *ssa.Send:
				resource := c.resource(value.Chan, env, 0, nil)
				c.emitChannelOperation(fn, value, "channel_send", resource, state, path)
			case *ssa.UnOp:
				if value.Op == token.ARROW {
					resource := c.resource(value.X, env, 0, nil)
					c.emitChannelOperation(fn, value, "channel_receive", resource, state, path)
				}
			case *ssa.Select:
				for index, selection := range value.States {
					resource := c.resource(selection.Chan, env, 0, nil)
					direction := "receive"
					if selection.Dir == types.SendOnly {
						direction = "send"
					}
					detail := fmt.Sprintf("state=%d; direction=%s; channel=%s; blocking_select=%t; %s; path=%s", index, direction, resource.ID, value.Blocking, describeHeld(state), formatPath(path))
					c.addInstruction(fn, value, "channel_select", detail, combinePrecision("exact_ssa", resource.Precision))
				}
			}
		}
	}
}

func (c *concurrencyInspector) blockEntries(fn *ssa.Function, env map[ssa.Value]resourceRef, initial heldState) map[*ssa.BasicBlock]heldState {
	entries := make(map[*ssa.BasicBlock]heldState)
	if len(fn.Blocks) == 0 {
		return entries
	}
	entry := fn.Blocks[0]
	entries[entry] = cloneHeldState(initial)
	queue := []*ssa.BasicBlock{entry}
	inQueue := map[*ssa.BasicBlock]bool{entry: true}
	for len(queue) > 0 {
		block := queue[0]
		queue = queue[1:]
		inQueue[block] = false
		out := cloneHeldState(entries[block])
		for _, instruction := range block.Instrs {
			call, ok := instruction.(ssa.CallInstruction)
			if !ok {
				continue
			}
			operation, ok := c.lockOperation(call, env)
			if ok {
				c.applyLock(&out, call, operation)
			}
		}
		for _, successor := range block.Succs {
			current, seen := entries[successor]
			if !seen {
				entries[successor] = cloneHeldState(out)
			} else {
				merged := mergeHeld(current, out)
				if equalHeld(current, merged) {
					continue
				}
				entries[successor] = merged
			}
			if !inQueue[successor] {
				queue = append(queue, successor)
				inQueue[successor] = true
			}
		}
	}
	return entries
}

func (c *concurrencyInspector) applyLock(state *heldState, call ssa.CallInstruction, operation lockOperation) {
	// A defer only registers work for function exit. In particular,
	// defer Unlock must not shorten the held region at the defer statement.
	if _, deferred := call.(*ssa.Defer); deferred {
		return
	}
	lock := heldLock{Resource: operation.Resource, Mode: operation.Mode}
	if operation.Action == "acquire" {
		addHeld(state, lock, !operation.Try)
		return
	}
	removeHeld(state, operation.Resource, operation.Mode)
}

func (c *concurrencyInspector) emitLock(fn *ssa.Function, instruction ssa.Instruction, operation lockOperation, state heldState, path []string) {
	kind := "lock_" + operation.Action
	detail := fmt.Sprintf("method=%s; resource=%s; mode=%s; %s; path=%s", operation.Method, operation.Resource.ID, operation.Mode, describeHeld(state), formatPath(path))
	if operation.Try {
		detail += "; result_not_correlated=true"
	}
	if _, deferred := instruction.(*ssa.Defer); deferred {
		kind = "deferred_" + kind
		detail += "; effect=at_function_exit"
	}
	c.addInstruction(fn, instruction, kind, detail, combinePrecision("exact_types", operation.Resource.Precision))
	if operation.Resource.Precision != "exact" {
		c.addInstruction(fn, instruction, "analysis_uncertainty", fmt.Sprintf("complete=false; reason=lock_resource_alias_not_exact; resource=%s; resource_precision=%s", operation.Resource.ID, operation.Resource.Precision), operation.Resource.Precision)
	}
}

func (c *concurrencyInspector) emitChannelOperation(fn *ssa.Function, instruction ssa.Instruction, kind string, resource resourceRef, state heldState, path []string) {
	info, ok := c.channels[resource.ID]
	capacity := "unknown"
	basis := "origin_unresolved"
	if ok {
		capacity, basis = info.Capacity, info.Basis
	}
	detail := fmt.Sprintf("channel=%s; capacity=%s; capacity_basis=%s; blocking=may; %s; path=%s", resource.ID, capacity, basis, describeHeld(state), formatPath(path))
	c.addInstruction(fn, instruction, kind, detail, combinePrecision("exact_ssa", resource.Precision))
	if resource.Precision == "unknown" || capacity == "unknown" {
		c.addInstruction(fn, instruction, "analysis_uncertainty", "complete=false; reason=channel_origin_or_capacity_unknown", "unknown")
	}
}

func (c *concurrencyInspector) emitBuiltinChannel(fn *ssa.Function, call ssa.CallInstruction, env map[ssa.Value]resourceRef, state heldState, path []string) bool {
	common := call.Common()
	builtin, ok := common.Value.(*ssa.Builtin)
	if !ok || builtin.Name() != "close" || len(common.Args) != 1 {
		return false
	}
	resource := c.resource(common.Args[0], env, 0, nil)
	kind := "channel_close"
	detail := fmt.Sprintf("channel=%s; close_is_nonblocking=true; %s; path=%s", resource.ID, describeHeld(state), formatPath(path))
	if _, deferred := call.(*ssa.Defer); deferred {
		kind = "deferred_channel_close"
		detail += "; effect=at_function_exit; exit_lockset=not_computed"
	}
	c.addInstruction(fn, call, kind, detail, combinePrecision("exact_ssa", resource.Precision))
	return true
}

func (c *concurrencyInspector) emitInvocation(fn *ssa.Function, call ssa.CallInstruction, env map[ssa.Value]resourceRef, state heldState, path []string, depth int) {
	if _, builtin := call.Common().Value.(*ssa.Builtin); builtin {
		return
	}
	kind := "call"
	calleeState := cloneHeldState(state)
	switch call.(type) {
	case *ssa.Go:
		kind = "goroutine_start"
		calleeState = newHeldState()
	case *ssa.Defer:
		kind = "defer_call"
	}
	targets, precision := c.callTargets(fn, call)
	if len(targets) == 0 {
		if call.Common().IsInvoke() || call.Common().StaticCallee() == nil {
			c.dynamicSeen = true
			c.addInstruction(fn, call, "analysis_uncertainty", fmt.Sprintf("complete=false; reason=unresolved_dynamic_call; call=%s; path=%s", call.Common().String(), formatPath(path)), "unknown")
		}
		return
	}
	if precision != "exact_static_call" {
		c.dynamicSeen = true
	}
	for _, target := range targets {
		if target == nil || !c.repoFunction(target) {
			continue
		}
		targetPath := appendPath(path, shortFunction(target))
		detail := fmt.Sprintf("kind=%s; target=%s; %s; path=%s", kind, target.String(), describeHeld(state), formatPath(targetPath))
		factKind := "call_edge"
		if kind == "goroutine_start" || kind == "defer_call" {
			factKind = kind
		}
		c.addInstruction(fn, call, factKind, detail, precision)
		if kind == "defer_call" {
			continue
		}
		bindings := c.bindCall(call, target, env)
		c.walk(target, bindings, calleeState, targetPath, depth+1)
	}
}

func (c *concurrencyInspector) callTargets(caller *ssa.Function, call ssa.CallInstruction) ([]*ssa.Function, string) {
	if static := call.Common().StaticCallee(); static != nil {
		return []*ssa.Function{static}, "exact_static_call"
	}
	if c.cg == nil {
		return nil, "unknown"
	}
	node := c.cg.Nodes[caller]
	if node == nil {
		return nil, "unknown"
	}
	seen := make(map[*ssa.Function]bool)
	var out []*ssa.Function
	for _, edge := range node.Out {
		if edge == nil || edge.Site != call || edge.Callee == nil || edge.Callee.Func == nil || seen[edge.Callee.Func] {
			continue
		}
		seen[edge.Callee.Func] = true
		out = append(out, edge.Callee.Func)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, "vta_overapproximation"
}

func (c *concurrencyInspector) bindCall(call ssa.CallInstruction, target *ssa.Function, callerEnv map[ssa.Value]resourceRef) map[ssa.Value]resourceRef {
	env := make(map[ssa.Value]resourceRef)
	common := call.Common()
	paramIndex := 0
	if common.IsInvoke() && len(target.Params) > 0 {
		// Interface values often passed through heap fields. Unless their origin
		// is exact, retain the target receiver's symbolic identity and make the
		// uncertainty visible instead of inventing an exact alias.
		receiver := c.resource(common.Value, callerEnv, 0, nil)
		if receiver.Precision == "exact" {
			env[target.Params[0]] = receiver
		}
		paramIndex = 1
	}
	for argIndex, argument := range common.Args {
		index := argIndex + paramIndex
		if index >= len(target.Params) {
			break
		}
		env[target.Params[index]] = c.resource(argument, callerEnv, 0, nil)
	}
	if closure, ok := common.Value.(*ssa.MakeClosure); ok {
		for index, binding := range closure.Bindings {
			if index < len(target.FreeVars) {
				env[target.FreeVars[index]] = c.resource(binding, callerEnv, 0, nil)
			}
		}
	}
	return env
}

func (c *concurrencyInspector) lockOperation(call ssa.CallInstruction, env map[ssa.Value]resourceRef) (lockOperation, bool) {
	common := call.Common()
	target := common.StaticCallee()
	if target == nil {
		return lockOperation{}, false
	}
	object, ok := target.Object().(*types.Func)
	if !ok || object.Pkg() == nil || object.Pkg().Path() != "sync" {
		return lockOperation{}, false
	}
	signature, ok := object.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return lockOperation{}, false
	}
	typeName := baseNamedType(signature.Recv().Type())
	if typeName == nil || typeName.Obj().Pkg() == nil || typeName.Obj().Pkg().Path() != "sync" {
		return lockOperation{}, false
	}
	receiverName := typeName.Obj().Name()
	method := object.Name()
	action, mode, try := "", "", false
	switch receiverName + "." + method {
	case "Mutex.Lock":
		action, mode = "acquire", "exclusive"
	case "Mutex.TryLock":
		action, mode, try = "acquire", "exclusive", true
	case "Mutex.Unlock":
		action, mode = "release", "exclusive"
	case "RWMutex.Lock":
		action, mode = "acquire", "write"
	case "RWMutex.TryLock":
		action, mode, try = "acquire", "write", true
	case "RWMutex.Unlock":
		action, mode = "release", "write"
	case "RWMutex.RLock":
		action, mode = "acquire", "read"
	case "RWMutex.TryRLock":
		action, mode, try = "acquire", "read", true
	case "RWMutex.RUnlock":
		action, mode = "release", "read"
	default:
		return lockOperation{}, false
	}
	if len(common.Args) == 0 {
		return lockOperation{}, false
	}
	resource := c.resource(common.Args[0], env, 0, nil)
	return lockOperation{Action: action, Mode: mode, Resource: resource, Method: receiverName + "." + method, Try: try}, true
}

func baseNamedType(value types.Type) *types.Named {
	for {
		switch typed := value.(type) {
		case *types.Pointer:
			value = typed.Elem()
		case *types.Named:
			return typed
		default:
			return nil
		}
	}
}

func (c *concurrencyInspector) resource(value ssa.Value, env map[ssa.Value]resourceRef, depth int, seen map[ssa.Value]bool) resourceRef {
	if value == nil || depth > 10 {
		return resourceRef{ID: "unknown", Precision: "unknown"}
	}
	if ref, ok := env[value]; ok {
		return ref
	}
	if seen == nil {
		seen = make(map[ssa.Value]bool)
	}
	if seen[value] {
		return resourceRef{ID: "unknown-cycle", Precision: "unknown"}
	}
	seen[value] = true
	defer delete(seen, value)

	switch typed := value.(type) {
	case *ssa.Parameter:
		fn := typed.Parent()
		if fn != nil && fn.Signature != nil && fn.Signature.Recv() != nil && len(fn.Params) > 0 && fn.Params[0] == typed {
			return resourceRef{ID: "receiver:" + fn.Signature.Recv().Type().String(), Precision: "symbolic_receiver"}
		}
		return resourceRef{ID: fmt.Sprintf("parameter:%s:%s", shortFunction(fn), typed.Name()), Precision: "symbolic_parameter"}
	case *ssa.FreeVar:
		return resourceRef{ID: fmt.Sprintf("freevar:%s:%s", shortFunction(typed.Parent()), typed.Name()), Precision: "unknown"}
	case *ssa.Global:
		return resourceRef{ID: "global:" + typed.String(), Precision: "exact"}
	case *ssa.Alloc:
		return resourceRef{ID: "alloc@" + c.positionID(typed.Pos()), Precision: "exact"}
	case *ssa.MakeChan:
		return resourceRef{ID: "channel@" + c.positionID(typed.Pos()), Precision: "exact"}
	case *ssa.FieldAddr:
		base := c.resource(typed.X, env, depth+1, seen)
		return resourceRef{ID: base.ID + "." + fieldName(typed.X.Type(), typed.Field), Precision: base.Precision}
	case *ssa.Field:
		base := c.resource(typed.X, env, depth+1, seen)
		return resourceRef{ID: base.ID + "." + fieldName(typed.X.Type(), typed.Field), Precision: weakenLoadedPrecision(base.Precision)}
	case *ssa.UnOp:
		base := c.resource(typed.X, env, depth+1, seen)
		if typed.Op == token.MUL {
			base.Precision = weakenLoadedPrecision(base.Precision)
		}
		return base
	case *ssa.MakeInterface:
		return c.resource(typed.X, env, depth+1, seen)
	case *ssa.ChangeInterface:
		return c.resource(typed.X, env, depth+1, seen)
	case *ssa.ChangeType:
		return c.resource(typed.X, env, depth+1, seen)
	case *ssa.Convert:
		return c.resource(typed.X, env, depth+1, seen)
	case *ssa.TypeAssert:
		return c.resource(typed.X, env, depth+1, seen)
	case *ssa.Extract:
		if call, ok := typed.Tuple.(*ssa.Call); ok {
			return c.callResult(call, typed.Index, env, depth+1, seen)
		}
		base := c.resource(typed.Tuple, env, depth+1, seen)
		return resourceRef{ID: base.ID + ".result" + strconv.Itoa(typed.Index), Precision: base.Precision}
	case *ssa.Phi:
		var refs []resourceRef
		for _, edge := range typed.Edges {
			refs = append(refs, c.resource(edge, env, depth+1, seen))
		}
		return mergeResources(refs)
	case *ssa.Call:
		return c.callResult(typed, 0, env, depth+1, seen)
	case *ssa.MakeClosure:
		return resourceRef{ID: "closure@" + c.positionID(typed.Pos()), Precision: "exact"}
	default:
		return resourceRef{ID: "unknown@" + c.positionID(value.Pos()), Precision: "unknown"}
	}
}

func (c *concurrencyInspector) callResult(call *ssa.Call, index int, env map[ssa.Value]resourceRef, depth int, seen map[ssa.Value]bool) resourceRef {
	targets, precision := c.callTargets(call.Parent(), call)
	var refs []resourceRef
	for _, target := range targets {
		if target == nil || target.Blocks == nil || !c.repoFunction(target) {
			continue
		}
		bindings := c.bindCall(call, target, env)
		for _, block := range target.Blocks {
			for _, instruction := range block.Instrs {
				ret, ok := instruction.(*ssa.Return)
				if !ok || index >= len(ret.Results) {
					continue
				}
				ref := c.resource(ret.Results[index], bindings, depth+1, seen)
				if precision != "exact_static_call" && ref.Precision == "exact" {
					ref.Precision = "vta_overapproximation"
				}
				refs = append(refs, ref)
			}
		}
	}
	if len(refs) == 0 {
		return resourceRef{ID: "call-result@" + c.positionID(call.Pos()), Precision: "unknown"}
	}
	return mergeResources(refs)
}

func mergeResources(refs []resourceRef) resourceRef {
	if len(refs) == 0 {
		return resourceRef{ID: "unknown", Precision: "unknown"}
	}
	ids := make(map[string]bool)
	precision := refs[0].Precision
	for _, ref := range refs {
		ids[ref.ID] = true
		if ref.Precision != precision {
			precision = "may_alias"
		}
	}
	if len(ids) == 1 {
		for id := range ids {
			return resourceRef{ID: id, Precision: precision}
		}
	}
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	return resourceRef{ID: "one_of(" + strings.Join(values, ",") + ")", Precision: "may_alias"}
}

func fieldName(value types.Type, index int) string {
	for {
		switch typed := value.(type) {
		case *types.Pointer:
			value = typed.Elem()
		case *types.Named:
			value = typed.Underlying()
		case *types.Struct:
			if index >= 0 && index < typed.NumFields() {
				return typed.Field(index).Name()
			}
			return fmt.Sprintf("field%d", index)
		default:
			return fmt.Sprintf("field%d", index)
		}
	}
}

func (c *concurrencyInspector) indexChannelOrigins() {
	for fn := range c.idx.cg.Nodes {
		if fn == nil || fn.Blocks == nil || !c.repoFunction(fn) {
			continue
		}
		for _, block := range fn.Blocks {
			for _, instruction := range block.Instrs {
				switch typed := instruction.(type) {
				case *ssa.MakeChan:
					ref := c.resource(typed, nil, 0, nil)
					c.channels[ref.ID] = channelInfo{Capacity: constantInt(typed.Size), Basis: "make_chan_constant"}
				case *ssa.Store:
					made, ok := typed.Val.(*ssa.MakeChan)
					if !ok {
						continue
					}
					ref := c.resource(typed.Addr, nil, 0, nil)
					c.channels[ref.ID] = channelInfo{Capacity: constantInt(made.Size), Basis: "assigned_from_make_chan"}
				}
			}
		}
	}
}

func constantInt(value ssa.Value) string {
	constantValue, ok := value.(*ssa.Const)
	if !ok || constantValue.Value == nil || constantValue.Value.Kind() != constant.Int {
		return "unknown"
	}
	return constantValue.Value.ExactString()
}

func (c *concurrencyInspector) repoFunction(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	position := c.idx.prog.Fset.Position(fn.Pos())
	if !position.IsValid() {
		return false
	}
	relative, err := filepath.Rel(c.idx.repo, position.Filename)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (c *concurrencyInspector) positionID(position token.Pos) string {
	resolved := c.idx.prog.Fset.Position(position)
	if !resolved.IsValid() {
		return "unknown"
	}
	relative, err := filepath.Rel(c.idx.repo, resolved.Filename)
	if err != nil {
		relative = resolved.Filename
	}
	return fmt.Sprintf("%s:%d", filepath.ToSlash(relative), resolved.Line)
}

func (c *concurrencyInspector) addInstruction(fn *ssa.Function, instruction ssa.Instruction, kind, detail, precision string) {
	position := c.idx.prog.Fset.Position(instruction.Pos())
	if !position.IsValid() {
		position = c.idx.prog.Fset.Position(fn.Pos())
	}
	c.add(ConcurrencyFact{File: position.Filename, Line: position.Line, Kind: kind, Detail: detail, Symbol: fn.String(), Precision: precision})
}

func (c *concurrencyInspector) addAtFunction(fn *ssa.Function, kind, detail, precision string) {
	position := c.idx.prog.Fset.Position(fn.Pos())
	c.add(ConcurrencyFact{File: position.Filename, Line: position.Line, Kind: kind, Detail: detail, Symbol: fn.String(), Precision: precision})
}

func (c *concurrencyInspector) add(fact ConcurrencyFact) {
	key := fmt.Sprintf("%s:%d:%s:%s", fact.File, fact.Line, fact.Kind, fact.Detail)
	if c.seenFacts[key] || len(c.facts) >= maxConcurrencyFacts {
		if len(c.facts) >= maxConcurrencyFacts {
			c.truncated = true
		}
		return
	}
	c.seenFacts[key] = true
	c.facts = append(c.facts, fact)
}

func describeHeld(state heldState) string {
	must := make([]string, 0, len(state.Must))
	mayOnly := make([]string, 0, len(state.May))
	for _, lock := range state.Must {
		must = append(must, lock.Mode+" "+lock.Resource.ID)
	}
	for key, lock := range state.May {
		if _, ok := state.Must[key]; !ok {
			mayOnly = append(mayOnly, lock.Mode+" "+lock.Resource.ID)
		}
	}
	sort.Strings(must)
	sort.Strings(mayOnly)
	return fmt.Sprintf("path_held_must=[%s]; path_held_may_only=[%s]", strings.Join(must, ","), strings.Join(mayOnly, ","))
}

func heldSignature(state heldState) string {
	keys := make([]string, 0, len(state.Must)+len(state.May))
	for key := range state.Must {
		keys = append(keys, "must:"+key)
	}
	for key := range state.May {
		if _, ok := state.Must[key]; !ok {
			keys = append(keys, "may:"+key)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ";")
}

func environmentSignature(env map[ssa.Value]resourceRef) string {
	values := make([]string, 0, len(env))
	for value, resource := range env {
		values = append(values, fmt.Sprintf("%s=%s/%s", value.Name(), resource.ID, resource.Precision))
	}
	sort.Strings(values)
	return strings.Join(values, ";")
}

func formatPath(path []string) string {
	return strings.Join(path, " -> ")
}

func appendPath(path []string, value string) []string {
	out := make([]string, 0, len(path)+1)
	out = append(out, path...)
	out = append(out, value)
	return out
}

func shortFunction(fn *ssa.Function) string {
	if fn == nil {
		return "unknown"
	}
	return fn.String()
}

func combinePrecision(primary, resource string) string {
	if resource == "" || resource == "exact" {
		return primary
	}
	if resource == "unknown" {
		return "unknown"
	}
	return primary + "+" + resource
}

func weakenLoadedPrecision(precision string) string {
	if precision == "unknown" || precision == "may_alias" {
		return precision
	}
	return "may_alias"
}
