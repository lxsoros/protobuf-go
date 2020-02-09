// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package impl

import (
	"sort"
	"sync/atomic"

	"google.golang.org/protobuf/internal/flags"
	proto "google.golang.org/protobuf/proto"
	pref "google.golang.org/protobuf/reflect/protoreflect"
	piface "google.golang.org/protobuf/runtime/protoiface"
)

type marshalOptions piface.MarshalOptions

func (o marshalOptions) Options() proto.MarshalOptions {
	return proto.MarshalOptions{
		AllowPartial:  true,
		Deterministic: o.Deterministic(),
		UseCachedSize: o.UseCachedSize(),
	}
}

func (o marshalOptions) Deterministic() bool { return o.Flags&piface.MarshalDeterministic != 0 }
func (o marshalOptions) UseCachedSize() bool { return o.Flags&piface.MarshalUseCachedSize != 0 }

// size is protoreflect.Methods.Size.
func (mi *MessageInfo) size(m pref.Message, opts piface.MarshalOptions) (size int) {
	var p pointer
	if ms, ok := m.(*messageState); ok {
		p = ms.pointer()
	} else {
		p = m.(*messageReflectWrapper).pointer()
	}
	return mi.sizePointer(p, marshalOptions(opts))
}

func (mi *MessageInfo) sizePointer(p pointer, opts marshalOptions) (size int) {
	mi.init()
	if p.IsNil() {
		return 0
	}
	if opts.UseCachedSize() && mi.sizecacheOffset.IsValid() {
		return int(atomic.LoadInt32(p.Apply(mi.sizecacheOffset).Int32()))
	}
	return mi.sizePointerSlow(p, opts)
}

func (mi *MessageInfo) sizePointerSlow(p pointer, opts marshalOptions) (size int) {
	if flags.ProtoLegacy && mi.isMessageSet {
		size = sizeMessageSet(mi, p, opts)
		if mi.sizecacheOffset.IsValid() {
			atomic.StoreInt32(p.Apply(mi.sizecacheOffset).Int32(), int32(size))
		}
		return size
	}
	if mi.extensionOffset.IsValid() {
		e := p.Apply(mi.extensionOffset).Extensions()
		size += mi.sizeExtensions(e, opts)
	}
	for _, f := range mi.orderedCoderFields {
		if f.funcs.size == nil {
			continue
		}
		fptr := p.Apply(f.offset)
		if f.isPointer && fptr.Elem().IsNil() {
			continue
		}
		size += f.funcs.size(fptr, f, opts)
	}
	if mi.unknownOffset.IsValid() {
		u := *p.Apply(mi.unknownOffset).Bytes()
		size += len(u)
	}
	if mi.sizecacheOffset.IsValid() {
		atomic.StoreInt32(p.Apply(mi.sizecacheOffset).Int32(), int32(size))
	}
	return size
}

// marshal is protoreflect.Methods.Marshal.
func (mi *MessageInfo) marshal(m pref.Message, in piface.MarshalInput, opts piface.MarshalOptions) (piface.MarshalOutput, error) {
	var p pointer
	if ms, ok := m.(*messageState); ok {
		p = ms.pointer()
	} else {
		p = m.(*messageReflectWrapper).pointer()
	}
	b, err := mi.marshalAppendPointer(in.Buf, p, marshalOptions(opts))
	return piface.MarshalOutput{Buf: b}, err
}

func (mi *MessageInfo) marshalAppendPointer(b []byte, p pointer, opts marshalOptions) ([]byte, error) {
	mi.init()
	if p.IsNil() {
		return b, nil
	}
	if flags.ProtoLegacy && mi.isMessageSet {
		return marshalMessageSet(mi, b, p, opts)
	}
	var err error
	// The old marshaler encodes extensions at beginning.
	if mi.extensionOffset.IsValid() {
		e := p.Apply(mi.extensionOffset).Extensions()
		// TODO: Special handling for MessageSet?
		b, err = mi.appendExtensions(b, e, opts)
		if err != nil {
			return b, err
		}
	}
	for _, f := range mi.orderedCoderFields {
		if f.funcs.marshal == nil {
			continue
		}
		fptr := p.Apply(f.offset)
		if f.isPointer && fptr.Elem().IsNil() {
			continue
		}
		b, err = f.funcs.marshal(b, fptr, f, opts)
		if err != nil {
			return b, err
		}
	}
	if mi.unknownOffset.IsValid() && !mi.isMessageSet {
		u := *p.Apply(mi.unknownOffset).Bytes()
		b = append(b, u...)
	}
	return b, nil
}

func (mi *MessageInfo) sizeExtensions(ext *map[int32]ExtensionField, opts marshalOptions) (n int) {
	if ext == nil {
		return 0
	}
	for _, x := range *ext {
		xi := getExtensionFieldInfo(x.Type())
		if xi.funcs.size == nil {
			continue
		}
		n += xi.funcs.size(x.Value(), xi.tagsize, opts)
	}
	return n
}

func (mi *MessageInfo) appendExtensions(b []byte, ext *map[int32]ExtensionField, opts marshalOptions) ([]byte, error) {
	if ext == nil {
		return b, nil
	}

	switch len(*ext) {
	case 0:
		return b, nil
	case 1:
		// Fast-path for one extension: Don't bother sorting the keys.
		var err error
		for _, x := range *ext {
			xi := getExtensionFieldInfo(x.Type())
			b, err = xi.funcs.marshal(b, x.Value(), xi.wiretag, opts)
		}
		return b, err
	default:
		// Sort the keys to provide a deterministic encoding.
		// Not sure this is required, but the old code does it.
		keys := make([]int, 0, len(*ext))
		for k := range *ext {
			keys = append(keys, int(k))
		}
		sort.Ints(keys)
		var err error
		for _, k := range keys {
			x := (*ext)[int32(k)]
			xi := getExtensionFieldInfo(x.Type())
			b, err = xi.funcs.marshal(b, x.Value(), xi.wiretag, opts)
			if err != nil {
				return b, err
			}
		}
		return b, nil
	}
}
