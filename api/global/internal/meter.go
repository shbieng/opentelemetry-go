package internal

import (
	"context"
	"sync"
	"sync/atomic"
	"unsafe"

	"go.opentelemetry.io/otel/api/core"
	"go.opentelemetry.io/otel/api/metric"
)

// This file contains the forwarding implementation of metric.Provider
// used as the default global instance.  Metric events using instruments
// provided by this implementation are no-ops until the first Meter
// implementation is set as the global provider.
//
// The implementation here uses Mutexes to maintain a list of active
// Meters in the Provider and Instruments in each Meter, under the
// assumption that these interfaces are not performance-critical.
//
// We have the invariant that setDelegate() will be called before a
// new metric.Provider implementation is registered as the global
// provider.  Mutexes in the Provider and Meters ensure that each
// instrument has a delegate before the global provider is set.
//
// LabelSets are implemented by delegating to the Meter instance using
// the metric.LabelSetDelegator interface.
//
// Bound instrument operations are implemented by delegating to the
// instrument after it is registered, with a sync.Once initializer to
// protect against races with Release().

type meterProvider struct {
	delegate metric.Provider

	lock   sync.Mutex
	meters []*meter
}

type meter struct {
	delegate unsafe.Pointer // (*metric.Meter)

	provider *meterProvider
	name     string

	lock       sync.Mutex
	syncInsts  []*syncImpl
	asyncInsts []*obsImpl
}

type instrument struct {
	descriptor metric.Descriptor
}

type syncImpl struct {
	delegate unsafe.Pointer // (*metric.SyncImpl)

	instrument

	constructor func(metric.Meter) (metric.SyncImpl, error)
}

type obsImpl struct {
	delegate unsafe.Pointer // (*metric.AsyncImpl)

	instrument

	constructor func(metric.Meter) (metric.AsyncImpl, error)
}

// SyncImpler is implemented by all of the sync metric
// instruments.
type SyncImpler interface {
	SyncImpl() metric.SyncImpl
}

// AsyncImpler is implemented by all of the async
// metric instruments.
type AsyncImpler interface {
	AsyncImpl() metric.AsyncImpl
}

type labelSet struct {
	delegate unsafe.Pointer // (* metric.LabelSet)

	meter *meter
	value []core.KeyValue

	initialize sync.Once
}

type syncHandle struct {
	delegate unsafe.Pointer // (*metric.HandleImpl)

	inst   *syncImpl
	labels metric.LabelSet

	initialize sync.Once
}

var _ metric.Provider = &meterProvider{}
var _ metric.Meter = &meter{}
var _ metric.LabelSet = &labelSet{}
var _ metric.LabelSetDelegate = &labelSet{}
var _ metric.InstrumentImpl = &syncImpl{}
var _ metric.BoundSyncImpl = &syncHandle{}
var _ metric.AsyncImpl = &obsImpl{}

func (inst *instrument) Descriptor() metric.Descriptor {
	return inst.descriptor
}

// Provider interface and delegation

func (p *meterProvider) setDelegate(provider metric.Provider) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.delegate = provider
	for _, m := range p.meters {
		m.setDelegate(provider)
	}
	p.meters = nil
}

func (p *meterProvider) Meter(name string) metric.Meter {
	p.lock.Lock()
	defer p.lock.Unlock()

	if p.delegate != nil {
		return p.delegate.Meter(name)
	}

	m := &meter{
		provider: p,
		name:     name,
	}
	p.meters = append(p.meters, m)
	return m
}

// Meter interface and delegation

func (m *meter) setDelegate(provider metric.Provider) {
	m.lock.Lock()
	defer m.lock.Unlock()

	d := new(metric.Meter)
	*d = provider.Meter(m.name)
	m.delegate = unsafe.Pointer(d)

	for _, inst := range m.syncInsts {
		inst.setDelegate(*d)
	}
	m.syncInsts = nil
	for _, obs := range m.asyncInsts {
		obs.setDelegate(*d)
	}
	m.asyncInsts = nil
}

func (m *meter) newSync(desc metric.Descriptor, constructor func(metric.Meter) (metric.SyncImpl, error)) (metric.SyncImpl, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	if meterPtr := (*metric.Meter)(atomic.LoadPointer(&m.delegate)); meterPtr != nil {
		return constructor(*meterPtr)
	}

	inst := &syncImpl{
		instrument: instrument{
			descriptor: desc,
		},
		constructor: constructor,
	}
	m.syncInsts = append(m.syncInsts, inst)
	return inst, nil
}

