package gorgonia

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"

	"github.com/chewxy/gorgonia/tensor"
	tf32 "github.com/chewxy/gorgonia/tensor/f32"
	tf64 "github.com/chewxy/gorgonia/tensor/f64"
	ti "github.com/chewxy/gorgonia/tensor/i"
	"github.com/chewxy/gorgonia/tensor/types"
	"github.com/pkg/errors"
)

/* This file contains tensor related Ops */

// atOp takes a Tensor and returns the value at the coordinates.
type atOp struct {
	coordinates coordinates
	d           int
}

// atOp has this type
//		op :: Tensor a → a
func (op atOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(floats))
	tt := newTensorType(op.d, a)

	return newFunctionType(tt, a)
}

func (op atOp) returnsPtr() bool    { return false }
func (op atOp) overwriteInput() int { return -1 }
func (op atOp) callsExtern() bool   { return false }

func (op atOp) inferShape(retType Type, inputs ...*Node) (retVal types.Shape, err error) {
	if len(inputs) < 1 {
		err = NewError(GraphError, "repeatOp should only have one or more inputs. Got %v instead", len(inputs))
	}
	return scalarShape, nil
}

func (op atOp) DiffWRT(i int) []bool                       { return make([]bool, i) }
func (op atOp) SymDiff(Nodes, *Node, *Node) (Nodes, error) { return nil, nondiffErr(op) }
func (op atOp) String() string                             { return fmt.Sprintf("At(%v)", op.coordinates) }

func (op atOp) Do(inputs ...Value) (retVal Value, err error) {
	if len(inputs) != 1 {
		err = NewError(GraphError, "AtOp only expects one input. Got %d instead", len(inputs))
		return
	}

	t := inputs[0].(Tensor)
	switch tt := t.Tensor.(type) {
	case *tf64.Tensor:
		r := tt.At(op.coordinates...)
		return anyToValue(r)
	case *tf32.Tensor:
		r := tt.At(op.coordinates...)
		return anyToValue(r)
	}
	panic("unreacheable")
}

func (op atOp) WriteHash(h hash.Hash) {
	fmt.Fprintf(h, "atOp")
	if err := binary.Write(h, binary.LittleEndian, op.d); err != nil {
		panic(err)
	}
	fmt.Fprintf(h, "at%v", op.coordinates)
}

func (op atOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

func (op atOp) isStmt() bool { return true }

type sizeOp struct {
	axis, d int
	val     int // if we know ahead of time what the size is...
}

// sizeOp is a function with this type:
//		sizeOp :: Tensor d a → a
func (op sizeOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(floats))

	// handle scalar cases
	if op.d == 0 {
		return newFunctionType(a, a)
	}

	tt := newTensorType(op.d, a)
	return newFunctionType(tt, a)
}

func (op sizeOp) returnsPtr() bool                               { return false }
func (op sizeOp) overwriteInput() int                            { return -1 }
func (op sizeOp) callsExtern() bool                              { return false }
func (op sizeOp) inferShape(Type, ...*Node) (types.Shape, error) { return scalarShape, nil } // TODO: return error
func (op sizeOp) DiffWRT(i int) []bool                           { return []bool{false} }
func (op sizeOp) String() string {
	if op.val != 0 {
		return fmt.Sprintf("SizeOf=%d", op.val)
	}
	return fmt.Sprintf("SizeOf(%d)", op.axis)
}

func (op sizeOp) SymDiff(inputs Nodes, output, gradNode *Node) (Nodes, error) {
	return nil, nondiffErr(op)
}

func (op sizeOp) Do(inputs ...Value) (retVal Value, err error) {
	if len(inputs) != 1 {
		err = NewError(GraphError, "sizeOp only takes one input. Got %v instead", len(inputs))
		return
	}

	var val interface{} // only ints, floats plz
	switch t := inputs[0].(type) {
	case Tensor:
		sh := t.Shape()
		if op.axis >= len(sh) {
			err = NewError(ShapeError, "Shape is %v. Want size of %d", sh, op.axis)
		}
		size := sh[op.axis]

		// cast as ... types
		switch t.Dtype() {
		case Float64:
			val = float64(size)
		case Float32:
			val = float32(size)
		case Int:
			val = size
		default:
			err = nyi("sizeOf.Do() Tensor input", t.Dtype())
			return
		}

	case Scalar:
		switch t.t {
		case Float64:
			val = float64(1.0)
		case Float32:
			val = float32(1.0)
		case Int:
			val = 1
		default:
			err = nyi("sizeOf.Do() Scalar input", t.t)
			return
		}
	}

	return NewScalarValue(val), nil
}

