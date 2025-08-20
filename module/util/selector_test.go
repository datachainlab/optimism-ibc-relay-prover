package util

import (
	"testing"
)

func TestSelectorSingle(t *testing.T) {
	values := []string{"localhost"}
	selector := NewSelector(values)
	if selector.Get() != values[0] {
		t.Errorf("Expected %s, got %s", values[0], selector.Get())
	}
}

func TestSelectorMulti(t *testing.T) {

	tester := func(initial uint32) {
		values := []string{"localhost:8090", "localhost:8080"}
		selector := NewSelector(values)
		selector.value.Store(initial)
		result := map[string]int{}
		count := 10000
		for i := 0; i < count; i++ {
			selected := selector.Get()
			result[selected]++
		}
		for _, value := range values {
			selected := result[value]
			if selected != count/2 {
				t.Errorf("Expected %s to be selected %d times, got %d", value, count/2, result[value])
			}
		}
	}
	tester(0)
	tester(4294967295) // max uint32
}
