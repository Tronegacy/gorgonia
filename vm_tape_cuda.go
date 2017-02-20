// +build cuda

package gorgonia

import (
	"github.com/chewxy/cu"
	"github.com/pkg/errors"
)

type modules map[string][]cu.Module // ths size of the slice has to be the same as the slice of contexts
type functions map[string][]cu.Function
type contexts []cu.Context

func (m modules) HasFunc(name string) bool {
	_, ok := m[name]
	return ok
}

func (m modules) Function(name string) (interface{}, error) {
	mod, ok := m[name]
	if !ok {
		return nil, errors.Errorf("Function %q not found", name)
	}
	return mod, nil
}

func finalizeTapeMachine(m *tapeMachine) {
	cudaLogf("Finalizing tape machine %p", m)
	for i, c := range m.c {
		cu.SetCurrent(c)
		for _, v := range m.m {
			mod := v[i]
			cu.Unload(mod)
		}
		cu.DestroyContext(&c)
	}
	m.cleanup()
}

func (m *tapeMachine) init() {
	var initCUDA bool
	for _, instr := range m.p.instructions {
		if eo, ok := instr.(execOp); ok {
			if _, ok := eo.op.(CUDADoer); ok {
				initCUDA = true
				break
			}
		}
	}
	if !initCUDA {
		// don't bother initializing contexts if no instructions were CUDA based
		return
	}

	devices, _ := cu.NumDevices()
	m.c = make(contexts, devices)
	m.d = make([]Device, devices)
	for i := range m.c {
		dev, err := cu.GetDevice(i)
		if err != nil {
			cudaLogf("Failed to get device %d: %v", i, err)
			m.cleanup()
			return
		}
		m.d[i] = Device(dev)
		ctx, err := dev.MakeContext(cu.SchedAuto)
		if err != nil {
			if err == cu.OutOfMemory {
				var free, total int64
				if free, total, err = cu.MemInfo(); err != nil {
					cudaLogf("Error while getting mem info: %v", err)
				}
				cudaLogf("Out of memory. Free: %v, total %v", free, total)
				m.cleanup()
				return
			}
			cudaLogf("Failed to make context for device %d. Error: %v", i, err)
			m.cleanup()
			return
		}

		m.c[i] = ctx
	}
	if len(m.c) > 0 {
		cu.SetCurrent(m.c[0])
	}
	m.m = make(modules)
	m.f = make(functions)
	m.loadStdLib()

	// var free, total int64
	// var err error
	// if free, total, err = cu.MemInfo(); err != nil {
	// 	cudaLogf("Error while getting mem info: %v", err)
	// }
	// cudaLogf("Machine %p initialized. CUDA Memory: %v/%v", m, free, total)
}

func (m *tapeMachine) cleanup() {
	m.c = nil
	m.m = nil
}

// LoadCUDAFunc loads a string representing a CUDA PTX file into the machine.
//
// The convention is to have one function per module, sharing the same name.
func (m *tapeMachine) LoadCUDAFunc(name, data string) (err error) {
	if len(m.c) == 0 {
		return nil
	}

	mods := make([]cu.Module, len(m.c))
	fns := make([]cu.Function, len(m.c))
	for i, c := range m.c {
		if err = cu.SetCurrent(c); err != nil {
			err = errors.Wrapf(err, "Unable to set current context when loading module %q at context %d", name, i)
			return
		}

		var mod cu.Module
		if mod, err = cu.LoadData(data); err != nil {
			err = errors.Wrapf(err, "Failed to load module %q data for %dth context %x", name, i, c)
			return
		}

		var fn cu.Function
		if fn, err = mod.Function(name); err != nil {
			err = errors.Wrapf(err, "Unable to get function %q in %dth context %x", name, i, c)
			return
		}
		mods[i] = mod
		fns[i] = fn
	}

	// set the first to current
	if len(m.c) > 0 {
		if err = cu.SetCurrent(m.c[0]); err != nil {
			err = errors.Wrapf(err, "Unable to set current")
			return
		}
	}

	m.m[name] = mods
	m.f[name] = fns
	return nil
}

func (m *tapeMachine) Contexts() []cu.Context {
	return []cu.Context(m.c)
}

func (m *tapeMachine) Modules() map[string][]cu.Module {
	return map[string][]cu.Module(m.m)
}

func (m *tapeMachine) Functions() map[string][]cu.Function {
	return map[string][]cu.Function(m.f)
}

// loads the standardlib
func (m *tapeMachine) loadStdLib() {
	if cudaStdLib == nil {
		return
	}

	for name, data := range cudaStdLib {
		if err := m.LoadCUDAFunc(name, data); err != nil {
			cudaLogf("Unable to load %q.: %v", name, err)
		}
	}
}
