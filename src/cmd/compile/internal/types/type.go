// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types

import (
	"cmd/compile/internal/base"
	"cmd/internal/src"
	"fmt"
	"strings"
	"sync"
)

// Object represents an ir.Node, but without needing to import cmd/compile/internal/ir,
// which would cause an import cycle. The uses in other packages must type assert
// values of type Object to ir.Node or a more specific type.
type Object interface {
	Pos() src.XPos
	Sym() *Sym
	Type() *Type
}

// A TypeObject is an Object representing a named type.
type TypeObject interface {
	Object
	TypeDefn() *Type // for "type T Defn", returns Defn
}

//go:generate stringer -type Kind -trimprefix T type.go

// Kind describes a kind of type.
type Kind uint8

const (
	Txxx Kind = iota

	TINT8
	TUINT8
	TINT16
	TUINT16
	TINT32
	TUINT32
	TINT64
	TUINT64
	TINT
	TUINT
	TUINTPTR

	TCOMPLEX64
	TCOMPLEX128

	TFLOAT32
	TFLOAT64

	TBOOL

	TPTR
	TFUNC
	TSLICE
	TARRAY
	TSTRUCT
	TCHAN
	TMAP
	TINTER
	TFORW
	TANY
	TSTRING
	TUNSAFEPTR
	TTYPEPARAM
	TUNION

	// pseudo-types for literals
	TIDEAL // untyped numeric constants
	TNIL
	TBLANK

	// pseudo-types used temporarily only during frame layout (CalcSize())
	TFUNCARGS
	TCHANARGS

	// SSA backend types
	TSSA     // internal types used by SSA backend (flags, memory, etc.)
	TTUPLE   // a pair of types, used by SSA backend
	TRESULTS // multiple types; the result of calling a function or method, with a memory at the end.

	NTYPE
)

// ChanDir is whether a channel can send, receive, or both.
type ChanDir uint8

func (c ChanDir) CanRecv() bool { return c&Crecv != 0 }
func (c ChanDir) CanSend() bool { return c&Csend != 0 }

const (
	// types of channel
	// must match ../../../../reflect/type.go:/ChanDir
	Crecv ChanDir = 1 << 0
	Csend ChanDir = 1 << 1
	Cboth ChanDir = Crecv | Csend
)

// Types stores pointers to predeclared named types.
//
// It also stores pointers to several special types:
//   - Types[TANY] is the placeholder "any" type recognized by SubstArgTypes.
//   - Types[TBLANK] represents the blank variable's type.
//   - Types[TINTER] is the canonical "interface{}" type.
//   - Types[TNIL] represents the predeclared "nil" value's type.
//   - Types[TUNSAFEPTR] is package unsafe's Pointer type.
var Types [NTYPE]*Type

var (
	// Predeclared alias types. These are actually created as distinct
	// defined types for better error messages, but are then specially
	// treated as identical to their respective underlying types.
	AnyType  *Type
	ByteType *Type
	RuneType *Type

	// Predeclared error interface type.
	ErrorType *Type
	// Predeclared comparable interface type.
	ComparableType *Type

	// Types to represent untyped string and boolean constants.
	UntypedString = newType(TSTRING)
	UntypedBool   = newType(TBOOL)

	// Types to represent untyped numeric constants.
	UntypedInt     = newType(TIDEAL)
	UntypedRune    = newType(TIDEAL)
	UntypedFloat   = newType(TIDEAL)
	UntypedComplex = newType(TIDEAL)
)

// A Type represents a Go type.
//
// There may be multiple unnamed types with identical structure. However, there must
// be a unique Type object for each unique named (defined) type. After noding, a
// package-level type can be looked up by building its unique symbol sym (sym =
// package.Lookup(name)) and checking sym.Def. If sym.Def is non-nil, the type
// already exists at package scope and is available at sym.Def.(*ir.Name).Type().
// Local types (which may have the same name as a package-level type) are
// distinguished by the value of vargen.
type Type struct {
	// extra contains extra etype-specific fields.
	// As an optimization, those etype-specific structs which contain exactly
	// one pointer-shaped field are stored as values rather than pointers when possible.
	//
	// TMAP: *Map
	// TFORW: *Forward
	// TFUNC: *Func
	// TSTRUCT: *Struct
	// TINTER: *Interface
	// TFUNCARGS: FuncArgs
	// TCHANARGS: ChanArgs
	// TCHAN: *Chan
	// TPTR: Ptr
	// TARRAY: *Array
	// TSLICE: Slice
	// TSSA: string
	// TTYPEPARAM:  *Typeparam
	// TUNION: *Union
	extra interface{}

	// width is the width of this Type in bytes.
	width int64 // valid if Align > 0

	// list of base methods (excluding embedding)
	methods Fields
	// list of all methods (including embedding)
	allMethods Fields

	// canonical OTYPE node for a named type (should be an ir.Name node with same sym)
	nod Object
	// the underlying type (type literal or predeclared type) for a defined type
	underlying *Type

	// Cache of composite types, with this type being the element type.
	cache struct {
		ptr   *Type // *T, or nil
		slice *Type // []T, or nil
	}

	sym    *Sym  // symbol containing name, for named types
	vargen int32 // unique name for OTYPE/ONAME

	kind  Kind  // kind of type
	align uint8 // the required alignment of this type, in bytes (0 means Width and Align have not yet been computed)

	flags bitset8

	// For defined (named) generic types, a pointer to the list of type params
	// (in order) of this type that need to be instantiated. For instantiated
	// generic types, this is the targs used to instantiate them. These targs
	// may be typeparams (for re-instantiated types such as Value[T2]) or
	// concrete types (for fully instantiated types such as Value[int]).
	// rparams is only set for named types that are generic or are fully
	// instantiated from a generic type, and is otherwise set to nil.
	// TODO(danscales): choose a better name.
	rparams *[]*Type

	// For an instantiated generic type, the base generic type.
	// This backpointer is useful, because the base type is the type that has
	// the method bodies.
	origType *Type
}

func (*Type) CanBeAnSSAAux() {}

const (
	typeNotInHeap  = 1 << iota // type cannot be heap allocated
	typeNoalg                  // suppress hash and eq algorithm generation
	typeDeferwidth             // width computation has been deferred and type is on deferredTypeStack
	typeRecur
	typeHasTParam // there is a typeparam somewhere in the type (generic function or type)
	typeIsShape   // represents a set of closely related types, for generics
	typeHasShape  // there is a shape somewhere in the type
)

func (t *Type) NotInHeap() bool  { return t.flags&typeNotInHeap != 0 }
func (t *Type) Noalg() bool      { return t.flags&typeNoalg != 0 }
func (t *Type) Deferwidth() bool { return t.flags&typeDeferwidth != 0 }
func (t *Type) Recur() bool      { return t.flags&typeRecur != 0 }
func (t *Type) HasTParam() bool  { return t.flags&typeHasTParam != 0 }
func (t *Type) IsShape() bool    { return t.flags&typeIsShape != 0 }
func (t *Type) HasShape() bool   { return t.flags&typeHasShape != 0 }

func (t *Type) SetNotInHeap(b bool)  { t.flags.set(typeNotInHeap, b) }
func (t *Type) SetBroke(b bool)      { base.Assertf(!b, "SetBroke") }
func (t *Type) SetNoalg(b bool)      { t.flags.set(typeNoalg, b) }
func (t *Type) SetDeferwidth(b bool) { t.flags.set(typeDeferwidth, b) }
func (t *Type) SetRecur(b bool)      { t.flags.set(typeRecur, b) }

// Generic types should never have alg functions.
func (t *Type) SetHasTParam(b bool) { t.flags.set(typeHasTParam, b); t.flags.set(typeNoalg, b) }

// Should always do SetHasShape(true) when doing SetIsShape(true).
func (t *Type) SetIsShape(b bool)  { t.flags.set(typeIsShape, b) }
func (t *Type) SetHasShape(b bool) { t.flags.set(typeHasShape, b) }

// Kind returns the kind of type t.
func (t *Type) Kind() Kind { return t.kind }

// Sym returns the name of type t.
func (t *Type) Sym() *Sym       { return t.sym }
func (t *Type) SetSym(sym *Sym) { t.sym = sym }

