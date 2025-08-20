package util

import "sync/atomic"

type Selector[T any] struct {
	value  atomic.Uint32
	target []T
}

func NewSelector[T any](target []T) *Selector[T] {
	return &Selector[T]{
		value:  atomic.Uint32{},
		target: target,
	}
}

func (s *Selector[T]) Get() T {
	value := s.value.Add(1)
	index := value % uint32(len(s.target))
	return s.target[index]
}
