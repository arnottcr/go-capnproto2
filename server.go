package capn

import (
	"errors"
	"sort"
	"sync"

	"golang.org/x/net/context"
)

// A ServerMethod describes a single method on a server object.
type ServerMethod struct {
	Method
	Impl        ServerFunc
	ResultsSize ObjectSize
}

// A ServerFunc is a function that implements a single method.
type ServerFunc func(ctx context.Context, options CallOptions, params, results Struct) error

// Closer is the interface that wraps the Close method.
type Closer interface {
	Close() error
}

// A server is a locally implemented interface.
type server struct {
	methods sortedMethods
	closer  Closer
}

// NewServer returns a client that makes calls to a set of methods.
// If closer is nil then the client's Close is a no-op.
func NewServer(methods []ServerMethod, closer Closer) Client {
	s := &server{
		methods: make(sortedMethods, len(methods)),
		closer:  closer,
	}
	copy(s.methods, methods)
	sort.Sort(s.methods)
	return s
}

func (s *server) Call(call *Call) Answer {
	out := NewBuffer(nil)
	sm := s.methods.find(&call.Method)
	if sm == nil {
		return ErrorAnswer(&MethodError{
			Method: &call.Method,
			Err:    ErrUnimplemented,
		})
	}
	results := out.NewRootStruct(sm.ResultsSize)
	ans := newServerAnswer()
	go func() {
		err := sm.Impl(call.Ctx, call.Options, call.PlaceParams(nil), results)
		if err == nil {
			ans.resolve(ImmediateAnswer(Object(results)))
		} else {
			ans.resolve(ErrorAnswer(err))
		}
	}()
	return ans
}

func (s *server) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

type serverAnswer struct {
	done chan struct{} // closed when resolve is called

	mu     sync.RWMutex
	answer Answer
}

func newServerAnswer() *serverAnswer {
	return &serverAnswer{done: make(chan struct{})}
}

func (ans *serverAnswer) resolve(a Answer) {
	ans.mu.Lock()
	ans.answer = a
	close(ans.done)
	ans.mu.Unlock()
}

func (ans *serverAnswer) Struct() (Struct, error) {
	<-ans.done
	ans.mu.RLock()
	a := ans.answer
	ans.mu.RUnlock()
	return a.Struct()
}

func (ans *serverAnswer) PipelineCall(transform []PipelineOp, call *Call) Answer {
	ans.mu.RLock()
	a := ans.answer
	ans.mu.RUnlock()
	if a != nil {
		return a.PipelineCall(transform, call)
	}

	sa := newServerAnswer()
	go func() {
		<-ans.done
		ans.mu.RLock()
		a := ans.answer
		ans.mu.RUnlock()
		sa.resolve(a.PipelineCall(transform, call))
	}()
	return sa
}

func (ans *serverAnswer) PipelineClose(transform []PipelineOp) error {
	<-ans.done
	ans.mu.RLock()
	a := ans.answer
	ans.mu.RUnlock()
	return a.PipelineClose(transform)
}

// MethodError is an error on an associated method.
type MethodError struct {
	Method *Method
	Err    error
}

// Error returns the method name concatenated with the error string.
func (me *MethodError) Error() string {
	return me.Method.String() + ": " + me.Err.Error()
}

// ErrUnimplemented is the error returned when a method is called on
// a server that does not implement the method.
var ErrUnimplemented = errors.New("method not implemented")

// IsUnimplemented reports whether e indicates an unimplemented method error.
func IsUnimplemented(e error) bool {
	if me, ok := e.(*MethodError); ok {
		e = me
	}
	return e == ErrUnimplemented
}

type sortedMethods []ServerMethod

// find returns the method with the given ID or nil.
func (sm sortedMethods) find(id *Method) *ServerMethod {
	i := sort.Search(len(sm), func(i int) bool {
		m := &sm[i]
		if m.InterfaceID != id.InterfaceID {
			return m.InterfaceID >= id.InterfaceID
		}
		return m.MethodID >= id.MethodID
	})
	if i == len(sm) {
		return nil
	}
	m := &sm[i]
	if m.InterfaceID != id.InterfaceID || m.MethodID != id.MethodID {
		return nil
	}
	return m
}

func (sm sortedMethods) Len() int {
	return len(sm)
}

func (sm sortedMethods) Less(i, j int) bool {
	if id1, id2 := sm[i].InterfaceID, sm[j].InterfaceID; id1 != id2 {
		return id1 < id2
	}
	return sm[i].MethodID < sm[j].MethodID
}

func (sm sortedMethods) Swap(i, j int) {
	sm[i], sm[j] = sm[j], sm[i]
}