// OrigType returns the original generic type that t is an
// instantiation of, if any.
func (t *Type) OrigType() *Type        { return t.origType }
func (t *Type) SetOrigType(orig *Type) { t.origType = orig }

// Underlying returns the underlying type of type t.
func (t *Type) Underlying() *Type { return t.underlying }

// SetNod associates t with syntax node n.
func (t *Type) SetNod(n Object) {
	// t.nod can be non-nil already
	// in the case of shared *Types, like []byte or interface{}.
	if t.nod == nil {
		t.nod = n
	}
}

// Pos returns a position associated with t, if any.
// This should only be used for diagnostics.
func (t *Type) Pos() src.XPos {
	if t.nod != nil {
		return t.nod.Pos()
	}
	return src.NoXPos
}

func (t *Type) RParams() []*Type {
	if t.rparams == nil {
		return nil
	}
	return *t.rparams
}

func (t *Type) SetRParams(rparams []*Type) {
	if len(rparams) == 0 {
		base.Fatalf("Setting nil or zero-length rparams")
	}
	t.rparams = &rparams
	// HasTParam should be set if any rparam is or has a type param. This is
	// to handle the case of a generic type which doesn't reference any of its
	// type params (e.g. most commonly, an empty struct).
	for _, rparam := range rparams {
		if rparam.HasTParam() {
			t.SetHasTParam(true)
			break
		}
		if rparam.HasShape() {
			t.SetHasShape(true)
			break
		}
	}
}

// IsBaseGeneric returns true if t is a generic type (not reinstantiated with
// another type params or fully instantiated.
func (t *Type) IsBaseGeneric() bool {
	return len(t.RParams()) > 0 && strings.Index(t.Sym().Name, "[") < 0
}

// IsInstantiatedGeneric returns t if t ia generic type that has been
// reinstantiated with new typeparams (i.e. is not fully instantiated).
func (t *Type) IsInstantiatedGeneric() bool {
	return len(t.RParams()) > 0 && strings.Index(t.Sym().Name, "[") >= 0 &&
		t.HasTParam()
}

// IsFullyInstantiated reports whether t is a fully instantiated generic type; i.e. an
// instantiated generic type where all type arguments are non-generic or fully
// instantiated generic types.
func (t *Type) IsFullyInstantiated() bool {
	return len(t.RParams()) > 0 && !t.HasTParam()
}

// NoPkg is a nil *Pkg value for clarity.
// It's intended for use when constructing types that aren't exported
// and thus don't need to be associated with any package.
var NoPkg *Pkg = nil

// Pkg returns the package that t appeared in.
//
// Pkg is only defined for function, struct, and interface types
// (i.e., types with named elements). This information isn't used by
// cmd/compile itself, but we need to track it because it's exposed by
// the go/types API.
func (t *Type) Pkg() *Pkg {
	switch t.kind {
	case TFUNC:
		return t.extra.(*Func).pkg
	case TSTRUCT:
		return t.extra.(*Struct).pkg
	case TINTER:
		return t.extra.(*Interface).pkg
	default:
		base.Fatalf("Pkg: unexpected kind: %v", t)
		return nil
	}
}

// Map contains Type fields specific to maps.
type Map struct {
	Key  *Type // Key type
	Elem *Type // Val (elem) type

	Bucket *Type // internal struct type representing a hash bucket
	Hmap   *Type // internal struct type representing the Hmap (map header object)
	Hiter  *Type // internal struct type representing hash iterator state
}

// MapType returns t's extra map-specific fields.
func (t *Type) MapType() *Map {
	t.wantEtype(TMAP)
	return t.extra.(*Map)
}

// Forward contains Type fields specific to forward types.
type Forward struct {
	Copyto      []*Type  // where to copy the eventual value to
	Embedlineno src.XPos // first use of this type as an embedded type
}

// ForwardType returns t's extra forward-type-specific fields.
func (t *Type) ForwardType() *Forward {
	t.wantEtype(TFORW)
	return t.extra.(*Forward)
}

// Func contains Type fields specific to func types.
type Func struct {
	Receiver *Type // function receiver
	Results  *Type // function results
	Params   *Type // function params
	TParams  *Type // type params of receiver (if method) or function

	pkg *Pkg

	// Argwid is the total width of the function receiver, params, and results.
	// It gets calculated via a temporary TFUNCARGS type.
	// Note that TFUNC's Width is Widthptr.
	Argwid int64
}

// FuncType returns t's extra func-specific fields.
func (t *Type) FuncType() *Func {
	t.wantEtype(TFUNC)
	return t.extra.(*Func)
}

// StructType contains Type fields specific to struct types.
type Struct struct {
	fields Fields
	pkg    *Pkg

	// Maps have three associated internal structs (see struct MapType).
	// Map links such structs back to their map type.
	Map *Type

	Funarg Funarg // type of function arguments for arg struct
}

// Fnstruct records the kind of function argument
type Funarg uint8

const (
	FunargNone    Funarg = iota
	FunargRcvr           // receiver
	FunargParams         // input parameters
	FunargResults        // output results
	FunargTparams        // type params
)

// StructType returns t's extra struct-specific fields.
func (t *Type) StructType() *Struct {
	t.wantEtype(TSTRUCT)
	return t.extra.(*Struct)
}

// Interface contains Type fields specific to interface types.
type Interface struct {
	pkg      *Pkg
	implicit bool
}

// Typeparam contains Type fields specific to typeparam types.
type Typeparam struct {
	index int // type parameter index in source order, starting at 0
	bound *Type
}

// Union contains Type fields specific to union types.
type Union struct {
	terms  []*Type
	tildes []bool // whether terms[i] is of form ~T
}

// Ptr contains Type fields specific to pointer types.
type Ptr struct {
	Elem *Type // element type
}

// ChanArgs contains Type fields specific to TCHANARGS types.
type ChanArgs struct {
	T *Type // reference to a chan type whose elements need a width check
}

// // FuncArgs contains Type fields specific to TFUNCARGS types.
type FuncArgs struct {
	T *Type // reference to a func type whose elements need a width check
}

// Chan contains Type fields specific to channel types.
type Chan struct {
	Elem *Type   // element type
	Dir  ChanDir // channel direction
}

// ChanType returns t's extra channel-specific fields.
func (t *Type) ChanType() *Chan {
	t.wantEtype(TCHAN)
	return t.extra.(*Chan)
}

type Tuple struct {
	first  *Type
	second *Type
	// Any tuple with a memory type must put that memory type second.
}

// Results are the output from calls that will be late-expanded.
type Results struct {
	Types []*Type // Last element is memory output from call.
}

// Array contains Type fields specific to array types.
type Array struct {
	Elem  *Type // element type
	Bound int64 // number of elements; <0 if unknown yet
}

// Slice contains Type fields specific to slice types.
type Slice struct {
	Elem *Type // element type
}

// A Field is a (Sym, Type) pairing along with some other information, and,
// depending on the context, is used to represent:
//  - a field in a struct
//  - a method in an interface or associated with a named type
//  - a function parameter
type Field struct {
	flags bitset8

	Embedded uint8 // embedded field

	Pos src.XPos

	// Name of field/method/parameter. Can be nil for interface fields embedded
	// in interfaces and unnamed parameters.
	Sym  *Sym
	Type *Type  // field type
	Note string // literal string annotation

	// For fields that represent function parameters, Nname points to the
	// associated ONAME Node. For fields that represent methods, Nname points to
	// the function name node.
	Nname Object

	// Offset in bytes of this field or method within its enclosing struct
	// or interface Type.  Exception: if field is function receiver, arg or
	// result, then this is BOGUS_FUNARG_OFFSET; types does not know the Abi.
	Offset int64
}

const (
	fieldIsDDD = 1 << iota // field is ... argument
	fieldBroke             // broken field definition
	fieldNointerface
)

func (f *Field) IsDDD() bool       { return f.flags&fieldIsDDD != 0 }
func (f *Field) Broke() bool       { return f.flags&fieldBroke != 0 }
func (f *Field) Nointerface() bool { return f.flags&fieldNointerface != 0 }