func (op sizeOp) WriteHash(h hash.Hash) {
	h.Write([]byte("sizeOf"))
	if err := binary.Write(h, binary.LittleEndian, byte(op.d)); err != nil {
		panic(err)
	}
	h.Write([]byte("on"))
	if err := binary.Write(h, binary.LittleEndian, byte(op.axis)); err != nil {
		panic(err)
	}
}

func (op sizeOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

type repeatOp struct {
	along axes

	inputShape types.Shape
	d          int

	arg0Dim  int
	children int
}

func newRepeatOp(along axes, children Nodes) *repeatOp {
	retVal := &repeatOp{
		along:    along,
		children: len(children),
		arg0Dim:  children[0].Dims(),
	}
	if s, err := retVal.inferShape(nil, children...); err == nil {
		retVal.inputShape = s
		retVal.d = s.Dims()
	}

	return retVal
}

// repeat is an overload of three functions:
//		repeat :: a → a → a → Tensor a
// 		repeat :: Tensor a → a → a → Tensor a
//		repeat :: a → 1 → 1 → a
//
// The last of which is a special case of the first. But I didn't want to create a dependent-type system
// for a trivial language, so I'll just hardcode this in
func (op repeatOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(floats))
	var i0t Type
	var rt Type

	if op.arg0Dim == 0 {
		i0t = a
	} else {
		i0t = newTensorType(op.arg0Dim, a)
	}

	// if we know the result already, then we know the return type as well
	if op.d == 0 && op.inputShape != nil {
		rt = a
	} else {
		rt = newTensorType(op.d, a)
	}

	var ft Types
	ft = append(ft, i0t)

	for i := 1; i < op.children; i++ {
		ft = append(ft, a)
	}
	ft = append(ft, rt)
	return newFunctionType(ft...)
}

func (op repeatOp) returnsPtr() bool    { return true }
func (op repeatOp) overwriteInput() int { return -1 }
func (op repeatOp) callsExtern() bool   { return false }

func (op repeatOp) inferShape(retType Type, inputs ...*Node) (retVal types.Shape, err error) {
	if len(inputs) < 2 {
		err = NewError(GraphError, "repeatOp should only have two or more inputs. Got %v instead", len(inputs))
	}

	if op.inputShape != nil {
		retVal = op.inputShape
		return
	}

	input := inputs[0]
	repeats := inputs[1:]

	knownRepeats := make([]int, len(repeats))
	for i, rep := range repeats {
		if size, ok := rep.op.(sizeOp); ok && size.val > 0 {
			knownRepeats[i] = size.val
		}
	}

	if input.IsScalar() {
		retVal = types.Shape{1, 1} // fill it up just in case
	} else {
		retVal = input.shape.Clone()
	}

	for i, axis := range op.along {
		rep := knownRepeats[i]
		if rep == 1 || rep == 0 { // 0 means unknown
			continue
		}

		retVal[axis] *= rep
	}

	if oneone.Eq(retVal) {
		retVal = scalarShape
	}

	return
}

func (op repeatOp) DiffWRT(i int) []bool {
	symdiffLogf("DiffWRT: %d", i)
	retVal := make([]bool, i)
	retVal[0] = true
	return retVal
}

func (op repeatOp) SymDiff(inputs Nodes, output, gradNode *Node) (retVal Nodes, err error) {
	var n *Node
	if n, err = Sum(gradNode, op.along...); err == nil {
		n.setGroup(gradClust)
	}
	retVal = make(Nodes, len(inputs))
	retVal[0] = n
	return
}

