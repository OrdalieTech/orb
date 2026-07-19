package jsonwire

import "bytes"

// OrderedMember is one name/value pair of an OrderedObject.
type OrderedMember struct {
	Name  string
	Value any
}

// OrderedObject is a JSON object that marshals its members in insertion order,
// matching JavaScript's object key ordering on the wire.
type OrderedObject []OrderedMember

func (object OrderedObject) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, member := range object {
		if index > 0 {
			output.WriteByte(',')
		}
		name, err := Marshal(member.Name)
		if err != nil {
			return nil, err
		}
		value, err := Marshal(member.Value)
		if err != nil {
			return nil, err
		}
		output.Write(name)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

// Value returns the value of the first member with the given name.
func (object OrderedObject) Value(name string) (any, bool) {
	for _, field := range object {
		if field.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

// Set replaces the value of the first member with the given name in place, or
// appends a new member.
func (object *OrderedObject) Set(name string, value any) {
	for index := range *object {
		if (*object)[index].Name == name {
			(*object)[index].Value = value
			return
		}
	}
	*object = append(*object, OrderedMember{Name: name, Value: value})
}

// Delete removes the first member with the given name, if present.
func (object *OrderedObject) Delete(name string) {
	for index := range *object {
		if (*object)[index].Name == name {
			*object = append((*object)[:index], (*object)[index+1:]...)
			return
		}
	}
}
