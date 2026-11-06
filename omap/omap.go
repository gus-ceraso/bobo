package omap

import "iter"

type Map[K comparable, V any] struct {
	entries    map[K]*entry[K, V]
	head, tail *entry[K, V]
}

type entry[K comparable, V any] struct {
	key        K
	value      V
	prev, next *entry[K, V]
}

func (m *Map[K, V]) Get(key K) (value V, ok bool) {
	if e := m.entries[key]; e != nil {
		return e.value, true
	}
	return
}

func (m *Map[K, V]) Set(key K, value V) {
	if e := m.entries[key]; e != nil {
		e.value = value
		return
	}
	if m.entries == nil {
		m.entries = make(map[K]*entry[K, V])
	}
	e := &entry[K, V]{key: key, value: value, prev: m.tail}
	if m.tail != nil {
		m.tail.next = e
	} else {
		m.head = e
	}
	m.tail = e
	m.entries[key] = e
}

func (m *Map[K, V]) Delete(key K) {
	e := m.entries[key]
	if e == nil {
		return
	}
	delete(m.entries, key)
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		m.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		m.tail = e.prev
	}
}

func (m *Map[K, V]) Len() int {
	return len(m.entries)
}

func (m *Map[K, V]) Copy() Map[K, V] {
	var copy Map[K, V]
	for key, value := range m.All() {
		copy.Set(key, value)
	}
	return copy
}

func (m *Map[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for e := m.head; e != nil; e = e.next {
			if !yield(e.key, e.value) {
				return
			}
		}
	}
}