func (f *Field) SetIsDDD(b bool)       { f.flags.set(fieldIsDDD, b) }
func (f *Field) SetBroke(b bool)       { f.flags.set(fieldBroke, b) }
func (f *Field) SetNointerface(b bool) { f.flags.set(fieldNointerface, b) }

// End returns the offset of the first byte immediately after this field.
func (f *Field) End() int64 {
	return f.Offset + f.Type.width
}

// IsMethod reports whether f represents a method rather than a struct field.
func (f *Field) IsMethod() bool {
	return f.Type.kind == TFUNC && f.Type.Recv() != nil
}

// Fields is a pointer to a slice of *Field.
// This saves space in Types that do not have fields or methods
// compared to a simple slice of *Field.
type Fields struct {
	s *[]*Field
}

// Len returns the number of entries in f.
func (f *Fields) Len() int {
	if f.s == nil {
		return 0
	}
	return len(*f.s)
}

// Slice returns the entries in f as a slice.
// Changes to the slice entries will be reflected in f.
func (f *Fields) Slice() []*Field {
	if f.s == nil {
		return nil
	}
	return *f.s
}

// Index returns the i'th element of Fields.
// It panics if f does not have at least i+1 elements.
func (f *Fields) Index(i int) *Field {
	return (*f.s)[i]
}

// Set sets f to a slice.
// This takes ownership of the slice.
func (f *Fields) Set(s []*Field) {
	if len(s) == 0 {
		f.s = nil
	} else {
		// Copy s and take address of t rather than s to avoid
		// allocation in the case where len(s) == 0.
		t := s
		f.s = &t
	}
}

// Append appends entries to f.
func (f *Fields) Append(s ...*Field) {
	if f.s == nil {
		f.s = new([]*Field)
	}
	*f.s = append(*f.s, s...)
}

// New returns a new Type of the specified kind.
func newType(et Kind) *Type {
	t := &Type{
		kind:  et,
		width: BADWIDTH,
	}
	t.underlying = t
	// TODO(josharian): lazily initialize some of these?
	switch t.kind {
	case TMAP:
		t.extra = new(Map)
	case TFORW:
		t.extra = new(Forward)
	case TFUNC:
		t.extra = new(Func)
	case TSTRUCT:
		t.extra = new(Struct)
	case TINTER:
		t.extra = new(Interface)
	case TPTR:
		t.extra = Ptr{}
	case TCHANARGS:
		t.extra = ChanArgs{}
	case TFUNCARGS:
		t.extra = FuncArgs{}
	case TCHAN:
		t.extra = new(Chan)
	case TTUPLE:
		t.extra = new(Tuple)
	case TRESULTS:
		t.extra = new(Results)
	case TTYPEPARAM:
		t.extra = new(Typeparam)
	case TUNION:
		t.extra = new(Union)
	}
	return t
}

// NewArray returns a new fixed-length array Type.
func NewArray(elem *Type, bound int64) *Type {
	if bound < 0 {
		base.Fatalf("NewArray: invalid bound %v", bound)
	}
	t := newType(TARRAY)
	t.extra = &Array{Elem: elem, Bound: bound}
	t.SetNotInHeap(elem.NotInHeap())
	if elem.HasTParam() {
		t.SetHasTParam(true)
	}
	if elem.HasShape() {
		t.SetHasShape(true)
	}
	return t
}

// NewSlice returns the slice Type with element type elem.
func NewSlice(elem *Type) *Type {
	if t := elem.cache.slice; t != nil {
		if t.Elem() != elem {
			base.Fatalf("elem mismatch")
		}
		if elem.HasTParam() != t.HasTParam() || elem.HasShape() != t.HasShape() {
			base.Fatalf("Incorrect HasTParam/HasShape flag for cached slice type")
		}
		return t
	}

	t := newType(TSLICE)
	t.extra = Slice{Elem: elem}
	elem.cache.slice = t
	if elem.HasTParam() {
		t.SetHasTParam(true)
	}
	if elem.HasShape() {
		t.SetHasShape(true)
	}
	return t
}

// NewChan returns a new chan Type with direction dir.
func NewChan(elem *Type, dir ChanDir) *Type {
	t := newType(TCHAN)
	ct := t.ChanType()
	ct.Elem = elem
	ct.Dir = dir
	if elem.HasTParam() {
		t.SetHasTParam(true)
	}
	if elem.HasShape() {
		t.SetHasShape(true)
	}
	return t
}

func NewTuple(t1, t2 *Type) *Type {
	t := newType(TTUPLE)
	t.extra.(*Tuple).first = t1
	t.extra.(*Tuple).second = t2
	if t1.HasTParam() || t2.HasTParam() {
		t.SetHasTParam(true)
	}
	if t1.HasShape() || t2.HasShape() {
		t.SetHasShape(true)
	}
	return t
}

func newResults(types []*Type) *Type {
	t := newType(TRESULTS)
	t.extra.(*Results).Types = types
	return t
}

func NewResults(types []*Type) *Type {
	if len(types) == 1 && types[0] == TypeMem {
		return TypeResultMem
	}
	return newResults(types)
}

func newSSA(name string) *Type {
	t := newType(TSSA)
	t.extra = name
	return t
}

// NewMap returns a new map Type with key type k and element (aka value) type v.
func NewMap(k, v *Type) *Type {
	t := newType(TMAP)
	mt := t.MapType()
	mt.Key = k
	mt.Elem = v
	if k.HasTParam() || v.HasTParam() {
		t.SetHasTParam(true)
	}
	if k.HasShape() || v.HasShape() {
		t.SetHasShape(true)
	}
	return t
}

// NewPtrCacheEnabled controls whether *T Types are cached in T.
// Caching is disabled just before starting the backend.
// This allows the backend to run concurrently.
var NewPtrCacheEnabled = true

// NewPtr returns the pointer type pointing to t.
func NewPtr(elem *Type) *Type {
	if elem == nil {
		base.Fatalf("NewPtr: pointer to elem Type is nil")
	}

	if t := elem.cache.ptr; t != nil {
		if t.Elem() != elem {
			base.Fatalf("NewPtr: elem mismatch")
		}
		if elem.HasTParam() != t.HasTParam() || elem.HasShape() != t.HasShape() {
			base.Fatalf("Incorrect HasTParam/HasShape flag for cached pointer type")
		}
		return t
	}

	t := newType(TPTR)
	t.extra = Ptr{Elem: elem}
	t.width = int64(PtrSize)
	t.align = uint8(PtrSize)
	if NewPtrCacheEnabled {
		elem.cache.ptr = t
	}
	if elem.HasTParam() {
		t.SetHasTParam(true)
	}
	if elem.HasShape() {
		t.SetHasShape(true)
	}
	return t
}

// NewChanArgs returns a new TCHANARGS type for channel type c.
func NewChanArgs(c *Type) *Type {
	t := newType(TCHANARGS)
	t.extra = ChanArgs{T: c}
	return t
}

// NewFuncArgs returns a new TFUNCARGS type for func type f.
func NewFuncArgs(f *Type) *Type {
	t := newType(TFUNCARGS)
	t.extra = FuncArgs{T: f}
	return t
}

func NewField(pos src.XPos, sym *Sym, typ *Type) *Field {
	f := &Field{
		Pos:    pos,
		Sym:    sym,
		Type:   typ,
		Offset: BADWIDTH,
	}
	if typ == nil {
		f.SetBroke(true)
	}
	return f
}

