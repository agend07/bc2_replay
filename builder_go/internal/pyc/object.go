package pyc

// Object is an insertion-ordered str-keyed dict with Python dict semantics: assigning an existing key keeps its
// position, Delete followed by Set moves the key to the end.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty Object.
func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Set is d[k] = v.
func (o *Object) Set(k string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Get is d.get(k) with a presence flag.
func (o *Object) Get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// Delete is del d[k] (no-op when missing).
func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Len is len(d).
func (o *Object) Len() int { return len(o.keys) }

// Keys returns the keys in insertion order (the slice must not be modified).
func (o *Object) Keys() []string { return o.keys }
