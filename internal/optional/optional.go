// Package optional distinguishes an omitted JSON property from an explicit
// null so PATCH requests can preserve untouched fields and clear nullable ones.
package optional

import "encoding/json"

// Value records whether a property was present and its decoded value.
// Value is nil when the property was null.
type Value[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON marks the field present, then decodes null or the value.
func (v *Value[T]) UnmarshalJSON(raw []byte) error {
	v.Set = true
	if string(raw) == "null" {
		v.Value = nil
		return nil
	}
	var decoded T
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	v.Value = &decoded
	return nil
}