// SubstAny walks t, replacing instances of "any" with successive
// elements removed from types.  It returns the substituted type.
func SubstAny(t *Type, types *[]*Type) *Type {
	if t == nil {
		return nil
	}

	switch t.kind {
	default:
		// Leave the type unchanged.

	case TANY:
		if len(*types) == 0 {
			base.Fatalf("SubstArgTypes: not enough argument types")
		}
		t = (*types)[0]
		*types = (*types)[1:]

	case TPTR:
		elem := SubstAny(t.Elem(), types)
		if elem != t.Elem() {
			t = t.copy()
			t.extra = Ptr{Elem: elem}
		}

	case TARRAY:
		elem := SubstAny(t.Elem(), types)
		if elem != t.Elem() {
			t = t.copy()
			t.extra.(*Array).Elem = elem
		}

	case TSLICE:
		elem := SubstAny(t.Elem(), types)
		if elem != t.Elem() {
			t = t.copy()
			t.extra = Slice{Elem: elem}
		}

	case TCHAN:
		elem := SubstAny(t.Elem(), types)
		if elem != t.Elem() {
			t = t.copy()
			t.extra.(*Chan).Elem = elem
		}

	case TMAP:
		key := SubstAny(t.Key(), types)
		elem := SubstAny(t.Elem(), types)
		if key != t.Key() || elem != t.Elem() {
			t = t.copy()
			t.extra.(*Map).Key = key
			t.extra.(*Map).Elem = elem
		}

	case TFUNC:
		recvs := SubstAny(t.Recvs(), types)
		params := SubstAny(t.Params(), types)
		results := SubstAny(t.Results(), types)
		if recvs != t.Recvs() || params != t.Params() || results != t.Results() {
			t = t.copy()
			t.FuncType().Receiver = recvs
			t.FuncType().Results = results
			t.FuncType().Params = params
		}

	case TSTRUCT:
		// Make a copy of all fields, including ones whose type does not change.
		// This prevents aliasing across functions, which can lead to later
		// fields getting their Offset incorrectly overwritten.
		fields := t.FieldSlice()
		nfs := make([]*Field, len(fields))
		for i, f := range fields {
			nft := SubstAny(f.Type, types)
			nfs[i] = f.Copy()
			nfs[i].Type = nft
		}
		t = t.copy()
		t.SetFields(nfs)
	}

	return t
}

// copy returns a shallow copy of the Type.
func (t *Type) copy() *Type {
	if t == nil {
		return nil
	}
	nt := *t
	// copy any *T Extra fields, to avoid aliasing
	switch t.kind {
	case TMAP:
		x := *t.extra.(*Map)
		nt.extra = &x
	case TFORW:
		x := *t.extra.(*Forward)
		nt.extra = &x
	case TFUNC:
		x := *t.extra.(*Func)
		nt.extra = &x
	case TSTRUCT:
		x := *t.extra.(*Struct)
		nt.extra = &x
	case TINTER:
		x := *t.extra.(*Interface)
		nt.extra = &x
	case TCHAN:
		x := *t.extra.(*Chan)
		nt.extra = &x
	case TARRAY:
		x := *t.extra.(*Array)
		nt.extra = &x
	case TTYPEPARAM:
		base.Fatalf("typeparam types cannot be copied")
	case TTUPLE, TSSA, TRESULTS:
		base.Fatalf("ssa types cannot be copied")
	}
	// TODO(mdempsky): Find out why this is necessary and explain.
	if t.underlying == t {
		nt.underlying = &nt
	}
	return &nt
}

func (f *Field) Copy() *Field {
	nf := *f
	return &nf
}

func (t *Type) wantEtype(et Kind) {
	if t.kind != et {
		base.Fatalf("want %v, but have %v", et, t)
	}
}

func (t *Type) Recvs() *Type   { return t.FuncType().Receiver }
func (t *Type) TParams() *Type { return t.FuncType().TParams }
func (t *Type) Params() *Type  { return t.FuncType().Params }
func (t *Type) Results() *Type { return t.FuncType().Results }

func (t *Type) NumRecvs() int   { return t.FuncType().Receiver.NumFields() }
func (t *Type) NumTParams() int { return t.FuncType().TParams.NumFields() }
func (t *Type) NumParams() int  { return t.FuncType().Params.NumFields() }
func (t *Type) NumResults() int { return t.FuncType().Results.NumFields() }

// IsVariadic reports whether function type t is variadic.
func (t *Type) IsVariadic() bool {
	n := t.NumParams()
	return n > 0 && t.Params().Field(n-1).IsDDD()
}

// Recv returns the receiver of function type t, if any.
func (t *Type) Recv() *Field {
	s := t.Recvs()
	if s.NumFields() == 0 {
		return nil
	}
	return s.Field(0)
}

// RecvsParamsResults stores the accessor functions for a function Type's
// receiver, parameters, and result parameters, in that order.
// It can be used to iterate over all of a function's parameter lists.
var RecvsParamsResults = [3]func(*Type) *Type{
	(*Type).Recvs, (*Type).Params, (*Type).Results,
}

// RecvsParams is like RecvsParamsResults, but omits result parameters.
var RecvsParams = [2]func(*Type) *Type{
	(*Type).Recvs, (*Type).Params,
}

// ParamsResults is like RecvsParamsResults, but omits receiver parameters.
var ParamsResults = [2]func(*Type) *Type{
	(*Type).Params, (*Type).Results,
}

// Key returns the key type of map type t.
func (t *Type) Key() *Type {
	t.wantEtype(TMAP)
	return t.extra.(*Map).Key
}

// Elem returns the type of elements of t.
// Usable with pointers, channels, arrays, slices, and maps.
func (t *Type) Elem() *Type {
	switch t.kind {
	case TPTR:
		return t.extra.(Ptr).Elem
	case TARRAY:
		return t.extra.(*Array).Elem
	case TSLICE:
		return t.extra.(Slice).Elem
	case TCHAN:
		return t.extra.(*Chan).Elem
	case TMAP:
		return t.extra.(*Map).Elem
	}
	base.Fatalf("Type.Elem %s", t.kind)
	return nil
}

// ChanArgs returns the channel type for TCHANARGS type t.
func (t *Type) ChanArgs() *Type {
	t.wantEtype(TCHANARGS)
	return t.extra.(ChanArgs).T
}

// FuncArgs returns the func type for TFUNCARGS type t.
func (t *Type) FuncArgs() *Type {
	t.wantEtype(TFUNCARGS)
	return t.extra.(FuncArgs).T
}

// IsFuncArgStruct reports whether t is a struct representing function parameters or results.
func (t *Type) IsFuncArgStruct() bool {
	return t.kind == TSTRUCT && t.extra.(*Struct).Funarg != FunargNone
}

// Methods returns a pointer to the base methods (excluding embedding) for type t.
// These can either be concrete methods (for non-interface types) or interface
// methods (for interface types).
func (t *Type) Methods() *Fields {
	return &t.methods
}

// AllMethods returns a pointer to all the methods (including embedding) for type t.
// For an interface type, this is the set of methods that are typically iterated
// over. For non-interface types, AllMethods() only returns a valid result after
// CalcMethods() has been called at least once.
func (t *Type) AllMethods() *Fields {
	if t.kind == TINTER {
		// Calculate the full method set of an interface type on the fly
		// now, if not done yet.
		CalcSize(t)
	}
	return &t.allMethods
}

// SetAllMethods sets the set of all methods (including embedding) for type t.
// Use this method instead of t.AllMethods().Set(), which might call CalcSize() on
// an uninitialized interface type.
func (t *Type) SetAllMethods(fs []*Field) {
	t.allMethods.Set(fs)
}

// Fields returns the fields of struct type t.
func (t *Type) Fields() *Fields {
	t.wantEtype(TSTRUCT)
	return &t.extra.(*Struct).fields
}

// Field returns the i'th field of struct type t.
func (t *Type) Field(i int) *Field {
	return t.Fields().Slice()[i]
}

// FieldSlice returns a slice of containing all fields of
// a struct type t.
func (t *Type) FieldSlice() []*Field {
	return t.Fields().Slice()
}

// SetFields sets struct type t's fields to fields.
func (t *Type) SetFields(fields []*Field) {
	// If we've calculated the width of t before,
	// then some other type such as a function signature
	// might now have the wrong type.
	// Rather than try to track and invalidate those,
	// enforce that SetFields cannot be called once
	// t's width has been calculated.
	if t.widthCalculated() {
		base.Fatalf("SetFields of %v: width previously calculated", t)
	}
	t.wantEtype(TSTRUCT)
	for _, f := range fields {
		// If type T contains a field F with a go:notinheap
		// type, then T must also be go:notinheap. Otherwise,
		// you could heap allocate T and then get a pointer F,
		// which would be a heap pointer to a go:notinheap
		// type.
		if f.Type != nil && f.Type.NotInHeap() {
			t.SetNotInHeap(true)
			break
		}
	}
	t.Fields().Set(fields)
}

// SetInterface sets the base methods of an interface type t.
func (t *Type) SetInterface(methods []*Field) {
	t.wantEtype(TINTER)
	t.Methods().Set(methods)
}