func syncCheck(has SyncImpler, err error) (metric.SyncImpl, error) {
	if has != nil {
		return has.SyncImpl(), err
	}
	if err == nil {
		err = metric.ErrSDKReturnedNilImpl
	}
	return nil, err
}

// Synchronous delegation

func (inst *syncImpl) setDelegate(d metric.Meter) {
	implPtr := new(metric.SyncImpl)

	var err error
	*implPtr, err = inst.constructor(d)

	if err != nil {
		// TODO: There is no standard way to deliver this error to the user.
		// See https://github.com/open-telemetry/opentelemetry-go/issues/514
		// Note that the default SDK will not generate any errors yet, this is
		// only for added safety.
		panic(err)
	}

	atomic.StorePointer(&inst.delegate, unsafe.Pointer(implPtr))
}

func (inst *syncImpl) Implementation() interface{} {
	if implPtr := (*metric.SyncImpl)(atomic.LoadPointer(&inst.delegate)); implPtr != nil {
		return (*implPtr).Implementation()
	}
	return inst
}

func (inst *syncImpl) Bind(labels metric.LabelSet) metric.BoundSyncImpl {
	if implPtr := (*metric.SyncImpl)(atomic.LoadPointer(&inst.delegate)); implPtr != nil {
		return (*implPtr).Bind(labels)
	}
	return &syncHandle{
		inst:   inst,
		labels: labels,
	}
}

func (bound *syncHandle) Unbind() {
	bound.initialize.Do(func() {})

	implPtr := (*metric.BoundSyncImpl)(atomic.LoadPointer(&bound.delegate))

	if implPtr == nil {
		return
	}

	(*implPtr).Unbind()
}

// Async delegation

func (m *meter) newAsync(desc metric.Descriptor, constructor func(metric.Meter) (metric.AsyncImpl, error)) (metric.AsyncImpl, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	if meterPtr := (*metric.Meter)(atomic.LoadPointer(&m.delegate)); meterPtr != nil {
		return constructor(*meterPtr)
	}

	inst := &obsImpl{
		instrument: instrument{
			descriptor: desc,
		},
		constructor: constructor,
	}
	m.asyncInsts = append(m.asyncInsts, inst)
	return inst, nil
}

func (obs *obsImpl) Implementation() interface{} {
	if implPtr := (*metric.AsyncImpl)(atomic.LoadPointer(&obs.delegate)); implPtr != nil {
		return (*implPtr).Implementation()
	}
	return obs
}

func asyncCheck(has AsyncImpler, err error) (metric.AsyncImpl, error) {
	if has != nil {
		return has.AsyncImpl(), err
	}
	if err == nil {
		err = metric.ErrSDKReturnedNilImpl
	}
	return nil, err
}

func (obs *obsImpl) setDelegate(d metric.Meter) {
	implPtr := new(metric.AsyncImpl)

	var err error
	*implPtr, err = obs.constructor(d)

	if err != nil {
		// TODO: There is no standard way to deliver this error to the user.
		// See https://github.com/open-telemetry/opentelemetry-go/issues/514
		// Note that the default SDK will not generate any errors yet, this is
		// only for added safety.
		panic(err)
	}

	atomic.StorePointer(&obs.delegate, unsafe.Pointer(implPtr))
}

// Metric updates

func (m *meter) RecordBatch(ctx context.Context, labels metric.LabelSet, measurements ...metric.Measurement) {
	if delegatePtr := (*metric.Meter)(atomic.LoadPointer(&m.delegate)); delegatePtr != nil {
		(*delegatePtr).RecordBatch(ctx, labels, measurements...)
	}
}

func (inst *syncImpl) RecordOne(ctx context.Context, number core.Number, labels metric.LabelSet) {
	if instPtr := (*metric.SyncImpl)(atomic.LoadPointer(&inst.delegate)); instPtr != nil {
		(*instPtr).RecordOne(ctx, number, labels)
	}
}

// Bound instrument initialization

func (bound *syncHandle) RecordOne(ctx context.Context, number core.Number) {
	instPtr := (*metric.SyncImpl)(atomic.LoadPointer(&bound.inst.delegate))
	if instPtr == nil {
		return
	}
	var implPtr *metric.BoundSyncImpl
	bound.initialize.Do(func() {
		implPtr = new(metric.BoundSyncImpl)
		*implPtr = (*instPtr).Bind(bound.labels)
		atomic.StorePointer(&bound.delegate, unsafe.Pointer(implPtr))
	})
	if implPtr == nil {
		implPtr = (*metric.BoundSyncImpl)(atomic.LoadPointer(&bound.delegate))
	}
	// This may still be nil if instrument was created and bound
	// without a delegate, then the instrument was set to have a
	// delegate and unbound.
	if implPtr == nil {
		return
	}
	(*implPtr).RecordOne(ctx, number)
}