func (op repeatOp) DoDiff(inputs Nodes, output *Node) (err error) {
	if len(inputs) < 2 {
		err = NewError(GraphError, "repeat expects at least 2 inputs. Got %v instead", len(inputs))
		return
	}

	xdv := inputs[0].boundTo.(*dualValue)
	ydv := output.boundTo.(*dualValue)

	var reps []int
	var repeats []Value
	for _, r := range inputs[1:] {
		repeats = append(repeats, r.Value())
	}

	if reps, err = valuesToInts(repeats); err != nil {
		return
	}

	xshape := xdv.Shape()
	var d Value
	d = ydv.d

	// we make it a colVec
	if xshape.IsVector() && !xshape.IsColVec() && !xshape.IsRowVec() {
		xshape = types.Shape{xshape[0], 1}
	}

	if xshape.IsScalar() {
		sum := newSumOp(op.along, output.shape, output.Dims())
		if d, err = sum.Do(d); err != nil {
			err = errors.Wrapf(err, doFail, sum)
			return
		}
	} else {
		for _, axis := range op.along {
			if xshape[axis] == 1 {
				sum := newSumOp(op.along, output.shape, output.Dims())
				if d, err = sum.Do(d); err != nil {
					err = errors.Wrapf(err, doFail, sum)
					return
				}
			} else {
				newShape := xshape.Clone()
				newShape = newShape[0 : axis+1]
				newShape = append(newShape, reps...)
				if axis+1 < xshape.Dims() {
					newShape = append(newShape, xshape[axis+1:]...)
				}

				along := []int{axis + 1}

				// a scalar can never get to this path
				t := d.(Tensor)
				if err = t.Reshape(newShape...); err != nil {
					err = errors.Wrapf(err, reshapeFail, newShape, t.DataSize())
					return
				}

				sum := newSumOp(along, newShape, len(newShape))
				if d, err = sum.Do(d); err != nil {
					err = errors.Wrapf(err, doFail, sum)
					return
				}
			}
		}
	}

	add := newElemBinOp(addOpType, inputs[0], output)
	d, err = add.UnsafeDo(xdv.d, d)

	if !add.returnsPtr() || inputs[0].IsScalar() {
		err = xdv.SetDeriv(d)
	}

	return
}

func (op repeatOp) String() string { return fmt.Sprintf("Repeat%v", op.along) }

func (op repeatOp) Do(inputs ...Value) (retVal Value, err error) {
	if len(inputs) < 2 {
		err = NewError(GraphError, "repeat expects at least 2 inputs. Got %v instead", len(inputs))
		return
	}

	// process inputs[1:]
	var reps []int
	repeats := inputs[1:]
	if len(repeats) != len(op.along) {
		err = NewError(GraphError, "repeat mismatch. Expected %d. Got %d inputs instead", len(op.along), len(repeats))
		return
	}

	if reps, err = valuesToInts(repeats); err != nil {
		return
	}

	// process inputs[0]
	var t types.Tensor
	switch iv := inputs[0].(type) {
	case Tensor:
		t = iv.Tensor
	case Scalar:
		switch iv.t {
		case Float64:
			f := iv.v.(float64)
			t = tf64.NewTensor(tf64.AsScalar(f))
		case Float32:
			f := iv.v.(float32)
			t = tf32.NewTensor(tf32.AsScalar(f))
		default:
			err = nyi("repeatOp.Do() Scalar Input", iv)
			return
		}
	}

	// actually do repeat
	for i, axis := range op.along {
		rep := reps[i]
		if rep == 1 {
			// then no need to waste CPU
			continue
		}
		if t, err = tensor.Repeat(t, axis, rep); err != nil {
			err = errors.Wrapf(err, repFail, axis, rep)
			return
		}
	}
	retVal = FromTensor(t)

	return
}