// ArgWidth returns the total aligned argument size for a function.
// It includes the receiver, parameters, and results.
func (t *Type) ArgWidth() int64 {
	t.wantEtype(TFUNC)
	return t.extra.(*Func).Argwid
}

func (t *Type) Size() int64 {
	if t.kind == TSSA {
		if t == TypeInt128 {
			return 16
		}
		return 0
	}
	CalcSize(t)
	return t.width
}

func (t *Type) Alignment() int64 {
	CalcSize(t)
	return int64(t.align)
}

func (t *Type) SimpleString() string {
	return t.kind.String()
}

// Cmp is a comparison between values a and b.
// -1 if a < b
//  0 if a == b
//  1 if a > b
type Cmp int8

const (
	CMPlt = Cmp(-1)
	CMPeq = Cmp(0)
	CMPgt = Cmp(1)
)

// Compare compares types for purposes of the SSA back
// end, returning a Cmp (one of CMPlt, CMPeq, CMPgt).
// The answers are correct for an optimizer
// or code generator, but not necessarily typechecking.
// The order chosen is arbitrary, only consistency and division
// into equivalence classes (Types that compare CMPeq) matters.
func (t *Type) Compare(x *Type) Cmp {
	if x == t {
		return CMPeq
	}
	return t.cmp(x)
}

func cmpForNe(x bool) Cmp {
	if x {
		return CMPlt
	}
	return CMPgt
}

func (r *Sym) cmpsym(s *Sym) Cmp {
	if r == s {
		return CMPeq
	}
	if r == nil {
		return CMPlt
	}
	if s == nil {
		return CMPgt
	}
	// Fast sort, not pretty sort
	if len(r.Name) != len(s.Name) {
		return cmpForNe(len(r.Name) < len(s.Name))
	}
	if r.Pkg != s.Pkg {
		if len(r.Pkg.Prefix) != len(s.Pkg.Prefix) {
			return cmpForNe(len(r.Pkg.Prefix) < len(s.Pkg.Prefix))
		}
		if r.Pkg.Prefix != s.Pkg.Prefix {
			return cmpForNe(r.Pkg.Prefix < s.Pkg.Prefix)
		}
	}
	if r.Name != s.Name {
		return cmpForNe(r.Name < s.Name)
	}
	return CMPeq
}

// cmp compares two *Types t and x, returning CMPlt,
// CMPeq, CMPgt as t<x, t==x, t>x, for an arbitrary
// and optimizer-centric notion of comparison.
// TODO(josharian): make this safe for recursive interface types
// and use in signatlist sorting. See issue 19869.
func (t *Type) cmp(x *Type) Cmp {
	// This follows the structure of function identical in identity.go
	// with two exceptions.
	// 1. Symbols are compared more carefully because a <,=,> result is desired.
	// 2. Maps are treated specially to avoid endless recursion -- maps
	//    contain an internal data type not expressible in Go source code.
	if t == x {
		return CMPeq
	}
	if t == nil {
		return CMPlt
	}
	if x == nil {
		return CMPgt
	}

	if t.kind != x.kind {
		return cmpForNe(t.kind < x.kind)
	}

	if t.sym != nil || x.sym != nil {
		// Special case: we keep byte and uint8 separate
		// for error messages. Treat them as equal.
		switch t.kind {
		case TUINT8:
			if (t == Types[TUINT8] || t == ByteType) && (x == Types[TUINT8] || x == ByteType) {
				return CMPeq
			}

		case TINT32:
			if (t == Types[RuneType.kind] || t == RuneType) && (x == Types[RuneType.kind] || x == RuneType) {
				return CMPeq
			}

		case TINTER:
			// Make sure named any type matches any empty interface.
			if t == AnyType && x.IsEmptyInterface() || x == AnyType && t.IsEmptyInterface() {
				return CMPeq
			}
		}
	}

	if c := t.sym.cmpsym(x.sym); c != CMPeq {
		return c
	}

	if x.sym != nil {
		// Syms non-nil, if vargens match then equal.
		if t.vargen != x.vargen {
			return cmpForNe(t.vargen < x.vargen)
		}
		return CMPeq
	}
	// both syms nil, look at structure below.

	switch t.kind {
	case TBOOL, TFLOAT32, TFLOAT64, TCOMPLEX64, TCOMPLEX128, TUNSAFEPTR, TUINTPTR,
		TINT8, TINT16, TINT32, TINT64, TINT, TUINT8, TUINT16, TUINT32, TUINT64, TUINT:
		return CMPeq

	case TSSA:
		tname := t.extra.(string)
		xname := x.extra.(string)
		// desire fast sorting, not pretty sorting.
		if len(tname) == len(xname) {
			if tname == xname {
				return CMPeq
			}
			if tname < xname {
				return CMPlt
			}
			return CMPgt
		}
		if len(tname) > len(xname) {
			return CMPgt
		}
		return CMPlt

	case TTUPLE:
		xtup := x.extra.(*Tuple)
		ttup := t.extra.(*Tuple)
		if c := ttup.first.Compare(xtup.first); c != CMPeq {
			return c
		}
		return ttup.second.Compare(xtup.second)

	case TRESULTS:
		xResults := x.extra.(*Results)
		tResults := t.extra.(*Results)
		xl, tl := len(xResults.Types), len(tResults.Types)
		if tl != xl {
			if tl < xl {
				return CMPlt
			}
			return CMPgt
		}
		for i := 0; i < tl; i++ {
			if c := tResults.Types[i].Compare(xResults.Types[i]); c != CMPeq {
				return c
			}
		}
		return CMPeq

	case TMAP:
		if c := t.Key().cmp(x.Key()); c != CMPeq {
			return c
		}
		return t.Elem().cmp(x.Elem())

	case TPTR, TSLICE:
		// No special cases for these, they are handled
		// by the general code after the switch.

	case TSTRUCT:
		if t.StructType().Map == nil {
			if x.StructType().Map != nil {
				return CMPlt // nil < non-nil
			}
			// to the fallthrough
		} else if x.StructType().Map == nil {
			return CMPgt // nil > non-nil
		} else if t.StructType().Map.MapType().Bucket == t {
			// Both have non-nil Map
			// Special case for Maps which include a recursive type where the recursion is not broken with a named type
			if x.StructType().Map.MapType().Bucket != x {
				return CMPlt // bucket maps are least
			}
			return t.StructType().Map.cmp(x.StructType().Map)
		} else if x.StructType().Map.MapType().Bucket == x {
			return CMPgt // bucket maps are least
		} // If t != t.Map.Bucket, fall through to general case

		tfs := t.FieldSlice()
		xfs := x.FieldSlice()
		for i := 0; i < len(tfs) && i < len(xfs); i++ {
			t1, x1 := tfs[i], xfs[i]
			if t1.Embedded != x1.Embedded {
				return cmpForNe(t1.Embedded < x1.Embedded)
			}
			if t1.Note != x1.Note {
				return cmpForNe(t1.Note < x1.Note)
			}
			if c := t1.Sym.cmpsym(x1.Sym); c != CMPeq {
				return c
			}
			if c := t1.Type.cmp(x1.Type); c != CMPeq {
				return c
			}
		}
		if len(tfs) != len(xfs) {
			return cmpForNe(len(tfs) < len(xfs))
		}
		return CMPeq

	case TINTER:
		tfs := t.AllMethods().Slice()
		xfs := x.AllMethods().Slice()
		for i := 0; i < len(tfs) && i < len(xfs); i++ {
			t1, x1 := tfs[i], xfs[i]
			if c := t1.Sym.cmpsym(x1.Sym); c != CMPeq {
				return c
			}
			if c := t1.Type.cmp(x1.Type); c != CMPeq {
				return c
			}
		}
		if len(tfs) != len(xfs) {
			return cmpForNe(len(tfs) < len(xfs))
		}
		return CMPeq

	case TFUNC:
		for _, f := range RecvsParamsResults {
			// Loop over fields in structs, ignoring argument names.
			tfs := f(t).FieldSlice()
			xfs := f(x).FieldSlice()
			for i := 0; i < len(tfs) && i < len(xfs); i++ {
				ta := tfs[i]
				tb := xfs[i]
				if ta.IsDDD() != tb.IsDDD() {
					return cmpForNe(!ta.IsDDD())
				}
				if c := ta.Type.cmp(tb.Type); c != CMPeq {
					return c
				}
			}
			if len(tfs) != len(xfs) {
				return cmpForNe(len(tfs) < len(xfs))
			}
		}
		return CMPeq

	case TARRAY:
		if t.NumElem() != x.NumElem() {
			return cmpForNe(t.NumElem() < x.NumElem())
		}

	case TCHAN:
		if t.ChanDir() != x.ChanDir() {
			return cmpForNe(t.ChanDir() < x.ChanDir())
		}

	default:
		e := fmt.Sprintf("Do not know how to compare %v with %v", t, x)
		panic(e)
	}

	// Common element type comparison for TARRAY, TCHAN, TPTR, and TSLICE.
	return t.Elem().cmp(x.Elem())
}