// LabelSet initialization

func (m *meter) Labels(labels ...core.KeyValue) metric.LabelSet {
	return &labelSet{
		meter: m,
		value: labels,
	}
}

func (labels *labelSet) Delegate() metric.LabelSet {
	meterPtr := (*metric.Meter)(atomic.LoadPointer(&labels.meter.delegate))
	if meterPtr == nil {
		// This is technically impossible, provided the global
		// Meter is updated after the meters and instruments
		// have been delegated.
		return labels
	}
	var implPtr *metric.LabelSet
	labels.initialize.Do(func() {
		implPtr = new(metric.LabelSet)
		*implPtr = (*meterPtr).Labels(labels.value...)
		atomic.StorePointer(&labels.delegate, unsafe.Pointer(implPtr))
	})
	if implPtr == nil {
		implPtr = (*metric.LabelSet)(atomic.LoadPointer(&labels.delegate))
	}
	return (*implPtr)
}

// Constructors

func (m *meter) NewInt64Counter(name string, opts ...metric.Option) (metric.Int64Counter, error) {
	return metric.WrapInt64CounterInstrument(m.newSync(
		metric.NewDescriptor(name, metric.CounterKind, core.Int64NumberKind, opts...),
		func(other metric.Meter) (metric.SyncImpl, error) {
			return syncCheck(other.NewInt64Counter(name, opts...))
		}))
}

func (m *meter) NewFloat64Counter(name string, opts ...metric.Option) (metric.Float64Counter, error) {
	return metric.WrapFloat64CounterInstrument(m.newSync(
		metric.NewDescriptor(name, metric.CounterKind, core.Float64NumberKind, opts...),
		func(other metric.Meter) (metric.SyncImpl, error) {
			return syncCheck(other.NewFloat64Counter(name, opts...))
		}))
}

func (m *meter) NewInt64Measure(name string, opts ...metric.Option) (metric.Int64Measure, error) {
	return metric.WrapInt64MeasureInstrument(m.newSync(
		metric.NewDescriptor(name, metric.MeasureKind, core.Int64NumberKind, opts...),
		func(other metric.Meter) (metric.SyncImpl, error) {
			return syncCheck(other.NewInt64Measure(name, opts...))
		}))
}

func (m *meter) NewFloat64Measure(name string, opts ...metric.Option) (metric.Float64Measure, error) {
	return metric.WrapFloat64MeasureInstrument(m.newSync(
		metric.NewDescriptor(name, metric.MeasureKind, core.Float64NumberKind, opts...),
		func(other metric.Meter) (metric.SyncImpl, error) {
			return syncCheck(other.NewFloat64Measure(name, opts...))
		}))
}

func (m *meter) RegisterInt64Observer(name string, callback metric.Int64ObserverCallback, opts ...metric.Option) (metric.Int64Observer, error) {
	return metric.WrapInt64ObserverInstrument(m.newAsync(
		metric.NewDescriptor(name, metric.ObserverKind, core.Int64NumberKind, opts...),
		func(other metric.Meter) (metric.AsyncImpl, error) {
			return asyncCheck(other.RegisterInt64Observer(name, callback, opts...))
		}))
}

func (m *meter) RegisterFloat64Observer(name string, callback metric.Float64ObserverCallback, opts ...metric.Option) (metric.Float64Observer, error) {
	return metric.WrapFloat64ObserverInstrument(m.newAsync(
		metric.NewDescriptor(name, metric.ObserverKind, core.Float64NumberKind, opts...),
		func(other metric.Meter) (metric.AsyncImpl, error) {
			return asyncCheck(other.RegisterFloat64Observer(name, callback, opts...))
		}))
}

func AtomicFieldOffsets() map[string]uintptr {
	return map[string]uintptr{
		"meterProvider.delegate": unsafe.Offsetof(meterProvider{}.delegate),
		"meter.delegate":         unsafe.Offsetof(meter{}.delegate),
		"syncImpl.delegate":      unsafe.Offsetof(syncImpl{}.delegate),
		"obsImpl.delegate":       unsafe.Offsetof(obsImpl{}.delegate),
		"labelSet.delegate":      unsafe.Offsetof(labelSet{}.delegate),
		"syncHandle.delegate":    unsafe.Offsetof(syncHandle{}.delegate),
	}
}