func (op repeatOp) WriteHash(h hash.Hash) {
	h.Write([]byte("repeat"))
	if err := binary.Write(h, binary.LittleEndian, byte(op.d)); err != nil {
		panic(err)
	}

	fmt.Fprintf(h, "%v", op.along)
	if err := binary.Write(h, binary.LittleEndian, byte(op.children)); err != nil {
		panic(err)
	}

	if op.arg0Dim == 0 {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
}

func (op repeatOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

// sliceOp represents a slicing operation. If end <= start, it means ":"
type sliceOp struct {
	along int // along which axis to slice?
	d     int // how many dimensions were the original tensor

	start int
	end   int
}

func newSliceOp(from, to, along, d int) sliceOp {
	return sliceOp{
		along: along,
		d:     d,
		start: from,
		end:   to,
	}
}

// slicing a tensor value T[:] has type
// 		slice :: Tensor a → Tensor a
// 		slice :: Tensor a → a
//
// The latter is in the case where the resulting dimensions is 0, returning a scalar
func (op sliceOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(floats))
	tt := newTensorType(op.d, a)

	selection := op.end - op.start

	if selection == 1 {
		if op.d == 1 {
			return newFunctionType(tt, a)
		}

		tt2 := newTensorType(op.d-1, a)
		return newFunctionType(tt, tt2)
	}

	return newFunctionType(tt, tt)
}

func (op sliceOp) inferShape(typ Type, inputs ...*Node) (s types.Shape, err error) {
	if len(inputs) > 1 {
		// error
		err = NewError(GraphError, "sliceOp should only have one or more inputs. Got %v instead", len(inputs))
		return
	}

	t := inputs[0]

	s = make(types.Shape, len(t.shape))
	for i, v := range t.shape {
		s[i] = v
	}
	if op.all() {
		return
	}

	s[op.along] = op.end - op.start

	if s.Eq(types.Shape{1, 1}) || s.IsScalar() {
		s = scalarShape
	}

	return
}

func (op sliceOp) DiffWRT(i int) []bool {
	if i > 1 {
		// error
		err := NewError(GraphError, "sliceOp should only have one or more inputs. Got %v instead", i)
		panic(err)
	}

	return []bool{true}
}

func (op sliceOp) SymDiff(inputs Nodes, outputNode, gradNode *Node) (retVal Nodes, err error) {
	if len(inputs) != 1 {
		err = NewError(GraphError, "sliceOp should only have one or more inputs. Got %v instead", len(inputs))
		return
	}

	t := inputs[0]
	incrOp := sliceIncrOp{op}

	retVal = make(Nodes, 1)
	retVal[0], err = applyOp(incrOp, t, gradNode)
	return
}

func (op sliceOp) DoDiff(inputs Nodes, output *Node) (err error) {
	if len(inputs) != 1 {
		err = NewError(GraphError, "sliceOp should only have one or more inputs. Got %v instead", len(inputs))
		return
	}

	xdv := inputs[0].boundTo.(*dualValue)
	ydv := output.boundTo.(*dualValue)
	incrOp := sliceIncrOp{op}

	var d Value
	if d, err = incrOp.Do(xdv.Value, ydv.d); err != nil {
		err = errors.Wrapf(err, doFail, incrOp)
		return
	}

	// there is no need to handle scalars, because you can never slice a scalar
	add := newElemBinOp(addOpType, inputs[0], output)
	if _, err = add.UnsafeDo(xdv.d, d); err != nil {
		err = errors.Wrapf(err, unsafeDoFail, add)
	}

	return
}

func (op sliceOp) Do(inputs ...Value) (retVal Value, err error) {
	if len(inputs) != 1 {
		err = NewError(GraphError, "sliceOp should only have one or more inputs. Got %v instead", len(inputs))
		return
	}

	t := inputs[0]
	// prep the slices
	var slices []types.Slice
	slices = make([]types.Slice, len(t.Shape()))

	if !op.all() {
		slices[op.along] = op
	}
	switch T := t.(type) {
	case Tensor:
		switch tt := T.Tensor.(type) {
		case *tf64.Tensor:
			// actually do shit
			var v64 *tf64.Tensor // it's a view though
			if v64, err = tt.Slice(slices...); err != nil {
				err = errors.Wrapf(err, sliceFail, slices)
				return
			}

			// prep retVal
			if v64.IsScalar() {
				retVal, err = anyToValue(v64.ScalarValue())
			} else {
				retVal = FromTensor(v64)
			}
		case *tf32.Tensor:
			// actually do shit
			var v32 *tf32.Tensor // it's a view though
			if v32, err = tt.Slice(slices...); err != nil {
				err = errors.Wrapf(err, sliceFail, slices)
				return
			}

			// prep retVal
			if v32.IsScalar() {
				retVal, err = anyToValue(v32.ScalarValue())
			} else {
				retVal = FromTensor(v32)
			}
		case *ti.Tensor:
			// actually do shit
			var vi *ti.Tensor // it's a view though
			if vi, err = tt.Slice(slices...); err != nil {
				err = errors.Wrapf(err, sliceFail, slices)
				return
			}

			// prep retVal
			if vi.IsScalar() {
				retVal, err = anyToValue(vi.ScalarValue())
			} else {
				retVal = FromTensor(vi)
			}
		// case *tb.Tensor:
		default:
			err = nyi("sliceOp.Do() Tensor Input", T)
			return
		}
	case Scalar:
		err = NewError(RuntimeError, "Cannot slice a scalar value")
		return
	default:
		err = nyi("sliceOp.Do() Unknown Input", t)
		return
	}
	return
}

func (op sliceOp) returnsPtr() bool    { return true }
func (op sliceOp) callsExtern() bool   { return false }
func (op sliceOp) overwriteInput() int { return -1 }
func (op sliceOp) WriteHash(h hash.Hash) {
	h.Write([]byte("slice"))
	if err := binary.Write(h, binary.LittleEndian, byte(op.d)); err != nil {
		panic(err)
	}
	fmt.Fprintf(h, "%v", op.along)
	if err := binary.Write(h, binary.LittleEndian, byte(op.start)); err != nil {
		panic(err)
	}
	if err := binary.Write(h, binary.LittleEndian, byte(op.end)); err != nil {
		panic(err)
	}
}
func (op sliceOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

func (op sliceOp) String() string {
	var buf bytes.Buffer
	buf.WriteString("T[")
	for i := 0; i < op.along; i++ {
		buf.WriteString(":, ")
	}

	if op.all() {
		buf.WriteString(":")
	} else {
		fmt.Fprintf(&buf, "%d:%d", op.start, op.end)
	}

	buf.WriteString("...]")
	return buf.String()
}

// sliceOp also implements types.Slice

func (op sliceOp) Start() int { return op.start }
func (op sliceOp) End() int   { return op.end }
func (op sliceOp) all() bool  { return op.end <= op.start }

// T[:] +=incr
// THIS IS AN UNSAFE OPERATION
type sliceIncrOp struct {
	sliceOp
}

// slicing a tensor value T[:] has type
// 		slice :: Tensor a → b → Tensor a
//
// b can be a or Vector a
func (op sliceIncrOp) Type() Type {
	a := newTypeVariable("a", withTVConstraints(floats))
	b := newTypeVariable("b", withTVConstraints(floats))
	tt := newTensorType(op.d, a)

	return newFunctionType(tt, b, tt)
}

func (op sliceIncrOp) inferShape(typ Type, inputs ...*Node) (s types.Shape, err error) {
	if len(inputs) != 2 {
		// error
		err = NewError(GraphError, "sliceIncrOp should only have one or more inputs. Got %v instead", len(inputs))
		return
	}

	t := inputs[0]
	s = t.shape
	return
}

func (op sliceIncrOp) DiffWRT(i int) []bool {
	if i > 2 {
		// error
		err := NewError(GraphError, "sliceOp should only have 2 inputs. Got %v instead", i)
		panic(err)
	}

	return []bool{true, false}
}

func (op sliceIncrOp) SymDiff(inputs Nodes, outputNode, gradNode *Node) (retVal Nodes, err error) {
	var slicedRes *Node
	if slicedRes, err = applyOp(op.sliceOp, gradNode); err != nil {
		err = errors.Wrap(err, operationError)
		return
	}
	retVal = Nodes{gradNode, slicedRes}
	return
}

func (op sliceIncrOp) DoDiff(inputs Nodes, output *Node) (err error) {
	xdv := inputs[0].boundTo.(*dualValue)
	ydv := inputs[1].boundTo.(*dualValue)
	zdv := output.boundTo.(*dualValue)

	// dzdx
	add := newElemBinOp(addOpType, inputs[0], output)

	if _, err = add.UnsafeDo(xdv.d, zdv.d); err != nil {
		err = errors.Wrapf(err, unsafeDoFail, add)
		return
	}

	// dzdy
	var d Value
	if d, err = op.sliceOp.Do(zdv.d); err != nil {
		err = errors.Wrapf(err, doFail, op)
		return
	}

	add = newElemBinOp(addOpType, inputs[1], output)
	if _, err = add.UnsafeDo(ydv.d, d); err != nil {
		err = errors.Wrapf(err, doFail, add)
	}
	return
}

func (op sliceIncrOp) Do(inputs ...Value) (retVal Value, err error) {
	machineLogf("Doing %v", op)
	enterLoggingContext()
	defer leaveLoggingContext()

	if len(inputs) != 2 {
		err = NewError(GraphError, "sliceIncrOp should only have one or more inputs. Got %v instead", len(inputs))
		return
	}

	t := inputs[0]
	incr := inputs[1]

	// prep the slices
	slices := make([]types.Slice, op.d)
	if !op.all() {
		slices[op.along] = op
	}

	switch T := t.(type) {
	case Tensor:
		switch tt := T.Tensor.(type) {
		case *tf64.Tensor:
			// actually do shit
			cloned := tf64.NewTensor(tf64.WithShape(tt.Shape()...))
			var v64 *tf64.Tensor
			if v64, err = cloned.Slice(slices...); err != nil {
				err = errors.Wrapf(err, sliceFail, slices)
				return
			}

			var val interface{}
			switch i := incr.(type) {
			case Scalar:
				val = i.v.(float64)
			case Tensor:
				val = i.Tensor.(*tf64.Tensor)
			}

			v64.VAdd(val)
			retVal = FromTensor(cloned)

		case *tf32.Tensor:
			// actually do shit
			cloned := tf32.NewTensor(tf32.WithShape(tt.Shape()...))
			var v32 *tf32.Tensor
			if v32, err = cloned.Slice(slices...); err != nil {
				err = errors.Wrapf(err, sliceFail, slices)
				return
			}

			var val interface{}
			switch i := incr.(type) {
			case Scalar:
				val = i.v.(float32)
			case Tensor:
				val = i.Tensor.(*tf32.Tensor)
			}

			v32.VAdd(val)
			retVal = FromTensor(cloned)
		case *ti.Tensor:
			// actually do shit
			cloned := ti.NewTensor(ti.WithShape(tt.Shape()...))
			var vi *ti.Tensor
			if vi, err = cloned.Slice(slices...); err != nil {
				err = errors.Wrapf(err, sliceFail, slices)
				return
			}

			var val interface{}
			switch i := incr.(type) {
			case Scalar:
				val = i.v.(int)
			case Tensor:
				val = i.Tensor.(*ti.Tensor)
			}

			vi.VAdd(val)
			retVal = FromTensor(cloned)
		// case *tb.Tensor:
		default:
			err = nyi("sliceIncrOp Tensor Input", T)
			return
		}
	case Scalar:
		err = NewError(RuntimeError, "Cannot slice a scalar value")
		return
	default:
		err = nyi("sliceIncrOp Unknown Input", t)
		return
	}
	logf("returning?")
	return
}

func (op sliceIncrOp) overwriteInput() int { return 0 }

func (op sliceIncrOp) WriteHash(h hash.Hash) {
	h.Write([]byte("sliceIncr"))
	if err := binary.Write(h, binary.LittleEndian, byte(op.d)); err != nil {
		panic(err)
	}
	if err := binary.Write(h, binary.LittleEndian, byte(op.along)); err != nil {
		panic(err)
	}
	if err := binary.Write(h, binary.LittleEndian, byte(op.start)); err != nil {
		panic(err)
	}
	if err := binary.Write(h, binary.LittleEndian, byte(op.end)); err != nil {
		panic(err)
	}
}

func (op sliceIncrOp) Hashcode() uint32 {
	h := fnv.New32a()
	op.WriteHash(h)
	return h.Sum32()
}

func (op sliceIncrOp) String() string {
	var buf bytes.Buffer
	buf.WriteString("T[")
	for i := 0; i < op.along; i++ {
		buf.WriteString(":, ")
	}

	if op.all() {
		buf.WriteString(":")
	} else {
		fmt.Fprintf(&buf, "%d:%d", op.start, op.end)
	}

	buf.WriteString("...]+=...")
	return buf.String()
}