// IsKind reports whether t is a Type of the specified kind.
func (t *Type) IsKind(et Kind) bool {
	return t != nil && t.kind == et
}

func (t *Type) IsBoolean() bool {
	return t.kind == TBOOL
}

var unsignedEType = [...]Kind{
	TINT8:    TUINT8,
	TUINT8:   TUINT8,
	TINT16:   TUINT16,
	TUINT16:  TUINT16,
	TINT32:   TUINT32,
	TUINT32:  TUINT32,
	TINT64:   TUINT64,
	TUINT64:  TUINT64,
	TINT:     TUINT,
	TUINT:    TUINT,
	TUINTPTR: TUINTPTR,
}

// ToUnsigned returns the unsigned equivalent of integer type t.
func (t *Type) ToUnsigned() *Type {
	if !t.IsInteger() {
		base.Fatalf("unsignedType(%v)", t)
	}
	return Types[unsignedEType[t.kind]]
}

func (t *Type) IsInteger() bool {
	switch t.kind {
	case TINT8, TUINT8, TINT16, TUINT16, TINT32, TUINT32, TINT64, TUINT64, TINT, TUINT, TUINTPTR:
		return true
	}
	return t == UntypedInt || t == UntypedRune
}

func (t *Type) IsSigned() bool {
	switch t.kind {
	case TINT8, TINT16, TINT32, TINT64, TINT:
		return true
	}
	return false
}

func (t *Type) IsUnsigned() bool {
	switch t.kind {
	case TUINT8, TUINT16, TUINT32, TUINT64, TUINT, TUINTPTR:
		return true
	}
	return false
}

func (t *Type) IsFloat() bool {
	return t.kind == TFLOAT32 || t.kind == TFLOAT64 || t == UntypedFloat
}

func (t *Type) IsComplex() bool {
	return t.kind == TCOMPLEX64 || t.kind == TCOMPLEX128 || t == UntypedComplex
}

// IsPtr reports whether t is a regular Go pointer type.
// This does not include unsafe.Pointer.
func (t *Type) IsPtr() bool {
	return t.kind == TPTR
}

// IsPtrElem reports whether t is the element of a pointer (to t).
func (t *Type) IsPtrElem() bool {
	return t.cache.ptr != nil
}

// IsUnsafePtr reports whether t is an unsafe pointer.
func (t *Type) IsUnsafePtr() bool {
	return t.kind == TUNSAFEPTR
}

// IsUintptr reports whether t is an uintptr.
func (t *Type) IsUintptr() bool {
	return t.kind == TUINTPTR
}

// IsPtrShaped reports whether t is represented by a single machine pointer.
// In addition to regular Go pointer types, this includes map, channel, and
// function types and unsafe.Pointer. It does not include array or struct types
// that consist of a single pointer shaped type.
// TODO(mdempsky): Should it? See golang.org/issue/15028.
func (t *Type) IsPtrShaped() bool {
	return t.kind == TPTR || t.kind == TUNSAFEPTR ||
		t.kind == TMAP || t.kind == TCHAN || t.kind == TFUNC
}

// HasNil reports whether the set of values determined by t includes nil.
func (t *Type) HasNil() bool {
	switch t.kind {
	case TCHAN, TFUNC, TINTER, TMAP, TNIL, TPTR, TSLICE, TUNSAFEPTR:
		return true
	}
	return false
}

func (t *Type) IsString() bool {
	return t.kind == TSTRING
}

func (t *Type) IsMap() bool {
	return t.kind == TMAP
}

func (t *Type) IsChan() bool {
	return t.kind == TCHAN
}

func (t *Type) IsSlice() bool {
	return t.kind == TSLICE
}

func (t *Type) IsArray() bool {
	return t.kind == TARRAY
}

func (t *Type) IsStruct() bool {
	return t.kind == TSTRUCT
}

func (t *Type) IsInterface() bool {
	return t.kind == TINTER
}

func (t *Type) IsUnion() bool {
	return t.kind == TUNION
}

func (t *Type) IsTypeParam() bool {
	return t.kind == TTYPEPARAM
}

// IsEmptyInterface reports whether t is an empty interface type.
func (t *Type) IsEmptyInterface() bool {
	return t.IsInterface() && t.AllMethods().Len() == 0
}

// IsScalar reports whether 't' is a scalar Go type, e.g.
// bool/int/float/complex. Note that struct and array types consisting
// of a single scalar element are not considered scalar, likewise
// pointer types are also not considered scalar.
func (t *Type) IsScalar() bool {
	switch t.kind {
	case TBOOL, TINT8, TUINT8, TINT16, TUINT16, TINT32,
		TUINT32, TINT64, TUINT64, TINT, TUINT,
		TUINTPTR, TCOMPLEX64, TCOMPLEX128, TFLOAT32, TFLOAT64:
		return true
	}
	return false
}

func (t *Type) PtrTo() *Type {
	return NewPtr(t)
}

func (t *Type) NumFields() int {
	if t.kind == TRESULTS {
		return len(t.extra.(*Results).Types)
	}
	return t.Fields().Len()
}
func (t *Type) FieldType(i int) *Type {
	if t.kind == TTUPLE {
		switch i {
		case 0:
			return t.extra.(*Tuple).first
		case 1:
			return t.extra.(*Tuple).second
		default:
			panic("bad tuple index")
		}
	}
	if t.kind == TRESULTS {
		return t.extra.(*Results).Types[i]
	}
	return t.Field(i).Type
}
func (t *Type) FieldOff(i int) int64 {
	return t.Field(i).Offset
}
func (t *Type) FieldName(i int) string {
	return t.Field(i).Sym.Name
}

func (t *Type) NumElem() int64 {
	t.wantEtype(TARRAY)
	return t.extra.(*Array).Bound
}

type componentsIncludeBlankFields bool

const (
	IgnoreBlankFields componentsIncludeBlankFields = false
	CountBlankFields  componentsIncludeBlankFields = true
)

// NumComponents returns the number of primitive elements that compose t.
// Struct and array types are flattened for the purpose of counting.
// All other types (including string, slice, and interface types) count as one element.
// If countBlank is IgnoreBlankFields, then blank struct fields
// (and their comprised elements) are excluded from the count.
// struct { x, y [3]int } has six components; [10]struct{ x, y string } has twenty.
func (t *Type) NumComponents(countBlank componentsIncludeBlankFields) int64 {
	switch t.kind {
	case TSTRUCT:
		if t.IsFuncArgStruct() {
			base.Fatalf("NumComponents func arg struct")
		}
		var n int64
		for _, f := range t.FieldSlice() {
			if countBlank == IgnoreBlankFields && f.Sym.IsBlank() {
				continue
			}
			n += f.Type.NumComponents(countBlank)
		}
		return n
	case TARRAY:
		return t.NumElem() * t.Elem().NumComponents(countBlank)
	}
	return 1
}

// SoleComponent returns the only primitive component in t,
// if there is exactly one. Otherwise, it returns nil.
// Components are counted as in NumComponents, including blank fields.
func (t *Type) SoleComponent() *Type {
	switch t.kind {
	case TSTRUCT:
		if t.IsFuncArgStruct() {
			base.Fatalf("SoleComponent func arg struct")
		}
		if t.NumFields() != 1 {
			return nil
		}
		return t.Field(0).Type.SoleComponent()
	case TARRAY:
		if t.NumElem() != 1 {
			return nil
		}
		return t.Elem().SoleComponent()
	}
	return t
}

