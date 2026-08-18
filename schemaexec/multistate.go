package schemaexec

import (
	"crypto/sha256"
	"encoding/binary"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// execState represents a single execution state in the multi-state VM.
// jq's backtracking semantics require tracking multiple possible execution paths.
type execState struct {
	pc                   int                       // Program counter
	stack                []SValue                  // Schema stack (copy for this state)
	scopes               []map[string]*oas3.Schema // Scope frames (copy for this state)
	scopeShapes          *scopeShapeNode           // Persistent structural hashes for scope frames
	shapeKey             string                    // Cached exact state-shape key
	relaxedShapeKey      string                    // Cached relaxed state-shape key
	shapeKeyValid        bool
	relaxedShapeKeyValid bool
	depth                int                     // Recursion depth (for limiting)
	accum                map[string]*oas3.Schema // Shared: allocID → canonical array
	schemaToAlloc        map[*oas3.Schema]string // Shared: schema pointer → allocID (for opAppend)
	allocCounter         *int                    // Shared: Monotonic counter for unique allocation IDs
	callstack            callStack               // Per-state: persistent return-address stack
	forks                forkContinuations       // Per-state: persistent pending backtracking continuations
	labels               labelContinuations      // Per-state: persistent active label boundaries
	tryDepth             int                     // Per-state: active try regions on the success path
	depthWidenBlocked    bool                    // Joined incompatible control contexts must fall back to output Top
	forkUpdates          []*forkControl          // Updates shared with eager fork alternatives

	// Path collection (for del/getpath/setpath operations)
	pathMode    bool          // Are we collecting a path (between opPathBegin/opPathEnd)?
	currentPath []PathSegment // Current path segments being collected

	// State tracking for logging
	id       int    // Unique state ID
	parentID int    // Parent state ID (0 for root)
	lineage  string // Lineage string (e.g., "0", "0.F", "0.F.C")

	// Variable intent tracking (to solve orphaned-allocID issue)
	varAllocHistory  map[string]map[string]struct{} // varKey -> set(allocID) ever assigned
	varDesiredItemFP map[string]string              // varKey -> schemaFingerprint(items)
	allocDesiredFP   map[string]string              // SHARED: allocID -> schemaFingerprint(items)
	schemaFPIntent   map[*oas3.Schema]string        // Per-state: schema pointer → items fingerprint

	// Theory 10: Hybrid Origin-Lattice
	allocOrigin      map[string]*AllocOrigin      // SHARED: allocID -> origin (where it was created)
	allocCardinality map[string]*ArrayCardinality // SHARED: allocID -> cardinality bounds
	dsu              *DSU                         // SHARED: Disjoint Set Union for allocID equivalence
}

// AllocOrigin tracks where an allocID was created in the AST/execution
type AllocOrigin struct {
	PC       int    // Program counter where allocation occurred
	Context  string // Semantic context (e.g., "reduce_accumulator", "map_accumulator")
	CallSite int    // Return address of the enclosing call frame (-1 at top level)
}

// sameOrigin reports whether two allocation origins are equivalent.
// The CallSite discriminator matters: two invocations of the same library
// function (e.g. two separate map() calls in one pipeline) allocate at the
// SAME internal PC — without the callsite, their accumulators would be
// DSU-unioned and their item types conflated (e.g. an array<number> from
// map(.price) leaking into the items of a later map({...})). Loop iterations
// within one call share the callsite and still merge, as intended.
func sameOrigin(a, b *AllocOrigin) bool {
	return a != nil && b != nil && a.PC == b.PC && a.Context == b.Context && a.CallSite == b.CallSite
}

// ArrayCardinality tracks bounds on array size for lattice-based merging
type ArrayCardinality struct {
	MinItems *int // Lower bound: 0 = maybe-empty, 1+ = must-be-non-empty
	MaxItems *int // Upper bound: nil = unbounded
}

// Join performs lattice join (LUB) on two cardinality bounds
// This is the mathematically sound merge operation for the cardinality lattice
func (a *ArrayCardinality) Join(other *ArrayCardinality) *ArrayCardinality {
	if a == nil && other == nil {
		return nil
	}
	if a == nil {
		return other
	}
	if other == nil {
		return a
	}

	// Join MinItems: take minimum (most permissive lower bound)
	var minItems *int
	if a.MinItems == nil && other.MinItems == nil {
		minItems = nil
	} else if a.MinItems == nil {
		minItems = other.MinItems
	} else if other.MinItems == nil {
		minItems = a.MinItems
	} else {
		min := *a.MinItems
		if *other.MinItems < min {
			min = *other.MinItems
		}
		minItems = &min
	}

	// Join MaxItems: take maximum (most permissive upper bound)
	var maxItems *int
	if a.MaxItems == nil || other.MaxItems == nil {
		// nil means unbounded, which dominates any finite bound
		maxItems = nil
	} else {
		max := *a.MaxItems
		if *other.MaxItems > max {
			max = *other.MaxItems
		}
		maxItems = &max
	}

	return &ArrayCardinality{
		MinItems: minItems,
		MaxItems: maxItems,
	}
}

// DSU implements Disjoint Set Union (Union-Find) for allocID equivalence classes
type DSU struct {
	parent map[string]string // allocID -> parent allocID
}

// NewDSU creates a new Disjoint Set Union structure
func NewDSU() *DSU {
	return &DSU{
		parent: make(map[string]string),
	}
}

// Find returns the canonical allocID for an equivalence class (with path compression)
func (d *DSU) Find(allocID string) string {
	if allocID == "" {
		return ""
	}
	if _, exists := d.parent[allocID]; !exists {
		// First time seeing this allocID, it's its own parent
		d.parent[allocID] = allocID
		return allocID
	}
	// Path compression
	if d.parent[allocID] != allocID {
		d.parent[allocID] = d.Find(d.parent[allocID])
	}
	return d.parent[allocID]
}

// Union merges two equivalence classes
func (d *DSU) Union(allocID1, allocID2 string) {
	if allocID1 == "" || allocID2 == "" {
		return
	}
	root1 := d.Find(allocID1)
	root2 := d.Find(allocID2)
	if root1 != root2 {
		// Union by making root1 the parent of root2
		d.parent[root2] = root1
	}
}

// PathSegment represents one segment of a path expression
type PathSegment struct {
	Key        interface{} // string, int, PathWildcard, or PathAllElements
	IsSymbolic bool        // True for unknown-index and all-elements segments
}

// PathWildcard represents an unknown single array index or slice path.
type PathWildcard struct{}

// PathAllElements represents every array index selected by .[] in path mode.
type PathAllElements struct{}

// forkContinuation is an immutable snapshot restored when control backtracks
// through an opFork. Cloned execution states share these values; code that
// refines or joins a continuation must construct a replacement value.
type forkContinuation struct {
	kind           int
	targetPC       int
	arrayAppendKey string
	stack          []SValue
	scopeDepth     int
	scopes         []map[string]*oas3.Schema
	callstackLen   int
	callstack      callStack
	depth          int
	pathMode       bool
	currentPath    []PathSegment
	tryDepth       int
	control        *forkControl
	shapeHash      [sha256.Size]byte
}

type callStackNode struct {
	returnPC int
	targetPC int
	prev     *callStackNode
	depth    int
	shape    [sha256.Size]byte
}

type scopeShapeNode struct {
	prev    *scopeShapeNode
	depth   int
	exact   [sha256.Size]byte
	relaxed [sha256.Size]byte
}

func appendScopeShape(prev *scopeShapeNode, frame map[string]*oas3.Schema) *scopeShapeNode {
	exactFrame, relaxedFrame := scopeFrameHashes(frame)
	var exactInput, relaxedInput [sha256.Size * 2]byte
	if prev != nil {
		copy(exactInput[:sha256.Size], prev.exact[:])
		copy(relaxedInput[:sha256.Size], prev.relaxed[:])
	}
	copy(exactInput[sha256.Size:], exactFrame[:])
	copy(relaxedInput[sha256.Size:], relaxedFrame[:])
	depth := 1
	if prev != nil {
		depth = prev.depth + 1
	}
	return &scopeShapeNode{
		prev:    prev,
		depth:   depth,
		exact:   sha256.Sum256(exactInput[:]),
		relaxed: sha256.Sum256(relaxedInput[:]),
	}
}

func scopeFrameHashes(frame map[string]*oas3.Schema) ([sha256.Size]byte, [sha256.Size]byte) {
	keys := make([]string, 0, len(frame))
	for key := range frame {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var exact strings.Builder
	for _, key := range keys {
		exact.WriteString(strconv.Quote(key))
		exact.WriteByte(0)
	}
	var count [8]byte
	binary.LittleEndian.PutUint64(count[:], uint64(len(frame)))
	return sha256.Sum256([]byte(exact.String())), sha256.Sum256(count[:])
}

// callStack is an immutable persistent stack with an exact cached structural
// key. Clones and fork snapshots share it safely.
type callStack struct {
	tail *callStackNode
}

func (s callStack) len() int {
	if s.tail == nil {
		return 0
	}
	return s.tail.depth
}

func (s callStack) append(returnPC, targetPC int) callStack {
	var hashInput [sha256.Size + 16]byte
	if s.tail != nil {
		copy(hashInput[:sha256.Size], s.tail.shape[:])
	}
	binary.LittleEndian.PutUint64(hashInput[sha256.Size:], uint64(returnPC))
	binary.LittleEndian.PutUint64(hashInput[sha256.Size+8:], uint64(targetPC))
	return callStack{tail: &callStackNode{
		returnPC: returnPC,
		targetPC: targetPC,
		prev:     s.tail,
		depth:    s.len() + 1,
		shape:    sha256.Sum256(hashInput[:]),
	}}
}

func (s callStack) pop() (int, callStack, bool) {
	if s.tail == nil {
		return 0, s, false
	}
	return s.tail.returnPC, callStack{tail: s.tail.prev}, true
}

func (s callStack) containsTarget(targetPC int) bool {
	for node := s.tail; node != nil; node = node.prev {
		if node.targetPC == targetPC {
			return true
		}
	}
	return false
}

func (s callStack) shapeHash() [sha256.Size]byte {
	if s.tail == nil {
		return [sha256.Size]byte{}
	}
	return s.tail.shape
}

func equalCallStacks(a, b callStack) bool {
	if a.tail == b.tail {
		return true
	}
	if a.len() != b.len() || a.shapeHash() != b.shapeHash() {
		return false
	}
	for left, right := a.tail, b.tail; left != nil && right != nil; left, right = left.prev, right.prev {
		if left.returnPC != right.returnPC || left.targetPC != right.targetPC {
			return false
		}
	}
	return true
}

type forkContinuationNode struct {
	value           forkContinuation
	prev            *forkContinuationNode
	depth           int
	shape           [sha256.Size]byte
	arrayGenerators *arrayGeneratorNode
	allPlain        bool
}

type arrayGeneratorNode struct {
	targetPC int
	key      string
	prev     *arrayGeneratorNode
	count    int
	shape    [sha256.Size]byte
}

// forkContinuations is a persistent immutable stack. Appending and truncating
// share prior nodes, so recursive state clones do not copy all pending forks.
type forkContinuations struct {
	tail *forkContinuationNode
}

func (f forkContinuations) len() int {
	if f.tail == nil {
		return 0
	}
	return f.tail.depth
}

func (f forkContinuations) append(value forkContinuation) forkContinuations {
	if value.shapeHash == ([sha256.Size]byte{}) {
		value.shapeHash = forkContinuationShapeHash(value)
	}
	var hashInput [sha256.Size * 2]byte
	if f.tail != nil {
		copy(hashInput[:sha256.Size], f.tail.shape[:])
	}
	copy(hashInput[sha256.Size:], value.shapeHash[:])
	allPlain := value.kind == opFork
	var arrayGenerators *arrayGeneratorNode
	if f.tail != nil {
		allPlain = allPlain && f.tail.allPlain
		arrayGenerators = f.tail.arrayGenerators
	}
	if value.arrayAppendKey != "" {
		var generatorInput strings.Builder
		if arrayGenerators != nil {
			generatorInput.Write(arrayGenerators.shape[:])
		}
		generatorInput.WriteString(strconv.Itoa(value.targetPC))
		generatorInput.WriteByte(':')
		generatorInput.WriteString(strconv.Quote(value.arrayAppendKey))
		count := 1
		if arrayGenerators != nil {
			count = arrayGenerators.count + 1
		}
		arrayGenerators = &arrayGeneratorNode{
			targetPC: value.targetPC,
			key:      value.arrayAppendKey,
			prev:     arrayGenerators,
			count:    count,
			shape:    sha256.Sum256([]byte(generatorInput.String())),
		}
	}
	return forkContinuations{tail: &forkContinuationNode{
		value:           value,
		prev:            f.tail,
		depth:           f.len() + 1,
		shape:           sha256.Sum256(hashInput[:]),
		arrayGenerators: arrayGenerators,
		allPlain:        allPlain,
	}}
}

func forkContinuationShapeHash(fork forkContinuation) [sha256.Size]byte {
	var buf strings.Builder
	buf.WriteString(strconv.Itoa(fork.kind))
	buf.WriteByte(':')
	buf.WriteString(strconv.Itoa(fork.targetPC))
	buf.WriteByte(':')
	buf.WriteString(strconv.Quote(fork.arrayAppendKey))
	buf.WriteByte(':')
	buf.WriteString(strconv.Itoa(len(fork.stack)))
	buf.WriteByte(':')
	buf.WriteString(strconv.Itoa(fork.scopeDepth))
	buf.WriteByte(':')
	buf.WriteString(strconv.Itoa(fork.callstackLen))
	buf.WriteByte(':')
	callstackHash := fork.callstack.shapeHash()
	buf.Write(callstackHash[:])
	buf.WriteByte(':')
	buf.WriteString(strconv.FormatBool(fork.pathMode))
	buf.WriteByte(':')
	buf.WriteString(pathSegmentsKey(fork.currentPath))
	buf.WriteByte(':')
	buf.WriteString(strconv.Itoa(fork.tryDepth))
	return sha256.Sum256([]byte(buf.String()))
}

func (f forkContinuations) atDepth(depth int) (forkContinuation, bool) {
	for node := f.tail; node != nil && node.depth >= depth; node = node.prev {
		if node.depth == depth {
			return node.value, true
		}
	}
	return forkContinuation{}, false
}

func (f forkContinuations) nodeAtDepth(depth int) *forkContinuationNode {
	for node := f.tail; node != nil && node.depth >= depth; node = node.prev {
		if node.depth == depth {
			return node
		}
	}
	return nil
}

func (f forkContinuations) truncate(depth int) forkContinuations {
	if depth <= 0 {
		return forkContinuations{}
	}
	for node := f.tail; node != nil; node = node.prev {
		if node.depth == depth {
			return forkContinuations{tail: node}
		}
	}
	return forkContinuations{}
}

func (f forkContinuations) pruneCallDepth(maxDepth int) forkContinuations {
	for f.tail != nil && f.tail.value.callstackLen > maxDepth {
		f.tail = f.tail.prev
	}
	return f
}

func (f forkContinuations) values() []forkContinuation {
	values := make([]forkContinuation, f.len())
	for node := f.tail; node != nil; node = node.prev {
		values[node.depth-1] = node.value
	}
	return values
}

func forkContinuationsFromValues(values []forkContinuation) forkContinuations {
	var result forkContinuations
	for _, value := range values {
		result = result.append(value)
	}
	return result
}

// equalArrayGeneratorContexts compares only the continuation facts used to
// justify dropping a depth-exhausted state into an enclosing array. Plain
// non-generator forks are irrelevant. Distinct contexts containing non-plain
// forks are not considered equal because later pruning could expose different
// generator contexts.
func equalArrayGeneratorContexts(a, b forkContinuations) bool {
	if a.tail == b.tail {
		return true
	}
	aPlain, bPlain := a.tail == nil || a.tail.allPlain, b.tail == nil || b.tail.allPlain
	if aPlain != bPlain {
		return false
	}
	if !aPlain {
		return false
	}
	var left, right *arrayGeneratorNode
	if a.tail != nil {
		left = a.tail.arrayGenerators
	}
	if b.tail != nil {
		right = b.tail.arrayGenerators
	}
	if left == right {
		return true
	}
	if left == nil || right == nil || left.count != right.count || left.shape != right.shape {
		return false
	}
	for left != nil && right != nil {
		if left.targetPC != right.targetPC || left.key != right.key {
			return false
		}
		left, right = left.prev, right.prev
	}
	return left == nil && right == nil
}

func mapForkContinuations(forks forkContinuations, transform func(forkContinuation) (forkContinuation, bool)) (forkContinuations, bool) {
	var visit func(*forkContinuationNode) (*forkContinuationNode, bool)
	visit = func(node *forkContinuationNode) (*forkContinuationNode, bool) {
		if node == nil {
			return nil, false
		}
		prev, prevChanged := visit(node.prev)
		value, valueChanged := transform(node.value)
		if !prevChanged && !valueChanged {
			return node, false
		}
		var hashInput [sha256.Size * 2]byte
		if prev != nil {
			copy(hashInput[:sha256.Size], prev.shape[:])
		}
		copy(hashInput[sha256.Size:], value.shapeHash[:])
		return &forkContinuationNode{
			value:           value,
			prev:            prev,
			depth:           node.depth,
			shape:           sha256.Sum256(hashInput[:]),
			arrayGenerators: node.arrayGenerators,
			allPlain:        node.allPlain,
		}, true
	}
	tail, changed := visit(forks.tail)
	return forkContinuations{tail: tail}, changed
}

// forkControl carries copy-on-write substitutions from a fork's continue path
// to its already-enqueued eager alternative.
type forkControl struct {
	replacements []schemaReplacement
}

type schemaReplacement struct {
	old *oas3.Schema
	new *oas3.Schema
}

// labelContinuation identifies which pending fork a labeled break must unwind
// to. The deterministic compiler key is also stored in the label variable.
type labelContinuation struct {
	key            string
	forkDepth      int
	callstackDepth int
}

type labelContinuationNode struct {
	value        labelContinuation
	prev         *labelContinuationNode
	depth        int
	maxForkDepth int
	shape        [sha256.Size]byte
}

type labelContinuations struct {
	tail *labelContinuationNode
}

func (labels labelContinuations) len() int {
	if labels.tail == nil {
		return 0
	}
	return labels.tail.depth
}

func (labels labelContinuations) append(value labelContinuation) labelContinuations {
	var input strings.Builder
	if labels.tail != nil {
		input.Write(labels.tail.shape[:])
	}
	input.WriteString(value.key)
	input.WriteByte(0)
	input.WriteString(strconv.Itoa(value.forkDepth))
	input.WriteByte(0)
	input.WriteString(strconv.Itoa(value.callstackDepth))
	maxForkDepth := value.forkDepth
	if labels.tail != nil {
		maxForkDepth = maxInt(maxForkDepth, labels.tail.maxForkDepth)
	}
	return labelContinuations{tail: &labelContinuationNode{
		value:        value,
		prev:         labels.tail,
		depth:        labels.len() + 1,
		maxForkDepth: maxForkDepth,
		shape:        sha256.Sum256([]byte(input.String())),
	}}
}

func (labels labelContinuations) find(key string) *labelContinuationNode {
	for node := labels.tail; node != nil; node = node.prev {
		if node.value.key == key {
			return node
		}
	}
	return nil
}

func (labels labelContinuations) pruneCallDepth(maxDepth int) labelContinuations {
	for labels.tail != nil && labels.tail.value.callstackDepth > maxDepth {
		labels.tail = labels.tail.prev
	}
	return labels
}

func (labels labelContinuations) values() []labelContinuation {
	values := make([]labelContinuation, labels.len())
	for node := labels.tail; node != nil; node = node.prev {
		values[node.depth-1] = node.value
	}
	return values
}

func labelContinuationsFromValues(values []labelContinuation) labelContinuations {
	var labels labelContinuations
	for _, value := range values {
		labels = labels.append(value)
	}
	return labels
}

func equalLabelContinuations(a, b labelContinuations) bool {
	if a.tail == b.tail {
		return true
	}
	if a.len() != b.len() || a.tail == nil || b.tail == nil || a.tail.shape != b.tail.shape {
		return false
	}
	for left, right := a.tail, b.tail; left != nil && right != nil; left, right = left.prev, right.prev {
		if left.value != right.value {
			return false
		}
	}
	return true
}

// clone creates a deep copy of this state for forking.
func (s *execState) clone() *execState {
	// Clone stack
	stackCopy := make([]SValue, len(s.stack))
	copy(stackCopy, s.stack)

	// Scope maps are immutable between writes. Copy only the frame slice;
	// storeVar/refinement clone the individual maps they change.
	scopesCopy := append([]map[string]*oas3.Schema(nil), s.scopes...)

	// Accumulator maps are SHARED across states (for array construction)
	// Call stack is per-state

	// Call stacks are persistent and paths are immutable between append
	// operations, so clones may share both snapshots.
	callstackCopy := s.callstack
	// Continuations form an immutable persistent stack.
	forksCopy := s.forks
	labelsCopy := s.labels
	forkUpdatesCopy := append([]*forkControl(nil), s.forkUpdates...)

	pathCopy := s.currentPath

	// Clone intent/history maps (per-state tracking)
	histCopy := make(map[string]map[string]struct{}, len(s.varAllocHistory))
	for k, set := range s.varAllocHistory {
		setCopy := make(map[string]struct{}, len(set))
		for id := range set {
			setCopy[id] = struct{}{}
		}
		histCopy[k] = setCopy
	}
	fpCopy := make(map[string]string, len(s.varDesiredItemFP))
	for k, fp := range s.varDesiredItemFP {
		fpCopy[k] = fp
	}
	schemaFPCopy := make(map[*oas3.Schema]string, len(s.schemaFPIntent))
	for ptr, fp := range s.schemaFPIntent {
		schemaFPCopy[ptr] = fp
	}

	return &execState{
		pc:                   s.pc,
		stack:                stackCopy,
		scopes:               scopesCopy,
		scopeShapes:          s.scopeShapes,
		shapeKey:             s.shapeKey,
		relaxedShapeKey:      s.relaxedShapeKey,
		shapeKeyValid:        s.shapeKeyValid,
		relaxedShapeKeyValid: s.relaxedShapeKeyValid,
		depth:                s.depth,
		accum:                s.accum,         // SHARED
		schemaToAlloc:        s.schemaToAlloc, // SHARED
		allocCounter:         s.allocCounter,  // SHARED pointer
		callstack:            callstackCopy,
		forks:                forksCopy,
		labels:               labelsCopy,
		tryDepth:             s.tryDepth,
		depthWidenBlocked:    s.depthWidenBlocked,
		forkUpdates:          forkUpdatesCopy,
		pathMode:             s.pathMode,
		currentPath:          pathCopy,
		id:                   s.id,       // Clone inherits ID initially, will be reassigned
		parentID:             s.parentID, // Clone inherits parent
		lineage:              s.lineage,  // Clone inherits lineage, will be extended
		varAllocHistory:      histCopy,
		varDesiredItemFP:     fpCopy,
		allocDesiredFP:       s.allocDesiredFP, // SHARED
		schemaFPIntent:       schemaFPCopy,
		// Theory 10: Hybrid Origin-Lattice (SHARED)
		allocOrigin:      s.allocOrigin,      // SHARED
		allocCardinality: s.allocCardinality, // SHARED
		dsu:              s.dsu,              // SHARED
	}
}

func cloneForkContinuation(fork forkContinuation) forkContinuation {
	cloned := fork
	cloned.stack = append([]SValue(nil), fork.stack...)
	cloned.scopes = cloneScopeMaps(fork.scopes)
	return cloned
}

func joinForkContinuations(a, b forkContinuations) forkContinuations {
	aValues, bValues := a.values(), b.values()
	limit := min(len(aValues), len(bValues))
	joined := make([]forkContinuation, 0, limit)
	for i := 0; i < limit; i++ {
		if !compatibleForkContinuations(aValues[i], bValues[i]) {
			break
		}
		fork := cloneForkContinuation(aValues[i])
		if fork.control != bValues[i].control {
			fork.control = nil
		}
		fork.depth = maxInt(fork.depth, bValues[i].depth)
		for j := range fork.stack {
			fork.stack[j].Schema = joinTwoSchemas(fork.stack[j].Schema, bValues[i].stack[j].Schema)
		}
		for j := range fork.scopes {
			for key, bValue := range bValues[i].scopes[j] {
				if aValue, ok := fork.scopes[j][key]; ok {
					fork.scopes[j][key] = joinTwoSchemas(aValue, bValue)
				} else {
					fork.scopes[j][key] = bValue
				}
			}
		}
		joined = append(joined, fork)
	}
	return forkContinuationsFromValues(joined)
}

func compatibleForkContinuations(a, b forkContinuation) bool {
	return a.kind == b.kind &&
		a.targetPC == b.targetPC &&
		a.arrayAppendKey == b.arrayAppendKey &&
		len(a.stack) == len(b.stack) &&
		a.scopeDepth == b.scopeDepth &&
		a.callstackLen == b.callstackLen &&
		a.pathMode == b.pathMode &&
		a.tryDepth == b.tryDepth &&
		equalCallStacks(a.callstack, b.callstack) &&
		reflect.DeepEqual(a.currentPath, b.currentPath)
}

func cloneScopeMaps(scopes []map[string]*oas3.Schema) []map[string]*oas3.Schema {
	cloned := make([]map[string]*oas3.Schema, len(scopes))
	for i, scope := range scopes {
		cloned[i] = make(map[string]*oas3.Schema, len(scope))
		for key, value := range scope {
			cloned[i][key] = value
		}
	}
	return cloned
}

func joinLabelContinuations(a, b labelContinuations, joinedForkDepth int) labelContinuations {
	aValues, bValues := a.values(), b.values()
	limit := min(len(aValues), len(bValues))
	joined := make([]labelContinuation, 0, limit)
	for i := 0; i < limit; i++ {
		if aValues[i] != bValues[i] ||
			aValues[i].forkDepth > joinedForkDepth {
			break
		}
		joined = append(joined, aValues[i])
	}
	return labelContinuationsFromValues(joined)
}

// recordSchemaFP records intended items fingerprint for a schema pointer
func (s *execState) recordSchemaFP(arr, items *oas3.Schema) {
	if arr == nil || items == nil {
		return
	}
	if s.schemaFPIntent == nil {
		s.schemaFPIntent = make(map[*oas3.Schema]string)
	}
	s.schemaFPIntent[arr] = schemaFingerprint(items)
}

// recordVarAlloc records that a variable has been assigned an allocID
func (s *execState) recordVarAlloc(key, allocID string) {
	if key == "" || allocID == "" {
		return
	}
	set, ok := s.varAllocHistory[key]
	if !ok {
		set = make(map[string]struct{}, 4)
		s.varAllocHistory[key] = set
	}
	set[allocID] = struct{}{}
}

// recordDesiredFP records the intended items schema fingerprint for a variable and its allocID
func (s *execState) recordDesiredFP(key string, items *oas3.Schema) {
	if key == "" || items == nil {
		return
	}
	fp := schemaFingerprint(items)
	s.varDesiredItemFP[key] = fp
	// Also record on the allocID for cross-variable propagation
	// Check what allocID this variable currently uses
	if v, ok := s.loadVar(key); ok {
		if allocID, ok := s.schemaToAlloc[v]; ok && allocID != "" {
			if s.allocDesiredFP == nil {
				s.allocDesiredFP = make(map[string]string)
			}
			s.allocDesiredFP[allocID] = fp
		}
	}
}

// push pushes a schema onto the stack.
func (s *execState) push(schema *oas3.Schema) {
	s.stack = append(s.stack, SValue{Schema: schema})
	s.invalidateShapeKey()
}

// pop removes and returns the top schema.
func (s *execState) pop() *oas3.Schema {
	if len(s.stack) == 0 {
		// Stack underflow - should not happen in correct bytecode
		return nil
	}
	top := s.stack[len(s.stack)-1].Schema
	s.stack = s.stack[:len(s.stack)-1]
	s.invalidateShapeKey()
	return top
}

// top returns the top schema without removing it.
func (s *execState) top() *oas3.Schema {
	if len(s.stack) == 0 {
		return nil
	}
	return s.stack[len(s.stack)-1].Schema
}

// pushFrame creates a new scope.
func (s *execState) pushFrame() {
	// Force append to allocate when the frame slice is shared by a clone.
	frame := make(map[string]*oas3.Schema)
	s.scopes = append(s.scopes[:len(s.scopes):len(s.scopes)], frame)
	s.scopeShapes = appendScopeShape(s.scopeShapes, frame)
	s.invalidateShapeKey()
}

// popFrame removes the current scope.
func (s *execState) popFrame() {
	if len(s.scopes) > 0 {
		s.scopes = s.scopes[:len(s.scopes)-1]
		if s.scopeShapes != nil {
			s.scopeShapes = s.scopeShapes.prev
		}
		s.invalidateShapeKey()
	}
}

// storeVar saves a schema to the current frame.
func (s *execState) storeVar(key string, schema *oas3.Schema) {
	if len(s.scopes) > 0 {
		index := len(s.scopes) - 1
		_, existed := s.scopes[index][key]
		frame := cloneScopeMap(s.scopes[index])
		frame[key] = schema
		s.scopes[index] = frame
		if !existed {
			var prev *scopeShapeNode
			if s.scopeShapes != nil {
				prev = s.scopeShapes.prev
			}
			s.scopeShapes = appendScopeShape(prev, frame)
			s.invalidateShapeKey()
		}
	}
}

func (s *execState) invalidateShapeKey() {
	s.shapeKeyValid = false
	s.relaxedShapeKeyValid = false
}

func cloneScopeMap(scope map[string]*oas3.Schema) map[string]*oas3.Schema {
	cloned := make(map[string]*oas3.Schema, len(scope))
	for key, value := range scope {
		cloned[key] = value
	}
	return cloned
}

// loadVar retrieves a schema from frames (inner to outer).
func (s *execState) loadVar(key string) (*oas3.Schema, bool) {
	for i := len(s.scopes) - 1; i >= 0; i-- {
		if schema, ok := s.scopes[i][key]; ok {
			return schema, true
		}
	}
	return nil, false
}

// newExecState creates an initial execution state.
func newExecState(input *oas3.Schema) *execState {
	counter := 0
	state := &execState{
		pc:               0,
		stack:            make([]SValue, 0, 16),
		scopes:           make([]map[string]*oas3.Schema, 0, 4),
		depth:            0,
		accum:            make(map[string]*oas3.Schema), // Shared accumulator
		schemaToAlloc:    make(map[*oas3.Schema]string), // Schema → allocID mapping
		allocCounter:     &counter,                      // Shared counter (pointer)
		callstack:        callStack{},
		id:               0,   // Initial state ID
		parentID:         0,   // Root has no parent
		lineage:          "0", // Root lineage
		varAllocHistory:  make(map[string]map[string]struct{}),
		varDesiredItemFP: make(map[string]string),
		allocDesiredFP:   make(map[string]string), // Shared
		schemaFPIntent:   make(map[*oas3.Schema]string),
		// Theory 10: Hybrid Origin-Lattice
		allocOrigin:      make(map[string]*AllocOrigin),
		allocCardinality: make(map[string]*ArrayCardinality),
		dsu:              NewDSU(),
	}
	state.pushFrame() // Initial global frame
	state.push(input) // Push input onto stack
	return state
}

// stateWorklist manages the queue of states to execute.
type stateWorklist struct {
	states      []*execState
	nextStateID int // Monotonic counter for state IDs
}

// newStateWorklist creates a new worklist.
func newStateWorklist() *stateWorklist {
	return &stateWorklist{
		states:      make([]*execState, 0, 32),
		nextStateID: 1, // Start from 1 (0 is reserved for root)
	}
}

// push adds a state to the worklist.
func (w *stateWorklist) push(state *execState) {
	w.states = append(w.states, state)
}

// pop removes and returns the next state (LIFO/stack for depth-first).
// This ensures iteration paths complete before done paths, which is critical
// for array construction where the done path loads the accumulated result.
func (w *stateWorklist) pop() *execState {
	if len(w.states) == 0 {
		return nil
	}
	// Pop from end (LIFO) instead of beginning (FIFO)
	state := w.states[len(w.states)-1]
	w.states = w.states[:len(w.states)-1]
	return state
}

// isEmpty checks if worklist is empty.
func (w *stateWorklist) isEmpty() bool {
	return len(w.states) == 0
}