// ChanDir returns the direction of a channel type t.
// The direction will be one of Crecv, Csend, or Cboth.
func (t *Type) ChanDir() ChanDir {
	t.wantEtype(TCHAN)
	return t.extra.(*Chan).Dir
}

func (t *Type) IsMemory() bool {
	if t == TypeMem || t.kind == TTUPLE && t.extra.(*Tuple).second == TypeMem {
		return true
	}
	if t.kind == TRESULTS {
		if types := t.extra.(*Results).Types; len(types) > 0 && types[len(types)-1] == TypeMem {
			return true
		}
	}
	return false
}
func (t *Type) IsFlags() bool   { return t == TypeFlags }
func (t *Type) IsVoid() bool    { return t == TypeVoid }
func (t *Type) IsTuple() bool   { return t.kind == TTUPLE }
func (t *Type) IsResults() bool { return t.kind == TRESULTS }

// IsUntyped reports whether t is an untyped type.
func (t *Type) IsUntyped() bool {
	if t == nil {
		return false
	}
	if t == UntypedString || t == UntypedBool {
		return true
	}
	switch t.kind {
	case TNIL, TIDEAL:
		return true
	}
	return false
}

// HasPointers reports whether t contains a heap pointer.
// Note that this function ignores pointers to go:notinheap types.
func (t *Type) HasPointers() bool {
	return PtrDataSize(t) > 0
}

var recvType *Type

// FakeRecvType returns the singleton type used for interface method receivers.
func FakeRecvType() *Type {
	if recvType == nil {
		recvType = NewPtr(newType(TSTRUCT))
	}
	return recvType
}

func FakeRecv() *Field {
	return NewField(src.NoXPos, nil, FakeRecvType())
}

var (
	// TSSA types. HasPointers assumes these are pointer-free.
	TypeInvalid   = newSSA("invalid")
	TypeMem       = newSSA("mem")
	TypeFlags     = newSSA("flags")
	TypeVoid      = newSSA("void")
	TypeInt128    = newSSA("int128")
	TypeResultMem = newResults([]*Type{TypeMem})
)

// NewNamed returns a new named type for the given type name. obj should be an
// ir.Name. The new type is incomplete (marked as TFORW kind), and the underlying
// type should be set later via SetUnderlying(). References to the type are
// maintained until the type is filled in, so those references can be updated when
// the type is complete.
func NewNamed(obj TypeObject) *Type {
	t := newType(TFORW)
	t.sym = obj.Sym()
	t.nod = obj
	if t.sym.Pkg == ShapePkg {
		t.SetIsShape(true)
		t.SetHasShape(true)
	}
	return t
}

// Obj returns the canonical type name node for a named type t, nil for an unnamed type.
func (t *Type) Obj() Object {
	if t.sym != nil {
		return t.nod
	}
	return nil
}

// typeGen tracks the number of function-scoped defined types that
// have been declared. It's used to generate unique linker symbols for
// their runtime type descriptors.
var typeGen int32

// SetVargen assigns a unique generation number to type t, which must
// be a defined type declared within function scope. The generation
// number is used to distinguish it from other similarly spelled
// defined types from the same package.
//
// TODO(mdempsky): Come up with a better solution.
func (t *Type) SetVargen() {
	base.Assertf(t.Sym() != nil, "SetVargen on anonymous type %v", t)
	base.Assertf(t.vargen == 0, "type %v already has Vargen %v", t, t.vargen)

	typeGen++
	t.vargen = typeGen
}

// SetUnderlying sets the underlying type of an incomplete type (i.e. type whose kind
// is currently TFORW). SetUnderlying automatically updates any types that were waiting
// for this type to be completed.
func (t *Type) SetUnderlying(underlying *Type) {
	if underlying.kind == TFORW {
		// This type isn't computed yet; when it is, update n.
		underlying.ForwardType().Copyto = append(underlying.ForwardType().Copyto, t)
		return
	}

	ft := t.ForwardType()

	// TODO(mdempsky): Fix Type rekinding.
	t.kind = underlying.kind
	t.extra = underlying.extra
	t.width = underlying.width
	t.align = underlying.align
	t.underlying = underlying.underlying

	if underlying.NotInHeap() {
		t.SetNotInHeap(true)
	}
	if underlying.HasTParam() {
		t.SetHasTParam(true)
	}
	if underlying.HasShape() {
		t.SetHasShape(true)
	}

	// spec: "The declared type does not inherit any methods bound
	// to the existing type, but the method set of an interface
	// type [...] remains unchanged."
	if t.IsInterface() {
		t.methods = underlying.methods
		t.allMethods = underlying.allMethods
	}

	// Update types waiting on this type.
	for _, w := range ft.Copyto {
		w.SetUnderlying(t)
	}

	// Double-check use of type as embedded type.
	if ft.Embedlineno.IsKnown() {
		if t.IsPtr() || t.IsUnsafePtr() {
			base.ErrorfAt(ft.Embedlineno, "embedded type cannot be a pointer")
		}
	}
}

func fieldsHasTParam(fields []*Field) bool {
	for _, f := range fields {
		if f.Type != nil && f.Type.HasTParam() {
			return true
		}
	}
	return false
}

func fieldsHasShape(fields []*Field) bool {
	for _, f := range fields {
		if f.Type != nil && f.Type.HasShape() {
			return true
		}
	}
	return false
}

// NewBasic returns a new basic type of the given kind.
func newBasic(kind Kind, obj Object) *Type {
	t := newType(kind)
	t.sym = obj.Sym()
	t.nod = obj
	return t
}

// NewInterface returns a new interface for the given methods and
// embedded types. Embedded types are specified as fields with no Sym.
func NewInterface(pkg *Pkg, methods []*Field, implicit bool) *Type {
	t := newType(TINTER)
	t.SetInterface(methods)
	for _, f := range methods {
		// f.Type could be nil for a broken interface declaration
		if f.Type != nil && f.Type.HasTParam() {
			t.SetHasTParam(true)
			break
		}
		if f.Type != nil && f.Type.HasShape() {
			t.SetHasShape(true)
			break
		}
	}
	if anyBroke(methods) {
		t.SetBroke(true)
	}
	t.extra.(*Interface).pkg = pkg
	t.extra.(*Interface).implicit = implicit
	return t
}

// NewTypeParam returns a new type param with the specified sym (package and name)
// and specified index within the typeparam list.
func NewTypeParam(sym *Sym, index int) *Type {
	t := newType(TTYPEPARAM)
	t.sym = sym
	t.extra.(*Typeparam).index = index
	t.SetHasTParam(true)
	return t
}

// Index returns the index of the type param within its param list.
func (t *Type) Index() int {
	t.wantEtype(TTYPEPARAM)
	return t.extra.(*Typeparam).index
}

// SetIndex sets the index of the type param within its param list.
func (t *Type) SetIndex(i int) {
	t.wantEtype(TTYPEPARAM)
	t.extra.(*Typeparam).index = i
}

// SetBound sets the bound of a typeparam.
func (t *Type) SetBound(bound *Type) {
	t.wantEtype(TTYPEPARAM)
	t.extra.(*Typeparam).bound = bound
}

// Bound returns the bound of a typeparam.
func (t *Type) Bound() *Type {
	t.wantEtype(TTYPEPARAM)
	return t.extra.(*Typeparam).bound
}

// IsImplicit reports whether an interface is implicit (i.e. elided from a type
// parameter constraint).
func (t *Type) IsImplicit() bool {
	t.wantEtype(TINTER)
	return t.extra.(*Interface).implicit
}

// MarkImplicit marks the interface as implicit.
func (t *Type) MarkImplicit() {
	t.wantEtype(TINTER)
	t.extra.(*Interface).implicit = true
}

// NewUnion returns a new union with the specified set of terms (types). If
// tildes[i] is true, then terms[i] represents ~T, rather than just T.
func NewUnion(terms []*Type, tildes []bool) *Type {
	t := newType(TUNION)
	if len(terms) != len(tildes) {
		base.Fatalf("Mismatched terms and tildes for NewUnion")
	}
	t.extra.(*Union).terms = terms
	t.extra.(*Union).tildes = tildes
	nt := len(terms)
	for i := 0; i < nt; i++ {
		if terms[i].HasTParam() {
			t.SetHasTParam(true)
		}
		if terms[i].HasShape() {
			t.SetHasShape(true)
		}
	}
	return t
}

// NumTerms returns the number of terms in a union type.
func (t *Type) NumTerms() int {
	t.wantEtype(TUNION)
	return len(t.extra.(*Union).terms)
}

// Term returns ith term of a union type as (term, tilde). If tilde is true, term
// represents ~T, rather than just T.
func (t *Type) Term(i int) (*Type, bool) {
	t.wantEtype(TUNION)
	u := t.extra.(*Union)
	return u.terms[i], u.tildes[i]
}

const BOGUS_FUNARG_OFFSET = -1000000000

func unzeroFieldOffsets(f []*Field) {
	for i := range f {
		f[i].Offset = BOGUS_FUNARG_OFFSET // This will cause an explosion if it is not corrected
	}
}

// NewSignature returns a new function type for the given receiver,
// parameters, results, and type parameters, any of which may be nil.
func NewSignature(pkg *Pkg, recv *Field, tparams, params, results []*Field) *Type {
	var recvs []*Field
	if recv != nil {
		recvs = []*Field{recv}
	}

	t := newType(TFUNC)
	ft := t.FuncType()

	funargs := func(fields []*Field, funarg Funarg) *Type {
		s := NewStruct(NoPkg, fields)
		s.StructType().Funarg = funarg
		return s
	}

	if recv != nil {
		recv.Offset = BOGUS_FUNARG_OFFSET
	}
	unzeroFieldOffsets(params)
	unzeroFieldOffsets(results)
	ft.Receiver = funargs(recvs, FunargRcvr)
	// TODO(danscales): just use nil here (save memory) if no tparams
	ft.TParams = funargs(tparams, FunargTparams)
	ft.Params = funargs(params, FunargParams)
	ft.Results = funargs(results, FunargResults)
	ft.pkg = pkg
	if len(tparams) > 0 || fieldsHasTParam(recvs) || fieldsHasTParam(params) ||
		fieldsHasTParam(results) {
		t.SetHasTParam(true)
	}
	if fieldsHasShape(recvs) || fieldsHasShape(params) || fieldsHasShape(results) {
		t.SetHasShape(true)
	}

	return t
}

// NewStruct returns a new struct with the given fields.
func NewStruct(pkg *Pkg, fields []*Field) *Type {
	t := newType(TSTRUCT)
	t.SetFields(fields)
	if anyBroke(fields) {
		t.SetBroke(true)
	}
	t.extra.(*Struct).pkg = pkg
	if fieldsHasTParam(fields) {
		t.SetHasTParam(true)
	}
	if fieldsHasShape(fields) {
		t.SetHasShape(true)
	}
	return t
}

func anyBroke(fields []*Field) bool {
	for _, f := range fields {
		if f.Broke() {
			return true
		}
	}
	return false
}

var (
	IsInt     [NTYPE]bool
	IsFloat   [NTYPE]bool
	IsComplex [NTYPE]bool
	IsSimple  [NTYPE]bool
)

var IsOrdered [NTYPE]bool

// IsReflexive reports whether t has a reflexive equality operator.
// That is, if x==x for all x of type t.
func IsReflexive(t *Type) bool {
	switch t.Kind() {
	case TBOOL,
		TINT,
		TUINT,
		TINT8,
		TUINT8,
		TINT16,
		TUINT16,
		TINT32,
		TUINT32,
		TINT64,
		TUINT64,
		TUINTPTR,
		TPTR,
		TUNSAFEPTR,
		TSTRING,
		TCHAN:
		return true

	case TFLOAT32,
		TFLOAT64,
		TCOMPLEX64,
		TCOMPLEX128,
		TINTER:
		return false

	case TARRAY:
		return IsReflexive(t.Elem())

	case TSTRUCT:
		for _, t1 := range t.Fields().Slice() {
			if !IsReflexive(t1.Type) {
				return false
			}
		}
		return true

	default:
		base.Fatalf("bad type for map key: %v", t)
		return false
	}
}

// Can this type be stored directly in an interface word?
// Yes, if the representation is a single pointer.
func IsDirectIface(t *Type) bool {
	switch t.Kind() {
	case TPTR:
		// Pointers to notinheap types must be stored indirectly. See issue 42076.
		return !t.Elem().NotInHeap()
	case TCHAN,
		TMAP,
		TFUNC,
		TUNSAFEPTR:
		return true

	case TARRAY:
		// Array of 1 direct iface type can be direct.
		return t.NumElem() == 1 && IsDirectIface(t.Elem())

	case TSTRUCT:
		// Struct with 1 field of direct iface type can be direct.
		return t.NumFields() == 1 && IsDirectIface(t.Field(0).Type)
	}

	return false
}

// IsInterfaceMethod reports whether (field) m is
// an interface method. Such methods have the
// special receiver type types.FakeRecvType().
func IsInterfaceMethod(f *Type) bool {
	return f.Recv().Type == FakeRecvType()
}

// IsMethodApplicable reports whether method m can be called on a
// value of type t. This is necessary because we compute a single
// method set for both T and *T, but some *T methods are not
// applicable to T receivers.
func IsMethodApplicable(t *Type, m *Field) bool {
	return t.IsPtr() || !m.Type.Recv().Type.IsPtr() || IsInterfaceMethod(m.Type) || m.Embedded == 2
}

// IsRuntimePkg reports whether p is package runtime.
func IsRuntimePkg(p *Pkg) bool {
	if base.Flag.CompilingRuntime && p == LocalPkg {
		return true
	}
	return p.Path == "runtime"
}

// IsReflectPkg reports whether p is package reflect.
func IsReflectPkg(p *Pkg) bool {
	if p == LocalPkg {
		return base.Ctxt.Pkgpath == "reflect"
	}
	return p.Path == "reflect"
}

// ReceiverBaseType returns the underlying type, if any,
// that owns methods with receiver parameter t.
// The result is either a named type or an anonymous struct.
func ReceiverBaseType(t *Type) *Type {
	if t == nil {
		return nil
	}

	// Strip away pointer if it's there.
	if t.IsPtr() {
		if t.Sym() != nil {
			return nil
		}
		t = t.Elem()
		if t == nil {
			return nil
		}
	}

	// Must be a named type or anonymous struct.
	if t.Sym() == nil && !t.IsStruct() {
		return nil
	}

	// Check types.
	if IsSimple[t.Kind()] {
		return t
	}
	switch t.Kind() {
	case TARRAY, TCHAN, TFUNC, TMAP, TSLICE, TSTRING, TSTRUCT:
		return t
	}
	return nil
}

func FloatForComplex(t *Type) *Type {
	switch t.Kind() {
	case TCOMPLEX64:
		return Types[TFLOAT32]
	case TCOMPLEX128:
		return Types[TFLOAT64]
	}
	base.Fatalf("unexpected type: %v", t)
	return nil
}

func ComplexForFloat(t *Type) *Type {
	switch t.Kind() {
	case TFLOAT32:
		return Types[TCOMPLEX64]
	case TFLOAT64:
		return Types[TCOMPLEX128]
	}
	base.Fatalf("unexpected type: %v", t)
	return nil
}

func TypeSym(t *Type) *Sym {
	return TypeSymLookup(TypeSymName(t))
}

func TypeSymLookup(name string) *Sym {
	typepkgmu.Lock()
	s := typepkg.Lookup(name)
	typepkgmu.Unlock()
	return s
}

func TypeSymName(t *Type) string {
	name := t.LinkString()
	// Use a separate symbol name for Noalg types for #17752.
	if TypeHasNoAlg(t) {
		name = "noalg." + name
	}
	return name
}

// Fake package for runtime type info (headers)
// Don't access directly, use typeLookup below.
var (
	typepkgmu sync.Mutex // protects typepkg lookups
	typepkg   = NewPkg("type", "type")
)

var SimType [NTYPE]Kind

// Fake package for shape types (see typecheck.Shapify()).
var ShapePkg = NewPkg("go.shape", "go.shape")
